package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestMembershipStmts(t *testing.T) {
	yes, no := true, false
	live := emptyLive()
	live.ServerVersion = 170000
	live.RoleMembers, live.RoleMemberOptions = map[string]map[string]bool{}, map[string]map[string]*LiveMembership{}
	for _, c := range []struct {
		o    *schema.MembershipOptions
		want string
	}{
		{nil, `grant "p" to "m";`},
		{&schema.MembershipOptions{Admin: &yes}, `grant "p" to "m" with admin option;`},
		{&schema.MembershipOptions{Admin: &no}, `grant "p" to "m";`},
		{&schema.MembershipOptions{Admin: &yes, Inherit: &no, Set: &no}, `grant "p" to "m" with admin true, inherit false, set false;`},
	} {
		if got := strings.Join(membershipStmts(&PlanDiff{}, live, "m", "p", c.o), "\n"); got != c.want {
			t.Errorf("%+v: got %s, want %s", c.o, got, c.want)
		}
	}
	live.RoleMembers["m"] = map[string]bool{"p": true}
	live.RoleMemberOptions["m"] = map[string]*LiveMembership{"p": {Admin: true, Inherit: true, Set: true}}
	got := strings.Join(membershipStmts(&PlanDiff{}, live, "m", "p", &schema.MembershipOptions{Admin: &no, Set: &no, Inherit: &yes}), "\n")
	if got != "revoke admin option for \"p\" from \"m\";\ngrant \"p\" to \"m\" with set false;" {
		t.Errorf("changes: %s", got)
	}
	if got := membershipStmts(&PlanDiff{}, live, "m", "p", nil); len(got) != 0 {
		t.Errorf("unmanaged options must not diff: %v", got)
	}
	live.ServerVersion = 150000
	plan := &PlanDiff{}
	if got := membershipStmts(plan, live, "m", "p", &schema.MembershipOptions{Set: &no}); len(got) != 0 || len(plan.Warnings) != 1 {
		t.Errorf("before 16: %v %v", got, plan.Warnings)
	}
}

func TestNormalizeValidUntil(t *testing.T) {
	for in, want := range map[string]string{
		"2030-01-01T00:00:00Z":   "2030-01-01T00:00:00Z",
		"2031-06-01 12:00:00+02": "2031-06-01T10:00:00Z",
		"2030-01-01":             "2030-01-01T00:00:00Z",
		"Infinity":               "infinity",
	} {
		if got := normalizeValidUntil(in); got != want {
			t.Errorf("normalizeValidUntil(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestSimulateRoleOptions(t *testing.T) {
	yes, no := true, false
	d := &schema.Database{Roles: map[string]*schema.Role{"m": {
		Name: "m", ConnectionLimit: -1, ValidUntil: "2030-01-01", Managed: map[string]bool{"validUntil": true},
		InRoles: []string{"p"}, MemberOptions: map[string]*schema.MembershipOptions{"p": {Admin: &yes, Set: &no}},
	}}}
	if sql := Render(Plan(Simulate(emptyLive(), d), d, false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}
