package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestGrantAllExpandList(t *testing.T) {
	for _, c := range []struct {
		privs, set, want []string
	}{
		{[]string{"all"}, []string{"execute"}, []string{"execute"}},
		{[]string{"all privileges"}, []string{"create", "usage"}, []string{"create", "usage"}},
		{[]string{"ALL  Privileges"}, []string{"usage"}, []string{"usage"}},
		{[]string{"all*"}, []string{"select", "update", "usage"}, []string{"select*", "update*", "usage*"}},
		{[]string{"all privileges*"}, []string{"usage"}, []string{"usage*"}},
		{[]string{"all", "select*"}, []string{"select", "update", "usage"}, []string{"select*", "update", "usage"}},
		{[]string{"all*", "select"}, []string{"select", "usage"}, []string{"select*", "usage*"}},
		{[]string{"all", "select"}, nil, []string{"all"}},
		{[]string{"all privileges", "select*"}, nil, []string{"all", "select*"}},
		{[]string{"all*", "select*"}, nil, []string{"all*"}},
	} {
		got, ok := expandAllList(c.privs, c.set)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("expandAllList(%q, %q) = %q, %v; want %q", c.privs, c.set, got, ok, c.want)
		}
	}
	if got, ok := expandAllList([]string{"select", "usage"}, []string{"usage"}); ok || !reflect.DeepEqual(got, []string{"select", "usage"}) {
		t.Errorf("a list without all must stay as it is, got %q, %v", got, ok)
	}
}

func TestGrantAllTablePrivilegesByVersion(t *testing.T) {
	if got := allPrivileges("table", 160000); strings.Contains(strings.Join(got, ","), "maintain") {
		t.Errorf("PostgreSQL 16 has no MAINTAIN, got %q", got)
	}
	if got := allPrivileges("table", 170000); !strings.Contains(strings.Join(got, ","), "maintain") {
		t.Errorf("PostgreSQL 17 has MAINTAIN, got %q", got)
	}
}

// grantAllDatabase declares every kind of object a grants block can be on,
// with ALL for role app.
func grantAllDatabase() *schema.Database {
	all := func(spelling string) map[string][]string { return map[string][]string{"app": {spelling}} }
	return &schema.Database{
		Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{
			"id": {Type: "int"},
			"v":  {Type: "int", Grants: all("all")},
		}, Grants: all("all privileges")}},
		Views:               map[string]*schema.View{"app.v": {Name: "v", Query: "select 1", Replace: false, Grants: all("all")}, "app.mv": {Name: "mv", Query: "select 1", Materialized: true, Grants: all("all*")}},
		Sequences:           map[string]*schema.Sequence{"app.s": {Name: "s", Grants: all("all")}},
		Functions:           map[string]*schema.Function{"app.f()": {Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select 1", Grants: all("all")}},
		Procedures:          map[string]*schema.Procedure{"app.p()": {Name: "p", ArgsSig: "()", Language: "sql", Body: "select 1", Grants: all("all")}},
		Types:               map[string]*schema.TypeDef{"app.e": {Name: "e", Kind: "enum", Labels: []string{"a"}, Grants: all("all")}},
		Domains:             map[string]*schema.Domain{"app.d": {Name: "d", Type: "int", Grants: all("all")}},
		SchemaGrants:        map[string]map[string][]string{"app": all("all")},
		Servers:             map[string]*schema.Server{"wh": {Name: "wh", Wrapper: "w", Grants: all("all")}},
		ForeignDataWrappers: map[string]*schema.ForeignDataWrapper{"w": {Name: "w", Grants: all("all privileges")}},
		Languages:           map[string]*schema.Language{"plpgsql": {Name: "plpgsql", Grants: all("all")}},
		DefaultPrivileges: []*schema.DefaultPrivilege{{Schemas: []string{"app"}, Grants: map[string]map[string][]string{
			"tables": all("all"), "sequences": all("all"), "functions": all("all"), "types": all("all"),
		}}, {Grants: map[string]map[string][]string{"schemas": all("all")}}},
	}
}

// grantAllLive is a database where everything grantAllDatabase declares
// exists, with the privileges PostgreSQL version stores for GRANT ALL.
func grantAllLive(version int) *Live {
	l := emptyLive()
	l.ServerVersion = version
	l.CurrentUser = "owner"
	acl := func(kind string, opt bool) map[string]map[string]bool {
		privs := map[string]bool{}
		for _, p := range allPrivileges(kind, version) {
			if opt {
				p += "*"
			}
			privs[p] = true
		}
		return map[string]map[string]bool{"app": privs}
	}
	l.Schemas["app"] = true
	l.SchemaGrants["app"] = acl("schema", false)
	l.Tables["app.t"] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer"}, "v": {Type: "integer"}}}
	l.TableGrants["app.t"] = acl("table", false)
	l.ColumnGrants["app.t"] = map[string]map[string]map[string]bool{"v": acl("column", false)}
	l.Views["app.v"] = true
	l.TableGrants["app.v"] = acl("table", false)
	l.MatViews["app.mv"] = true
	l.TableGrants["app.mv"] = acl("table", true)
	l.Sequences["app.s"] = true
	l.TableGrants["app.s"] = acl("sequence", false)
	l.Functions["app.f()"] = true
	l.FunctionDefs["app.f()"] = &LiveFunction{Signature: "app.f()", Kind: "f", Returns: "int", Language: "sql", Body: "select 1", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100}
	l.FunctionPublicExec["app.f()"] = true
	l.FunctionGrants["app.f()"] = acl("routine", false)
	l.Procedures["app.p()"] = true
	l.ProcedureDefs["app.p()"] = &LiveProcedure{Signature: "app.p()", Language: "sql", Body: "select 1", Security: "invoker"}
	l.FunctionPublicExec["app.p()"] = true
	l.FunctionGrants["app.p()"] = acl("routine", false)
	l.Types["app.e"] = true
	l.EnumLabels["app.e"] = []string{"a"}
	l.Domains["app.d"] = true
	l.DomainDefs = map[string]*LiveDomain{"app.d": {Checks: map[string]string{}}}
	l.TypeGrants = map[string]map[string]map[string]bool{"app.e": acl("type", false), "app.d": acl("type", false)}
	l.Wrappers = map[string]*LiveWrapper{"w": {Owner: "owner", Grants: acl("fdw", false)}}
	l.Servers = map[string]*LiveServer{"wh": {Wrapper: "w", Owner: "owner", Grants: acl("server", false)}}
	l.Languages = map[string]*LiveLanguage{"plpgsql": {Owner: "owner", Trusted: true, Grants: map[string]map[string]bool{"public": {"usage": true}, "app": {"usage": true}}}}
	l.DefaultACLs = map[string]map[string]map[string]bool{
		"owner|app|tables":    acl("table", false),
		"owner|app|sequences": acl("sequence", false),
		"owner|app|functions": acl("routine", false),
		"owner|app|types":     acl("type", false),
		"owner||schemas":      acl("schema", false),
	}
	l.DefaultACLRows = map[string]bool{"owner|app|tables": true, "owner|app|sequences": true, "owner|app|functions": true, "owner|app|types": true, "owner||schemas": true}
	return l
}

func grantStatements(p *PlanDiff) []string {
	out := []string{}
	for _, sec := range [][]string{p.Prelude, p.Creates, p.Alters, p.Drops} {
		for _, s := range sec {
			if strings.HasPrefix(s, "grant ") || strings.HasPrefix(s, "revoke ") || strings.HasPrefix(s, "alter default privileges") {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestGrantAllSkippedIfExpandedLive(t *testing.T) {
	for _, version := range []int{150000, 160000, 170000, 180000} {
		if got := grantStatements(Plan(grantAllLive(version), grantAllDatabase(), false)); len(got) > 0 {
			t.Errorf("PostgreSQL %d stores ALL expanded, which must compare equal, got:\n%s", version/10000, strings.Join(got, "\n"))
		}
	}
}

func TestGrantAllGrantsMaintainAfterUpgrade(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}, Grants: map[string][]string{"app": {"all"}}}}}
	live := grantAllLive(160000)
	live.ServerVersion = 170000
	got := grantStatements(Plan(live, d, false))
	if want := []string{`grant maintain on table "app"."t" to "app";`}; !reflect.DeepEqual(got, want) {
		t.Errorf("PostgreSQL 17 adds MAINTAIN to ALL: got %q, want %q", got, want)
	}
}

func TestGrantAllCreate(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 170000
	live.CurrentUser = "owner"
	live.Schemas["app"] = true
	d := &schema.Database{
		Tables:    map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int", Grants: map[string][]string{"app": {"all"}}}}, Grants: map[string][]string{"app": {"all privileges*"}}}},
		Sequences: map[string]*schema.Sequence{"app.s": {Name: "s", Grants: map[string][]string{"app": {"all"}}}},
	}
	sql := Render(Plan(live, d, false))
	for _, want := range []string{
		`grant delete, insert, maintain, references, select, trigger, truncate, update on table "app"."t" to "app" with grant option;`,
		`grant insert ("id"), references ("id"), select ("id"), update ("id") on table "app"."t" to "app";`,
		`grant select, update, usage on sequence "app"."s" to "app";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
}

func TestGrantAllRevokesWhatTheKindLacks(t *testing.T) {
	live := grantAllLive(160000)
	live.TableGrants["app.s"]["app"]["delete"] = true
	d := &schema.Database{Sequences: map[string]*schema.Sequence{"app.s": {Name: "s", Grants: map[string][]string{"app": {"all"}}}}}
	if got, want := grantStatements(Plan(live, d, false)), []string{`revoke delete on sequence "app"."s" from "app";`}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGrantAllKeptWithoutServer(t *testing.T) {
	d := grantAllDatabase()
	sql := Render(Plan(emptyLive(), d, false))
	d.DefaultPrivileges = nil
	sql += Render(Plan(emptyLive(), d, false))
	for _, want := range []string{
		`alter default privileges in schema "app" grant all on tables to "app";`,
		`grant all on table "app"."t" to "app";`,
		`grant all ("v") on table "app"."t" to "app";`,
		`grant all on table "app"."mv" to "app" with grant option;`,
		`grant all on schema "app" to "app";`,
		`grant all on foreign data wrapper "w" to "app";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "all privileges") {
		t.Errorf("all privileges is written as all, got\n%s", sql)
	}
}

func TestGrantAllSimulated(t *testing.T) {
	for _, version := range []int{0, 160000, 180000} {
		d := grantAllDatabase()
		live := emptyLive()
		live.ServerVersion = version
		live.CurrentUser = "owner"
		after := Simulate(live, d)
		if got := grantStatements(Plan(after, d, false)); len(got) > 0 {
			t.Errorf("version %d: a plan after the simulated one must not grant again, got:\n%s", version, strings.Join(got, "\n"))
		}
	}
}

func TestGrantAllLeavesDesiredAlone(t *testing.T) {
	d := grantAllDatabase()
	Plan(grantAllLive(170000), d, false)
	Simulate(grantAllLive(170000), d)
	if got := d.Tables["app.t"].Grants["app"]; !reflect.DeepEqual(got, []string{"all privileges"}) {
		t.Errorf("the declaration must not change, got %q", got)
	}
	if got := d.DefaultPrivileges[0].Grants["tables"]["app"]; !reflect.DeepEqual(got, []string{"all"}) {
		t.Errorf("the declaration must not change, got %q", got)
	}
}
