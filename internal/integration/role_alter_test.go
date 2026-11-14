package integration

import (
	"context"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationRoleAttributesAltered(t *testing.T) {
	pool := connect(t)
	name := freshRole(t, pool, "attrs")
	ctx := context.Background()
	roles := func(r *schema.Role) *schema.Database {
		r.Name = name
		return &schema.Database{Tables: map[string]*schema.Table{}, Roles: map[string]*schema.Role{name: r}}
	}
	plan := func(d *schema.Database) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, d, false)
	}
	steps := []*schema.Database{
		roles(&schema.Role{Login: true, CreateDB: true, ConnectionLimit: 5, Managed: map[string]bool{"login": true, "createdb": true, "connectionLimit": true}}),
		roles(&schema.Role{Login: false, NoInherit: true, BypassRLS: true, ConnectionLimit: -1, Managed: map[string]bool{"login": true, "inherit": true, "bypassRLS": true, "connectionLimit": true}}),
	}
	for i, d := range steps {
		applyPlan(t, pool, plan(d))
		if sql := diff.Render(plan(d)); sql != "" {
			t.Fatalf("step %d: expected no changes after apply, got:\n%s", i, sql)
		}
	}
	var login, createdb, inherit, bypass bool
	var limit int
	if err := pool.QueryRow(ctx, `select rolcanlogin, rolcreatedb, rolinherit, rolbypassrls, rolconnlimit from pg_roles where rolname = $1`, name).Scan(&login, &createdb, &inherit, &bypass, &limit); err != nil {
		t.Fatal(err)
	}
	if login || !createdb || inherit || !bypass || limit != -1 {
		t.Errorf("unexpected role state: login=%v createdb=%v inherit=%v bypassrls=%v limit=%d", login, createdb, inherit, bypass, limit)
	}
}
