package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestDefaultPrivilegesGrantOption(t *testing.T) {
	d := &schema.Database{DefaultPrivileges: []*schema.DefaultPrivilege{{Schemas: []string{"app"}, Grants: map[string]map[string][]string{
		"sequences": {"reader": {"select*", "usage"}},
	}}}}
	live := emptyLive()
	live.CurrentUser = "owner"
	sql := strings.Join(planDefaultPrivileges(live, d), "\n")
	for _, want := range []string{
		`alter default privileges in schema "app" grant usage on sequences to "reader";`,
		`alter default privileges in schema "app" grant select on sequences to "reader" with grant option;`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	live.DefaultACLs = map[string]map[string]map[string]bool{"owner|app|sequences": {"reader": {"select": true, "usage*": true}}}
	live.DefaultACLRows = map[string]bool{"owner|app|sequences": true}
	sql = strings.Join(planDefaultPrivileges(live, d), "\n")
	if !strings.Contains(sql, `grant select on sequences to "reader" with grant option;`) || !strings.Contains(sql, `revoke grant option for usage on sequences from "reader";`) {
		t.Errorf("want grant-option changes, got:\n%s", sql)
	}
}

func TestTypeRevokePublic(t *testing.T) {
	d := &schema.Database{
		Types:   map[string]*schema.TypeDef{"app.e": {Name: "e", Kind: "enum", Labels: []string{"a"}, RevokePublic: true}},
		Domains: map[string]*schema.Domain{"app.pos": {Name: "pos", Type: "int", RevokePublic: true}},
	}
	sql := strings.Join(planGrants(emptyLive(), d), "\n")
	if !strings.Contains(sql, `revoke usage on type "app"."e" from public;`) || !strings.Contains(sql, `revoke usage on domain "app"."pos" from public;`) {
		t.Errorf("new types must revoke PUBLIC usage:\n%s", sql)
	}
	live := emptyLive()
	live.Types = map[string]bool{"app.e": true}
	live.Domains = map[string]bool{"app.pos": true}
	live.TypePublicRevoked = map[string]bool{"app.e": true, "app.pos": true}
	if sql := strings.Join(planGrants(live, d), "\n"); sql != "" {
		t.Errorf("already revoked must not diff:\n%s", sql)
	}
}

func TestServerGrants(t *testing.T) {
	d := &schema.Database{Servers: map[string]*schema.Server{"remote": {Name: "remote", Wrapper: "postgres_fdw", Grants: map[string][]string{"app": {"usage"}}}}}
	live := emptyLive()
	live.CurrentUser = "owner"
	if sql := strings.Join(planGrants(live, d), "\n"); sql != `grant usage on foreign server "remote" to "app";` {
		t.Errorf("new server: %s", sql)
	}
	live.Servers = map[string]*LiveServer{"remote": {Wrapper: "postgres_fdw", Owner: "owner", Grants: map[string]map[string]bool{"app": {"usage": true}, "old": {"usage": true}}}}
	if sql := strings.Join(planGrants(live, d), "\n"); sql != `revoke usage on foreign server "remote" from "old";` {
		t.Errorf("existing server: %s", sql)
	}
}

func TestDefaultPrivilegesKeepRoleCase(t *testing.T) {
	d := &schema.Database{DefaultPrivileges: []*schema.DefaultPrivilege{{Schemas: []string{"app"}, Grants: map[string]map[string][]string{
		"tables": {"Pgy_Reader": {"select"}},
	}}}}
	live := emptyLive()
	live.CurrentUser = "owner"
	if sql := strings.Join(planDefaultPrivileges(live, d), "\n"); sql != `alter default privileges in schema "app" grant select on tables to "Pgy_Reader";` {
		t.Errorf("got %s", sql)
	}
	live.DefaultACLs = map[string]map[string]map[string]bool{"owner|app|tables": {"Pgy_Reader": {"select": true}}}
	live.DefaultACLRows = map[string]bool{"owner|app|tables": true}
	if sql := strings.Join(planDefaultPrivileges(live, d), "\n"); sql != "" {
		t.Errorf("a granted mixed-case role must not diff: %s", sql)
	}
	created := CreatedGrants(live.DefaultACLs, "owner", "app.t", "tables")
	if !created["Pgy_Reader"]["select"] {
		t.Errorf("created grants: %v", created)
	}
}

func TestTableRevokeRegrantsColumnPrivileges(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name: "t", Grants: map[string][]string{},
		Columns: map[string]*schema.Column{"a": {Type: "int", Grants: map[string][]string{"reader": {"select"}}}, "b": {Type: "int"}},
	}}}
	live := liveWithTable("app.t", map[string]*LiveColumn{"a": {Type: "integer"}, "b": {Type: "integer"}})
	live.CurrentUser = "owner"
	live.TableGrants = map[string]map[string]map[string]bool{"app.t": {"reader": {"select": true}}}
	live.ColumnGrants = map[string]map[string]map[string]map[string]bool{"app.t": {"a": {"reader": {"select": true}}}}
	sql := strings.Join(planGrants(live, d), "\n")
	want := "revoke select on table \"app\".\"t\" from \"reader\";\ngrant select (\"a\") on table \"app\".\"t\" to \"reader\";"
	if sql != want {
		t.Errorf("got:\n%s\nwant:\n%s", sql, want)
	}
}
