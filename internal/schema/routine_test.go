package schema

import (
	"reflect"
	"testing"
)

const overloadYAML = `
schema app:
  function area(r float8):
    returns: float8
    language: sql
    body: select pi() * r * r
  function area(w float8, h float8):
    returns: float8
    language: sql
    body: select w * h
  function area_label(r float8):
    returns: text
    language: sql
    body: select 'x'
  procedure log(msg text):
    language: sql
    body: select 1
  procedure log(msg text, level int):
    language: sql
    body: select 1
  table shapes:
    dependsOn: [function app.area]
    columns:
      id: {type: int}
  table labels:
    dependsOn: ["function app.area(w float8, h float8)"]
    columns:
      id: {type: int}
`

func TestFunctionOverloadsParsed(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(overloadYAML))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"app.area(r float8)", "app.area(w float8, h float8)", "app.area_label(r float8)"} {
		if db.Functions[k] == nil {
			t.Errorf("missing function %q; have %v", k, keys(db.Functions))
		}
	}
	for _, k := range []string{"app.log(msg text)", "app.log(msg text, level int)"} {
		if db.Procedures[k] == nil {
			t.Errorf("missing procedure %q; have %v", k, keys(db.Procedures))
		}
	}
}

func TestFunctionOverloadsDependsOnResolution(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(overloadYAML))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"function app.area":                    {"app.area(r float8)", "app.area(w float8, h float8)"},
		"function app.area(w float8,h float8)": {"app.area(w float8, h float8)"},
		"function app.area(x int)":             {"app.area(r float8)", "app.area(w float8, h float8)"},
		"function area":                        {"app.area(r float8)", "app.area(w float8, h float8)"},
		"function app.are":                     {},
		"procedure app.log(msg text)":          {"app.log(msg text)"},
		"app.area_label":                       {"app.area_label(r float8)"},
	}
	for raw, want := range cases {
		got := ResolveDependencyKeys(raw, db)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ResolveDependencyKeys(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestFunctionOverloadsOrderedBeforeDependents(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(overloadYAML))
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := TopologicalSort(db)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, e := range sorted {
		pos[e.Key] = i
	}
	if pos["app.area(r float8)"] > pos["app.shapes"] || pos["app.area(w float8, h float8)"] > pos["app.shapes"] {
		t.Errorf("every overload must precede a table depending on the bare name: %v", sorted)
	}
}

func TestFunctionKeyCompactsWhitespace(t *testing.T) {
	if got := FunctionKey("app.f", "( a int ,  b   text )"); got != "app.f(a int, b text)" {
		t.Errorf("got %q", got)
	}
	if got := RoutineName("app.f(a int)"); got != "app.f" {
		t.Errorf("got %q", got)
	}
}

func keys[V any](m map[string]V) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
