package diff

import (
	"strings"
	"testing"
)

const dottedObjectsYAML = `
schema app:
  table "x.y":
    columns:
      k.v: {type: integer}
      n: {type: integer, nullable: true}
      s: {type: text, nullable: true, collate: app."co.ll"}
    primaryKey: [k.v]
    comment: dotted
    grants: {public: [select]}
  table "in.h":
    inherits: [app."x.y"]
    columns:
      extra: {type: integer, nullable: true}
  table "pa.rt":
    columns:
      k: {type: integer, nullable: true}
    partitionBy: {type: list, columns: [k]}
  table "pa.rt1":
    partitionOf: app."pa.rt"
    forValues: {in: ["1"]}
  table "ty.ped":
    of: app."co.mp"
  table plain:
    columns:
      ref: {type: integer, nullable: true}
    foreignKeys:
      plain_ref_fkey:
        columns: [ref]
        references: {table: app."x.y", columns: [k.v]}
  type "co.mp":
    type: composite
    attributes: {a: integer}
  type "en.um":
    type: enum
    labels: [a]
  domain "do.m":
    type: integer
  collation "co.ll":
    locale: C
  sequence "se.q":
    ownedBy: app."x.y".n
  view "v.w":
    query: select 1 as x
  function "f.n"(a integer):
    returns: integer
    language: sql
    body: select a
    comment: dotted function
`

func TestDottedObjectNamesCreate(t *testing.T) {
	sql := Render(Plan(emptyLive(), loadYAML(t, dottedObjectsYAML), false))
	for _, want := range []string{
		`create table if not exists "app"."x.y" ("k.v" integer not null, "n" integer, "s" text collate "app"."co.ll");`,
		`create table if not exists "app"."in.h" ("extra" integer) inherits ("app"."x.y");`,
		`create table if not exists "app"."pa.rt1" partition of "app"."pa.rt" for values in (1);`,
		`create table if not exists "app"."ty.ped" of "app"."co.mp";`,
		`foreign key ("ref") references "app"."x.y"("k.v");`,
		`create type "app"."co.mp" as ("a" integer);`,
		`create type "app"."en.um" as enum ('a');`,
		`create domain "app"."do.m" as integer;`,
		`create collation if not exists "app"."co.ll"`,
		`create sequence if not exists "app"."se.q";`,
		`alter sequence "app"."se.q" owned by "app"."x.y"."n";`,
		`create or replace view "app"."v.w" as select 1 as x;`,
		`create function "app"."f.n"(a integer) returns integer`,
		`comment on table "app"."x.y" is 'dotted';`,
		`comment on function "app"."f.n"(a integer) is 'dotted function';`,
		`grant select on table "app"."x.y" to public;`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
	if strings.Contains(sql, `create schema if not exists "x"`) || strings.Contains(sql, `"x"."y"`) {
		t.Errorf("a quoted name is one identifier, got\n%s", sql)
	}
}

func TestDottedObjectNamesSkippedIfExists(t *testing.T) {
	d := loadYAML(t, dottedObjectsYAML)
	live := Simulate(emptyLive(), d)
	if sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("declared dotted names must match the live objects, got\n%s", sql)
	}
	live.Tables["app.x.z"] = &LiveTable{Columns: map[string]*LiveColumn{}}
	if sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true})); !strings.Contains(sql, `drop table "app"."x.z";`) {
		t.Errorf("an undeclared dotted name is pruned as one identifier, got\n%s", sql)
	}
}

func TestDottedObjectRenames(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Tables["app.o.ld"] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer", Nullable: true}}}
	live.Types["app.e.old"] = true
	d := loadYAML(t, `
schema app:
  table "x.y":
    renamedFrom: '"o.ld"'
    columns:
      id: {type: integer, nullable: true}
  type "e.new":
    renamedFrom: app."e.old"
    type: enum
    labels: []
`)
	sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true}))
	for _, want := range []string{`alter table "app"."o.ld" rename to "x.y";`, `alter type "app"."e.old" rename to "e.new";`} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "drop ") || strings.Contains(sql, "create table") {
		t.Errorf("a rename must not drop or create, got\n%s", sql)
	}
}

func TestDottedSchemaNotPruned(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"], live.Schemas["app.sub"] = true, true
	for _, k := range []string{"app.t", "app.sub.u", "app.old"} {
		live.Tables[k] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer", Nullable: true}}}
	}
	live.Views["app.sub.w"] = true
	live.Types["app.sub.e"] = true
	d := loadYAML(t, "schema app:\n  table t:\n    columns:\n      id: {type: integer, nullable: true}\n")
	sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true}))
	if !strings.Contains(sql, `drop table "app"."old";`) {
		t.Errorf("app.old is pruned, got\n%s", sql)
	}
	if strings.Contains(sql, "sub") {
		t.Errorf("objects of schema app.sub must be left alone, got\n%s", sql)
	}
}

func TestDottedCollationReference(t *testing.T) {
	for in, want := range map[string][2]string{
		`app."co.ll"`: {`"app"."co.ll"`, "co.ll"},
		`"C"`:         {`"C"`, "C"},
		`app.ci`:      {`"app"."ci"`, "ci"},
		`C`:           {`"C"`, "C"},
	} {
		if got := collationIdent(in); got != want[0] {
			t.Errorf("collationIdent(%s) = %s, want %s", in, got, want[0])
		}
		if got := collationName(in); got != want[1] {
			t.Errorf("collationName(%s) = %s, want %s", in, got, want[1])
		}
	}
}
