package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestIntegrationValidateConstraintAfterNotValid(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	steps := []string{`
schema SCH:
  table p:
    columns: {id: {type: int}}
    primaryKey: [id]
  table t:
    columns: {id: {type: int}, p_id: {type: int, nullable: true}}
    constraints:
      t_id_check: {type: check, expression: id > 0, notValid: true}
    foreignKeys:
      t_p_fkey: {columns: [p_id], references: {table: SCH.p, columns: [id]}, notValid: true}
`, `
schema SCH:
  table p:
    columns: {id: {type: int}}
    primaryKey: [id]
  table t:
    columns: {id: {type: int}, p_id: {type: int, nullable: true}}
    constraints:
      t_id_check: {type: check, expression: id > 0}
    foreignKeys:
      t_p_fkey: {columns: [p_id], references: {table: SCH.p, columns: [id]}}
`}
	for i, y := range steps {
		if sql := convergeYAML(t, pool, sch, y); i == 1 && (!contains(sql, `validate constraint "t_id_check"`) || !contains(sql, `validate constraint "t_p_fkey"`)) {
			t.Errorf("removing notValid must validate the constraints, got:\n%s", sql)
		}
		if i == 0 {
			if _, err := pool.Exec(context.Background(), `insert into "`+sch+`".p values (1); insert into "`+sch+`".t values (1, 1)`); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestIntegrationConstraintsOnExistingTablesValidatedSeparately(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	opts := diff.Options{Unsafe: true}
	base := `
schema SCH:
  table p:
    columns: {id: {type: int, primaryKey: true}}
  table t:
    columns: {id: {type: int, primaryKey: true}, p_id: {type: int, nullable: true}, n: {type: int, nullable: true}}
`
	convergeYAMLWith(t, pool, sch, base, opts)
	if _, err := pool.Exec(ctx, `insert into `+sch+`.p values (1); insert into `+sch+`.t values (1, 1, 5)`); err != nil {
		t.Fatal(err)
	}
	withConstraints := base + `    constraints:
      t_n_positive: {type: check, expression: n > 0}
    foreignKeys:
      t_p_fkey: {columns: [p_id], references: {table: SCH.p, columns: [id]}}
`
	d := loadYAML(t, sch, withConstraints)
	plan := diff.PlanWith(introspect(t, pool, d), d, opts)
	main := diff.Render(plan)
	for _, want := range []string{`add constraint "t_n_positive" check (n > 0) not valid`, `add constraint "t_p_fkey" foreign key ("p_id") references "` + sch + `"."p"("id") not valid`} {
		if !strings.Contains(main, want) {
			t.Errorf("missing %q in the main migration:\n%s", want, main)
		}
	}
	if len(plan.Concurrent) != 2 || len(plan.Warnings) != 0 {
		t.Errorf("expected two VALIDATE statements and no lock warnings, got %v / %+v", plan.Concurrent, plan.Warnings)
	}
	convergeYAMLWith(t, pool, sch, withConstraints, opts)
	var pending int
	if err := pool.QueryRow(ctx, `select count(*) from pg_constraint where conrelid = '`+sch+`.t'::regclass and not convalidated`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("constraints left unvalidated: %d %v", pending, err)
	}

	if _, err := pool.Exec(ctx, `alter table `+sch+`.t drop constraint t_n_positive; update `+sch+`.t set n = -1`); err != nil {
		t.Fatal(err)
	}
	plan = diff.PlanWith(introspect(t, pool, d), d, opts)
	if stmt, err := applyAsMigrations(ctx, pool, plan); err == nil || !strings.Contains(stmt, "validate constraint") {
		t.Fatalf("validating over a violating row must fail in the follow-up migration, got %q %v", stmt, err)
	}
	if _, err := pool.Exec(ctx, `update `+sch+`.t set n = 1`); err != nil {
		t.Fatal(err)
	}
	retry := diff.PlanWith(introspect(t, pool, d), d, opts)
	if diff.Render(retry) != "" || len(retry.Concurrent) != 1 || !strings.Contains(retry.Concurrent[0], `validate constraint "t_n_positive"`) {
		t.Fatalf("expected only the pending VALIDATE, got %q / %v", diff.Render(retry), retry.Concurrent)
	}
	convergeYAMLWith(t, pool, sch, withConstraints, opts)
}

func TestIntegrationValidateImmediate(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	table := `
schema SCH:
  table p:
    columns: {id: {type: int, primaryKey: true}}
  table t:
`
	convergeYAMLWith(t, pool, sch, table+"    columns: {id: {type: int, primaryKey: true}, p_id: {type: int, nullable: true}, n: {type: int, nullable: true}}\n", diff.Options{})
	if _, err := pool.Exec(ctx, `insert into `+sch+`.p values (1); insert into `+sch+`.t values (1, 1, 5)`); err != nil {
		t.Fatal(err)
	}
	immediate := table + `    validate: immediate
    columns: {id: {type: int, primaryKey: true}, p_id: {type: int, nullable: true}, n: {type: int}}
    constraints:
      t_n_positive: {type: check, expression: n > 0}
    foreignKeys:
      t_p_fkey: {columns: [p_id], references: {table: SCH.p, columns: [id]}}
`
	d := loadYAML(t, sch, immediate)
	plan := diff.PlanWith(introspect(t, pool, d), d, diff.Options{})
	sql := diff.Render(plan)
	if len(plan.Concurrent) != 0 || strings.Contains(sql, "not valid") {
		t.Errorf("validate: immediate adds every constraint validated in the main migration, got:\n%s%v", sql, plan.Concurrent)
	}
	locks := 0
	for _, w := range plan.Warnings {
		if w.Kind == "lock" {
			locks++
		}
	}
	if locks != 3 {
		t.Errorf("want lock warnings for the check, the foreign key and SET NOT NULL, got %+v", plan.Warnings)
	}
	convergeYAMLWith(t, pool, sch, immediate, diff.Options{})
	var pending int
	if err := pool.QueryRow(ctx, `select count(*) from pg_constraint where conrelid = '`+sch+`.t'::regclass and not convalidated`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("constraints left unvalidated: %d %v", pending, err)
	}

	if _, err := pool.Exec(ctx, `alter table `+sch+`.t drop constraint t_n_positive, drop constraint t_p_fkey;
		alter table `+sch+`.t add constraint t_n_positive check (n > 0) not valid, add constraint t_p_fkey foreign key (p_id) references `+sch+`.p (id) not valid`); err != nil {
		t.Fatal(err)
	}
	plan = diff.PlanWith(introspect(t, pool, d), d, diff.Options{})
	if len(plan.Concurrent) != 0 || !strings.Contains(diff.Render(plan), `drop constraint "t_n_positive"`) {
		t.Errorf("NOT VALID constraints are replaced by validated ones instead of validated later, got:\n%s%v", diff.Render(plan), plan.Concurrent)
	}
	convergeYAMLWith(t, pool, sch, immediate, diff.Options{})
	if err := pool.QueryRow(ctx, `select count(*) from pg_constraint where conrelid = '`+sch+`.t'::regclass and not convalidated`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("constraints left unvalidated after the replacement: %d %v", pending, err)
	}
}
