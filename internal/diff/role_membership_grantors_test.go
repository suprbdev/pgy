package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// addGrants records member's membership in parent as the given grants, one
// per grantor, as introspection reads it on PostgreSQL 16 and later.
func addGrants(live *Live, member, parent string, grants ...MembershipGrant) {
	if live.RoleMembers[member] == nil {
		live.RoleMembers[member] = map[string]bool{}
	}
	live.RoleMembers[member][parent] = true
	if live.RoleMemberOptions[member] == nil {
		live.RoleMemberOptions[member] = map[string]*LiveMembership{}
	}
	lm := &LiveMembership{Grants: grants}
	for _, g := range grants {
		lm.Grantors = append(lm.Grantors, g.Grantor)
		lm.Admin, lm.Inherit, lm.Set = lm.Admin || g.Admin, lm.Inherit || g.Inherit, lm.Set || g.Set
	}
	live.RoleMemberOptions[member][parent] = lm
}

func memberOf(parent string, o *schema.MembershipOptions) *schema.Database {
	member := roleDecl("member")
	member.InRoles = []string{parent}
	member.MemberOptions = map[string]*schema.MembershipOptions{parent: o}
	return rolesDB(member)
}

func TestMembershipGrantorsRevokeEachGrant(t *testing.T) {
	yes, no := true, false
	live := roleAdminLive(170000)
	live.RoleDefs["app"].Superuser = true
	addGrants(live, "member", "plat",
		MembershipGrant{Grantor: "app", Inherit: true, Set: true},
		MembershipGrant{Grantor: "pgy", Admin: true, Inherit: true})
	p := PlanWith(live, memberOf("plat", &schema.MembershipOptions{Admin: &no, Inherit: &no, Set: &no}), Options{})
	wantSQL(t, Render(p),
		`revoke admin option for "plat" from "member" granted by "pgy";`,
		`revoke inherit option for "plat" from "member" granted by "app";`,
		`revoke inherit option for "plat" from "member" granted by "pgy";`,
		`revoke set option for "plat" from "member" granted by "app";`)
	if len(p.Warnings) > 0 {
		t.Errorf("a superuser acts for every grantor, got %v", p.Warnings)
	}
	if sql := Render(PlanWith(live, memberOf("plat", &schema.MembershipOptions{Admin: &yes, Inherit: &yes, Set: &yes}), Options{})); sql != "" {
		t.Errorf("an option one grant gives is the membership's, got:\n%s", sql)
	}
}

func TestMembershipGrantorsGrantAsExistingGrantor(t *testing.T) {
	yes := true
	live := roleAdminLive(180000)
	live.RoleDefs["app"].Superuser = true
	live.RoleDefs["lead"], live.Roles["lead"] = &LiveRole{Inherit: true, ConnectionLimit: -1}, true
	addGrants(live, "lead", "plat", MembershipGrant{Grantor: "pgy", Admin: true})
	addGrants(live, "member", "plat", MembershipGrant{Grantor: "lead"}, MembershipGrant{Grantor: "pgy"})
	wantSQL(t, Render(PlanWith(live, memberOf("plat", &schema.MembershipOptions{Admin: &yes, Set: &yes}), Options{})),
		`grant "plat" to "member" with admin true, set true granted by "lead";`)
	live.RoleMemberOptions["lead"]["plat"].Admin = false
	wantSQL(t, Render(PlanWith(live, memberOf("plat", &schema.MembershipOptions{Admin: &yes}), Options{})),
		`grant "plat" to "member" with admin option granted by "pgy";`)
}

func TestMembershipGrantorsOtherGrantorLeftOut(t *testing.T) {
	no := false
	live := roleAdminLive(170000)
	addGrants(live, "app", "plat", MembershipGrant{Grantor: "pgy", Admin: true})
	addGrants(live, "member", "plat",
		MembershipGrant{Grantor: "app", Admin: true, Inherit: true, Set: true},
		MembershipGrant{Grantor: "pgy", Admin: true, Inherit: true, Set: true})
	p := PlanWith(live, memberOf("plat", &schema.MembershipOptions{Admin: &no, Inherit: &no}), Options{})
	wantSQL(t, Render(p),
		`revoke admin option for "plat" from "member" granted by "app";`,
		`revoke inherit option for "plat" from "member" granted by "app";`)
	wantRoleWarnings(t, p, `role plat: app, the connecting role, does not have the privileges of pgy, whose grant of the membership the statement changes, so the plan leaves out the 2 statements (revoke admin option for "plat" from "member" granted by "pgy"; ...); have a superuser run them`)
}

func TestMembershipGrantorsNewGrantKeepsOtherOptions(t *testing.T) {
	yes, no := true, false
	live := roleAdminLive(160000)
	addGrants(live, "app", "plat", MembershipGrant{Grantor: "pgy", Admin: true})
	addGrants(live, "member", "plat", MembershipGrant{Grantor: "pgy"})
	for _, c := range []struct {
		o    *schema.MembershipOptions
		want string
	}{
		{&schema.MembershipOptions{Inherit: &yes}, `grant "plat" to "member" with inherit true, set false;`},
		{&schema.MembershipOptions{Inherit: &yes, Set: &yes}, `grant "plat" to "member" with inherit true, set true;`},
		{&schema.MembershipOptions{Admin: &yes, Inherit: &no}, `grant "plat" to "member" with admin true, inherit false, set false;`},
	} {
		p := PlanWith(live, memberOf("plat", c.o), Options{})
		wantSQL(t, Render(p), c.want)
		if w := notOwnerWarnings(p); len(w) > 0 {
			t.Errorf("%+v: the connecting role grants with its own ADMIN option, got %q", c.o, w)
		}
	}
	live.RoleDefs["member"].Inherit = false
	wantSQL(t, Render(PlanWith(live, memberOf("plat", &schema.MembershipOptions{Admin: &yes}), Options{})),
		`grant "plat" to "member" with admin true, set false;`)
}

func TestMembershipGrantorsPostgres15Unchanged(t *testing.T) {
	yes, no := true, false
	live := roleAdminLive(150000)
	live.RoleDefs["app"].Superuser = true
	addGrants(live, "member", "plat", MembershipGrant{Grantor: "root", Admin: true, Inherit: true, Set: true})
	wantSQL(t, Render(PlanWith(live, memberOf("plat", &schema.MembershipOptions{Admin: &no}), Options{})),
		`revoke admin option for "plat" from "member";`)
	addGrants(live, "member", "plat", MembershipGrant{Grantor: "root", Inherit: true, Set: true})
	wantSQL(t, Render(PlanWith(live, memberOf("plat", &schema.MembershipOptions{Admin: &yes}), Options{})),
		`grant "plat" to "member" with admin option;`)
}

func TestMembershipGrantorsSimulated(t *testing.T) {
	no := false
	live := roleAdminLive(170000)
	live.RoleDefs["app"].Superuser = true
	addGrants(live, "member", "plat",
		MembershipGrant{Grantor: "app", Admin: true, Inherit: true, Set: true},
		MembershipGrant{Grantor: "pgy", Admin: true, Inherit: true, Set: true})
	d := memberOf("plat", &schema.MembershipOptions{Admin: &no, Set: &no})
	if sql := Render(PlanWith(Simulate(live, d), d, Options{})); sql != "" {
		t.Errorf("a later step plans nothing for the simulated options, got:\n%s", sql)
	}
	if g := live.RoleMemberOptions["member"]["plat"].Grants; !g[0].Admin || !g[1].Set {
		t.Errorf("simulating leaves the snapshot as it is: %+v", g)
	}
	if sql := strings.TrimSpace(Render(PlanWith(live, d, Options{}))); strings.Count(sql, "\n") != 3 {
		t.Errorf("want a revoke per grant and option, got:\n%s", sql)
	}
}
