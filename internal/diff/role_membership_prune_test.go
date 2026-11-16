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
