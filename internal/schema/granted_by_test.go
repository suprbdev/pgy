package schema

import (
	"reflect"
	"testing"
)

const grantedByDoc = `
servers:
  wh:
    wrapper: postgres_fdw
    grantedBy: {admin: {reader: [usage]}}
foreignDataWrappers:
  postgres_fdw:
    grantedBy: {admin: {reader: [usage]}}
languages:
  plpgsql:
    grantedBy: {admin: {reader: [usage]}}
schema app:
  grantedBy: {admin: {reader: [usage]}}
  table doc:
    columns:
      id: {type: integer, grantedBy: {admin: {reader: [update]}}}
    grants: {admin: [select with grant option]}
    grantedBy:
      admin: {reader: [Select], lead: [select with grant option]}
      lead: {}
  view v:
    query: select 1 as x
    grantedBy: {admin: {reader: [select]}}
    columnGrantedBy: {x: {admin: {reader: [select]}}}
  sequence s:
    grantedBy: {admin: {reader: [usage]}}
  function f():
    returns: integer
    language: sql
    body: select 1
    grantedBy: {admin: {reader: [execute]}}
  procedure p():
    language: sql
    body: select 1
    grantedBy: {admin: {reader: [execute]}}
  aggregate agg(integer):
    sfunc: int4pl
    stype: integer
    grantedBy: {admin: {reader: [execute]}}
  type e:
    type: enum
    labels: [x]
    grantedBy: {admin: {reader: [usage]}}
  domain d:
    type: integer
    grantedBy: {admin: {reader: [usage]}}
`

func TestGrantedByParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(grantedByDoc))
	if err != nil {
		t.Fatal(err)
	}
	usage := GrantedBy{"admin": {"reader": {"usage"}}}
	execute := GrantedBy{"admin": {"reader": {"execute"}}}
	sel := GrantedBy{"admin": {"reader": {"select"}}}
	tbl := d.Tables["app.doc"]
	for name, c := range map[string]struct{ got, want GrantedBy }{
		"table":       {tbl.GrantedBy, GrantedBy{"admin": {"reader": {"select"}, "lead": {"select*"}}, "lead": {}}},
		"column":      {tbl.Columns["id"].GrantedBy, GrantedBy{"admin": {"reader": {"update"}}}},
		"view":        {d.Views["app.v"].GrantedBy, sel},
		"view column": {d.Views["app.v"].ColumnGrantedBy["x"], sel},
		"sequence":    {d.Sequences["app.s"].GrantedBy, usage},
		"function":    {d.Functions["app.f()"].GrantedBy, execute},
		"procedure":   {d.Procedures["app.p()"].GrantedBy, execute},
		"aggregate":   {d.Aggregates["app.agg(integer)"].GrantedBy, execute},
		"type":        {d.Types["app.e"].GrantedBy, usage},
		"domain":      {d.Domains["app.d"].GrantedBy, usage},
		"schema":      {d.SchemaGrantedBy["app"], usage},
		"server":      {d.Servers["wh"].GrantedBy, usage},
		"wrapper":     {d.ForeignDataWrappers["postgres_fdw"].GrantedBy, usage},
		"language":    {d.Languages["plpgsql"].GrantedBy, usage},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s grantedBy = %v, want %v", name, c.got, c.want)
		}
	}
	if !reflect.DeepEqual(tbl.Grants, map[string][]string{"admin": {"select*"}}) {
		t.Errorf("grants next to grantedBy = %v", tbl.Grants)
	}
}

func TestGrantedByParseFormats(t *testing.T) {
	want := GrantedBy{"admin": {"reader": {"select"}}}
	for name, doc := range map[string]string{
		"map":     "tables:\n  app.t:\n    columns: {a: {type: int}}\n    grantedBy: {admin: {reader: [select]}}\n",
		"list":    "tables:\n  - name: t\n    schema: app\n    columns: [{name: a, type: int}]\n    grantedBy: {admin: {reader: [select]}}\n",
		"schemas": "schemas:\n  app:\n    grantedBy: {admin: {reader: [select]}}\n    t:\n      columns: {a: {type: int}}\n      grantedBy: {admin: {reader: [select]}}\n",
	} {
		d, err := parseFlexibleDatabase([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := d.Tables["app.t"].GrantedBy; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: grantedBy = %v, want %v", name, got, want)
		}
		if name == "schemas" {
			if got := d.SchemaGrantedBy["app"]; !reflect.DeepEqual(got, want) {
				t.Errorf("schemas: schema grantedBy = %v, want %v", got, want)
			}
			if d.Tables["app.grantedBy"] != nil {
				t.Errorf("a schema's grantedBy is not a table")
			}
		}
	}
}

func TestJSONSchemaGrantedBy(t *testing.T) {
	s := compileEditorSchema(t)
	if err := s.Validate(yamlToJSONValue(t, []byte(grantedByDoc))); err != nil {
		t.Errorf("grantedBy blocks rejected:\n%v", err)
	}
	for i, doc := range []string{
		"schema app:\n  table t:\n    columns: {a: {type: int}}\n    grantedBy: {admin: [select]}\n",
		"schema app:\n  table t:\n    columns: {a: {type: int}}\n    grantedBy: [admin]\n",
		"languages:\n  plpgsql: {}\n",
	} {
		if err := s.Validate(yamlToJSONValue(t, []byte(doc))); err == nil {
			t.Errorf("invalid doc %d accepted", i)
		}
	}
}
