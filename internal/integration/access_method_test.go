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

// accessMethods returns the access method of every table and materialized
// view in schema x, "" for a partitioned table without one.
func accessMethods(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		select c.relname, coalesce(am.amname::text, '')
		from pg_class c left join pg_am am on am.oid = c.relam
		where c.relnamespace = 'x'::regnamespace and c.relkind in ('r', 'p', 'm')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, am string
		if err := rows.Scan(&name, &am); err != nil {
			t.Fatal(err)
		}
		out[name] = am
	}
	return out
}

func TestIntegrationAccessMethods(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	src := freshDatabase(t, admin, "pgytest_access_methods")
	var version int
	if err := src.QueryRow(ctx, "select current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	partitionedUsing := ""
	if version >= 170000 {
		partitionedUsing = " using heap2"
	}
	if _, err := src.Exec(ctx, `
		create access method heap2 type table handler heap_tableam_handler;
		create schema x;
		create table x.t (id int primary key, v text) using heap2;
		create table x.plain (id int primary key);
		create table x.p (id int, at int) partition by range (at)`+partitionedUsing+`;
		create table x.p1 partition of x.p for values from (0) to (10) using heap2;
		create materialized view x.mv using heap2 as select 1 as one;
		insert into x.t values (1, 'a'), (2, 'b');`); err != nil {
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
	want := 3
	if version >= 170000 {
		want = 4
	}
	if n := strings.Count(all, "accessMethod: heap2"); n != want || strings.Contains(all, "accessMethod: heap\n") {
		t.Errorf("want accessMethod: heap2 on t, p1 and mv (and p from PostgreSQL 17 on), %d objects, and nothing for the default heap; got %d in:\n%s", want, n, all)
	}
	pulled, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}
	opts := diff.Options{Unsafe: true, Prune: true}
	if sql := diff.Render(diff.PlanWith(live, pulled, opts)); sql != "" {
		t.Fatalf("pulled YAML must diff clean against its source:\n%s\n%s", sql, all)
	}

	dst := freshDatabase(t, admin, "pgytest_access_methods_copy")
	if _, err := dst.Exec(ctx, `create access method heap2 type table handler heap_tableam_handler`); err != nil {
		t.Fatal(err)
	}
	if stmt, err := applyAsMigrations(ctx, dst, diff.PlanWith(introspectAll(t, dst), pulled, diff.Options{})); err != nil {
		t.Fatalf("rebuilding: %q: %v", stmt, err)
	}
	if sql := diff.Render(diff.PlanWith(introspectAll(t, dst), pulled, opts)); sql != "" {
		t.Errorf("rebuilt database differs from the pulled YAML:\n%s", sql)
	}
	if got, want := accessMethods(t, dst), accessMethods(t, src); len(got) != len(want) || got["t"] != "heap2" || got["p1"] != "heap2" || got["mv"] != "heap2" || got["p"] != want["p"] || got["plain"] != "heap" {
		t.Errorf("rebuilt access methods %v, source %v", got, want)
	}

	if _, err := src.Exec(ctx, `alter table x.t set access method heap; alter materialized view x.mv set access method heap`); err != nil {
		t.Fatal(err)
	}
	moved := introspectAll(t, src)
	if safe := diff.PlanReportingUnsafe(moved, pulled, diff.Options{}); strings.Contains(diff.Render(safe), "set access method") {
		t.Errorf("rewriting a table for its access method needs --unsafe:\n%s", diff.Render(safe))
	}
	plan := diff.PlanWith(moved, pulled, diff.Options{Unsafe: true})
	sql := diff.Render(plan)
	for _, want := range []string{
		`alter table "x"."t" set access method "heap2";`,
		`alter materialized view "x"."mv" set access method "heap2";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	locks := 0
	for _, w := range plan.Warnings {
		if w.Kind == "lock" && strings.Contains(w.Message, "SET ACCESS METHOD") {
			locks++
		}
	}
	if locks != 2 {
		t.Errorf("want a lock warning per rewrite, got %d: %+v", locks, plan.Warnings)
	}
	if stmt, err := applyAsMigrations(ctx, src, plan); err != nil {
		t.Fatalf("changing back: %q: %v", stmt, err)
	}
	if sql := diff.Render(diff.PlanWith(introspectAll(t, src), pulled, opts)); sql != "" {
		t.Errorf("after the changes the source must match the YAML again:\n%s", sql)
	}
	var n int
	if err := src.QueryRow(ctx, `select count(*) from x.t`).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows of x.t after the rewrite: %d, %v", n, err)
	}
}
