package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

// exprCastsWithout declares definitions on a citext column without casts;
// exprCastsWith casts the column to text in each of them, which makes
// comparisons case sensitive. The text comparison drops the casts, so it
// takes the two for the same.
const exprCastsWithout = `
extensions:
  - {name: citext, schema: public}
schema app:
  table users:
    columns:
      id: {type: int}
      email: {type: citext}
    primaryKey: [id]
    indexes:
      users_email_key: {columns: [email], unique: true}
      users_admin: {columns: [id], where: "email = 'Admin@example.com'"}
    constraints:
      users_email_chk: {type: check, expression: "email <> 'root@example.com'"}
    rowLevelSecurity: true
    policies:
      users_self: {using: "email = current_setting('app.email', true)::citext"}
`

var exprCastsWith = strings.NewReplacer(
	"columns: [email], unique", `columns: ["(email::text)"], unique`,
	`"email = 'Admin`, `"email::text = 'Admin`,
	`"email <> 'root`, `"email::text <> 'root`,
	`"email = current_setting('app.email', true)::citext"`, `"email::text = current_setting('app.email', true)"`,
).Replace(exprCastsWithout)

func planSQL(p *diff.PlanDiff) string { return diff.Render(p) + diff.RenderConcurrent(p) }

// assertConverged requires d to diff clean against pool once probed, with
// and without --unsafe.
func assertConverged(t *testing.T, pool *pgxpool.Pool, d *schema.Database) *diff.Live {
	t.Helper()
	live := probed(t, pool, d)
	if sql := planSQL(diff.Plan(live, d, true)); sql != "" {
		t.Errorf("want a clean diff once applied, got:\n%s\nprobes: %v", sql, exprProbeKeys(live))
	}
	if p := diff.PlanReportingUnsafe(live, d, diff.Options{}); diff.Render(p) != "" || len(p.Warnings) > 0 {
		t.Errorf("want nothing planned or reported without --unsafe, got:\n%s%+v", diff.Render(p), p.Warnings)
	}
	return live
}

// TestIntegrationProbeExpressionsCasts adds and removes casts on a citext
// column in an index key, a partial index predicate, a check and a policy.
// The text comparison takes each pair for the same; the probe finds them
// different, and both directions are planned and converge.
func TestIntegrationProbeExpressionsCasts(t *testing.T) {
	pool, without := exprProbeDatabase(t, "pgytest_expr_probe_casts", exprCastsWithout)
	with := loadYAML(t, "app", exprCastsWith)
	if live := assertConverged(t, pool, without); live.ExprProbes != nil {
		t.Errorf("YAML that leaves out only the casts PostgreSQL adds to constants needs no probe, got %v", exprProbeKeys(live))
	}
	want := []string{
		`drop index "app"."users_email_key";`,
		`create unique index if not exists "users_email_key"`,
		`drop index "app"."users_admin";`,
		`create index if not exists "users_admin"`,
		`alter table "app"."users" drop constraint "users_email_chk";`,
		`add constraint "users_email_chk" check`,
		`drop policy "users_self" on "app"."users";`,
		`create policy "users_self"`,
	}
	for _, step := range []struct {
		name string
		d    *schema.Database
	}{{"casts added", with}, {"casts removed", without}} {
		t.Run(step.name, func(t *testing.T) {
			if sql := planSQL(diff.Plan(introspect(t, pool, step.d), step.d, true)); sql != "" {
				t.Fatalf("the fixture must match as text, or it doesn't exercise the probe; got:\n%s", sql)
			}
			live := probed(t, pool, step.d)
			plan := diff.Plan(live, step.d, true)
			sql := planSQL(plan)
			for _, stmt := range want {
				if !strings.Contains(sql, stmt) {
					t.Errorf("want %q in:\n%s\nprobes: %v", stmt, sql, exprProbeKeys(live))
				}
			}
			if p := diff.PlanReportingUnsafe(live, step.d, diff.Options{}); len(p.Warnings) == 0 {
				t.Error("without --unsafe the redefinitions are reported as left out")
			}
			applyPlan(t, pool, plan)
			if live := assertConverged(t, pool, step.d); live.ExprProbes != nil {
				t.Errorf("a cast on both sides needs no probe, got %v", exprProbeKeys(live))
			}
		})
	}
}

// TestIntegrationProbeExpressionsCatalogSchema switches a default between
// core's gen_random_uuid() and pgcrypto's public.gen_random_uuid(), which
// the text comparison takes for the same, in both directions. A public.
// qualifier on a function pg_catalog doesn't have costs one catalog query
// and no probe.
func TestIntegrationProbeExpressionsCatalogSchema(t *testing.T) {
	const core = `
extensions:
  - {name: pgcrypto, schema: public}
schema public:
  function slug():
    returns: text
    language: sql
    body: select 'x'
schema app:
  table t:
    columns:
      id: {type: uuid, default: "gen_random_uuid()"}
      s: {type: text, default: "public.slug()"}
`
	pool, d := exprProbeDatabase(t, "pgytest_expr_probe_catalog", core)
	ctx := context.Background()
	live := introspect(t, pool, d)
	acquired := pool.Stat().AcquireCount()
	if err := diff.ProbeExpressions(ctx, pool, live, d); err != nil {
		t.Fatal(err)
	}
	if n := pool.Stat().AcquireCount() - acquired; n != 1 || live.ExprProbes != nil {
		t.Errorf("public.slug() needs one catalog query and no probe, got %d connections and probes %v", n, exprProbeKeys(live))
	}
	if _, err := pool.Exec(ctx, `alter table app.t alter column id set default public.gen_random_uuid()`); err != nil {
		t.Fatal(err)
	}
	pgcrypto := loadYAML(t, "app", strings.Replace(core, `default: "gen_random_uuid()"`, `default: "public.gen_random_uuid()"`, 1))
	for _, step := range []struct {
		name, stmt string
		d          *schema.Database
	}{
		{"drift to pgcrypto repaired", `alter table "app"."t" alter column "id" set default gen_random_uuid();`, d},
		{"changed to pgcrypto", `alter table "app"."t" alter column "id" set default public.gen_random_uuid();`, pgcrypto},
	} {
		t.Run(step.name, func(t *testing.T) {
			if sql := planSQL(diff.Plan(introspect(t, pool, step.d), step.d, true)); sql != "" {
				t.Fatalf("the two must match as text; got:\n%s", sql)
			}
			live := probed(t, pool, step.d)
			plan := diff.Plan(live, step.d, false)
			if sql := planSQL(plan); !strings.Contains(sql, step.stmt) || strings.Contains(sql, `"s" set default`) {
				t.Errorf("want only %q, got:\n%s\nprobes: %v", step.stmt, sql, exprProbeKeys(live))
			}
			applyPlan(t, pool, plan)
			assertConverged(t, pool, step.d)
		})
	}
}

// TestIntegrationProbeExpressionsRetypedCasts changes only the type or the
// modifier of a cast in an index expression and a check, which the text
// comparison used to drop. It is a change with or without the probe.
func TestIntegrationProbeExpressionsRetypedCasts(t *testing.T) {
	const before = `
schema app:
  table r:
    columns:
      id: {type: bigint}
      name: {type: text}
      n: {type: numeric, nullable: true}
    indexes:
      r_name: {columns: ["(name::varchar(10))"]}
      r_n: {columns: [id], where: "n::numeric(6,2) > 1"}
    constraints:
      r_check: {type: check, expression: "name::varchar(10) <> '' and n::numeric(6,2) < 1000"}
`
	pool, _ := exprProbeDatabase(t, "pgytest_expr_probe_retyped", before)
	d := loadYAML(t, "app", strings.NewReplacer("varchar(10)", "varchar(20)", "numeric(6,2)", "numeric(8,2)").Replace(before))
	want := []string{
		`drop index "app"."r_name";`,
		`create index if not exists "r_name" on "app"."r"(((name::varchar(20))));`,
		`drop index "app"."r_n";`,
		`alter table "app"."r" drop constraint "r_check";`,
	}
	for name, live := range map[string]*diff.Live{"text only": introspect(t, pool, d), "probed": probed(t, pool, d)} {
		sql := planSQL(diff.Plan(live, d, true))
		for _, stmt := range want {
			if !strings.Contains(sql, stmt) {
				t.Errorf("%s: want %q in:\n%s", name, stmt, sql)
			}
		}
	}
	applyPlan(t, pool, diff.Plan(probed(t, pool, d), d, true))
	assertConverged(t, pool, d)
}

// TestIntegrationProbeEveryExpression regroups the operands of a check and
// an index expression, which the text comparison can't see since it drops
// parentheses: only ProbeEveryExpression finds the change. On YAML as pgy
// pull writes it, it probes every definition and finds nothing to change.
func TestIntegrationProbeEveryExpression(t *testing.T) {
	ctx := context.Background()
	pool, d := exprProbeDatabase(t, "pgytest_expr_probe_every", `
schema app:
  table g:
    columns:
      id: {type: bigint}
      a: {type: int}
      b: {type: int}
      c: {type: int}
    constraints:
      g_abc: {type: check, expression: "(a + b) * c > 0"}
    indexes:
      g_sum: {columns: ["((a + b) * c)"]}
`)
	for _, stmt := range []string{
		`alter table app.g drop constraint g_abc`,
		`alter table app.g add constraint g_abc check (a + b * c > 0)`,
		`drop index app.g_sum`,
		`create index g_sum on app.g ((a + b * c))`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if sql := planSQL(diff.Plan(probed(t, pool, d), d, true)); sql != "" {
		t.Fatalf("other parentheses match as text and drop nothing, so the default probe leaves them; got:\n%s", sql)
	}
	every := func() *diff.Live {
		live := introspect(t, pool, d)
		if err := diff.ProbeEveryExpression(ctx, pool, live, d); err != nil {
			t.Fatal(err)
		}
		return live
	}
	plan := diff.Plan(every(), d, true)
	sql := planSQL(plan)
	for _, stmt := range []string{`add constraint "g_abc" check ((a + b) * c > 0)`, `create index if not exists "g_sum"`} {
		if !strings.Contains(sql, stmt) {
			t.Errorf("want %q in:\n%s", stmt, sql)
		}
	}
	applyPlan(t, pool, plan)
	if sql := planSQL(diff.Plan(every(), d, true)); sql != "" {
		t.Errorf("want a clean diff once applied, got:\n%s", sql)
	}

	for name, y := range map[string]string{"extension types": exprProbeExtensionTypes, "builtin types": exprProbeBuiltinTypes} {
		t.Run(name, func(t *testing.T) {
			pool, _ := exprProbeDatabase(t, "pgytest_expr_probe_every_pulled", y)
			d := pulledYAML(t, pool)
			live := introspect(t, pool, d)
			if err := diff.ProbeEveryExpression(ctx, pool, live, d); err != nil {
				t.Fatal(err)
			}
			seen := map[diff.ExprKind]bool{}
			for k, p := range live.ExprProbes {
				seen[k.Kind] = true
				if p.Err != "" {
					t.Errorf("%s %s %s: %s", k.Kind, k.Object, k.Name, p.Err)
				}
			}
			for _, kind := range exprProbeAllKinds {
				if !seen[kind] {
					t.Errorf("every definition is probed, but no %s was: %v", kind, exprProbeKeys(live))
				}
			}
			for _, opts := range []diff.Options{{Unsafe: true}, {Unsafe: true, Prune: true}} {
				if sql := planSQL(diff.PlanWith(live, d, opts)); sql != "" {
					t.Errorf("pulled YAML must diff clean (%+v), got:\n%s", opts, sql)
				}
			}
		})
	}
}
