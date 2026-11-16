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

// inPlaceTablespace creates an in-place tablespace (no directory needed) for
// the test, or skips it when the server or role doesn't allow one.
func inPlaceTablespace(t *testing.T, admin *pgxpool.Pool, name string) {
	t.Helper()
	ctx := context.Background()
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	_, _ = conn.Exec(ctx, `drop tablespace if exists `+name)
	if _, err := conn.Exec(ctx, `set allow_in_place_tablespaces = on`); err != nil {
		t.Skipf("in-place tablespaces unavailable: %v", err)
	}
	_, err = conn.Exec(ctx, `create tablespace `+name+` location ''`)
	_, _ = conn.Exec(ctx, `reset allow_in_place_tablespaces`)
	if err != nil {
		t.Skipf("cannot create a tablespace: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `drop tablespace if exists `+name) })
}

func TestIntegrationTablespaces(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	inPlaceTablespace(t, admin, "pgytest_ts")
	src := freshDatabase(t, admin, "pgytest_tablespaces")
	if _, err := src.Exec(ctx, `
		create schema x;
		create table x.t (id int primary key using index tablespace pgytest_ts, v int, r int4range,
			constraint t_v_uq unique (v) using index tablespace pgytest_ts,
			constraint t_r_excl exclude using gist (r with &&) using index tablespace pgytest_ts where (v > 0)) tablespace pgytest_ts;
		create index t_v on x.t (v) tablespace pgytest_ts;
		create table x.plain (id int primary key);
		create table x.p (id int, at int) partition by range (at) tablespace pgytest_ts;
		create table x.p1 partition of x.p for values from (0) to (10) tablespace pgytest_ts;
		create materialized view x.mv tablespace pgytest_ts as select 1 as one;
		create index mv_one on x.mv (one) tablespace pgytest_ts;`); err != nil {
		t.Fatal(err)
	}
	live := introspectAll(t, src)
	res, err := pull.Build(live, pull.Options{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := []string{}
	all := ""
	for _, f := range res.Files {
		p := filepath.Join(dir, f.Name)
		if err := os.WriteFile(p, f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
		paths, all = append(paths, p), all+string(f.Content)
	}
	if n := strings.Count(all, "tablespace: pgytest_ts"); n != 9 {
		t.Errorf("want the tablespace on t, its key, unique and exclusion indexes, t_v, p, p1, mv and mv_one (9 objects), got %d in:\n%s", n, all)
	}
	pulled, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}
	opts := diff.Options{Unsafe: true, Prune: true}
	if sql := diff.Render(diff.PlanWith(live, pulled, opts)); sql != "" {
		t.Fatalf("pulled YAML must diff clean against its source:\n%s\n%s", sql, all)
	}
	dst := freshDatabase(t, admin, "pgytest_tablespaces_copy")
	if stmt, err := applyAsMigrations(ctx, dst, diff.PlanWith(introspectAll(t, dst), pulled, diff.Options{})); err != nil {
		t.Fatalf("rebuilding: %q: %v", stmt, err)
	}
	if sql := diff.Render(diff.PlanWith(introspectAll(t, dst), pulled, opts)); sql != "" {
		t.Errorf("rebuilt database differs from the pulled YAML:\n%s", sql)
	}

	if _, err := src.Exec(ctx, `alter table x.t set tablespace pg_default; alter index x.t_v set tablespace pg_default; alter materialized view x.mv set tablespace pg_default`); err != nil {
		t.Fatal(err)
	}
	moved := introspectAll(t, src)
	plan := diff.PlanWith(moved, pulled, diff.Options{})
	sql := diff.Render(plan)
	for _, want := range []string{
		`alter table "x"."t" set tablespace "pgytest_ts";`,
		`alter index "x"."t_v" set tablespace "pgytest_ts";`,
		`alter materialized view "x"."mv" set tablespace "pgytest_ts";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	locks := 0
	for _, w := range plan.Warnings {
		if w.Kind == "lock" && strings.Contains(w.Message, "another tablespace") {
			locks++
		}
	}
	if locks != 3 {
		t.Errorf("want a lock warning per move, got %d: %+v", locks, plan.Warnings)
	}
	if stmt, err := applyAsMigrations(ctx, src, plan); err != nil {
		t.Fatalf("moving back: %q: %v", stmt, err)
	}
	if sql := diff.Render(diff.PlanWith(introspectAll(t, src), pulled, opts)); sql != "" {
		t.Errorf("after the moves the source must match the YAML again:\n%s", sql)
	}
}
