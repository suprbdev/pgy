package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

// convergeYAML applies the --unsafe plan for y (with SCH replaced by sch),
// fails unless a second plan is empty, and returns the applied SQL,
// including the statements that run outside a transaction.
func convergeYAML(t *testing.T, pool *pgxpool.Pool, sch, y string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(y, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.Plan(introspect(t, pool, d), d, true)
	applyPlan(t, pool, plan)
	live := introspect(t, pool, d)
	if again := diff.Plan(live, d, true); diff.Render(again)+diff.RenderConcurrent(again) != "" {
		t.Fatalf("plan did not converge:\n%s%s", diff.Render(again), diff.RenderConcurrent(again))
	}
	return diff.Render(plan) + diff.RenderConcurrent(plan)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// introspect reads the live schema, compares the data rows d declares and
// checks the settings it declares.
func introspect(t *testing.T, pool *pgxpool.Pool, d *schema.Database) *diff.Live {
	t.Helper()
	ctx := context.Background()
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectData(ctx, pool, live, d); err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectSettings(ctx, pool, live, d); err != nil {
		t.Fatal(err)
	}
	return live
}
