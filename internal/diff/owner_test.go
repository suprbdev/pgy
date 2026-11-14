package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestOwnersAltered(t *testing.T) {
	d := &schema.Database{
		Tables:       map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}, Owner: "app_owner"}},
		Views:        map[string]*schema.View{"app.mv": {Name: "mv", Materialized: true, Query: "select 1", Owner: "app_owner"}},
		Functions:    map[string]*schema.Function{"app.f(a int)": {Name: "f", Schema: "app", ArgsSig: "(a int default 1)", Returns: "int", Language: "sql", Body: "select a", Owner: "app_owner"}},
		Types:        map[string]*schema.TypeDef{"app.mood": {Name: "mood", Kind: "enum", Labels: []string{"ok"}, Owner: "app_owner"}},
		SchemaOwners: map[string]string{"app": "app_owner"},
	}
	live := emptyLive()
	live.Schemas["app"] = true
	live.Owners = map[string]string{"schema:app": "postgres", "rel:app.t": "app_owner"}
	p := Plan(live, d, false)
	for _, want := range []string{
		`alter schema "app" owner to "app_owner";`,
		`alter type "app"."mood" owner to "app_owner";`,
		`alter materialized view "app"."mv" owner to "app_owner";`,
		`alter function "app"."f"(a int) owner to "app_owner";`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
	if findAlter(p, `alter table "app"."t" owner`) {
		t.Errorf("table already owned by app_owner, got %v", p.Alters)
	}
}

func TestDefaultPrivileges(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{}, DefaultPrivileges: []*schema.DefaultPrivilege{
		{ForRole: "app_owner", Schemas: []string{"app"}, Grants: map[string]map[string][]string{
			"tables":    {"app_read": {"select"}, "app_rw": {"insert", "select", "update"}},
			"sequences": {"app_rw": {"usage"}},
		}},
		{ForRole: "app_owner", Grants: map[string]map[string][]string{"functions": {"public": {}}}},
	}}
	live := emptyLive()
	live.Schemas["app"] = true
	live.DefaultACLs = map[string]map[string]map[string]bool{"app_owner|app|tables": {"app_read": {"select": true, "delete": true}}}
	live.DefaultACLRows = map[string]bool{"app_owner|app|tables": true}
	p := Plan(live, d, false)
	want := []string{
		`alter default privileges for role "app_owner" in schema "app" revoke delete on tables from "app_read";`,
		`alter default privileges for role "app_owner" revoke execute on functions from public;`,
		`alter default privileges for role "app_owner" in schema "app" grant usage on sequences to "app_rw";`,
		`alter default privileges for role "app_owner" in schema "app" grant insert, select, update on tables to "app_rw";`,
	}
	got := map[string]bool{}
	for _, s := range p.Creates {
		got[s] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing %q in %v", w, p.Creates)
		}
	}
	live.DefaultACLs = map[string]map[string]map[string]bool{
		"app_owner|app|tables":    {"app_read": {"select": true}, "app_rw": {"insert": true, "select": true, "update": true}},
		"app_owner|app|sequences": {"app_rw": {"usage": true}},
	}
	live.DefaultACLRows = map[string]bool{"app_owner|app|tables": true, "app_owner|app|sequences": true, "app_owner||functions": true}
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("matching default privileges must not diff, got:\n%s", sql)
	}
}

func TestDefaultPrivilegesCreateTheirSchemaFirst(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{}, DefaultPrivileges: []*schema.DefaultPrivilege{
		{Schemas: []string{"app"}, Grants: map[string]map[string][]string{"tables": {"reader": {"select"}}}},
	}}
	p := Plan(emptyLive(), d, false)
	if len(p.Creates) < 2 || p.Creates[0] != `create schema if not exists "app";` {
		t.Errorf("the schema must be created before its default privileges, got %v", p.Creates)
	}
}
