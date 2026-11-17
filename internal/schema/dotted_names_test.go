package schema

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDottedNameSplit(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
		ok   bool
	}{
		{`"x.y"`, []string{"x.y"}, true},
		{`app."x.y"`, []string{"app", "x.y"}, true},
		{`"app"."x.y"`, []string{"app", "x.y"}, true},
		{`app."x.y"."k.v"`, []string{"app", "x.y", "k.v"}, true},
		{`"say ""hi"".x"`, []string{`say "hi".x`}, true},
		{`"Mixed"`, []string{"Mixed"}, true},
		{`app.x.y`, nil, false},
		{`"x.y`, nil, false},
		{`""`, nil, false},
		{`app.."x"`, nil, false},
		{`"x"y`, nil, false},
		{`a"b.c`, nil, false},
	} {
		got, ok := SplitName(c.in)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitName(%s) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
	for name, want := range map[string]string{"x.y": `"x.y"`, `a"b`: `"a""b"`, "plain": "plain", "Mixed Case": "Mixed Case"} {
		if got := QuoteName(name); got != want {
			t.Errorf("QuoteName(%q) = %s, want %s", name, got, want)
		}
		if parts, ok := SplitName(QuoteName(name)); want != name && (!ok || len(parts) != 1 || parts[0] != name) {
			t.Errorf("SplitName(QuoteName(%q)) = %q", name, parts)
		}
	}
	if got := QualifiedName("app.x.y"); got != `app."x.y"` {
		t.Errorf("QualifiedName = %s", got)
	}
	if got := QualifiedName("app.t"); got != "app.t" {
		t.Errorf("QualifiedName = %s", got)
	}
}

func TestDottedNameOwnedByParts(t *testing.T) {
	for in, want := range map[string][3]string{
		"t.c":             {"app", "t", "c"},
		"other.t.c":       {"other", "t", "c"},
		"app.t.a.b":       {"app", "t", "a.b"},
		`app."x.y".c`:     {"app", "x.y", "c"},
		`"x.y"."k.v"`:     {"app", "x.y", "k.v"},
		`app."x.y"."k.v"`: {"app", "x.y", "k.v"},
		`app.plain."k.v"`: {"app", "plain", "k.v"},
	} {
		s, table, column, ok := OwnedByParts("app.seq", in)
		if !ok || [3]string{s, table, column} != want {
			t.Errorf("OwnedByParts(%s) = %s, %s, %s, %v; want %v", in, s, table, column, ok, want)
		}
	}
	for _, in := range []string{"", "none", "NONE", "t"} {
		if _, _, _, ok := OwnedByParts("app.seq", in); ok {
			t.Errorf("OwnedByParts(%q) names no column", in)
		}
	}
}

const dottedNamesYAML = `
schema app:
  table "x.y":
    renamedFrom: '"o.ld"'
    columns:
      k.v: {type: integer}
    primaryKey: [k.v]
  table "in.h":
    inherits: [app."x.y", '"x.y"']
    columns:
      e: {type: int}
  table "pa.rt":
    columns:
      k: {type: int}
    partitionBy: {type: list, columns: [k]}
  table "pa.rt1":
    partitionOf: '"pa.rt"'
    forValues: {in: ["1"]}
  table "ty.ped":
    of: app."co.mp"
  table plain:
    columns:
      ref: {type: int}
    foreignKeys:
      fk:
        columns: [ref]
        references: {table: app."x.y", columns: [k.v]}
  table legacy.t:
    columns:
      id: {type: int}
  foreign table "f.t":
    server: s
    columns:
      id: {type: int}
  type "co.mp":
    type: composite
    attributes: {a: int}
  type "en.um":
    renamedFrom: app."e.old"
    type: enum
    labels: [a]
  domain "do.m":
    type: int
  sequence "se.q":
    ownedBy: app."x.y"."k.v"
  view "v.w":
    query: select 1
  materialized view "m.v":
    query: select 1
  function "f.n"(a int):
    renamedFrom: '"f.old"'
    returns: int
    language: sql
    body: select a
  procedure "p.r"():
    language: sql
    body: select 1
  aggregate "ag.g"(int):
    sfunc: int4pl
    stype: int
  collation "co.ll":
    locale: C
  text search configuration "cf.g":
    copy: simple
  text search dictionary "di.ct":
    template: simple
  function uses():
    returns: int
    language: sql
    dependsOn: [table app."x.y", view app."v.w", function app."f.n"(int), procedure "p.r"()]
    body: select 1
schema "Quoted":
  table t:
    columns:
      id: {type: int}
publications:
  pub:
    tables: [app."x.y"]
`

func TestDottedNameKeysValidate(t *testing.T) {
	if err := compileEditorSchema(t).Validate(yamlToJSONValue(t, []byte(dottedNamesYAML))); err != nil {
		t.Errorf("quoted names must pass the JSON Schema: %v", err)
	}
}

func TestDottedNameKeysParse(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(dottedNamesYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"app.x.y", "app.in.h", "app.pa.rt", "app.pa.rt1", "app.ty.ped", "app.plain", "legacy.t", "app.f.t", "Quoted.t"} {
		if d.Tables[k] == nil {
			t.Errorf("no table %s in %v", k, keys(d.Tables))
		}
	}
	if d.Tables["app.f.t"] == nil || d.Tables["app.f.t"].Foreign == nil {
		t.Errorf("foreign table app.f.t not parsed")
	}
	for kind, m := range map[string]map[string]bool{
		"types":      {"app.co.mp": d.Types["app.co.mp"] != nil, "app.en.um": d.Types["app.en.um"] != nil},
		"domains":    {"app.do.m": d.Domains["app.do.m"] != nil},
		"sequences":  {"app.se.q": d.Sequences["app.se.q"] != nil},
		"views":      {"app.v.w": d.Views["app.v.w"] != nil, "app.m.v": d.Views["app.m.v"] != nil},
		"functions":  {"app.f.n(a int)": d.Functions["app.f.n(a int)"] != nil},
		"procedures": {"app.p.r()": d.Procedures["app.p.r()"] != nil},
		"aggregates": {"app.ag.g(int)": d.Aggregates["app.ag.g(int)"] != nil},
		"collations": {"app.co.ll": d.Collations["app.co.ll"] != nil},
		"ts":         {"app.cf.g": d.TSConfigs["app.cf.g"] != nil, "app.di.ct": d.TSDictionaries["app.di.ct"] != nil},
	} {
		for k, ok := range m {
			if !ok {
				t.Errorf("%s: no %s", kind, k)
			}
		}
	}
	if got := d.Tables["app.in.h"].Inherits; !reflect.DeepEqual(got, []string{"app.x.y", "app.x.y"}) {
		t.Errorf("inherits = %q", got)
	}
	if got := d.Tables["app.pa.rt1"].PartitionOf; got != "app.pa.rt" {
		t.Errorf("partitionOf = %q", got)
	}
	if got := d.Tables["app.ty.ped"].Of; got != "app.co.mp" {
		t.Errorf("of = %q", got)
	}
	if got := d.Tables["app.plain"].ForeignKeys[0].RefTable; got != "app.x.y" {
		t.Errorf("references = %q", got)
	}
	if got := d.Tables["app.x.y"].RenamedFrom; got != "app.o.ld" {
		t.Errorf("table renamedFrom = %q", got)
	}
	if got := d.Types["app.en.um"].RenamedFrom; got != "app.e.old" {
		t.Errorf("type renamedFrom = %q", got)
	}
	if f := d.Functions["app.f.n(a int)"]; f == nil || f.Name != "f.n" || f.RenamedFrom != "app.f.old" {
		t.Errorf("function = %+v", f)
	}
	if td := d.Types["app.en.um"]; td == nil || td.Name != "en.um" {
		t.Errorf("type name = %+v", td)
	}
	if got := d.Publications["pub"].Tables[0].Table; got != "app.x.y" {
		t.Errorf("publication table = %q", got)
	}
	f := d.Functions["app.uses()"]
	if f == nil {
		t.Fatalf("no function app.uses()")
	}
	got := []string{}
	for _, dep := range f.DependsOn {
		got = append(got, ResolveDependencyKeys(dep, d)...)
	}
	if want := []string{"app.x.y", "app.v.w", "app.f.n(a int)", "app.p.r()"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dependsOn resolves to %q, want %q", got, want)
	}
}
