package schema

import (
	"reflect"
	"testing"
)

const attributeOnlyYAML = `
extensions: [pgcrypto, pg_stat_statements, hstore]
schema public:
  function gen_random_bytes(integer):
    revokePublic: true
    grants: {app: [execute]}
  function crypt(text, text):
    parallel: restricted
    leakproof: true
    comment: hash a password
    owner: app_owner
  procedure tidy(integer):
    security: definer
    set: {search_path: public}
    grants: {app: [execute]}
  view pg_stat_statements:
    grants: {monitoring: [select], public: []}
    comment: statement statistics
  materialized view pg_summary:
    owner: app_owner
  type hstore:
    revokePublic: true
    grants: {app: [usage]}
    comment: key/value pairs
`

func TestAttributeOnlyDeclarationsParsed(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(attributeOnlyYAML))
	if err != nil {
		t.Fatal(err)
	}
	f := db.Functions["public.gen_random_bytes(integer)"]
	if f == nil || f.HasBody() || !f.RevokePublic || !reflect.DeepEqual(f.Grants, map[string][]string{"app": {"execute"}}) {
		t.Errorf("function without a body: %+v", f)
	}
	f = db.Functions["public.crypt(text, text)"]
	if f == nil || f.HasBody() || f.Parallel != "restricted" || f.Leakproof == nil || !*f.Leakproof || f.Comment != "hash a password" || f.Owner != "app_owner" {
		t.Errorf("function attributes without a body: %+v", f)
	}
	p := db.Procedures["public.tidy(integer)"]
	if p == nil || p.HasBody() || p.Security != "definer" || p.Set["search_path"] != "public" || p.Grants == nil {
		t.Errorf("procedure without a body: %+v", p)
	}
	v := db.Views["public.pg_stat_statements"]
	if v == nil || v.Query != "" || v.Materialized || v.Comment != "statement statistics" ||
		!reflect.DeepEqual(v.Grants, map[string][]string{"monitoring": {"select"}, "public": {}}) {
		t.Errorf("view without a query: %+v", v)
	}
	if mv := db.Views["public.pg_summary"]; mv == nil || mv.Query != "" || !mv.Materialized || mv.Owner != "app_owner" {
		t.Errorf("materialized view without a query: %+v", mv)
	}
	td := db.Types["public.hstore"]
	if td == nil || td.Kind != "" || !td.RevokePublic || td.Comment != "key/value pairs" || !reflect.DeepEqual(td.Grants, map[string][]string{"app": {"usage"}}) {
		t.Errorf("type without a kind: %+v", td)
	}
}

func TestJSONSchemaAcceptsAttributeOnlyDeclarations(t *testing.T) {
	s := compileEditorSchema(t)
	if err := s.Validate(yamlToJSONValue(t, []byte(attributeOnlyYAML))); err != nil {
		t.Errorf("declarations of objects an extension owns rejected:\n%v", err)
	}
}

func TestJSONSchemaKeepsDefinitionsComplete(t *testing.T) {
	s := compileEditorSchema(t)
	for name, y := range map[string]string{
		"function body without returns":      "schema public:\n  function f():\n    language: sql\n    body: select 1\n",
		"function body without language":     "schema public:\n  function f():\n    returns: int\n    body: select 1\n",
		"function returns without body":      "schema public:\n  function f():\n    returns: int\n",
		"function language without body":     "schema public:\n  function f():\n    language: sql\n",
		"function transform without body":    "schema public:\n  function f(hstore):\n    transform: [hstore]\n",
		"procedure body without language":    "schema public:\n  procedure p():\n    body: select 1\n",
		"procedure language without body":    "schema public:\n  procedure p():\n    language: sql\n",
		"view options without query":         "schema public:\n  view v:\n    securityInvoker: true\n",
		"view triggers without query":        "schema public:\n  view v:\n    triggers:\n      t: {timing: instead of, events: [insert], procedure: f()}\n",
		"materialized view indexes no query": "schema public:\n  materialized view v:\n    indexes:\n      i: {columns: [a]}\n",
		"enum labels without type":           "schema public:\n  type t:\n    labels: [a, b]\n",
		"composite attributes without type":  "schema public:\n  type t:\n    attributes: {a: int}\n",
		"range subtype without type":         "schema public:\n  type t:\n    subtype: int\n",
	} {
		if err := s.Validate(yamlToJSONValue(t, []byte(y))); err == nil {
			t.Errorf("%s: accepted, want a validation error", name)
		}
	}
}
