package schema

import (
	"reflect"
	"testing"
)

func TestRoutineBodyPresenceParsed(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  function empty():
    returns: void
    language: sql
    body: ""
  function blank():
    returns: void
    language: sql
    body: "  "
  function attrs():
    leakproof: true
  function null_body():
    leakproof: true
    body:
  function real():
    returns: int
    language: sql
    body: select 1
  procedure empty_proc():
    language: sql
    body: ""
  procedure no_body():
    language: sql
`))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]bool{"app.empty()": true, "app.blank()": true, "app.attrs()": false, "app.null_body()": false, "app.real()": true} {
		f := db.Functions[k]
		if f == nil {
			t.Fatalf("missing function %s; have %v", k, keys(db.Functions))
		}
		if f.HasBody() != want {
			t.Errorf("function %s: HasBody = %v, want %v", k, f.HasBody(), want)
		}
	}
	for k, want := range map[string]bool{"app.empty_proc()": true, "app.no_body()": false} {
		p := db.Procedures[k]
		if p == nil {
			t.Fatalf("missing procedure %s", k)
		}
		if p.HasBody() != want {
			t.Errorf("procedure %s: HasBody = %v, want %v", k, p.HasBody(), want)
		}
	}
	if f := (&Function{Body: "select 1"}); !f.HasBody() {
		t.Error("a function built with a body has one")
	}
}

func TestRoutineBodyKeptVerbatim(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte("schema app:\n  function f():\n    returns: int\n    language: plpython3u\n    body: |2\n          if a > 0:\n              return 1\n          return -1\n  function g():\n    returns: int\n    language: sql\n    body: \"\\tselect 1 \\n\\n\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Functions["app.f()"].Body; got != "    if a > 0:\n        return 1\n    return -1\n" {
		t.Errorf("body = %q", got)
	}
	if got := db.Functions["app.g()"].Body; got != "\tselect 1 \n\n" {
		t.Errorf("body = %q", got)
	}
}

func TestRoutineTransformParsed(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  function list(a hstore, b jsonb):
    returns: jsonb
    language: plpython3u
    transform: [hstore, jsonb]
    body: return b
  function one(a hstore):
    returns: int
    language: plperl
    transform: hstore
    body: return 1
  function spelled(a hstore, b jsonb):
    returns: int
    language: plperl
    transform: [for type hstore, FOR TYPE public.jsonb]
    body: return 1
  function csv(a hstore, b jsonb):
    returns: int
    language: plperl
    transform: hstore, for type jsonb
    body: return 1
  function none(a hstore):
    returns: int
    language: plperl
    transform: []
    body: return 1
  function unmanaged(a hstore):
    returns: int
    language: plperl
    body: return 1
  procedure proc(a jsonb):
    language: plpython3u
    transform: [jsonb]
    body: return
`))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string][]string{
		"app.list(a hstore, b jsonb)":    {"hstore", "jsonb"},
		"app.one(a hstore)":              {"hstore"},
		"app.spelled(a hstore, b jsonb)": {"hstore", "public.jsonb"},
		"app.csv(a hstore, b jsonb)":     {"hstore", "jsonb"},
		"app.none(a hstore)":             {},
		"app.unmanaged(a hstore)":        nil,
	} {
		f := db.Functions[k]
		if f == nil {
			t.Fatalf("missing function %s; have %v", k, keys(db.Functions))
		}
		if !reflect.DeepEqual(f.Transform, want) {
			t.Errorf("function %s: transform = %#v, want %#v", k, f.Transform, want)
		}
	}
	if got := db.Procedures["app.proc(a jsonb)"].Transform; !reflect.DeepEqual(got, []string{"jsonb"}) {
		t.Errorf("procedure transform = %#v", got)
	}
}

func TestSplitLanguage(t *testing.T) {
	for in, want := range map[string]struct {
		name      string
		transform []string
	}{
		"plpgsql":                              {"plpgsql", nil},
		" PLPerl ":                             {"PLPerl", nil},
		"":                                     {"", nil},
		"plpython3u transform for type hstore": {"plpython3u", []string{"hstore"}},
		"plpython3u TRANSFORM FOR TYPE hstore, FOR TYPE public.jsonb": {"plpython3u", []string{"hstore", "public.jsonb"}},
		"plperl\n  transform for type hstore,\n  for type jsonb":      {"plperl", []string{"hstore", "jsonb"}},
	} {
		name, transform := SplitLanguage(in)
		if name != want.name || !reflect.DeepEqual(transform, want.transform) {
			t.Errorf("SplitLanguage(%q) = %q, %#v", in, name, transform)
		}
	}
	if got := RoutineTransform("plperl transform for type hstore", nil); !reflect.DeepEqual(got, []string{"hstore"}) {
		t.Errorf("a clause in the language declares the transforms: %#v", got)
	}
	if got := RoutineTransform("plperl transform for type hstore", []string{}); got == nil || len(got) != 0 {
		t.Errorf("the transform list wins over the language: %#v", got)
	}
	if got := RoutineTransform("plperl", nil); got != nil {
		t.Errorf("no declaration is unmanaged: %#v", got)
	}
}

func TestJSONSchemaAcceptsTransformAndEmptyBodies(t *testing.T) {
	s := compileEditorSchema(t)
	doc := `
schema app:
  function merge(a hstore, b jsonb):
    returns: jsonb
    language: plpython3u
    transform: [hstore, jsonb]
    body: |2
          a.update(b)
          return a
  function keys(h hstore):
    returns: text
    language: plperl
    transform: hstore
    body: return join(',', keys %{$_[0]});
  function plain(a jsonb):
    returns: jsonb
    language: plpython3u
    transform: []
    body: return a
  function noop():
    returns: void
    language: sql
    body: ""
  procedure log(doc jsonb):
    language: plpython3u
    transform: [jsonb]
    body: ""
  aggregate cat_all(text): {sfunc: app.cat, stype: text, initcond: "", minitcond: ""}
`
	if err := s.Validate(yamlToJSONValue(t, []byte(doc))); err != nil {
		t.Errorf("transform lists and empty bodies rejected:\n%v", err)
	}
	for _, bad := range []string{"transform: 1", "transform: [1]", "transform: {hstore: true}", `transform: ""`} {
		doc := "schema app:\n  function f():\n    returns: int\n    language: plperl\n    " + bad + "\n    body: return 1\n"
		if err := s.Validate(yamlToJSONValue(t, []byte(doc))); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

func TestAggregateInitCondPresenceParsed(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  aggregate empty(text):
    sfunc: app.cat
    stype: text
    initcond: ""
    minitcond: ""
  aggregate zero(int):
    sfunc: int4pl
    stype: int
    initcond: 0
  aggregate unset(int):
    sfunc: int4pl
    stype: int
  aggregate null_cond(int):
    sfunc: int4pl
    stype: int
    initcond:
`))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]struct {
		cond string
		has  bool
	}{
		"app.empty(text)":    {"", true},
		"app.zero(int)":      {"0", true},
		"app.unset(int)":     {"", false},
		"app.null_cond(int)": {"", false},
	} {
		ag := db.Aggregates[k]
		if ag == nil {
			t.Fatalf("missing aggregate %s; have %v", k, keys(db.Aggregates))
		}
		if ag.InitCond != want.cond || ag.HasInitCond() != want.has {
			t.Errorf("aggregate %s: initcond %q, declared %v", k, ag.InitCond, ag.HasInitCond())
		}
	}
	if v, ok := db.Aggregates["app.empty(text)"].Options["minitcond"]; !ok || v != "" {
		t.Errorf("minitcond = %q, declared %v", v, ok)
	}
	if _, ok := db.Aggregates["app.zero(int)"].Options["minitcond"]; ok {
		t.Error("an undeclared minitcond must stay out of the options")
	}
}
