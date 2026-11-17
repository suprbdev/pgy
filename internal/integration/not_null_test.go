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

// notNullNames lists the NOT NULL constraints of a table as column=name
// pairs in column order, with a ! after a constraint that is NOT VALID.
func notNullNames(t *testing.T, pool *pgxpool.Pool, table string) string {
	t.Helper()
	var got string
	err := pool.QueryRow(context.Background(), `
		select coalesce(string_agg(a.attname || '=' || con.conname || case when con.convalidated then '' else '!' end, ' ' order by a.attnum), '')
		from pg_constraint con join pg_attribute a on a.attrelid = con.conrelid and a.attnum = con.conkey[1]
		where con.contype = 'n' and con.conrelid = $1::regclass`, table).Scan(&got)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestIntegrationNotNullName names column NOT NULL constraints when a
// table, a foreign table and a column are created and when an existing
// column becomes NOT NULL, renames a constraint whose name changed, and
// pulls the names that are not PostgreSQL's default.
func TestIntegrationNotNullName(t *testing.T) {
	requireServerVersion(t, 180000)
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	server := sch + "_files"
	t.Cleanup(func() {
		pool.Exec(context.Background(), `drop server if exists "`+server+`" cascade`) //nolint:errcheck
	})
	opts := diff.Options{Unsafe: true}
	const base = `
extensions:
  - {name: file_fdw, schema: public, ifNotExists: true}
servers:
  SCH_files: {wrapper: file_fdw}
schema SCH:
  table t:
    columns:
COLUMNS
    constraints:
      t_d_set: {type: check, expression: d is not null}
  foreign table f:
    server: SCH_files
    options: {filename: /dev/null}
    columns:
      n: {type: int, notNullName: FOREIGN}
`
	load := func(columns, foreign string) string {
		return strings.NewReplacer("COLUMNS", columns, "FOREIGN", foreign).Replace(base)
	}
	sql := convergeYAMLWith(t, pool, sch, load(`
      id: {type: int, primaryKey: true, notNullName: t_id_required}
      a: {type: int, notNullName: a_must_exist}
      b: {type: text}
      c: {type: int, nullable: true}
      d: {type: int, nullable: true}
      v: {type: int, notValid: true, notNullName: v_pending}`, "f_n_required"), opts)
	for _, want := range []string{
		`"a" int constraint "a_must_exist" not null`,
		`"id" int constraint "t_id_required" not null`,
		`add constraint "v_pending" not null "v" not valid;`,
		`"n" int constraint "f_n_required" not null`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if got, want := notNullNames(t, pool, sch+".t"), "id=t_id_required a=a_must_exist b=t_b_not_null v=v_pending!"; got != want {
		t.Errorf("created constraints:\n got %s\nwant %s", got, want)
	}
	if _, err := pool.Exec(ctx, `insert into `+sch+`.t values (1, 1, 'x', 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}

	changed := load(`
      id: {type: int, primaryKey: true, notNullName: t_id_required}
      a: {type: int, notNullName: a_required}
      b: {type: text, notNullName: b_required}
      c: {type: int, notNullName: c_required}
      d: {type: int, notNullName: d_required}
      v: {type: int, notNullName: v_checked}
      e: {type: int, notNullName: e_required, default: 0}`, "f_n_needed")
	sql = convergeYAMLWith(t, pool, sch, changed, opts)
	for _, want := range []string{
		`alter table "` + sch + `"."t" rename constraint "a_must_exist" to "a_required";`,
		`alter table "` + sch + `"."t" rename constraint "t_b_not_null" to "b_required";`,
		`alter table "` + sch + `"."t" add constraint "c_required" not null "c" not valid;`,
		`alter table "` + sch + `"."t" validate constraint "c_required";`,
		`alter table "` + sch + `"."t" add constraint "d_required" not null "d";`,
		`alter table "` + sch + `"."t" rename constraint "v_pending" to "v_checked";`,
		`alter table "` + sch + `"."t" validate constraint "v_checked";`,
		`alter table "` + sch + `"."t" add column "e" int constraint "e_required" not null default 0;`,
		`alter table "` + sch + `"."f" rename constraint "f_n_required" to "f_n_needed";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "set not null") || strings.Contains(sql, "t_id_required") {
		t.Errorf("only the changed names are touched:\n%s", sql)
	}
	if got, want := notNullNames(t, pool, sch+".t"), "id=t_id_required a=a_required b=b_required c=c_required d=d_required v=v_checked e=e_required"; got != want {
		t.Errorf("constraints after the change:\n got %s\nwant %s", got, want)
	}
	if got, want := notNullNames(t, pool, sch+".f"), "n=f_n_needed"; got != want {
		t.Errorf("foreign table constraint: got %s, want %s", got, want)
	}

	if _, err := pool.Exec(ctx, `alter table `+sch+`.t rename constraint a_required to drifted`); err != nil {
		t.Fatal(err)
	}
	d := loadYAML(t, sch, changed)
	live := introspect(t, pool, d)
	if got, want := diff.Render(diff.PlanWith(live, d, opts)), `alter table "`+sch+`"."t" rename constraint "drifted" to "a_required";`+"\n"; got != want {
		t.Errorf("a constraint renamed in the database is renamed back:\n got %s\nwant %s", got, want)
	}
	if err := diff.IntrospectDetails(ctx, pool, live); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(live, pull.Options{Schemas: []string{sch}})
	if err != nil {
		t.Fatal(err)
	}
	pulled := ""
	for _, f := range res.Files {
		pulled += string(f.Content)
	}
	for _, want := range []string{
		"id: {type: integer, notNullName: t_id_required}",
		"a: {type: integer, notNullName: drifted}",
		`e: {type: integer, notNullName: e_required, default: "0"}`,
		"n: {type: integer, notNullName: f_n_needed}",
	} {
		if !strings.Contains(pulled, want) {
			t.Errorf("pulled YAML lacks %q:\n%s", want, pulled)
		}
	}
	convergeYAMLWith(t, pool, sch, changed, opts)
	assertPullRoundTrip(t, ctx, pool, introspect(t, pool, d), sch, d)
}

// TestIntegrationNotNullNameOnEveryVersion declares notNullName on a new
// and on an existing column: the plan converges on every server, and one
// that records no constraint names ignores the key with a warning.
func TestIntegrationNotNullNameOnEveryVersion(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	opts := diff.Options{Unsafe: true}
	convergeYAMLWith(t, pool, sch, `
schema SCH:
  table t:
    columns: {id: {type: int, notNullName: t_id_required}, a: {type: int, nullable: true}}
`, opts)
	const named = `
schema SCH:
  table t:
    columns: {id: {type: int, notNullName: t_id_required}, a: {type: int, notNullName: a_required}}
`
	sql := convergeYAMLWith(t, pool, sch, named, opts)
	d := loadYAML(t, sch, named)
	live := introspect(t, pool, d)
	ignored := false
	for _, w := range diff.PlanWith(live, d, opts).Warnings {
		ignored = ignored || strings.Contains(w.Message, "notNullName on column "+sch+".t.a is ignored")
	}
	if live.ServerVersion >= 180000 {
		if got, want := notNullNames(t, pool, sch+".t"), "id=t_id_required a=a_required"; got != want || ignored {
			t.Errorf("constraints: got %s, want %s (warned: %v)\n%s", got, want, ignored, sql)
		}
		return
	}
	if !strings.Contains(sql, `alter column "a" set not null;`) || strings.Contains(sql, "a_required") || !ignored {
		t.Errorf("before PostgreSQL 18 the name is ignored with a warning (warned: %v):\n%s", ignored, sql)
	}
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
