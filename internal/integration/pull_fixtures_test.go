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
	"github.com/suprbdev/pgy/internal/validate"
)

// TestIntegrationPullFixtures loads each SQL schema in testdata/pull into a
// fresh database, pulls it with owners, and requires the pulled YAML to
// diff clean against that database and to rebuild an identical schema in a
// second fresh database.
func TestIntegrationPullFixtures(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	fixtures, err := filepath.Glob(filepath.Join("testdata", "pull", "*.sql"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, f := range fixtures {
		name := strings.TrimSuffix(filepath.Base(f), ".sql")
		t.Run(name, func(t *testing.T) {
			sql, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src := freshDatabase(t, admin, "pgytest_pull_"+name)
			if _, err := src.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("loading fixture: %v", err)
			}
			live := introspectAll(t, src)
			res, err := pull.Build(live, pull.Options{Owners: true})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			paths := []string{}
			for _, file := range res.Files {
				p := filepath.Join(dir, file.Name)
				if err := os.WriteFile(p, file.Content, 0o644); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, p)
			}
			for _, issue := range validate.Files(paths) {
				if issue.Severity == validate.Error {
					t.Errorf("pgy validate rejects pulled YAML: %s", issue)
				}
			}
			pulled, err := schema.LoadAndMerge(paths)
			if err != nil {
				t.Fatal(err)
			}
			dump := func() {
				for _, file := range res.Files {
					t.Logf("%s:\n%s", file.Name, file.Content)
				}
			}
			opts := diff.Options{Unsafe: true, Prune: true}
			if sql := diff.Render(diff.PlanWith(live, pulled, opts)); sql != "" {
				dump()
				t.Fatalf("pulled YAML must diff clean against its source:\n%s", sql)
			}
			dst := freshDatabase(t, admin, "pgytest_pull_"+name+"_copy")
			plan := diff.PlanWith(introspectAll(t, dst), pulled, diff.Options{})
			if stmt, err := applyAsMigrations(ctx, dst, plan); err != nil {
				dump()
				t.Fatalf("rebuilding from pulled YAML: %q: %v", stmt, err)
			}
			if sql := diff.Render(diff.PlanWith(introspectAll(t, dst), pulled, opts)); sql != "" {
				dump()
				t.Errorf("rebuilt database differs from the pulled YAML:\n%s", sql)
			}
			if a := pgDump(t, "pgytest_pull_"+name); a != "" {
				if d := dumpDifferences(a, pgDump(t, "pgytest_pull_"+name+"_copy")); len(d) > 0 {
					dump()
					t.Errorf("pg_dump of the rebuilt database differs from the original:\n%s", strings.Join(d, "\n"))
				}
			}
		})
	}
}

func introspectAll(t *testing.T, pool *pgxpool.Pool) *diff.Live {
	t.Helper()
	ctx := context.Background()
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectDetails(ctx, pool, live); err != nil {
		t.Fatal(err)
	}
	return live
}
