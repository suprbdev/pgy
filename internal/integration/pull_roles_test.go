package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

// TestIntegrationPullRolesAsConnectingRole pulls the roles through a role
// that is not a superuser, as on a hosted platform: its memberships and
// settings are written without its attributes, the bootstrap superuser is
// left out, and the YAML diffs clean with --unsafe --prune although the
// role holds, on PostgreSQL 16 and later, the membership CREATE ROLE gave
// it in a role it created.
func TestIntegrationPullRolesAsConnectingRole(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	platform, owner := freshRole(t, admin, "platform"), freshRole(t, admin, "owner")
	made := owner + "_made"
	t.Cleanup(func() { admin.Exec(context.Background(), fmt.Sprintf("drop role if exists %q", made)) }) //nolint
	for _, stmt := range []string{
		fmt.Sprintf("drop role if exists %q", made),
		fmt.Sprintf("alter role %q login createrole password 'pgy'", owner),
		fmt.Sprintf("grant %q to %q", platform, owner),
		fmt.Sprintf("alter role %q set statement_timeout = '5s'", owner),
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	freshDatabase(t, admin, "pgytest_pull_roles")
	cfg, err := pgxpool.ParseConfig(dsn(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database, cfg.ConnConfig.User, cfg.ConnConfig.Password = "pgytest_pull_roles", owner, "pgy"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, fmt.Sprintf("create role %q", made)); err != nil {
		t.Fatal(err)
	}

	live := introspectAll(t, pool)
	res, err := pull.Build(live, pull.Options{Roles: true, Single: true})
	if err != nil {
		t.Fatal(err)
	}
	y := string(res.Files[0].Content)
	var bootstrap string
	if err := admin.QueryRow(ctx, "select rolname from pg_roles where oid = 10").Scan(&bootstrap); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		fmt.Sprintf("  %s: {inRoles: [%s], set: {statement_timeout: 5s}}\n", owner, platform),
		fmt.Sprintf("  %s: {}\n", made),
	} {
		if !strings.Contains(y, want) {
			t.Errorf("want %q in:\n%s", want, y)
		}
	}
	if strings.Contains(y, "\n  "+bootstrap+":") {
		t.Errorf("the bootstrap superuser %s must be left out:\n%s", bootstrap, y)
	}
	pulled, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
	if err != nil {
		t.Fatal(err)
	}
	if sql := diff.Render(diff.PlanWith(live, pulled, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("the pulled roles must diff clean with --unsafe --prune through the role they were pulled with:\n%s", sql)
	}
}
