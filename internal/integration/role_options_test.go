package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationRoleValidUntilAndMembershipOptions(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	member, parent, other := freshRole(t, pool, "member"), freshRole(t, pool, "parent"), freshRole(t, pool, "other")
	live := func() *diff.Live {
		t.Helper()
		l, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	pg16 := live().ServerVersion >= 160000
	yes, no := true, false
	step := func(validUntil string, parentOpts, otherOpts *schema.MembershipOptions) {
		t.Helper()
		d := &schema.Database{Tables: map[string]*schema.Table{}, Roles: map[string]*schema.Role{member: {
			Name: member, ConnectionLimit: -1, ValidUntil: validUntil, Managed: map[string]bool{"validUntil": true},
			InRoles: []string{parent, other}, MemberOptions: map[string]*schema.MembershipOptions{parent: parentOpts, other: otherOpts},
		}}}
		applyPlan(t, pool, diff.Plan(live(), d, false))
		if sql := diff.Render(diff.Plan(live(), d, false)); sql != "" {
			t.Fatalf("expected no changes after apply, got:\n%s", sql)
		}
	}
	step("2030-01-01T00:00:00Z", &schema.MembershipOptions{Admin: &yes}, &schema.MembershipOptions{Inherit: &no, Set: &no})
	step("2031-06-01 12:00:00+02", &schema.MembershipOptions{Admin: &no}, &schema.MembershipOptions{Inherit: &yes, Set: &yes})
	step("infinity", nil, nil)

	var validUntil string
	if err := pool.QueryRow(ctx, `select rolvaliduntil::text from pg_roles where rolname = $1`, member).Scan(&validUntil); err != nil {
		t.Fatal(err)
	}
	if validUntil != "infinity" {
		t.Errorf("valid until = %s", validUntil)
	}
	var admin bool
	if err := pool.QueryRow(ctx, `select bool_or(admin_option) from pg_auth_members where member = $1::regrole and roleid = $2::regrole`, member, parent).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if admin {
		t.Error("admin option must have been revoked")
	}
	if !pg16 {
		d := &schema.Database{Roles: map[string]*schema.Role{member: {Name: member, ConnectionLimit: -1, InRoles: []string{other}, MemberOptions: map[string]*schema.MembershipOptions{other: {Set: &no}}}}}
		p := diff.Plan(live(), d, false)
		if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0].Message, "need PostgreSQL 16") || strings.Contains(diff.Render(p), "set false") {
			t.Errorf("before PostgreSQL 16 set must be skipped with a warning: %v\n%s", p.Warnings, diff.Render(p))
		}
	}
}
