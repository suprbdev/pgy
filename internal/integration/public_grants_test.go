package integration

import (
	"context"
	"testing"
)

func TestIntegrationGrantsBlocksCoverPublic(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	y := `
schema SCH:
  table t:
    columns: {id: {type: int, primaryKey: true}}
    grants: {}
  function f():
    returns: int
    language: sql
    body: select 1
    grants: {public: []}
  type e:
    type: enum
    labels: [a]
    grants: {public: [usage]}
`
	convergeYAML(t, pool, sch, y)
	if _, err := pool.Exec(ctx, `grant insert on `+sch+`.t to public`); err != nil {
		t.Fatal(err)
	}
	if sql := convergeYAML(t, pool, sch, y); !contains(sql, "revoke insert on table") {
		t.Errorf("want the drifted PUBLIC grant revoked, got:\n%s", sql)
	}
	var tableInsert, funcExec, typeUsage bool
	if err := pool.QueryRow(ctx, `select has_table_privilege('public', $1::regclass, 'insert'),
		has_function_privilege('public', $2::regprocedure, 'execute'),
		has_type_privilege('public', $3::regtype, 'usage')`, sch+".t", sch+".f()", sch+".e").Scan(&tableInsert, &funcExec, &typeUsage); err != nil {
		t.Fatal(err)
	}
	if tableInsert || funcExec || !typeUsage {
		t.Errorf("PUBLIC: table insert %v, function execute %v, type usage %v", tableInsert, funcExec, typeUsage)
	}
	var schemaUsage bool
	if err := pool.QueryRow(ctx, `select has_schema_privilege('public', 'public', 'usage')`).Scan(&schemaUsage); err != nil {
		t.Fatal(err)
	}
	if !schemaUsage {
		t.Error("the public schema's PUBLIC usage must be left alone")
	}
}
