package integration

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

// statStatementsUpdate skips the test unless the server can install
// pg_stat_statements 1.6 and update it to 1.10. Every update script on that
// path drops and recreates the pg_stat_statements view or one of its
// functions.
func statStatementsUpdate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from pg_available_extension_versions where name = 'pg_stat_statements' and version in ('1.6', '1.10')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Skip("this server cannot install pg_stat_statements 1.6 and 1.10")
	}
}

func extensionVersion(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var v string
	if err := pool.QueryRow(context.Background(), `select extversion from pg_extension where extname = $1`, name).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestIntegrationExtensionUpdateRecreatesDependentViews(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	statStatementsUpdate(t, admin)
	reader := freshRole(t, admin, "reader")
	db := freshDatabase(t, admin, "pgytest_ext_update")
	if _, err := db.Exec(ctx, `
		create extension pg_stat_statements version '1.6' schema public;
		create schema app;
		create function app.noop() returns trigger language plpgsql as $$ begin return null; end $$;
		create view app.slow as select queryid, query, calls from pg_stat_statements;
		create view app.top as select query, calls from app.slow order by calls desc limit 10;
		create view app.entries as select count(*) as n from pg_stat_statements(false);
		create trigger slow_ins instead of insert on app.slow for each row execute function app.noop();
		create rule slow_del as on delete to app.slow do instead nothing;
		comment on view app.slow is 'slow statements';
		comment on column app.top.calls is 'how often';
		alter view app.top alter column calls set default 0;
		grant select on app.top to "`+reader+`";`); err != nil {
		t.Fatal(err)
	}
	const y = `
extensions:
  - {name: pg_stat_statements, schema: public, version: "VERSION"}
schema app:
  function noop():
    returns: trigger
    language: plpgsql
    body: begin return null; end
  view slow:
    query: select queryid, query, calls from pg_stat_statements
    triggers:
      slow_ins: {timing: instead of, events: [insert], level: row, procedure: app.noop()}
    rules:
      slow_del: {event: delete, instead: true, do: nothing}
  view top:
    query: select query, calls from app.slow order by calls desc limit 10
  view entries:
    query: select count(*) as n from pg_stat_statements(false)
`
	all := diff.Options{Unsafe: true, Prune: true}
	if sql := diff.Render(planExtensionsLive(t, db, strings.Replace(y, "VERSION", "1.6", 1), all)); sql != "" {
		t.Fatalf("the YAML must describe the database before the update:\n%s", sql)
	}
	updated := strings.Replace(y, "VERSION", "1.10", 1)

	safe := planExtensionsLive(t, db, updated, diff.Options{})
	if got := strings.TrimSpace(diff.Render(safe)); got != `alter extension "pg_stat_statements" update to '1.10';` {
		t.Fatalf("without --unsafe the plan is the update alone, got:\n%s", got)
	}
	if msgs := warningMessages(safe); !strings.Contains(msgs, "extension pg_stat_statements: views depend on it, directly or through other views (app.entries, app.slow, app.top)") || !strings.Contains(msgs, "--unsafe drops them before the update and recreates them after it") {
		t.Errorf("want a warning naming the dependent views, got:\n%s", msgs)
	}
	if stmt, err := applyAsMigrations(ctx, db, safe); err == nil || !strings.Contains(err.Error(), "because other objects depend on it") {
		t.Fatalf("the update alone must be rejected while the views exist, got %q: %v", stmt, err)
	}
	if v := extensionVersion(t, db, "pg_stat_statements"); v != "1.6" {
		t.Fatalf("the rejected update must leave version 1.6, got %s", v)
	}

	plan := planExtensionsLive(t, db, updated, diff.Options{Unsafe: true})
	sql := diff.Render(plan)
	order := []string{
		`drop view "app"."entries";`,
		`drop view "app"."top";`,
		`drop view "app"."slow";`,
		`alter extension "pg_stat_statements" update to '1.10';`,
		`create or replace view "app"."slow"`,
		`create or replace view "app"."top"`,
	}
	at := -1
	for _, want := range order {
		i := strings.Index(sql, want)
		if i < 0 || i < at {
			t.Fatalf("want %q after the statements before it, got:\n%s", want, sql)
		}
		at = i
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("unexpected warnings:\n%s", warningMessages(plan))
	}
	applyPlan(t, db, plan)

	if v := extensionVersion(t, db, "pg_stat_statements"); v != "1.10" {
		t.Errorf("want version 1.10, got %s", v)
	}
	var comment, colComment, colDefault string
	var granted, trigger, rule bool
	if err := db.QueryRow(ctx, `
		select coalesce(obj_description('app.slow'::regclass, 'pg_class'), ''),
		       coalesce(col_description('app.top'::regclass, 2), ''),
		       coalesce((select pg_get_expr(d.adbin, d.adrelid) from pg_attrdef d where d.adrelid = 'app.top'::regclass and d.adnum = 2), ''),
		       has_table_privilege($1, 'app.top'::regclass, 'select'),
		       exists (select 1 from pg_trigger where tgrelid = 'app.slow'::regclass and tgname = 'slow_ins'),
		       exists (select 1 from pg_rewrite where ev_class = 'app.slow'::regclass and rulename = 'slow_del')`, reader).Scan(&comment, &colComment, &colDefault, &granted, &trigger, &rule); err != nil {
		t.Fatal(err)
	}
	if comment != "slow statements" || colComment != "how often" || colDefault != "0" || !granted || !trigger || !rule {
		t.Errorf("after recreation: comment %q, column comment %q, column default %q, grant %v, trigger %v, rule %v", comment, colComment, colDefault, granted, trigger, rule)
	}
	if sql := diff.Render(planExtensionsLive(t, db, updated, all)); sql != "" {
		t.Errorf("second diff must be clean:\n%s", sql)
	}
}

func TestIntegrationExtensionUpdateKeepsMaterializedViewsAndRoutines(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	statStatementsUpdate(t, admin)
	db := freshDatabase(t, admin, "pgytest_ext_update_kept")
	if _, err := db.Exec(ctx, `
		create extension pg_stat_statements version '1.6' schema public;
		create schema app;
		create view app.slow as select queryid, calls from pg_stat_statements;
		create materialized view app.snap as select queryid, calls from app.slow with no data;
		create view app.free as select count(*) as n from pg_stat_statements;
		create function app.calls() returns bigint language sql begin atomic select sum(calls)::bigint from pg_stat_statements; end;
		create table app.t (id int, s app.slow);
		create rule slow_del as on delete to app.slow do instead nothing;`); err != nil {
		t.Fatal(err)
	}
	const y = `
extensions:
  - {name: pg_stat_statements, schema: public, version: "1.10"}
schema app:
  view slow:
    query: select queryid, calls from pg_stat_statements
    rules:
      slow_del: {event: delete, instead: true, do: nothing}
  materialized view snap:
    query: select queryid, calls from app.slow
    withData: false
  view free:
    query: select count(*) as n from pg_stat_statements
  function calls():
    returns: bigint
    language: sql
    body: begin atomic select sum(calls)::bigint from pg_stat_statements; end
`
	d, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
	if err != nil {
		t.Fatal(err)
	}
	live, err := diff.Introspect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectExtensions(ctx, db, live, d, diff.Options{}); err != nil {
		t.Fatal(err)
	}
	want := []diff.UpdateDependent{
		{Kind: "function", Name: "app.calls()"},
		{Kind: "view", Name: "app.free"},
		{Kind: "view", Name: "app.slow"},
		{Kind: "matview", Name: "app.snap", On: "app.slow"},
		{Kind: "other", Name: "column s of table app.t", On: "app.slow"},
	}
	if got := live.ExtensionDefs["pg_stat_statements"].UpdateDependents; !reflect.DeepEqual(got, want) {
		t.Fatalf("dependents of the extension:\n got %+v\nwant %+v", got, want)
	}

	opts := diff.Options{Unsafe: true}
	plan := planExtensionsLive(t, db, y, opts)
	sql := diff.Render(plan)
	if !strings.Contains(sql, `drop view "app"."free";`) || strings.Contains(sql, "drop materialized view") || strings.Contains(sql, "drop function") || strings.Contains(sql, `drop view "app"."slow"`) {
		t.Fatalf("only the view nothing else uses is recreated, got:\n%s", sql)
	}
	if msgs := warningMessages(plan); !strings.Contains(msgs, "extension pg_stat_statements: objects that pgy doesn't drop for an update depend on it, directly or through views (function app.calls(), materialized view app.snap, column s of table app.t)") {
		t.Errorf("want a warning naming what stays, got:\n%s", msgs)
	}
	if stmt, err := applyAsMigrations(ctx, db, plan); err == nil || !strings.Contains(err.Error(), "because other objects depend on it") {
		t.Fatalf("the update must be rejected while the materialized view and the function exist, got %q: %v", stmt, err)
	}

	if _, err := db.Exec(ctx, `drop materialized view app.snap; drop function app.calls(); drop table app.t`); err != nil {
		t.Fatal(err)
	}
	plan = planExtensionsLive(t, db, y, opts)
	if len(plan.Warnings) != 0 {
		t.Errorf("unexpected warnings:\n%s", warningMessages(plan))
	}
	applyPlan(t, db, plan)
	if v := extensionVersion(t, db, "pg_stat_statements"); v != "1.10" {
		t.Errorf("want version 1.10, got %s", v)
	}
	if sql := diff.Render(planExtensionsLive(t, db, y, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("second diff must be clean:\n%s", sql)
	}
}
