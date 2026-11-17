package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
)

func TestIntegrationGrantAllConverges(t *testing.T) {
	admin := connect(t)
	app := freshRole(t, admin, "app")
	db := freshDatabase(t, admin, "pgytest_grant_all")
	ctx := context.Background()
	y := strings.ReplaceAll(`
foreignDataWrappers:
  pgyall:
    grants: {APP: [all privileges]}
servers:
  wh:
    wrapper: pgyall
    grants: {APP: [all]}
languages:
  plpgsql:
    grants: {APP: [all]}
defaultPrivileges:
  - in: [app]
    tables: {APP: [all]}
    sequences: {APP: [all]}
    functions: {APP: [all]}
    types: {APP: [all]}
  - schemas: {APP: [all with grant option]}
schema app:
  grants: {APP: [all]}
  table t:
    columns:
      id: {type: integer}
      v: {type: integer, grants: {APP: [all]}}
    grants: {APP: [all privileges]}
  table u:
    columns:
      id: {type: integer}
    grants: {APP: [all, select with grant option]}
  view v:
    query: select id from app.t
    dependsOn: [app.t]
    grants: {APP: [all]}
  materialized view mv:
    query: select id from app.t
    dependsOn: [app.t]
    grants: {APP: [all with grant option]}
  sequence s:
    grants: {APP: [all]}
  function f():
    returns: int
    language: sql
    body: select 1
    grants: {APP: [all]}
  procedure p():
    language: sql
    body: select 1
    grants: {APP: [all]}
  type e:
    type: enum
    labels: [a, b]
    grants: {APP: [all]}
  domain d:
    type: int
    grants: {APP: [all]}
`, "APP", app)
	convergeLive(t, db, y, diff.Options{})

	var version int
	if err := db.QueryRow(ctx, "select current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	var tableACL, uACL string
	if err := db.QueryRow(ctx, `select array_to_string(relacl, ',') from pg_class where oid = 'app.t'::regclass`).Scan(&tableACL); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select array_to_string(relacl, ',') from pg_class where oid = 'app.u'::regclass`).Scan(&uACL); err != nil {
		t.Fatal(err)
	}
	maintain := ""
	if version >= 170000 {
		maintain = "m"
	}
	if want := app + "=arwdDxt" + maintain + "/"; !strings.Contains(tableACL, want) {
		t.Errorf("app.t ACL = %q, want %s", tableACL, want)
	}
	if want := app + "=ar*wdDxt" + maintain + "/"; !strings.Contains(uACL, want) {
		t.Errorf("app.u ACL = %q, want %s", uACL, want)
	}

	res, err := pull.Build(introspectAll(t, db), pull.Options{Schemas: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	pulled := ""
	for _, f := range res.Files {
		pulled += string(f.Content)
	}
	privs := "delete, insert, references, select, trigger, truncate, update"
	if version >= 170000 {
		privs = "delete, insert, maintain, references, select, trigger, truncate, update"
	}
	if !strings.Contains(pulled, app+": ["+privs+"]") {
		t.Errorf("pull writes the expanded privileges, want %s: [%s] in:\n%s", app, privs, pulled)
	}
}
