package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

// TestIntegrationDataRoundTrip pulls a table of awkward values with its
// rows and rebuilds it in a fresh database: every row must come back with
// the same text form.
func TestIntegrationDataRoundTrip(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	sql, err := os.ReadFile(filepath.Join("testdata", "data", "tricky_values.sql"))
	if err != nil {
		t.Fatal(err)
	}
	src := freshDatabase(t, admin, "pgytest_data_src")
	if _, err := src.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	rows, err := diff.TableRows(ctx, src, "d1.ref", false)
	if err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(introspectAll(t, src), pull.Options{Data: map[string][]map[string]any{"d1.ref": rows}})
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
	dst := freshDatabase(t, admin, "pgytest_data_dst")
	if stmt, err := applyAsMigrations(ctx, dst, diff.PlanWith(introspectAll(t, dst), pulled, diff.Options{})); err != nil {
		t.Fatalf("rebuilding: %q: %v", stmt, err)
	}
	text := func(pool *pgxpool.Pool) []string {
		r, err := pool.Query(ctx, `select r::text from d1.ref r order by id`)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		out := []string{}
		for r.Next() {
			var s string
			if err := r.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	want, got := text(src), text(dst)
	if len(want) != len(got) {
		t.Fatalf("rows: want %d, got %d", len(want), len(got))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("row %d differs:\nsource:  %s\nrebuilt: %s", i, want[i], got[i])
		}
	}
	d := introspectAll(t, dst)
	if err := diff.IntrospectData(ctx, dst, d, pulled); err != nil {
		t.Fatal(err)
	}
	if sql := diff.Render(diff.PlanWith(d, pulled, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("rebuilt data must diff clean:\n%s", sql)
	}
}
