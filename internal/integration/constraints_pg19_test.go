package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
)

func serverVersion(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var v int
	if err := pool.QueryRow(context.Background(), "select current_setting('server_version_num')::int").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// constraintStates returns "name enforced validated comment" for each
// check and foreign key constraint of table sch.t. conenforced is new in
// PostgreSQL 18.
func constraintStates(t *testing.T, pool *pgxpool.Pool, sch string) string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select conname || ' ' || coalesce((to_jsonb(c) ->> 'conenforced')::bool, true) || ' ' || convalidated || ' ' || coalesce(obj_description(oid, 'pg_constraint'), '-')
		from pg_constraint c where conrelid = $1::regclass and contype in ('c', 'f') order by conname`, q(sch)+".t")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return strings.Join(out, "; ")
}

const enforcedYAML = `
schema SCH:
  table p:
    columns: {id: {type: int, primaryKey: true}}
  table t:
VALIDATE    columns: {id: {type: int, primaryKey: true}, pid: {type: int, nullable: true}, a: {type: int, nullable: true}}
    constraints:
      t_ck: {type: check, expression: a > 0, comment: ck noteCHECK}
    foreignKeys:
      t_fk: {columns: [pid], references: {table: SCH.p, columns: [id]}, comment: fk noteFK}
`

func enforcedStep(validate, check, fk string) string {
	r := strings.NewReplacer("VALIDATE", validate, "CHECK", check, "FK", fk)
	return r.Replace(enforcedYAML)
}

func TestIntegrationEnforcedToggle(t *testing.T) {
	requireServerVersion(t, 180000)
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	pg19 := serverVersion(t, pool) >= 190000
	convergeYAMLWith(t, pool, sch, enforcedStep("", "", ""), diff.Options{})
	if _, err := pool.Exec(ctx, `insert into `+q(sch)+`.p values (1); insert into `+q(sch)+`.t values (1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	const all = "t_ck true true ck note; t_fk true true fk note"

	off := enforcedStep("", ", enforced: false", ", enforced: false")
	sql := convergeYAMLWith(t, pool, sch, off, diff.Options{})
	if !contains(sql, `alter constraint "t_fk" not enforced`) || pg19 != contains(sql, `alter constraint "t_ck" not enforced`) {
		t.Errorf("NOT ENFORCED is set in place where the server can (foreign keys on 18, checks on 19), got:\n%s", sql)
	}
	if got, want := constraintStates(t, pool, sch), "t_ck false false ck note; t_fk false false fk note"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}

	sql = convergeYAMLWith(t, pool, sch, enforcedStep("", "", ""), diff.Options{})
	if !contains(sql, `add constraint "t_ck" check (a > 0) not valid`) || !contains(sql, `validate constraint "t_fk"`) || contains(sql, "alter constraint") {
		t.Errorf("enforcing adds the constraints NOT VALID and validates them later, got:\n%s", sql)
	}
	if got := constraintStates(t, pool, sch); got != all {
		t.Errorf("got %s, want %s", got, all)
	}

	convergeYAMLWith(t, pool, sch, off, diff.Options{})
	sql = convergeYAMLWith(t, pool, sch, enforcedStep("", ", enforced: false, notValid: true", ", notValid: true"), diff.Options{})
	if !contains(sql, `add constraint "t_fk" foreign key ("pid") references "`+sch+`"."p"("id") not valid`) || contains(sql, "validate constraint") {
		t.Errorf("notValid: true enforces the foreign key for new rows only, got:\n%s", sql)
	}
	if got, want := constraintStates(t, pool, sch), "t_ck false false ck note; t_fk true false fk note"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}

	immediate := enforcedStep("    validate: immediate\n", "", "")
	d := loadYAML(t, sch, immediate)
	plan := diff.PlanWith(introspect(t, pool, d), d, diff.Options{})
	if locks := lockWarnings(plan); len(locks) != 2 {
		t.Errorf("enforcing the check and validating the foreign key in one step get lock warnings, got %v", locks)
	}
	sql = convergeYAMLWith(t, pool, sch, immediate, diff.Options{})
	if pg19 != contains(sql, `alter constraint "t_ck" enforced`) || len(diff.RenderConcurrent(plan)) != 0 {
		t.Errorf("validate: immediate enforces the check in one step, in place on 19, got:\n%s", sql)
	}
	if got := constraintStates(t, pool, sch); got != all {
		t.Errorf("got %s, want %s", got, all)
	}

	convergeYAMLWith(t, pool, sch, off, diff.Options{})
	sql = convergeYAMLWith(t, pool, sch, immediate, diff.Options{})
	if !contains(sql, `alter constraint "t_fk" enforced`) {
		t.Errorf("validate: immediate enforces the foreign key in place, got:\n%s", sql)
	}
	if got := constraintStates(t, pool, sch); got != all {
		t.Errorf("got %s, want %s", got, all)
	}

	convergeYAMLWith(t, pool, sch, off, diff.Options{})
	if _, err := pool.Exec(ctx, `insert into `+q(sch)+`.t values (2, 9, -1)`); err != nil {
		t.Fatal(err)
	}
	d = loadYAML(t, sch, immediate)
	if stmt, err := applyAsMigrations(ctx, pool, diff.PlanWith(introspect(t, pool, d), d, diff.Options{})); err == nil || !strings.Contains(stmt, `"t_`) {
		t.Errorf("enforcing over violating rows must fail, got %q %v", stmt, err)
	}
}

func lockWarnings(p *diff.PlanDiff) []string {
	out := []string{}
	for _, w := range p.Warnings {
		if w.Kind == "lock" {
			out = append(out, w.Message)
		}
	}
	return out
}

func TestIntegrationNotEnforcedAddedWithoutLockWarning(t *testing.T) {
	requireServerVersion(t, 180000)
	pool := connect(t)
	sch := freshSchema(t, pool)
	convergeYAMLWith(t, pool, sch, `
schema SCH:
  table p:
    columns: {id: {type: int, primaryKey: true}}
  table t:
    columns: {id: {type: int, primaryKey: true}, pid: {type: int, nullable: true}, a: {type: int, nullable: true}}
`, diff.Options{})
	y := enforcedStep("    validate: immediate\n", ", enforced: false", ", enforced: false")
	d := loadYAML(t, sch, y)
	if locks := lockWarnings(diff.PlanWith(introspect(t, pool, d), d, diff.Options{})); len(locks) != 0 {
		t.Errorf("NOT ENFORCED constraints check no rows, got %v", locks)
	}
	convergeYAMLWith(t, pool, sch, y, diff.Options{})
	if got, want := constraintStates(t, pool, sch), "t_ck false false ck note; t_fk false false fk note"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestIntegrationValidateImmediateReplacementKeepsComment(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	convergeYAMLWith(t, pool, sch, enforcedStep("", ", notValid: true", ", notValid: true"), diff.Options{})
	sql := convergeYAMLWith(t, pool, sch, enforcedStep("    validate: immediate\n", "", ""), diff.Options{})
	if !contains(sql, `drop constraint "t_ck"`) || !contains(sql, `comment on constraint "t_ck"`) || !contains(sql, `comment on constraint "t_fk"`) {
		t.Errorf("a replaced constraint gets its comment again, got:\n%s", sql)
	}
	if got, want := constraintStates(t, pool, sch), "t_ck true true ck note; t_fk true true fk note"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestIntegrationVirtualSetExpressionWithCheck(t *testing.T) {
	requireServerVersion(t, 180000)
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	pg19 := serverVersion(t, pool) >= 190000
	table := `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      a: {type: int, nullable: true}
      g: {type: int, nullable: true, generated: EXPR, virtual: true, comment: g note}
    constraints:
      t_a_check: {type: check, expression: a > 0, enforced: false}
      t_g_check: {type: check, expression: g < 1000}
  table n:
    columns:
      id: {type: int, primaryKey: true}
      a: {type: int}
      g: {type: int, generated: EXPR, virtual: true}
`
	convergeYAMLWith(t, pool, sch, strings.ReplaceAll(table, "EXPR", "a * 2"), diff.Options{Unsafe: true})
	if _, err := pool.Exec(ctx, `insert into `+q(sch)+`.t values (1, 5); insert into `+q(sch)+`.n values (1, 5)`); err != nil {
		t.Fatal(err)
	}
	changed := strings.ReplaceAll(table, "EXPR", "a * 3")
	d := loadYAML(t, sch, changed)
	safe := diff.PlanWith(introspect(t, pool, d), d, diff.Options{})
	if msg := warningMessages(safe); pg19 == strings.Contains(msg, "PostgreSQL 18 rejects SET EXPRESSION") {
		t.Errorf("only PostgreSQL 18 warns about generated column t.g, got:\n%s", msg)
	}
	sql := convergeYAMLWith(t, pool, sch, changed, diff.Options{Unsafe: true})
	if !contains(sql, `alter table "`+sch+`"."n" alter column "g" set expression`) || pg19 != contains(sql, `alter table "`+sch+`"."t" alter column "g" set expression`) {
		t.Errorf("SET EXPRESSION is used where the server takes it, got:\n%s", sql)
	}
	var g int
	var note string
	if err := pool.QueryRow(ctx, `select g, col_description('`+q(sch)+`.t'::regclass, (select attnum from pg_attribute where attrelid = '`+q(sch)+`.t'::regclass and attname = 'g')) from `+q(sch)+`.t`).Scan(&g, &note); err != nil || g != 15 || note != "g note" {
		t.Errorf("got g = %d, comment %q, %v", g, note, err)
	}
	if got := constraintStates(t, pool, sch); got != "t_a_check false false -; t_g_check true true -" {
		t.Errorf("constraints: %s", got)
	}
}

func TestIntegrationGeneratedKindSwitch(t *testing.T) {
	requireServerVersion(t, 180000)
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	table := `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      s: {type: int, nullable: true, generated: id * 2, virtual: SKIND, comment: s note}
      v: {type: int, nullable: true, generated: id * 3, virtual: VKIND}
`
	convergeYAMLWith(t, pool, sch, strings.NewReplacer("SKIND", "false", "VKIND", "true").Replace(table), diff.Options{Unsafe: true})
	if _, err := pool.Exec(ctx, `insert into `+q(sch)+`.t values (1)`); err != nil {
		t.Fatal(err)
	}
	sql := convergeYAMLWith(t, pool, sch, strings.NewReplacer("SKIND", "true", "VKIND", "false").Replace(table), diff.Options{Unsafe: true})
	if !contains(sql, `add column "s" int generated always as (id * 2) virtual`) || !contains(sql, `add column "v" int generated always as (id * 3) stored`) {
		t.Errorf("switching between stored and virtual adds the columns again, got:\n%s", sql)
	}
	var kinds, note string
	var s, v int
	if err := pool.QueryRow(ctx, `select string_agg(attname || '=' || attgenerated::text, ',' order by attname), col_description(min(attrelid), min(attnum) filter (where attname = 's'))
		from pg_attribute where attrelid = $1::regclass and attname in ('s', 'v')`, q(sch)+".t").Scan(&kinds, &note); err != nil || kinds != "s=v,v=s" || note != "s note" {
		t.Errorf("got %q, comment %q, %v", kinds, note, err)
	}
	if err := pool.QueryRow(ctx, `select s, v from `+q(sch)+`.t`).Scan(&s, &v); err != nil || s != 2 || v != 3 {
		t.Errorf("got s = %d, v = %d, %v", s, v, err)
	}
}
