package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// roleAdminLive is a server of the given version where the connecting role
// app has LOGIN and CREATEROLE and no membership, pgy is the bootstrap
// superuser, and plat, member and root (a superuser) exist.
func roleAdminLive(version int) *Live {
	live := emptyLive()
	live.ServerVersion = version
	live.CurrentUser = "app"
	live.OwnerRoles = map[string]bool{"app": true}
	live.RoleDefs = map[string]*LiveRole{
		"app":    {Login: true, CreateRole: true, Inherit: true, ConnectionLimit: -1},
		"pgy":    {Login: true, Superuser: true, Inherit: true, ConnectionLimit: -1, Bootstrap: true},
		"plat":   {Inherit: true, ConnectionLimit: -1},
		"member": {Inherit: true, ConnectionLimit: -1},
		"root":   {Superuser: true, Inherit: true, ConnectionLimit: -1},
	}
	for name := range live.RoleDefs {
		live.Roles[name] = true
	}
	live.RoleMemberOptions = map[string]map[string]*LiveMembership{}
	return live
}

func addMembership(live *Live, member, parent string, admin bool, grantors ...string) {
	if live.RoleMembers[member] == nil {
		live.RoleMembers[member] = map[string]bool{}
	}
	live.RoleMembers[member][parent] = true
	if live.RoleMemberOptions[member] == nil {
		live.RoleMemberOptions[member] = map[string]*LiveMembership{}
	}
	live.RoleMemberOptions[member][parent] = &LiveMembership{Admin: admin, Inherit: true, Set: true, Grantors: grantors}
}

func roleDecl(name string) *schema.Role {
	return &schema.Role{Name: name, ConnectionLimit: -1, Managed: map[string]bool{}}
}

func rolesDB(roles ...*schema.Role) *schema.Database {
	d := &schema.Database{Tables: map[string]*schema.Table{}, Roles: map[string]*schema.Role{}}
	for _, r := range roles {
		d.Roles[r.Name] = r
	}
	return d
}

func statementCount(p *PlanDiff) int {
	if sql := strings.TrimSpace(Render(p)); sql != "" {
		return strings.Count(sql, "\n") + 1
	}
	return 0
}

func wantRoleWarnings(t *testing.T, p *PlanDiff, want ...string) {
	t.Helper()
	got := notOwnerWarnings(p)
	if len(got) != len(want) {
		t.Fatalf("want %d not_owner warnings, got %q", len(want), got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("warning %d:\n got %q\nwant %q...", i, got[i], want[i])
		}
	}
}

func TestRoleAdminSettingsLeftOutWithoutAdminOption(t *testing.T) {
	plat := roleDecl("plat")
	plat.Set = map[string]string{"statement_timeout": "8s"}
	d := rolesDB(plat)
	p := PlanWith(roleAdminLive(180000), d, Options{})
	if sql := Render(p); sql != "" {
		t.Errorf("the statement is left out, got:\n%s", sql)
	}
	wantRoleWarnings(t, p, `role plat: app, the connecting role, does not have the ADMIN option on it, which PostgreSQL 16 and later need, with the CREATEROLE attribute, to alter a role or comment on it, so the plan leaves out the statement (alter role "plat" set statement_timeout = '8s';); have a superuser run it`)

	live := roleAdminLive(180000)
	addMembership(live, "app", "plat", true, "pgy")
	p = PlanWith(live, d, Options{})
	wantSQL(t, Render(p), `alter role "plat" set statement_timeout = '8s';`)
	if w := notOwnerWarnings(p); len(w) > 0 {
		t.Errorf("with the ADMIN option there is no warning, got %q", w)
	}

	live = roleAdminLive(150000)
	if p := PlanWith(live, d, Options{}); Render(p) == "" || len(notOwnerWarnings(p)) > 0 {
		t.Errorf("on PostgreSQL 15 CREATEROLE is enough, got:\n%s%q", Render(p), notOwnerWarnings(p))
	}
	live.RoleDefs["app"].CreateRole = false
	wantRoleWarnings(t, PlanWith(live, d, Options{}), "role plat: app, the connecting role, does not have the CREATEROLE attribute, which altering a role or commenting on it needs, so the plan leaves out the statement")
}

func TestRoleAdminAdminOptionThroughAnotherRole(t *testing.T) {
	plat := roleDecl("plat")
	plat.Set = map[string]string{"work_mem": "8MB"}
	live := roleAdminLive(160000)
	live.RoleDefs["admins"] = &LiveRole{}
	addMembership(live, "app", "admins", false)
	addMembership(live, "admins", "plat", true, "pgy")
	p := PlanWith(live, rolesDB(plat), Options{})
	wantSQL(t, Render(p), `alter role "plat" set work_mem = '8MB';`)
	if w := notOwnerWarnings(p); len(w) > 0 {
		t.Errorf("a role the connecting role is a member of holds the ADMIN option, got %q", w)
	}
	live.RoleMemberOptions["admins"]["plat"].Admin = false
	if p := PlanWith(live, rolesDB(plat), Options{}); Render(p) != "" || len(notOwnerWarnings(p)) != 1 {
		t.Errorf("a membership without the ADMIN option doesn't give it, got:\n%s%q", Render(p), notOwnerWarnings(p))
	}
}

func TestRoleAdminSuperuserRoleLeftOut(t *testing.T) {
	root := roleDecl("root")
	root.Set = map[string]string{"work_mem": "8MB"}
	root.Comment = "the platform"
	member := roleDecl("member")
	member.InRoles = []string{"root"}
	d := rolesDB(root, member)
	for _, version := range []int{150000, 180000} {
		p := PlanWith(roleAdminLive(version), d, Options{})
		if sql := Render(p); sql != "" {
			t.Errorf("%d: every statement on a superuser is left out, got:\n%s", version, sql)
		}
		wantRoleWarnings(t, p, `role root: app, the connecting role, is not a superuser, and only a superuser may change a superuser role, so the plan leaves out the 3 statements (grant "root" to "member"; ...); have a superuser run them`)
	}
	live := roleAdminLive(180000)
	live.RoleDefs["app"].Superuser = true
	if p := PlanWith(live, d, Options{}); statementCount(p) != 3 || len(notOwnerWarnings(p)) > 0 {
		t.Errorf("a superuser plans everything, got:\n%s%q", Render(p), notOwnerWarnings(p))
	}
}

func TestRoleAdminMembershipLeftOut(t *testing.T) {
	member := roleDecl("member")
	member.InRoles = []string{"plat", "pg_read_all_data"}
	d := rolesDB(member)
	p := PlanWith(roleAdminLive(170000), d, Options{})
	if sql := Render(p); sql != "" {
		t.Errorf("both grants are left out, got:\n%s", sql)
	}
	wantRoleWarnings(t, p,
		`role pg_read_all_data: app, the connecting role, does not have the ADMIN option on it, which PostgreSQL 16 and later need to grant or revoke a role, so the plan leaves out the statement (grant "pg_read_all_data" to "member";)`,
		`role plat: app, the connecting role, does not have the ADMIN option on it, which PostgreSQL 16 and later need to grant or revoke a role, so the plan leaves out the statement (grant "plat" to "member";)`)

	live := roleAdminLive(170000)
	live.RoleDefs["app"].CreateRole = false
	addMembership(live, "app", "plat", true, "pgy")
	p = PlanWith(live, d, Options{})
	wantSQL(t, Render(p), `grant "plat" to "member";`)
	wantRoleWarnings(t, p, "role pg_read_all_data: ")

	live = roleAdminLive(150000)
	if p := PlanWith(live, d, Options{}); strings.Count(Render(p), "grant ") != 2 || len(notOwnerWarnings(p)) > 0 {
		t.Errorf("on PostgreSQL 15 CREATEROLE is enough, got:\n%s%q", Render(p), notOwnerWarnings(p))
	}
	live.RoleDefs["app"].CreateRole = false
	addMembership(live, "app", "plat", true, "pgy")
	p = PlanWith(live, d, Options{})
	wantSQL(t, Render(p), `grant "plat" to "member";`)
	wantRoleWarnings(t, p, "role pg_read_all_data: app, the connecting role, has neither the CREATEROLE attribute nor the ADMIN option on it, one of which granting or revoking a role needs")
}

func TestRoleAdminMembershipOptionsAndRevokes(t *testing.T) {
	no := false
	member := roleDecl("member")
	member.InRoles = []string{"plat"}
	member.MemberOptions = map[string]*schema.MembershipOptions{"plat": {Admin: &no}}
	live := roleAdminLive(180000)
	live.RoleDefs["old"], live.Roles["old"] = &LiveRole{}, true
	addMembership(live, "member", "plat", true, "pgy")
	addMembership(live, "member", "old", false, "app", "pgy")
	addMembership(live, "app", "old", true, "pgy")
	p := PlanWith(live, rolesDB(member), Options{Unsafe: true, Prune: true})
	wantSQL(t, Render(p), `revoke "old" from "member" granted by "app";`)
	wantRoleWarnings(t, p,
		`role old: app, the connecting role, does not have the privileges of pgy, which granted the membership the statement revokes, so the plan leaves out the statement (revoke "old" from "member" granted by "pgy";)`,
		`role plat: app, the connecting role, does not have the ADMIN option on it, which PostgreSQL 16 and later need to grant or revoke a role, so the plan leaves out the statement (revoke admin option for "plat" from "member" granted by "pgy";)`)
}

func TestRoleAdminAttributes(t *testing.T) {
	decl := func(set func(r *schema.Role)) *schema.Database {
		r := roleDecl("plat")
		set(r)
		return rolesDB(r)
	}
	login := decl(func(r *schema.Role) { r.Login, r.Managed["login"] = true, true })
	bypass := decl(func(r *schema.Role) {
		r.Login, r.BypassRLS, r.Managed["login"], r.Managed["bypassRLS"] = true, true, true, true
	})
	both := decl(func(r *schema.Role) {
		r.CreateDB, r.Replication, r.Managed["createdb"], r.Managed["replication"] = true, true, true, true
	})
	super := decl(func(r *schema.Role) { r.Superuser, r.Managed["superuser"] = true, true })

	live := roleAdminLive(160000)
	addMembership(live, "app", "plat", true, "pgy")
	wantSQL(t, Render(PlanWith(live, login, Options{})), `alter role "plat" with login;`)
	wantRoleWarnings(t, PlanWith(live, bypass, Options{}), `role plat: app, the connecting role, does not have the BYPASSRLS attribute, which PostgreSQL 16 and later need to change it on another role, so the plan leaves out the statement (alter role "plat" with login bypassrls;)`)
	wantRoleWarnings(t, PlanWith(live, both, Options{}), "role plat: app, the connecting role, does not have the CREATEDB and REPLICATION attributes, which PostgreSQL 16 and later need to change them on another role")
	live.RoleDefs["app"].BypassRLS, live.RoleDefs["app"].CreateDB = true, true
	wantSQL(t, Render(PlanWith(live, bypass, Options{})), `alter role "plat" with login bypassrls;`)
	wantRoleWarnings(t, PlanWith(live, both, Options{}), "role plat: app, the connecting role, does not have the REPLICATION attribute")
	wantRoleWarnings(t, PlanWith(live, super, Options{}), "role plat: app, the connecting role, is not a superuser, and only a superuser may change the SUPERUSER attribute")

	live = roleAdminLive(150000)
	wantSQL(t, Render(PlanWith(live, login, Options{})), `alter role "plat" with login;`)
	wantRoleWarnings(t, PlanWith(live, bypass, Options{}), "role plat: app, the connecting role, is not a superuser, and before PostgreSQL 16 only a superuser may alter a replication role or change the REPLICATION or BYPASSRLS attribute")
	live.RoleDefs["plat"].Replication = true
	wantRoleWarnings(t, PlanWith(live, login, Options{}), "role plat: app, the connecting role, is not a superuser, and before PostgreSQL 16")
	live.RoleDefs["plat"].Replication, live.RoleDefs["app"].CreateRole = false, false
	wantRoleWarnings(t, PlanWith(live, login, Options{}), "role plat: app, the connecting role, does not have the CREATEROLE attribute")
}

func TestRoleAdminOwnRole(t *testing.T) {
	app := roleDecl("app")
	app.Login, app.Managed["login"], app.ConnectionLimit, app.Managed["connectionLimit"] = true, true, 5, true
	app.Set = map[string]string{"work_mem": "8MB"}
	app.Comment = "deployer"
	d := rolesDB(app)
	d.DatabaseRoleSettings = map[string]map[string]string{"current_user": {"statement_timeout": "5s"}}
	p := PlanWith(roleAdminLive(180000), d, Options{})
	wantSQL(t, Render(p),
		`alter role "app" set work_mem = '8MB';`,
		`do $$ begin execute format('alter role current_user in database %I set statement_timeout = ''5s''', current_database()); end $$;`)
	wantRoleWarnings(t, p, `role app: app, the connecting role, does not have the ADMIN option on it, which PostgreSQL 16 and later need, with the CREATEROLE attribute, to alter a role or comment on it, so the plan leaves out the 2 statements (alter role "app" with connection limit 5; ...); have a superuser run them`)
	if p := PlanWith(roleAdminLive(150000), d, Options{}); statementCount(p) != 4 || len(notOwnerWarnings(p)) > 0 {
		t.Errorf("on PostgreSQL 15 CREATEROLE lets a role alter itself, got:\n%s%q", Render(p), notOwnerWarnings(p))
	}
}

func TestRoleAdminDatabaseRoleSettingsAndComment(t *testing.T) {
	plat := roleDecl("plat")
	plat.Comment = "platform"
	d := rolesDB(plat)
	d.DatabaseRoleSettings = map[string]map[string]string{"plat": {"statement_timeout": "5s"}}
	p := PlanWith(roleAdminLive(180000), d, Options{})
	if sql := Render(p); sql != "" {
		t.Errorf("both statements are left out, got:\n%s", sql)
	}
	wantRoleWarnings(t, p, `role plat: app, the connecting role, does not have the ADMIN option on it, which PostgreSQL 16 and later need, with the CREATEROLE attribute, to alter a role or comment on it, so the plan leaves out the 2 statements (`)
}

func TestRoleAdminRolesThePlanCreates(t *testing.T) {
	fresh := roleDecl("fresh")
	fresh.Set = map[string]string{"work_mem": "8MB"}
	fresh.Comment = "new"
	member := roleDecl("member")
	member.InRoles = []string{"fresh"}
	d := rolesDB(fresh, member)
	p := PlanWith(roleAdminLive(180000), d, Options{})
	for _, want := range []string{`create role "fresh"`, `alter role "fresh" set work_mem = '8MB';`, `comment on role "fresh" is 'new';`, `grant "fresh" to "member";`} {
		if !strings.Contains(Render(p), want) {
			t.Errorf("missing %q in:\n%s", want, Render(p))
		}
	}
	if w := notOwnerWarnings(p); len(w) > 0 {
		t.Errorf("the creator of a role may change it, got %q", w)
	}

	sim := Simulate(roleAdminLive(180000), rolesDB(fresh))
	if !sim.adminOf("fresh") || !sim.CreatorGrant("app", "fresh") {
		t.Fatalf("a simulated CREATE ROLE gives its creator the ADMIN option: %+v", sim.RoleMemberOptions["app"])
	}
	p = PlanWith(sim, d, Options{})
	wantSQL(t, Render(p), `grant "fresh" to "member";`)
	if w := notOwnerWarnings(p); len(w) > 0 {
		t.Errorf("a later step may grant it, got %q", w)
	}
	if sim := Simulate(roleAdminLive(150000), rolesDB(fresh)); sim.RoleMembers["app"]["fresh"] {
		t.Errorf("PostgreSQL 15 gives the creator no membership")
	}
}

func TestRoleAdminUnknownKeepsEverything(t *testing.T) {
	plat := roleDecl("plat")
	plat.Set = map[string]string{"work_mem": "8MB"}
	d := rolesDB(plat)
	for name, adjust := range map[string]func(*Live){
		"superuser":     func(l *Live) { l.RoleDefs["app"].Superuser = true },
		"unknown":       func(l *Live) { l.OwnerRoles = nil },
		"no attributes": func(l *Live) { delete(l.RoleDefs, "app") },
		"from-empty":    func(l *Live) { l.Offline = true },
	} {
		live := roleAdminLive(180000)
		adjust(live)
		p := PlanWith(live, d, Options{})
		if !strings.Contains(Render(p), `alter role "plat" set work_mem = '8MB';`) || len(notOwnerWarnings(p)) > 0 {
			t.Errorf("%s: the statement stays:\n%s\n%q", name, Render(p), notOwnerWarnings(p))
		}
	}
}

func TestRoleAdminExternalRole(t *testing.T) {
	member := roleDecl("member")
	member.InRoles = []string{"plat"}
	p := PlanWith(roleAdminLive(180000), rolesDB(member), Options{External: &schema.External{Roles: []string{"plat"}}})
	if sql := Render(p); sql != "" {
		t.Errorf("the grant is left out, got:\n%s", sql)
	}
	wantRoleWarnings(t, p, "role plat, which the config's external.roles lists: app, the connecting role, does not have the ADMIN option on it")
}

func TestRoleStatement(t *testing.T) {
	for _, tc := range []struct {
		stmt string
		want roleChange
		ok   bool
	}{
		{`alter role "a" with login nobypassrls connection limit 3 valid until 'nosuperuser';`, roleChange{role: "a", kind: roleAttributes, attrs: map[string]bool{"login": true, "bypassrls": true}}, true},
		{`alter role "a" with valid until 'infinity';`, roleChange{role: "a", kind: roleAttributes, attrs: map[string]bool{}}, true},
		{`alter role "a""b" set search_path = app, public;`, roleChange{role: `a"b`, kind: roleSettings}, true},
		{`alter role "a" reset work_mem;`, roleChange{role: "a", kind: roleSettings}, true},
		{`alter role current_user set work_mem = '8MB';`, roleChange{role: "me", kind: roleSettings}, true},
		{`do $$ begin execute format('alter role "it''s 100%%" in database %I set work_mem = ''8MB''', current_database()); end $$;`, roleChange{role: "it's 100%", kind: roleSettings}, true},
		{`do $$ begin execute format('alter database %I set work_mem = ''8MB''', current_database()); end $$;`, roleChange{}, false},
		{`comment on role "a" is 'x';`, roleChange{role: "a", kind: roleComment}, true},
		{`grant "p" to "m" with admin option;`, roleChange{role: "p", kind: roleMembership}, true},
		{`grant "p" to "m" with inherit false;`, roleChange{role: "p", kind: roleMembership}, true},
		{`revoke "p" from "m";`, roleChange{role: "p", kind: roleMembership}, true},
		{`revoke "p" from "m" granted by "g";`, roleChange{role: "p", kind: roleMembership, grantor: "g"}, true},
		{`revoke admin option for "p" from "m";`, roleChange{role: "p", kind: roleMembership, options: true}, true},
		{`revoke inherit option for "p" from "m" granted by "g";`, roleChange{role: "p", kind: roleMembership, grantor: "g", options: true}, true},
		{`grant "p" to "m" with set true granted by current_user;`, roleChange{role: "p", kind: roleMembership, grantor: "me", options: true}, true},
		{`grant select on table "public"."t" to "m";`, roleChange{}, false},
		{`revoke grant option for select on table "public"."t" from "m";`, roleChange{}, false},
		{`alter table "public"."t" owner to "a";`, roleChange{}, false},
		{`do $$ begin if not exists (select from pg_catalog.pg_roles where rolname = 'a') then create role "a"; end if; end $$;`, roleChange{}, false},
	} {
		got, ok := roleStatement(tc.stmt, "me")
		if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v %v\nwant %+v %v", tc.stmt, got, ok, tc.want, tc.ok)
		}
	}
}
