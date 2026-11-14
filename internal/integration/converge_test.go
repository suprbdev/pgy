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
// fails unless a second plan is empty, and returns the applied SQL.
func convergeYAML(t *testing.T, pool *pgxpool.Pool, sch, y string) string {
	t.Helper()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(y, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.Plan(live, d, true)
	applyPlan(t, pool, plan)
	if live, err = diff.Introspect(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if again := diff.Render(diff.Plan(live, d, true)); again != "" {
		t.Fatalf("plan did not converge:\n%s", again)
	}
	return diff.Render(plan)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
