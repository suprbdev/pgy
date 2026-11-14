package schema

import "testing"

func TestOwnersAndDefaultPrivilegesParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
defaultPrivileges:
  - for: app_owner
    in: app
    tables: {app_read: [SELECT]}
  - functions: {public: []}
schema app:
  owner: app_owner
  table t:
    owner: app_owner
    columns: {id: {type: int}}
  function f():
    returns: int
    language: sql
    body: select 1
    owner: app_owner
  view v:
    query: select 1
    owner: app_owner
schemas:
  billing:
    owner: billing_owner
    invoices:
      columns: {id: {type: int}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if db.SchemaOwners["app"] != "app_owner" || db.SchemaOwners["billing"] != "billing_owner" {
		t.Errorf("schema owners: %v", db.SchemaOwners)
	}
	if db.Tables["billing.invoices"] == nil {
		t.Error("owner key must not be parsed as a table in the schemas map form")
	}
	if db.Tables["app.t"].Owner != "app_owner" || db.Functions["app.f()"].Owner != "app_owner" || db.Views["app.v"].Owner != "app_owner" {
		t.Error("object owners not parsed")
	}
	if len(db.DefaultPrivileges) != 2 {
		t.Fatalf("want 2 default privilege entries, got %d", len(db.DefaultPrivileges))
	}
	dp := db.DefaultPrivileges[0]
	if dp.ForRole != "app_owner" || len(dp.Schemas) != 1 || dp.Schemas[0] != "app" || dp.Grants["tables"]["app_read"][0] != "select" {
		t.Errorf("unexpected default privileges: %+v", dp)
	}
	if fp := db.DefaultPrivileges[1].Grants["functions"]; fp == nil || fp["public"] == nil || len(fp["public"]) != 0 {
		t.Errorf("empty list must be kept as a revoke-all: %+v", fp)
	}
}
