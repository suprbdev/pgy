package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

// offlineLive is the snapshot pgy diff --from-empty plans against.
func offlineLive() *diff.Live {
	return &diff.Live{Schemas: map[string]bool{}, Tables: map[string]*diff.LiveTable{}, Types: map[string]bool{}, Functions: map[string]bool{}, Extensions: map[string]bool{}, Offline: true}
}

// planAsDiff plans y against the database the way pgy diff does, with
// every introspection and probe it runs.
func planAsDiff(t *testing.T, pool *pgxpool.Pool, y string, opts diff.Options) *diff.PlanDiff {
	t.Helper()
	ctx := context.Background()
	d, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
	if err != nil {
		t.Fatal(err)
	}
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return diff.IntrospectExtensions(ctx, pool, live, d, opts) },
		func() error { return diff.ProbeExtensionObjects(ctx, pool, live, d, opts) },
		func() error { return diff.IntrospectDataWith(ctx, pool, live, d, opts) },
		func() error { return diff.ProbeViews(ctx, pool, live, d) },
		func() error { return diff.ProbeExpressions(ctx, pool, live, d) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	return diff.PlanWith(live, d, opts)
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
			plan = planAsDiff(t, pool, y, diff.Options{})
			want := strings.Join([]string{
				`create extension "pg_cron" schema "pg_catalog";`,
				`create policy "job_readers" on "cron"."job" for select using (true);`,
				`alter table "cron"."job_run_details" force row level security;`,
			}, "\n")
			if sql := strings.TrimSpace(diff.Render(plan)); sql != want {
				t.Errorf("with a connection the plan knows the extension's policies, got:\n%s", sql)
			}
		}
		if stmt, err := applyAsMigrations(ctx, pool, plan); err != nil {
			t.Fatalf("offline=%v: %s: %v\n%s", offline, stmt, err, diff.Render(plan))
		}
		if sql := diff.Render(planAsDiff(t, pool, y, diff.Options{Unsafe: true, Prune: true})); sql != "" {
			t.Fatalf("offline=%v: one migration must converge, the second diff has:\n%s\nafter:\n%s", offline, sql, diff.Render(plan))
		}
	}
}

func TestIntegrationExtensionObjectsProbe(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_ext_objects")
	y := `
extensions:
  - {name: pg_stat_statements, schema: public}
  - {name: citext, schema: public}
schema public:
  view pg_stat_statements:
    grants: {public: [select]}
  function citext_eq(citext, citext):
    comment: equality
  function citext_nonexistent(citext):
    leakproof: true
`
	d, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
	if err != nil {
		t.Fatal(err)
	}
	live, err := diff.Introspect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectExtensions(ctx, db, live, d, diff.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := diff.ProbeExtensionObjects(ctx, db, live, d, diff.Options{}); err != nil {
		t.Fatal(err)
	}
	if eo := live.ExtensionObjects; eo == nil || !eo.Declared["citext"] || !eo.Declared["pg_stat_statements"] || !eo.Objects.Views["public.pg_stat_statements"] || eo.Objects.Views["public.pg_stat_statements_info"] != (live.ServerVersion >= 140000) {
		t.Fatalf("the probe must find the extensions' objects: %+v", eo)
	}
	var left int
	if err := db.QueryRow(ctx, `select count(*) from pg_extension where extname in ('citext', 'pg_stat_statements')`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("the probe must leave nothing behind: %d extensions, %v", left, err)
	}
	plan := diff.PlanWith(live, d, diff.Options{})
	want := strings.Join([]string{
		`create extension "citext" schema "public";`,
		`create extension "pg_stat_statements" schema "public";`,
		`comment on function "public"."citext_eq"(citext, citext) is 'equality';`,
	}, "\n")
	if sql := strings.TrimSpace(diff.Render(plan)); sql != want {
		t.Errorf("the plan compares the declarations with what the extensions create, got:\n%s", sql)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0].Message, "function public.citext_nonexistent(citext) has no body") {
		t.Errorf("a declaration of an object the extension doesn't create is reported, got %v", plan.Warnings)
	}
	applyPlan(t, db, plan)
	if sql := diff.Render(planAsDiff(t, db, y, diff.Options{})); sql != "" {
		t.Fatalf("one migration must converge, the second diff has:\n%s", sql)
	}
}

func TestIntegrationExtensionTableDeclaredWithColumns(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_ext_table_columns")
	if _, err := db.Exec(ctx, `create extension postgis; drop extension postgis`); err != nil {
		t.Skipf("postgis: %v", err)
	}
	y := `
extensions:
  - {name: postgis, schema: public}
schema public:
  table spatial_ref_sys:
    columns:
      srid: {type: integer}
      auth_name: {type: "varchar(256)", nullable: true}
      auth_srid: {type: integer, nullable: true}
      srtext: {type: "varchar(2048)", nullable: true}
      proj4text: {type: "varchar(2048)", nullable: true}
    primaryKey: [srid]
    constraints:
      spatial_ref_sys_srid_check: {type: check, expression: "srid > 0 AND srid <= 998999"}
    grants: {public: [select]}
    data:
      rows:
        - {srid: 990001, auth_name: pgy, auth_srid: 990001, srtext: 'LOCAL_CS["pgy local",UNIT["metre",1]]', proj4text: '+proj=longlat +datum=WGS84 +no_defs'}
`
	d, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
	if err != nil {
		t.Fatal(err)
	}
	if w := diff.PlanWith(offlineLive(), d, diff.Options{}).Warnings; len(w) != 1 || !strings.Contains(w[0].Message, "diff --from-empty cannot tell which tables extension postgis creates") {
		t.Errorf("without a database the plan warns, got %v", w)
	}
	plan := planAsDiff(t, db, y, diff.Options{})
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0].Message, "table public.spatial_ref_sys belongs to extension postgis") {
		t.Errorf("want the warning about the extension's table, got %v", plan.Warnings)
	}
	applyPlan(t, db, plan)
	if sql := diff.Render(planAsDiff(t, db, y, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Fatalf("one migration must converge, the second diff has:\n%s\nafter:\n%s", sql, diff.Render(plan))
	}
	var n int
	if err := db.QueryRow(ctx, `select count(*) from spatial_ref_sys where srid = 990001 and auth_name = 'pgy'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the declared row: %d, %v", n, err)
	}
}
