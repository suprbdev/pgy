package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

const numericLiteralLive = `
create domain SCH.amount as numeric check (value >= 0.50);
create table SCH.t (
  id bigint primary key,
  n numeric,
  m numeric(5,2),
  i int,
  f float8,
  ok boolean generated always as (n > 1.50) stored,
  d numeric default 1.50,
  constraint t_n check (n > 1.50),
  constraint t_neg check (n > -1.50),
  constraint t_m check (m > 1.5),
  constraint t_i check (i > 1.50),
  constraint t_f check (f > 1.50),
  constraint t_in check (n in (1.50, 2.5, 3.00)),
  constraint t_between check (n between 0.50 and 9.90)
);
create index t_part on SCH.t (id) where n > 1.50;
alter table SCH.t enable row level security;
create policy t_p on SCH.t using (n > 1.50);
`

const numericLiteralYAML = `
schema SCH:
  domain amount:
    type: numeric
    check: VALUE >= 0.5
  table t:
    columns:
      id: {type: bigint}
      n: {type: numeric, nullable: true}
      m: {type: "numeric(5,2)", nullable: true}
      i: {type: int, nullable: true}
      f: {type: float8, nullable: true}
      ok: {type: boolean, nullable: true, generated: "n > 1.5"}
      d: {type: numeric, nullable: true, default: "1.50"}
    primaryKey: [id]
    constraints:
      t_n: {type: check, expression: "n > 1.5"}
      t_neg: {type: check, expression: "n > -1.5"}
      t_m: {type: check, expression: "m > 1.50"}
      t_i: {type: check, expression: "i > 1.5"}
      t_f: {type: check, expression: "f > 1.5"}
      t_in: {type: check, expression: "n in (1.5, 2.50, 3.0)"}
      t_between: {type: check, expression: "n between .5 and 9.9"}
    indexes:
      t_part: {columns: [id], where: "n > 1.5"}
    rowLevelSecurity: true
    policies:
      t_p: {using: "n > 1.5"}
`

// TestIntegrationNumericLiteralScaleNoChange declares the constants a live
// schema compares with in another scale (1.5 against 1.50) in checks,
// domain checks, a generated column, an index predicate and a policy:
// nothing is planned, with or without --unsafe.
func TestIntegrationNumericLiteralScaleNoChange(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	if _, err := pool.Exec(context.Background(), strings.ReplaceAll(numericLiteralLive, "SCH", sch)); err != nil {
		t.Fatal(err)
	}
	assertConverged(t, pool, loadYAML(t, sch, numericLiteralYAML))
}

// TestIntegrationNumericLiteralValueChange changes the value of compared
// constants, and the scale of a default, which shows in the values it
// inserts: each is planned.
func TestIntegrationNumericLiteralValueChange(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	if _, err := pool.Exec(context.Background(), strings.ReplaceAll(numericLiteralLive, "SCH", sch)); err != nil {
		t.Fatal(err)
	}
	y := strings.NewReplacer(
		`t_n: {type: check, expression: "n > 1.5"}`, `t_n: {type: check, expression: "n > 1.6"}`,
		`t_i: {type: check, expression: "i > 1.5"}`, `t_i: {type: check, expression: "i > 1.6"}`,
		`where: "n > 1.5"`, `where: "n > 1.6"`,
		`default: "1.50"`, `default: "1.5"`,
	).Replace(numericLiteralYAML)
	d := loadYAML(t, sch, y)
	sql := planSQL(diff.Plan(probed(t, pool, d), d, true))
	for _, want := range []string{
		`add constraint "t_n" check (n > 1.6)`,
		`add constraint "t_i" check (i > 1.6)`,
		`create index if not exists "t_part"`,
		`alter column "d" set default 1.5`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("plan lacks %q:\n%s", want, sql)
		}
	}
	for _, unwanted := range []string{`"t_neg"`, `"t_m"`, `"t_f"`, `"t_in"`, `"t_between"`, `"t_p"`, `"ok"`, `amount`} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("plan changes %s, which only differs in scale:\n%s", unwanted, sql)
		}
	}
	applyPlan(t, pool, diff.Plan(probed(t, pool, d), d, true))
	assertConverged(t, pool, d)
}
