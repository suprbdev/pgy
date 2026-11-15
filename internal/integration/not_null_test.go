package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationSetNotNullWithoutLongLockOnPG18(t *testing.T) {
	requireServerVersion(t, 180000)
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	opts := diff.Options{Unsafe: true}
	convergeYAMLWith(t, pool, sch, `
schema SCH:
  table t:
    columns: {id: {type: int, primaryKey: true}, a: {type: int, nullable: true}}
`, opts)
	if _, err := pool.Exec(ctx, `insert into `+sch+`.t select g, g from generate_series(1, 100) g`); err != nil {
		t.Fatal(err)
	}
	notNull := `
schema SCH:
  table t:
    columns: {id: {type: int, primaryKey: true}, a: {type: int}}
`
	sql := convergeYAMLWith(t, pool, sch, notNull, opts)
	if !strings.Contains(sql, `add constraint "t_a_not_null" not null "a" not valid`) || strings.Contains(sql, "set not null") {
		t.Errorf("expected the NOT VALID constraint instead of SET NOT NULL:\n%s", sql)
	}
	var validated bool
	if err := pool.QueryRow(ctx, `select convalidated from pg_constraint where conname = 't_a_not_null' and conrelid = '`+sch+`.t'::regclass`).Scan(&validated); err != nil || !validated {
		t.Fatalf("constraint not validated: %v %v", validated, err)
	}

	if _, err := pool.Exec(ctx, `create table `+sch+`.u (id int primary key, b int); insert into `+sch+`.u values (1, null)`); err != nil {
		t.Fatal(err)
	}
	withU := notNull + "  table u:\n    columns: {id: {type: int, primaryKey: true}, b: {type: int}}\n"
	d := loadYAML(t, sch, withU)
	plan := diff.PlanWith(introspect(t, pool, d), d, opts)
	if stmt, err := applyAsMigrations(ctx, pool, plan); err == nil || !strings.Contains(stmt, "validate constraint") {
		t.Fatalf("validating over a NULL must fail in the follow-up migration, got %q %v", stmt, err)
	}
	if _, err := pool.Exec(ctx, `update `+sch+`.u set b = 0`); err != nil {
		t.Fatal(err)
	}
	retry := diff.PlanWith(introspect(t, pool, d), d, opts)
	if diff.Render(retry) != "" || len(retry.Concurrent) != 1 || !strings.Contains(retry.Concurrent[0], `validate constraint "u_b_not_null"`) {
		t.Fatalf("expected only the pending VALIDATE, got %q / %v", diff.Render(retry), retry.Concurrent)
	}
	convergeYAMLWith(t, pool, sch, withU, opts)
}

func loadYAML(t *testing.T, sch, y string) *schema.Database {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(y, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
