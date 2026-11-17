package integration

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

// routineSources maps each user routine to its stored source text.
func routineSources(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		select n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', coalesce(p.prosrc, '')
		from pg_proc p join pg_namespace n on n.oid = p.pronamespace
		where n.nspname not in ('pg_catalog', 'information_schema', 'pgy')
		and not exists (select 1 from pg_depend d where d.classid = 'pg_proc'::regclass and d.objid = p.oid and d.deptype in ('e', 'i'))`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var sig, src string
		if err := rows.Scan(&sig, &src); err != nil {
			t.Fatal(err)
		}
		out[sig] = src
	}
	return out
}

// routineSourceDifferences lists the routines both databases have whose
// stored source differs.
func routineSourceDifferences(t *testing.T, a, b *pgxpool.Pool) []string {
	t.Helper()
	want, got := routineSources(t, a), routineSources(t, b)
	out := []string{}
	for sig, src := range want {
		if rebuilt, ok := got[sig]; ok && rebuilt != src {
			out = append(out, fmt.Sprintf("%s: %q, rebuilt as %q", sig, src, rebuilt))
		}
	}
	sort.Strings(out)
	return out
}

// pullAndRebuild pulls src and applies the pulled YAML to dst, requiring
// the YAML to diff clean against both afterwards.
func pullAndRebuild(t *testing.T, src, dst *pgxpool.Pool) string {
	t.Helper()
	live := introspectAll(t, src)
	res, err := pull.Build(live, pull.Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	y := string(res.Files[0].Content)
	pulled, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
	if err != nil {
		t.Fatalf("%v\n%s", err, y)
	}
	opts := diff.Options{Unsafe: true, Prune: true}
	if sql := diff.Render(diff.PlanWith(live, pulled, opts)); sql != "" {
		t.Fatalf("pulled YAML must diff clean against its source:\n%s\n%s", sql, y)
	}
	if stmt, err := applyAsMigrations(context.Background(), dst, diff.PlanWith(introspectAll(t, dst), pulled, diff.Options{})); err != nil {
		t.Fatalf("rebuilding from pulled YAML: %q: %v\n%s", stmt, err, y)
	}
	if sql := diff.Render(diff.PlanWith(introspectAll(t, dst), pulled, opts)); sql != "" {
		t.Errorf("rebuilt database differs from the pulled YAML:\n%s", sql)
	}
	return y
}

const standInLanguage = `create language plsample handler plpgsql_call_handler;`

const standInTransforms = standInLanguage + `
	create function public.to_pl(internal) returns internal language internal immutable as 'int4in';
	create transform for integer language plsample (from sql with function public.to_pl(internal));
	create transform for text language plsample (from sql with function public.to_pl(internal));`

func TestIntegrationRoutineBodyIndentationSurvivesPull(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	src := freshDatabase(t, admin, "pgytest_routine_indent")
	dst := freshDatabase(t, admin, "pgytest_routine_indent_copy")
	for _, db := range []*pgxpool.Pool{src, dst} {
		if _, err := db.Exec(ctx, standInLanguage); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := src.Exec(ctx, `
		create function public.sign(a integer) returns integer language plsample as $$
    if a > 0:
        return 1
    return -1
$$;
		create function public.tabbed(a integer) returns integer language plsample as E'\n\tif a > 0:\n\t\treturn 1\n\treturn -1\n';
		create function public.inline(a integer) returns integer language plsample as $$return a$$;
		create function public.crlf(a integer) returns integer language plsample as E'\r\n    return a\r\n';
		create procedure public.note(a integer) language plsample as $$
    plpy.notice(str(a))
$$;`); err != nil {
		t.Fatal(err)
	}
	y := pullAndRebuild(t, src, dst)
	if d := routineSourceDifferences(t, src, dst); len(d) > 0 {
		t.Errorf("bodies differ after pull and rebuild:\n%s\n%s", strings.Join(d, "\n"), y)
	}
	if got := routineSources(t, dst); len(got) != 5 {
		t.Errorf("rebuilt routines = %v", got)
	}
}

func TestIntegrationPLPythonIndentedBodyRunsAfterRebuild(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	src := freshDatabase(t, admin, "pgytest_routine_plpython")
	if _, err := src.Exec(ctx, `create extension plpython3u`); err != nil {
		t.Skipf("plpython3u is not available on this server: %v", err)
	}
	dst := freshDatabase(t, admin, "pgytest_routine_plpython_copy")
	if _, err := src.Exec(ctx, `
		create extension hstore;
		create extension hstore_plpython3u;
		create function public.sign(a integer) returns integer language plpython3u as $$
    if a > 0:
        return 1
    return -1
$$;
		create function public.size(h hstore) returns integer language plpython3u transform for type hstore as $$
    return len(h)
$$;`); err != nil {
		t.Fatal(err)
	}
	y := pullAndRebuild(t, src, dst)
	if d := routineSourceDifferences(t, src, dst); len(d) > 0 {
		t.Errorf("bodies differ after pull and rebuild:\n%s\n%s", strings.Join(d, "\n"), y)
	}
	var sign, size int
	if err := dst.QueryRow(ctx, `select public.sign(-5), public.size('a=>1, b=>2')`).Scan(&sign, &size); err != nil || sign != -1 || size != 2 {
		t.Errorf("rebuilt PL/Python functions: %d, %d, %v\n%s", sign, size, err, y)
	}
}

func TestIntegrationRoutineEmptyBodies(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"empty": `
schema SCH:
  function noop():
    returns: void
    language: sql
    body: ""
  procedure noop_proc():
    language: sql
    body: ""
`,
	})
	pool := connect(t)
	sch := freshSchema(t, pool)
	sql := convergeYAML(t, pool, sch, `
schema SCH:
  function noop():
    returns: void
    language: sql
    body: ""
`)
	if !contains(sql, `create function "`+sch+`"."noop"() returns void language sql as $$$$;`) {
		t.Errorf("a function with an empty body must be created:\n%s", sql)
	}
	if _, err := pool.Exec(context.Background(), "select "+sch+".noop()"); err != nil {
		t.Error(err)
	}
}

func TestIntegrationRoutineBodyFramingOfEarlierVersionsNoDiff(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		create function %[1]s.block() returns integer language plpgsql as E'\nbegin\n  return 1;\nend;\n\n';
		create function %[1]s.stripped() returns integer language plpgsql as E'\nbegin\n  return 1;\nend;\n';
		create function %[1]s.plain() returns integer language sql as E'\nselect 1\n';
		create procedure %[1]s.proc() language sql as E'\nselect 1\n\n';`, sch)); err != nil {
		t.Fatal(err)
	}
	d := loadYAML(t, sch, `
schema SCH:
  function block():
    returns: integer
    language: plpgsql
    body: |
      begin
        return 1;
      end;
  function stripped():
    returns: integer
    language: plpgsql
    body: |-
      begin
        return 1;
      end;
  function plain():
    returns: integer
    language: sql
    body: select 1
  procedure proc():
    language: sql
    body: |
      select 1
`)
	if sql := diff.Render(diff.Plan(introspect(t, pool, d), d, true)); sql != "" {
		t.Errorf("bodies created by earlier versions must not diff:\n%s", sql)
	}
}

func TestIntegrationRoutineLanguageDrift(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_routine_language")
	if _, err := db.Exec(ctx, standInLanguage+`
		create trusted language plsafe handler plpgsql_call_handler;
		create schema app;
		create function app.f(a integer) returns integer language plsample as 'return a';
		create procedure app.p(a integer) language plsample as 'return';`); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{writeYAML(t, `
schema app:
  function f(a integer):
    returns: integer
    language: plsafe
    body: return a
  procedure p(a integer):
    language: plsafe
    body: return
`)})
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.Plan(introspectAll(t, db), d, false)
	sql := diff.Render(plan)
	for _, want := range []string{
		`create or replace function "app"."f"(a integer) returns integer language plsafe as $$return a$$;`,
		`create or replace procedure "app"."p"(a integer) language plsafe as $$return$$;`,
	} {
		if !contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	applyPlan(t, db, plan)
	if again := diff.Render(diff.Plan(introspectAll(t, db), d, false)); again != "" {
		t.Errorf("plan did not converge:\n%s", again)
	}
	var languages string
	if err := db.QueryRow(ctx, `select string_agg(l.lanname, ',' order by p.proname) from pg_proc p join pg_language l on l.oid = p.prolang where p.pronamespace = 'app'::regnamespace`).Scan(&languages); err != nil || languages != "plsafe,plsafe" {
		t.Errorf("languages = %q, %v", languages, err)
	}
}

func TestIntegrationRoutineTransform(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_routine_transform")
	if _, err := db.Exec(ctx, standInTransforms); err != nil {
		t.Fatal(err)
	}
	transforms := func() string {
		t.Helper()
		var got string
		if err := db.QueryRow(ctx, `
			select string_agg(p.proname || '=' || coalesce(p.protrftypes::regtype[]::text, '-'), ' ' order by p.proname)
			from pg_proc p where p.pronamespace = 'app'::regnamespace`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	apply := func(y string) string {
		t.Helper()
		d, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
		if err != nil {
			t.Fatal(err)
		}
		plan := diff.Plan(introspectAll(t, db), d, false)
		applyPlan(t, db, plan)
		if again := diff.Render(diff.Plan(introspectAll(t, db), d, false)); again != "" {
			t.Fatalf("plan did not converge:\n%s", again)
		}
		return diff.Render(plan)
	}
	y := `
schema app:
  function both(a integer, b text):
    returns: integer
    language: plsample
    transform: [integer, text]
    body: return a
  function one(a integer):
    returns: integer
    language: plsample
    transform: integer
    body: return a
  function inline(a integer):
    returns: integer
    language: plsample transform for type integer
    body: return a
  function none(a integer):
    returns: integer
    language: plsample
    body: return a
  procedure proc(a integer):
    language: plsample
    transform: [for type integer]
    body: return
`
	sql := apply(y)
	for _, want := range []string{
		`function "app"."both"(a integer, b text) returns integer language plsample transform for type integer, for type text as`,
		`function "app"."one"(a integer) returns integer language plsample transform for type integer as`,
		`function "app"."inline"(a integer) returns integer language plsample transform for type integer as`,
		`function "app"."none"(a integer) returns integer language plsample as`,
		`procedure "app"."proc"(a integer) language plsample transform for type integer as`,
	} {
		if !contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if got := transforms(); got != "both={integer,text} inline={integer} none=- one={integer} proc={integer}" {
		t.Errorf("transforms = %s", got)
	}

	pulled, err := pull.Build(introspectAll(t, db), pull.Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"transform: [integer, text]", "transform: [integer]", "transform: []"} {
		if !contains(string(pulled.Files[0].Content), want) {
			t.Errorf("pulled YAML lacks %q:\n%s", want, pulled.Files[0].Content)
		}
	}

	if _, err := db.Exec(ctx, `
		create or replace function app.both(a integer, b text) returns integer language plsample transform for type integer as 'return a';
		create or replace function app.one(a integer) returns integer language plsample as 'return a';
		create or replace function app.inline(a integer) returns integer language plsample as 'return a';
		create or replace function app.none(a integer) returns integer language plsample transform for type integer as 'return a + 1';
		create or replace procedure app.proc(a integer) language plsample as 'return';`); err != nil {
		t.Fatal(err)
	}
	sql = apply(y)
	if got := transforms(); got != "both={integer,text} inline={integer} none={integer} one={integer} proc={integer}" {
		t.Errorf("transforms after repairing drift = %s\n%s", got, sql)
	}
	if !contains(sql, `function "app"."none"(a integer) returns integer language plsample transform for type integer as`) {
		t.Errorf("replacing a routine that declares no transforms must keep the live ones:\n%s", sql)
	}

	apply(strings.Replace(strings.Replace(y, "transform: [integer, text]", "transform: []", 1), "    language: plsample\n    body: return a\n", "    language: plsample\n    transform: []\n    body: return a\n", 1))
	if got := transforms(); got != "both=- inline={integer} none=- one={integer} proc={integer}" {
		t.Errorf("transforms after declaring none = %s", got)
	}
}

func TestIntegrationPruneKeepsCompiledFunctions(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_routine_prune")
	if _, err := db.Exec(ctx, `
		create schema app;
		create table app.t (id integer primary key);
		create function app.handler() returns language_handler language c as '$libdir/plpgsql', 'plpgsql_call_handler';
		create function app.to_pl(internal) returns internal language internal immutable as 'int4in';
		create function app.stray() returns integer language sql as 'select 1';`); err != nil {
		t.Fatal(err)
	}
	live := introspectAll(t, db)
	res, err := pull.Build(live, pull.Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(res.Warnings, "\n")
	for _, want := range []string{"function app.handler is written in c and cannot be pulled", "function app.to_pl is written in internal and cannot be pulled"} {
		if !contains(warnings, want) {
			t.Errorf("missing warning %q in:\n%s", want, warnings)
		}
	}
	pulled, err := schema.LoadAndMerge([]string{writeYAML(t, string(res.Files[0].Content))})
	if err != nil {
		t.Fatal(err)
	}
	opts := diff.Options{Unsafe: true, Prune: true}
	if sql := diff.Render(diff.PlanWith(live, pulled, opts)); sql != "" {
		t.Errorf("pulled YAML must diff clean against its source:\n%s", sql)
	}
	if pulled.Functions[schema.FunctionKey("app.stray", "()")] == nil {
		t.Fatalf("pulled YAML lacks app.stray():\n%s", res.Files[0].Content)
	}
	delete(pulled.Functions, schema.FunctionKey("app.stray", "()"))
	sql := diff.Render(diff.PlanWith(live, pulled, opts))
	if !contains(sql, `drop function "app"."stray"()`) || contains(sql, "handler") || contains(sql, "to_pl") {
		t.Errorf("prune must drop only the function YAML can declare:\n%s", sql)
	}
}

func TestIntegrationPullReportsLanguagesAndTransforms(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_pull_unsupported_global")
	if _, err := db.Exec(ctx, standInTransforms+`
		create extension postgres_fdw;
		create foreign data wrapper second_pg handler postgres_fdw_handler validator postgres_fdw_validator;
		create schema app;
		create table app.t (id integer primary key);`); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []pull.Options{{}, {Schemas: []string{"app"}}} {
		res, err := pull.Build(introspectAll(t, db), opts)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(res.Warnings, "\n")
		for _, want := range []string{
			"language plsample is not supported by pgy and was left out",
			"transform for integer language plsample is not supported by pgy and was left out",
			"transform for text language plsample is not supported by pgy and was left out",
		} {
			if !contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
		if contains(got, "plpgsql") || contains(got, "wrapper postgres_fdw") {
			t.Errorf("extension members must not be reported:\n%s", got)
		}
		if contains(got, "second_pg") {
			t.Errorf("a wrapper no extension owns is pulled, not reported:\n%s", got)
		}
		database := ""
		for _, f := range res.Files {
			if f.Name == "database.yml" {
				database = string(f.Content)
			}
		}
		if want := "foreignDataWrappers:\n  second_pg:\n    handler: public.postgres_fdw_handler\n    validator: public.postgres_fdw_validator\n    options: {}\n"; !contains(database, want) {
			t.Errorf("want:\n%s\nin database.yml:\n%s", want, database)
		}
	}
}
