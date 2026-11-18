package integration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

// offlineLive is the snapshot pgy diff --from-empty plans against.
func offlineLive() *diff.Live {
	return &diff.Live{Schemas: map[string]bool{}, Tables: map[string]*diff.LiveTable{}, Types: map[string]bool{}, Functions: map[string]bool{}, Extensions: map[string]bool{}, Offline: true}
}

// cronInTestDatabase creates pg_cron in the test database, the only one it
// installs in (cron.database_name), drops it again when the test ends, and
// skips the test where the server has no pg_cron or the database has it
// already.
func cronInTestDatabase(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var installed bool
	if err := pool.QueryRow(ctx, `select exists (select from pg_extension where extname = 'pg_cron')`).Scan(&installed); err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Skip("pg_cron is installed in the test database already")
	}
	if _, err := pool.Exec(ctx, `create extension pg_cron`); err != nil {
		t.Skipf("pg_cron cannot be created in the test database: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `drop extension if exists pg_cron`) }) //nolint:errcheck
}

func TestIntegrationExtensionCreatedTablePolicies(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	cronInTestDatabase(t, pool)
	y := `
extensions:
  - {name: pg_cron, schema: pg_catalog}
schema cron:
  table job:
    rowLevelSecurity: true
    policies:
      cron_job_policy:
        using: (username = CURRENT_USER)
      job_readers:
        for: select
        using: "true"
  table job_run_details:
    rowLevelSecurity: true
    forceRowLevelSecurity: true
    policies:
      cron_job_run_details_policy:
        using: (username = CURRENT_USER)
`
	d, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
	if err != nil {
		t.Fatal(err)
	}
	for _, offline := range []bool{false, true} {
		if _, err := pool.Exec(ctx, `drop extension pg_cron`); err != nil {
			t.Fatal(err)
		}
		var plan *diff.PlanDiff
		if offline {
			plan = diff.PlanWith(offlineLive(), d, diff.Options{})
		} else {
			plan = planExtensionsLive(t, pool, y, diff.Options{})
		}
		if stmt, err := applyAsMigrations(ctx, pool, plan); err != nil {
			t.Fatalf("offline=%v: %s: %v\n%s", offline, stmt, err, diff.Render(plan))
		}
		if sql := diff.Render(planExtensionsLive(t, pool, y, diff.Options{Unsafe: true, Prune: true})); sql != "" {
			t.Fatalf("offline=%v: one migration must converge, the second diff has:\n%s\nafter:\n%s", offline, sql, diff.Render(plan))
		}
	}
}
