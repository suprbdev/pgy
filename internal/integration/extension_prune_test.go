package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
)

func installedExtensions(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var names string
	if err := pool.QueryRow(context.Background(), `select string_agg(extname, ',' order by extname) from pg_extension`).Scan(&names); err != nil {
		t.Fatal(err)
	}
	return names
}

func warningMessages(p *diff.PlanDiff) string {
	msgs := []string{}
	for _, w := range p.Warnings {
		msgs = append(msgs, w.Message)
	}
	return strings.Join(msgs, "\n")
}

func TestIntegrationExtensionPrune(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_ext_prune")
	if _, err := db.Exec(ctx, `
		create schema app;
		create schema platform;
		create extension citext schema app;
		create extension pg_trgm schema app;
		create extension cube schema app;
		create extension earthdistance schema app;
		create extension hstore schema platform;
		create table app.users (id int primary key, email app.citext not null, name text not null);
		create index users_name_trgm on app.users using gin (name app.gin_trgm_ops);
		create table app.legacy (id int primary key, email app.citext, tags platform.hstore);
		create view app.similar as select id, app.similarity(name, 'pgy') as score from app.users;`); err != nil {
		t.Fatal(err)
	}
	const users = `
schema app:
  table users:
    columns:
      id: {type: integer, primaryKey: true}
      email: {type: app.citext}
      name: {type: text}
`
	prune := diff.Options{Unsafe: true, Prune: true}

	for _, y := range []string{users, "extensions: []\n" + users} {
		for _, opts := range []diff.Options{{}, {Unsafe: true}} {
			if sql := diff.Render(planExtensionsLive(t, db, y, opts)); strings.Contains(sql, "drop extension") {
				t.Fatalf("extensions are only dropped under --unsafe --prune, got with %+v:\n%s", opts, sql)
			}
		}
	}
	if sql := diff.Render(planExtensionsLive(t, db, users, prune)); strings.Contains(sql, "drop extension") {
		t.Fatalf("without an extensions block extensions stay unmanaged:\n%s", sql)
	}

	const declared = `
extensions:
  - {name: earthdistance, schema: app, cascade: true}
` + users
	plan := planExtensionsLive(t, db, declared, prune)
	want := []string{
		`drop index "app"."users_name_trgm";`,
		`drop view "app"."similar";`,
		`drop table "app"."legacy";`,
		`drop extension "pg_trgm";`,
	}
	if got := strings.TrimSpace(diff.Render(plan)); got != strings.Join(want, "\n") {
		t.Fatalf("want:\n%s\ngot:\n%s", strings.Join(want, "\n"), got)
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0].Kind != "dependents" ||
		!strings.Contains(plan.Warnings[0].Message, "extension citext is not listed under extensions but still in use (column email of table app.users)") {
		t.Fatalf("want one warning naming what still uses citext, got:\n%s", warningMessages(plan))
	}
	applyPlan(t, db, plan)
	if got := installedExtensions(t, db); got != "citext,cube,earthdistance,hstore,plpgsql" {
		t.Fatalf("installed extensions: %s", got)
	}
	plan = planExtensionsLive(t, db, declared, prune)
	if sql := diff.Render(plan); sql != "" {
		t.Errorf("second diff must be clean:\n%s", sql)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0].Message, "extension citext") {
		t.Errorf("citext is still in use and still reported, got:\n%s", warningMessages(plan))
	}

	withoutEmail := strings.Replace(declared, "      email: {type: app.citext}\n", "", 1)
	plan = planExtensionsLive(t, db, withoutEmail, prune)
	want = []string{
		`alter table "app"."users" drop column "email";`,
		`drop extension "citext";`,
	}
	if got := strings.TrimSpace(diff.Render(plan)); got != strings.Join(want, "\n") || len(plan.Warnings) != 0 {
		t.Fatalf("want:\n%s\ngot:\n%s\nwarnings:\n%s", strings.Join(want, "\n"), got, warningMessages(plan))
	}
	applyPlan(t, db, plan)

	empty := strings.Replace(withoutEmail, "  - {name: earthdistance, schema: app, cascade: true}\n", "", 1)
	empty = strings.Replace(empty, "extensions:\n", "extensions: []\n", 1)
	keep := prune
	keep.KeepExtensions = []string{"earthdistance"}
	if sql := diff.Render(planExtensionsLive(t, db, empty, keep)); sql != "" {
		t.Errorf("earthdistance is kept by the ignore list, and cube because earthdistance requires it:\n%s", sql)
	}
	plan = planExtensionsLive(t, db, empty, prune)
	if got := strings.TrimSpace(diff.Render(plan)); got != `drop extension "cube", "earthdistance";` {
		t.Fatalf("an extension and the one it requires go in one statement, got:\n%s", got)
	}
	applyPlan(t, db, plan)
	if got := installedExtensions(t, db); got != "hstore,plpgsql" {
		t.Errorf("hstore lives in a schema the YAML declares nothing in and must stay; installed: %s", got)
	}
	if sql := diff.Render(planExtensionsLive(t, db, empty, prune)); sql != "" {
		t.Errorf("final diff must be clean:\n%s", sql)
	}
}

func TestIntegrationExtensionPruneDependentKinds(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_ext_prune_kinds")
	if _, err := db.Exec(ctx, `
		create schema app;
		create extension citext schema app;
		create extension hstore schema app;
		create extension pg_trgm schema app;
		create extension cube schema app;
		create table app.keep (
			id int primary key,
			name text not null default '',
			score real default app.similarity('a', 'b'),
			constraint keep_name_check check (name::app.citext operator(app.<>) 'x'::app.citext)
		);
		create statistics app.keep_stats on (lower(name)), (app.similarity(name, 'x')) from app.keep;
		create rule keep_rule as on delete to app.keep where (old.name::app.citext operator(app.=) 'a'::app.citext) do instead nothing;
		create table app.events (id int, who app.citext, at date not null) partition by range (at);
		create table app.events_2024 partition of app.events for values from ('2024-01-01') to ('2025-01-01');
		create index events_who on app.events (who);
		create table app.log (id int, who app.citext, at date not null, constraint log_who_check check (who operator(app.<>) 'x'::app.citext)) partition by range (at);
		create table app.log_2024 partition of app.log for values from ('2024-01-01') to ('2025-01-01');
		create index log_who on app.log (who);
		create table app.docs (
			id int primary key,
			title app.citext not null default ''::app.citext,
			tags app.hstore,
			constraint docs_title_check check (title operator(app.<>) 'x'::app.citext)
		);
		create index docs_sku on app.docs ((tags operator(app.->) 'sku'));
		create index keep_name_trgm on app.keep using gin (name app.gin_trgm_ops);
		create view app.doc_keys as select id, app.akeys(tags) as keys from app.docs;
		create materialized view app.doc_titles as select id, title from app.docs;
		create domain app.email as app.citext check (value operator(app.<>) ''::app.citext);
		create type app.pair as (a app.citext, b int);
		create function app.norm(e app.citext) returns app.citext language sql immutable as $$ select lower(e::text)::app.citext $$;
		create function app.sku(h app.hstore) returns text language sql immutable return h operator(app.->) 'sku';
		create aggregate app.cat(app.citext) (sfunc = textcat, stype = text);`); err != nil {
		t.Fatal(err)
	}
	const y = `
extensions: []
schema app:
  table keep:
    columns:
      id: {type: integer, primaryKey: true}
      name: {type: text, default: "''"}
  table log:
    columns:
      id: {type: integer, nullable: true}
      at: {type: date}
    partitionBy: {type: range, columns: [at]}
  table log_2024:
    partitionOf: app.log
    forValues: {from: [2024-01-01], to: [2025-01-01]}
`
	prune := diff.Options{Unsafe: true, Prune: true}
	plan := planExtensionsLive(t, db, y, prune)
	sql := diff.Render(plan)
	for _, stmt := range []string{
		`alter table "app"."log" drop constraint "log_who_check";`,
		`drop index "app"."log_who";`,
		`alter table "app"."log" drop column "who";`,
		`drop table "app"."docs", "app"."events";`,
	} {
		if !strings.Contains(sql, stmt) {
			t.Errorf("missing %s in:\n%s", stmt, sql)
		}
	}
	last := `drop extension "citext", "cube", "hstore", "pg_trgm";`
	if !strings.HasSuffix(strings.TrimSpace(sql), last) {
		t.Fatalf("every dependent is pruned in the same plan, so the extensions go last:\n%s", sql)
	}
	for _, w := range plan.Warnings {
		t.Errorf("unexpected warning: %s", w.Message)
	}
	applyPlan(t, db, plan)
	if got := installedExtensions(t, db); got != "plpgsql" {
		t.Errorf("installed extensions: %s", got)
	}
	if sql := diff.Render(planExtensionsLive(t, db, y, prune)); sql != "" {
		t.Errorf("second diff must be clean:\n%s", sql)
	}
}
