package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestGrantsBlocksCoverPublic(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	live.CurrentUser = "owner"
	live.Schemas["app"] = true
	live.Schemas["public"] = true
	live.TableGrants = map[string]map[string]map[string]bool{"app.t": {"public": {"insert": true}}}
	live.SchemaGrants = map[string]map[string]map[string]bool{"app": {"public": {"usage": true}}, "public": {"public": {"usage": true}}}
	live.Functions = map[string]bool{"app.f()": true}
	live.FunctionDefs = map[string]*LiveFunction{"app.f()": {Signature: "app.f()"}}
	live.FunctionPublicExec = map[string]bool{"app.f()": true}
	live.Types = map[string]bool{"app.e": true}
	d := &schema.Database{
		Tables:       map[string]*schema.Table{"app.t": {Name: "t", Grants: map[string][]string{"app": {"select"}}, Columns: map[string]*schema.Column{"a": {Type: "int"}}}},
		SchemaGrants: map[string]map[string][]string{"app": {"app": {"usage"}}, "public": {"app": {"usage"}}},
		Functions:    map[string]*schema.Function{"app.f()": {Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select 1", Grants: map[string][]string{"app": {"execute"}}}},
		Types:        map[string]*schema.TypeDef{"app.e": {Name: "e", Kind: "enum", Labels: []string{"a"}, Grants: map[string][]string{"public": {}}}},
	}
	stmts, warns := planGrantsWarned(live, d)
	sql := strings.Join(stmts, "\n")
	for _, want := range []string{
		`revoke insert on table "app"."t" from public;`,
		`revoke usage on schema "app" from public;`,
		`revoke usage on type "app"."e" from public;`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	for _, unwanted := range []string{`on schema "public" from public`, `on function "app"."f"() from public`} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, sql)
		}
	}
	if len(warns) != 2 {
		t.Errorf("want warnings for the table and schema app (not the type, which lists public): %v", warns)
	}

	d.Tables["app.t"].Grants["public"] = []string{"insert"}
	d.Functions["app.f()"].Grants["public"] = []string{}
	d.Functions["app.f()"].RevokePublic = true
	stmts, _ = planGrantsWarned(live, d)
	sql = strings.Join(stmts, "\n")
	if strings.Contains(sql, `on table "app"."t" from public`) {
		t.Errorf("a listed PUBLIC grant must stay:\n%s", sql)
	}
	if strings.Count(sql, `on function "app"."f"() from public`) != 1 {
		t.Errorf("public: [] on a function revokes its default EXECUTE once:\n%s", sql)
	}

	d.Tables["app.t"].Grants = nil
	if stmts, _ := planGrantsWarned(live, d); strings.Contains(strings.Join(stmts, "\n"), `on table "app"."t"`) {
		t.Error("a table without a grants block is unmanaged")
	}
}

func TestNewTableRevokesDefaultPublicGrant(t *testing.T) {
	live := emptyLive()
	live.CurrentUser = "owner"
	live.DefaultACLs = map[string]map[string]map[string]bool{"owner|app|tables": {"public": {"select": true}}}
	live.DefaultACLRows = map[string]bool{"owner|app|tables": true}
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Grants: map[string][]string{}, Columns: map[string]*schema.Column{"a": {Type: "int"}}}}}
	if sql := strings.Join(planGrants(live, d), "\n"); sql != `revoke select on table "app"."t" from public;` {
		t.Errorf("got %s", sql)
	}
}
