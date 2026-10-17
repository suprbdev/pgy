package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/db"
	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/group"
	"github.com/suprbdev/pgy/internal/schema"
)

func freshDatabase(t *testing.T, admin *pgxpool.Pool, name string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	if _, err := admin.Exec(ctx, fmt.Sprintf("drop database if exists %q", name)); err != nil {
		t.Fatalf("drop database: %v", err)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf("create database %q", name)); err != nil {
		t.Fatalf("create database: %v", err)
	}
	cfg, err := pgxpool.ParseConfig(dsn(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect %s: %v", name, err)
	}
	t.Cleanup(func() {
		pool.Close()
		admin.Exec(context.Background(), fmt.Sprintf("drop database if exists %q", name)) //nolint
	})
	return pool
}

func existingRoles(t *testing.T, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(context.Background(), "select rolname from pg_roles")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out[r] = true
	}
	return out
}

func TestIntegrationSplitMatchesSingleMigration(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()

	var paths []string
	for _, pattern := range []string{"modules/*/*.yml", "links/*.yml", "schema.yml"} {
		m, err := filepath.Glob(filepath.Join("..", "..", "examples", pattern))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, m...)
	}
	full, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}

	before := existingRoles(t, admin)
	t.Cleanup(func() {
		for name := range full.Roles {
			if !before[name] {
				admin.Exec(context.Background(), fmt.Sprintf("drop role if exists %q", name)) //nolint
			}
		}
	})

	single := freshDatabase(t, admin, "pgytest_split_single")
	live, err := diff.Introspect(ctx, single)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyInTx(ctx, single, diff.Render(diff.Plan(live, full, false))); err != nil {
		t.Fatalf("single migration: %v", err)
	}

	split := freshDatabase(t, admin, "pgytest_split_grouped")
	live, err = diff.Introspect(ctx, split)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := group.Split(paths, group.ModeFile)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := group.Plan(live, paths, groups, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) < 10 {
		t.Fatalf("expected the examples to split into many migrations, got %d", len(steps))
	}
	for _, s := range steps {
		if err := db.ApplyInTx(ctx, split, diff.Render(s.Plan)); err != nil {
			t.Fatalf("step %s: %v\n%s", s.Name, err, diff.Render(s.Plan))
		}
	}

	residual := func(pool *pgxpool.Pool) string {
		l, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Render(diff.Plan(l, full, false))
	}
	if got, want := residual(split), residual(single); got != want {
		t.Fatalf("split build diverges from single migration build\nsplit residual:\n%s\nsingle residual:\n%s", got, want)
	}

	live, err = diff.Introspect(ctx, split)
	if err != nil {
		t.Fatal(err)
	}
	again, err := group.Plan(live, paths, groups, false)
	if err != nil {
		t.Fatal(err)
	}
	var regrouped []string
	for _, s := range again {
		regrouped = append(regrouped, s.Plan.Creates...)
		regrouped = append(regrouped, s.Plan.Alters...)
		regrouped = append(regrouped, s.Plan.Drops...)
	}
	l, err := diff.Introspect(ctx, split)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(l, full, false)
	expected := append(append(append([]string{}, p.Creates...), p.Alters...), p.Drops...)
	sort.Strings(regrouped)
	sort.Strings(expected)
	if strings.Join(regrouped, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("re-planning an up-to-date database should match the single plan\nsplit:\n%s\nsingle:\n%s", group.Render(again), diff.Render(p))
	}
}
