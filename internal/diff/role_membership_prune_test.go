package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func membershipLive(version int) *Live {
	live := emptyLive()
	live.ServerVersion = version
	for _, r := range []string{"app", "base", "other"} {
		live.Roles[r] = true
	}
	live.RoleDefs = map[string]*LiveRole{"app": {Inherit: true, ConnectionLimit: -1}}
	live.RoleMembers = map[string]map[string]bool{"app": {"base": true, "other": true}}
	live.RoleMemberOptions = map[string]map[string]*LiveMembership{"app": {
		"base":  {Inherit: true, Set: true, Grantors: []string{"admin", "granter"}},
		"other": {Inherit: true, Set: true, Grantors: []string{"admin"}},
	}}
	return live
}

func membershipRoles(inRoles []string) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, Roles: map[string]*schema.Role{
		"app": {Name: "app", ConnectionLimit: -1, Managed: map[string]bool{}, InRoles: inRoles},
	}}
}

func TestRoleMembershipsPrunedOnlyWithPrune(t *testing.T) {
	d := membershipRoles([]string{"other"})
	if sql := Render(Plan(membershipLive(170000), d, true)); sql != "" {
		t.Errorf("--unsafe alone keeps memberships, got:\n%s", sql)
	}
	sql := Render(PlanWith(membershipLive(170000), d, Options{Unsafe: true, Prune: true}))
	want := "revoke \"base\" from \"app\" granted by \"admin\";\nrevoke \"base\" from \"app\" granted by \"granter\";"
	if !strings.Contains(sql, want) || strings.Contains(sql, `"other" from`) {
		t.Errorf("PostgreSQL 16+: want a REVOKE per grantor of the undeclared membership, got:\n%s", sql)
	}
	if sql := Render(PlanWith(membershipLive(150004), d, Options{Unsafe: true, Prune: true})); !strings.Contains(sql, `revoke "base" from "app";`) || strings.Contains(sql, "granted by") {
		t.Errorf("before PostgreSQL 16: want one plain REVOKE, got:\n%s", sql)
	}
	if sql := Render(PlanWith(membershipLive(170000), membershipRoles(nil), Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("a role without inRoles leaves its memberships unmanaged, got:\n%s", sql)
	}
	if sql := Render(PlanWith(membershipLive(170000), membershipRoles([]string{}), Options{Unsafe: true, Prune: true})); strings.Count(sql, "revoke") != 3 {
		t.Errorf("inRoles: [] revokes every membership, got:\n%s", sql)
	}
}

func TestRoleMembershipPruneKeepsCreatorGrantOfConnectingRole(t *testing.T) {
	creator := func(version int, current string) *Live {
		live := membershipLive(version)
		live.CurrentUser = current
		live.Roles["boot"], live.Roles["made"] = true, true
		live.RoleDefs["boot"] = &LiveRole{Superuser: true, Bootstrap: true, Inherit: true, ConnectionLimit: -1}
		live.RoleMembers["app"]["made"] = true
		live.RoleMemberOptions["app"]["made"] = &LiveMembership{Admin: true, Grantors: []string{"boot"}}
		return live
	}
	d := membershipRoles([]string{"base", "other"})
	prune := Options{Unsafe: true, Prune: true}
	if sql := Render(PlanWith(creator(170000, "app"), d, prune)); sql != "" {
		t.Errorf("the connecting role keeps the membership CREATE ROLE gave it, got:\n%s", sql)
	}
	if sql := Render(PlanWith(creator(170000, "other"), d, prune)); !strings.Contains(sql, `revoke "made" from "app" granted by "boot";`) {
		t.Errorf("another role's membership is pruned, got:\n%s", sql)
	}
	live := creator(170000, "app")
	live.RoleMemberOptions["app"]["made"].Set = true
	if sql := Render(PlanWith(live, d, prune)); !strings.Contains(sql, `revoke "made" from "app" granted by "boot";`) {
		t.Errorf("a membership CREATE ROLE does not grant is pruned, got:\n%s", sql)
	}
	if sql := Render(PlanWith(creator(150004, "app"), d, prune)); !strings.Contains(sql, `revoke "made" from "app";`) {
		t.Errorf("before PostgreSQL 16 CREATE ROLE grants nothing, so the membership is pruned, got:\n%s", sql)
	}
}
