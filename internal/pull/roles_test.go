package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func rolesLive() *diff.Live {
	member := func(admin, inherit, set bool, grantors ...string) *diff.LiveMembership {
		return &diff.LiveMembership{Admin: admin, Inherit: inherit, Set: set, Grantors: grantors}
	}
	return &diff.Live{
		ServerVersion: 170000,
		CurrentUser:   "owner",
		RoleDefs: map[string]*diff.LiveRole{
			"boot":     {Login: true, Superuser: true, Inherit: true, ConnectionLimit: -1, Bootstrap: true},
			"postgres": {Login: true, CreateRole: true, Inherit: true, ConnectionLimit: -1, Config: map[string]string{"search_path": `"$user", public, extensions`}},
			"owner":    {Login: true, CreateRole: true, CreateDB: true, Inherit: true, ConnectionLimit: -1, Config: map[string]string{"statement_timeout": "5s"}},
			"platform": {Inherit: true, ConnectionLimit: -1},
			"anon":     {Inherit: true, ConnectionLimit: -1},
		},
		RoleMembers: map[string]map[string]bool{
			"postgres": {"anon": true},
			"owner":    {"platform": true, "anon": true},
		},
		RoleMemberOptions: map[string]map[string]*diff.LiveMembership{
			"postgres": {"anon": member(false, true, true, "boot")},
			"owner":    {"platform": member(false, true, true, "boot"), "anon": member(true, false, false, "boot")},
		},
		Details: &diff.LiveDetails{},
	}
}

func TestPullRolesKeepsOrdinaryPostgresAndTheConnectingRole(t *testing.T) {
	res, err := Build(rolesLive(), Options{Roles: true, Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{
		`postgres: {login: true, createrole: true, inRoles: [anon], set: {search_path: '"$user", public, extensions'}}`,
		`owner: {inRoles: [platform], set: {statement_timeout: 5s}}`,
		"platform: {}",
		"anon: {}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "boot") {
		t.Errorf("the bootstrap superuser must be left out:\n%s", got)
	}

	live := rolesLive()
	live.RoleDefs["postgres"].Bootstrap, live.RoleDefs["boot"].Bootstrap = true, false
	live.RoleMemberOptions["owner"]["anon"].Grantors = []string{"postgres"}
	res, err = Build(live, Options{Roles: true, Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got = string(res.Files[0].Content)
	if strings.Contains(got, "postgres:") || !strings.Contains(got, "boot: {login: true, superuser: true}") {
		t.Errorf("only the bootstrap superuser is left out, whatever its name:\n%s", got)
	}

	live = rolesLive()
	live.RoleMembers["owner"], live.RoleMemberOptions["owner"], live.RoleDefs["owner"].Config = nil, nil, nil
	res, err = Build(live, Options{Roles: true, Single: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Files[0].Content); strings.Contains(got, "owner:") {
		t.Errorf("a connecting role without memberships, settings or comment needs no entry:\n%s", got)
	}
}
