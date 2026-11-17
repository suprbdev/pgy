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
	"github.com/suprbdev/pgy/internal/validate"
)

// TestIntegrationDataRoundTripOddNames pulls rows of a table whose column
// names and JSON keys YAML would read as numbers, null, booleans or a merge
// key, or that contain a dot, and rebuilds the table with its rows.
func TestIntegrationDataRoundTripOddNames(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	src := freshDatabase(t, admin, "pgytest_oddnames_src")
	if _, err := src.Exec(ctx, `
create schema d2;
create table d2.pivot (id int primary key, "2024" numeric, "null" text, "true" boolean, "a.b" text, "<<" int, j jsonb);
insert into d2.pivot values
  (1, 1.5, 'x', true, 'dot', 3, '{"1": "a", "null": 2, "true": [1], "<<": 4, "nested": {"2": null}}'),
  (2, null, null, null, null, null, null);
`); err != nil {
		t.Fatal(err)
	}
	rows, err := diff.TableRows(ctx, src, "d2.pivot", false)
	if err != nil {
		t.Fatal(err)
	}
	live := introspectAll(t, src)
	res, err := pull.Build(live, pull.Options{Data: map[string][]map[string]any{"d2.pivot": rows}})
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
		t.Logf("%s:\n%s", f.Name, f.Content)
	}
	for _, issue := range validate.Files(paths) {
		t.Errorf("pgy validate: %s", issue)
	}
	pulled, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectData(ctx, src, live, pulled); err != nil {
		t.Fatal(err)
	}
	opts := diff.Options{Unsafe: true, Prune: true}
	if sql := diff.Render(diff.PlanWith(live, pulled, opts)); sql != "" {
		t.Fatalf("pulled YAML must diff clean against its source:\n%s", sql)
	}
	dst := freshDatabase(t, admin, "pgytest_oddnames_dst")
	if stmt, err := applyAsMigrations(ctx, dst, diff.PlanWith(introspectAll(t, dst), pulled, diff.Options{})); err != nil {
		t.Fatalf("rebuilding: %q: %v", stmt, err)
	}
	text := func(pool *pgxpool.Pool) []string {
		r, err := pool.Query(ctx, `select r::text from d2.pivot r order by id`)
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
	if sql := diff.Render(diff.PlanWith(d, pulled, opts)); sql != "" {
		t.Errorf("rebuilt data must diff clean:\n%s", sql)
	}
}

// TestIntegrationUnquotedScalarKeys applies hand-written YAML whose column
// and option names are unquoted numbers, null and booleans, and requires
// the next diff, with --unsafe --prune, to be empty.
func TestIntegrationUnquotedScalarKeys(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "schema.yml")
	if err := os.WriteFile(p, []byte(`
extensions:
  - {name: file_fdw, ifNotExists: true, schema: public}
servers:
  files: {wrapper: file_fdw}
schema k:
  foreign table csv:
    server: files
    options: {filename: /tmp/a.csv, format: csv, null: "NULL"}
    columns:
      a: {type: text, nullable: true}
      null: {type: text, nullable: true, options: {force_not_null: "true"}}
  table pivot:
    columns:
      id: {type: integer}
      2024: {type: numeric, nullable: true, comment: year}
      2025: {type: numeric, nullable: true}
      true: {type: boolean, nullable: true}
      null: {type: text, nullable: true}
      1.5: {type: text, nullable: true}
    primaryKey: [id]
    indexes:
      pivot_years: {columns: [2024, true], include: [2025]}
    constraints:
      pivot_u: {type: unique, columns: [2025, null]}
    data:
      key: [id]
      rows:
        - {id: 1, 2024: 1.5, 2025: 2, true: false, null: n, 1.5: f}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	desired, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	pool := freshDatabase(t, admin, "pgytest_scalar_keys")
	plan := func() *diff.PlanDiff {
		live := introspectAll(t, pool)
		if err := diff.IntrospectData(ctx, pool, live, desired); err != nil {
			t.Fatal(err)
		}
		return diff.PlanWith(live, desired, diff.Options{Unsafe: true, Prune: true})
	}
	if stmt, err := applyAsMigrations(ctx, pool, plan()); err != nil {
		t.Fatalf("applying: %q: %v", stmt, err)
	}
	if sql := diff.Render(plan()); sql != "" {
		t.Errorf("second diff must be empty:\n%s", sql)
	}
	var cols, opts string
	if err := pool.QueryRow(ctx, `
select (select string_agg(attname, ',' order by attnum) from pg_attribute where attrelid = 'k.pivot'::regclass and attnum > 0),
       (select array_to_string(ftoptions, ',') from pg_foreign_table where ftrelid = 'k.csv'::regclass)`).Scan(&cols, &opts); err != nil {
		t.Fatal(err)
	}
	if cols != "id,2024,2025,true,null,1.5" {
		t.Errorf("columns = %s", cols)
	}
	if opts != "filename=/tmp/a.csv,format=csv,null=NULL" {
		t.Errorf("foreign table options = %s", opts)
	}
}
