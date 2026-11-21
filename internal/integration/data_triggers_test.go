package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

// TestIntegrationDataBeforeTriggers pulls tables with rows, an audit
// trigger, a rule, a deferred constraint trigger, a deferred foreign key and
// forced row level security, and rebuilds them in a fresh database: the rows
// go in before the triggers and rules, so they fire none of them and leave
// no deferred trigger events for the ALTER TABLE statements after them. It
// then writes the rows into the existing tables of a third database, where
// the triggers fire and the plan fires the deferred events before altering
// the tables.
func TestIntegrationDataBeforeTriggers(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	sql, err := os.ReadFile(filepath.Join("testdata", "data", "triggers.sql"))
	if err != nil {
		t.Fatal(err)
	}
	src := freshDatabase(t, admin, "pgytest_data_triggers")
	if _, err := src.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	data := map[string][]map[string]any{}
	for _, fq := range []string{"d4.account", "d4.seat"} {
		rows, err := diff.TableRows(ctx, src, fq, false)
		if err != nil {
			t.Fatal(err)
		}
		data[fq] = rows
	}
	res, err := pull.Build(introspectAll(t, src), pull.Options{Data: data})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := []string{}
	for _, f := range res.Files {
		p := filepath.Join(dir, f.Name)
		if err := os.WriteFile(p, f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	pulled, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}
	count := func(pool *pgxpool.Pool, table string) int {
		var n int
		if err := pool.QueryRow(ctx, "select count(*) from "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	converged := func(pool *pgxpool.Pool) {
		t.Helper()
		l := introspectAll(t, pool)
		if err := diff.IntrospectData(ctx, pool, l, pulled); err != nil {
			t.Fatal(err)
		}
		if sql := diff.Render(diff.PlanWith(l, pulled, diff.Options{})); sql != "" {
			t.Errorf("rebuilt database must diff clean:\n%s", sql)
		}
	}

	dst := freshDatabase(t, admin, "pgytest_data_triggers_copy")
	if stmt, err := applyAsMigrations(ctx, dst, diff.PlanWith(introspectAll(t, dst), pulled, diff.Options{})); err != nil {
		t.Fatalf("rebuilding: %q: %v", stmt, err)
	}
	if n := count(dst, "d4.audit"); n != 0 {
		t.Errorf("the rows fired the triggers or rules of their new tables: %d audit rows", n)
	}
	if n := count(dst, "d4.account") + count(dst, "d4.seat"); n != 4 {
		t.Errorf("want 4 rows, got %d", n)
	}
	converged(dst)
	if a := pgDump(t, "pgytest_data_triggers"); a != "" {
		if d := dumpDifferences(a, pgDump(t, "pgytest_data_triggers_copy")); len(d) > 0 {
			t.Errorf("pg_dump of the rebuilt database differs from the original:\n%s", strings.Join(d, "\n"))
		}
	}

	existing := freshDatabase(t, admin, "pgytest_data_triggers_existing")
	for _, stmt := range []string{string(sql), "truncate d4.seat, d4.account, d4.audit", "alter table d4.account disable row level security, no force row level security"} {
		if _, err := existing.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	l := introspectAll(t, existing)
	if err := diff.IntrospectData(ctx, existing, l, pulled); err != nil {
		t.Fatal(err)
	}
	plan := diff.PlanWith(l, pulled, diff.Options{})
	if !strings.Contains(diff.Render(plan), "set constraints all immediate;") {
		t.Errorf("expected the deferred trigger events fired after the rows:\n%s", diff.Render(plan))
	}
	if stmt, err := applyAsMigrations(ctx, existing, plan); err != nil {
		t.Fatalf("writing rows into existing tables: %q: %v", stmt, err)
	}
	if n := count(existing, "d4.audit"); n == 0 {
		t.Error("the triggers of existing tables fire on the rows written into them")
	}
	converged(existing)
}
