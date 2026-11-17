package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

// planExtensionsLive introspects pool the way pgy diff does and plans y
// against it.
func planExtensionsLive(t *testing.T, pool *pgxpool.Pool, y string, opts diff.Options) *diff.PlanDiff {
	t.Helper()
	ctx := context.Background()
	d, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
	if err != nil {
		t.Fatal(err)
	}
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectExtensions(ctx, pool, live, d, opts); err != nil {
		t.Fatal(err)
	}
	if err := diff.ProbeViews(ctx, pool, live, d); err != nil {
		t.Fatal(err)
	}
	return diff.PlanWith(live, d, opts)
}

func TestIntegrationExtensionMoveCarriesDependents(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_ext_move")
	if _, err := db.Exec(ctx, `
		create extension citext schema public;
		create extension hstore schema public;
		create extension moddatetime schema public;
		create extension "uuid-ossp" schema public;
		create schema app;
		create table app.t (
			id uuid primary key default uuid_generate_v4(),
			c citext not null default '',
			h hstore not null default ''::hstore,
			n text generated always as (lower(c::text)) stored,
			updated_at timestamp,
			constraint t_c_check check (c <> '')
		);
		create index t_c on app.t (c citext_pattern_ops);
		create index t_keys on app.t using gin (h);
		create index t_sku on app.t ((h -> 'sku'));
		create trigger t_mdt before update on app.t for each row execute function moddatetime(updated_at);
		create view app.v as select id, c, akeys(h) as keys from app.t;
		create function app.norm(e citext) returns citext language sql immutable as $$ select lower(e::text)::citext $$;`); err != nil {
		t.Fatal(err)
	}
	const moved = `
extensions:
  - {name: citext, schema: ext}
  - {name: hstore, schema: ext}
  - {name: moddatetime, schema: ext}
  - {name: uuid-ossp, schema: "ext-s"}
schema app:
  table t:
    columns:
      id: {type: uuid, primaryKey: true, default: '"ext-s".uuid_generate_v4()'}
      c: {type: ext.citext, default: "''"}
      h: {type: ext.hstore, default: "''::ext.hstore"}
      n: {type: text, nullable: true, generated: "lower(c::text)"}
      updated_at: {type: timestamp, nullable: true}
    indexes:
      t_c: {columns: [c], opclass: ext.citext_pattern_ops}
      t_keys: {columns: [h], using: gin}
      t_sku: {columns: ["(h operator(ext.->) 'sku')"]}
    constraints:
      t_c_check: {type: check, expression: "c operator(ext.<>) ''"}
    triggers:
      t_mdt: {timing: before, events: [update], level: row, procedure: "ext.moddatetime('updated_at')"}
  view v:
    query: select id, c, ext.akeys(h) as keys from app.t
  function norm(e ext.citext):
    returns: ext.citext
    language: sql
    immutable: true
    body: select lower(e::text)::citext
`
	opts := diff.Options{Unsafe: true, Prune: true}
	plan := planExtensionsLive(t, db, moved, opts)
	want := []string{
		`create schema if not exists "ext";`,
		`create schema if not exists "ext-s";`,
		`alter extension "citext" set schema "ext";`,
		`alter extension "hstore" set schema "ext";`,
		`alter extension "moddatetime" set schema "ext";`,
		`alter extension "uuid-ossp" set schema "ext-s";`,
	}
	if got := strings.TrimSpace(diff.Render(plan)); got != strings.Join(want, "\n") {
		t.Fatalf("only the extensions move, everything else follows them; got:\n%s", got)
	}
	for _, w := range plan.Warnings {
		t.Errorf("unexpected warning: %s", w.Message)
	}
	applyPlan(t, db, plan)
	if sql := diff.Render(planExtensionsLive(t, db, moved, opts)); sql != "" {
		t.Errorf("the moved database must diff clean:\n%s", sql)
	}
}

func TestIntegrationExtensionMoveWithNewDependents(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_ext_move_new")
	if _, err := db.Exec(ctx, `
		create extension citext schema public;
		create extension btree_gist schema public;
		create extension hstore version '1.4' schema public;
		create table public.users (id int primary key, email citext not null);`); err != nil {
		t.Fatal(err)
	}
	const y = `
extensions:
  - {name: citext, schema: ext}
  - {name: btree_gist, schema: ext}
  - {name: hstore, schema: public, version: "1.6"}
schema public:
  table users:
    columns:
      id: {type: integer, primaryKey: true}
      email: {type: ext.citext}
    indexes:
      users_id_gist: {columns: [id], using: gist, opclass: ext.gist_int4_ops}
  table invites:
    columns:
      id: {type: integer, primaryKey: true}
      email: {type: ext.citext}
  view defined:
    query: select id, hstore_to_jsonb_loose('a=>1'::hstore) as j from public.users
`
	plan := planExtensionsLive(t, db, y, diff.Options{Unsafe: true})
	sql := diff.Render(plan)
	for _, later := range []string{`create table if not exists "public"."invites"`, `"users_id_gist"`, `"public"."defined"`} {
		for _, first := range []string{`alter extension "citext" set schema "ext";`, `alter extension "btree_gist" set schema "ext";`, `alter extension "hstore" update to '1.6';`} {
			i, j := strings.Index(sql, first), strings.Index(sql, later)
			if i < 0 || j < 0 || i > j {
				t.Fatalf("%q must precede %q:\n%s", first, later, sql)
			}
		}
	}
	if strings.Contains(sql, `alter column "email"`) {
		t.Errorf("users.email only follows the extension:\n%s", sql)
	}
	applyPlan(t, db, plan)
	if sql := diff.Render(planExtensionsLive(t, db, y, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("second diff must be clean:\n%s", sql)
	}
}

func TestIntegrationExtensionCreateOrder(t *testing.T) {
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_ext_order")
	const y = `
extensions:
  - {name: bool_plperl, schema: public}
  - {name: hstore_plperl, schema: public}
  - {name: earthdistance, schema: public}
  - {name: cube, schema: public}
  - {name: hstore, schema: public}
  - {name: pg_trgm, schema: public}
  - {name: plperl}
database:
  set:
    pg_trgm.similarity_threshold: 0.4
`
	plan := planExtensionsLive(t, db, y, diff.Options{})
	sql := diff.Render(plan)
	for _, pair := range [][2]string{
		{`create extension "plperl";`, `create extension "bool_plperl"`},
		{`create extension "plperl";`, `create extension "hstore_plperl"`},
		{`create extension "hstore"`, `create extension "hstore_plperl"`},
		{`create extension "cube"`, `create extension "earthdistance"`},
		{`create extension "pg_trgm"`, `pg_trgm.similarity_threshold`},
	} {
		i, j := strings.Index(sql, pair[0]), strings.Index(sql, pair[1])
		if i < 0 || j < 0 || i > j {
			t.Fatalf("%q must precede %q:\n%s", pair[0], pair[1], sql)
		}
	}
	for _, w := range plan.Warnings {
		t.Errorf("unexpected warning: %s", w.Message)
	}
	if stmt, err := applyAsMigrations(context.Background(), db, plan); err != nil {
		if strings.Contains(err.Error(), "could not load library") || strings.Contains(err.Error(), "could not access file") {
			t.Skipf("this server cannot load PL/Perl: %v", err)
		}
		t.Fatalf("%q: %v", stmt, err)
	}
	if sql := diff.Render(planExtensionsLive(t, db, y, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("second diff must be clean:\n%s", sql)
	}
}

func TestIntegrationExtensionServerChecks(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_ext_checks")
	if _, err := db.Exec(ctx, `create extension hstore version '1.5' schema public`); err != nil {
		t.Fatal(err)
	}
	plan := planExtensionsLive(t, db, `
extensions:
  - {name: hstore, schema: public, version: "1.4"}
  - {name: citext, schema: public, version: "9.9"}
  - {name: pgy_no_such_extension, schema: public}
`, diff.Options{Unsafe: true})
	sql := diff.Render(plan)
	if strings.Contains(sql, `alter extension "hstore"`) {
		t.Errorf("a downgrade must not be planned:\n%s", sql)
	}
	msgs := []string{}
	for _, w := range plan.Warnings {
		msgs = append(msgs, w.Message)
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{
		"extension hstore: PostgreSQL has no update path from the installed version 1.5 to 1.4",
		"extension citext: version 9.9 is not available on this server (available: ",
		"extension pgy_no_such_extension is not available on this server",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing warning %q in:\n%s", want, all)
		}
	}
	plan = planExtensionsLive(t, db, "extensions:\n  - {name: hstore, schema: public, version: \"1.6\"}\n", diff.Options{})
	if got := strings.TrimSpace(diff.Render(plan)); got != `alter extension "hstore" update to '1.6';` || len(plan.Warnings) != 0 {
		t.Fatalf("an update with a path is planned without warnings; got %q, warnings %v", got, plan.Warnings)
	}
	applyPlan(t, db, plan)
}

func TestIntegrationPullExtensionVersionAndRequirements(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	src := freshDatabase(t, admin, "pgytest_ext_pull")
	if _, err := src.Exec(ctx, `
		create extension hstore version '1.5' schema public;
		create extension earthdistance schema public cascade;
		create extension citext schema public;`); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(introspectAll(t, src), pull.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var doc string
	for _, f := range res.Files {
		doc += string(f.Content)
	}
	for _, want := range []string{
		`{name: hstore, ifNotExists: true, schema: public, version: "1.5"}`,
		`{name: earthdistance, ifNotExists: true, schema: public, dependsOn: [extension cube]}`,
		`{name: citext, ifNotExists: true, schema: public}`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %s in:\n%s", want, doc)
		}
	}
	dst := freshDatabase(t, admin, "pgytest_ext_pull_copy")
	applyPlan(t, dst, planExtensionsLive(t, dst, doc, diff.Options{}))
	var version string
	if err := dst.QueryRow(ctx, `select extversion from pg_extension where extname = 'hstore'`).Scan(&version); err != nil || version != "1.5" {
		t.Errorf("the rebuilt database must have hstore 1.5, got %q (%v)", version, err)
	}
}
