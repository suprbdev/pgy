package integration

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
	"github.com/suprbdev/pgy/internal/validate"
)

// wrapperStates describes every foreign-data wrapper of db: its handler,
// validator, options, owner, comment, ACL and the extension it belongs to.
func wrapperStates(t *testing.T, db *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := db.Query(context.Background(), `
		select w.fdwname::text, format('handler=%s validator=%s options=%s owner=%s comment=%s acl=%s extension=%s',
			case when w.fdwhandler = 0 then '-' else w.fdwhandler::regproc::text end,
			case when w.fdwvalidator = 0 then '-' else w.fdwvalidator::regproc::text end,
			coalesce(array_to_string(w.fdwoptions, ','), ''), pg_get_userbyid(w.fdwowner),
			coalesce(obj_description(w.oid, 'pg_foreign_data_wrapper'), ''),
			coalesce((select string_agg(a::text, ',' order by a::text collate "C") from unnest(w.fdwacl) a), ''),
			coalesce((select x.extname::text from pg_depend d join pg_extension x on x.oid = d.refobjid
			          where d.classid = 'pg_foreign_data_wrapper'::regclass and d.objid = w.oid and d.deptype = 'e'), ''))
		from pg_foreign_data_wrapper w`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, state string
		if err := rows.Scan(&name, &state); err != nil {
			t.Fatal(err)
		}
		out[name] = state
	}
	return out
}

// languageACLs returns the ACL of every language of db as sorted aclitems,
// "" for one whose ACL was never changed.
func languageACLs(t *testing.T, db *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := db.Query(context.Background(), `
		select l.lanname::text, coalesce((select string_agg(a::text, ',' order by a::text collate "C") from unnest(l.lanacl) a), '')
		from pg_language l`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, acl string
		if err := rows.Scan(&name, &acl); err != nil {
			t.Fatal(err)
		}
		out[name] = acl
	}
	return out
}

// convergeLive plans y against db the way pgy diff does, applies the plan
// and requires a second diff with --unsafe --prune to be empty. It returns
// the SQL it applied.
func convergeLive(t *testing.T, db *pgxpool.Pool, y string, opts diff.Options) string {
	t.Helper()
	plan := planExtensionsLive(t, db, y, opts)
	for _, w := range plan.Warnings {
		t.Errorf("unexpected warning: %s", w.Message)
	}
	applyPlan(t, db, plan)
	if sql := diff.Render(planExtensionsLive(t, db, y, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Fatalf("one migration must converge, the second diff has:\n%s\nafter:\n%s", sql, diff.Render(plan))
	}
	return diff.Render(plan)
}

// aclOf renders the aclitems that grantor granted in the order wrapperStates
// and languageACLs list them.
func aclOf(grantor string, items ...string) string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item + "/" + grantor
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func superuserRole(t *testing.T, admin *pgxpool.Pool, suffix string) string {
	t.Helper()
	name := freshRole(t, admin, suffix)
	if _, err := admin.Exec(context.Background(), fmt.Sprintf("alter role %q superuser", name)); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestIntegrationWrapperLifecycle(t *testing.T) {
	admin := connect(t)
	app, boss := freshRole(t, admin, "app"), superuserRole(t, admin, "boss")
	db := freshDatabase(t, admin, "pgytest_fdw_lifecycle")
	y := func(s string) string { return strings.NewReplacer("APP", app, "BOSS", boss).Replace(s) }
	opts := diff.Options{Unsafe: true, Prune: true}

	sql := convergeLive(t, db, y(`
extensions:
  - {name: postgres_fdw, schema: public}
  - {name: file_fdw, schema: public}
  - {name: dblink, schema: public}
foreignDataWrappers:
  postgres_fdw:
    grants: {APP: [usage]}
  dblink_fdw:
    comment: links
  pg2:
    handler: public.postgres_fdw_handler
    validator: postgres_fdw_validator
    owner: BOSS
    grants: {APP: [usage with grant option]}
    comment: second wrapper
  legacy:
    options: {debug: true, level: 03}
  checked:
    validator: postgresql_fdw_validator
servers:
  on_pg2: {wrapper: pg2, options: {dbname: x}}
  on_legacy: {wrapper: legacy}
`), opts)
	for _, pair := range [][2]string{
		{`create extension "postgres_fdw"`, `grant usage on foreign data wrapper "postgres_fdw"`},
		{`create extension "postgres_fdw"`, `create foreign data wrapper "pg2" handler "public"."postgres_fdw_handler" validator "postgres_fdw_validator";`},
		{`create foreign data wrapper "pg2"`, `create server "on_pg2"`},
		{`create foreign data wrapper "legacy" options ("debug" 'true', "level" '03');`, `create server "on_legacy"`},
	} {
		if i, j := strings.Index(sql, pair[0]), strings.Index(sql, pair[1]); i < 0 || j < i {
			t.Fatalf("%q must precede %q:\n%s", pair[0], pair[1], sql)
		}
	}
	if strings.Contains(sql, `create foreign data wrapper "postgres_fdw"`) || strings.Contains(sql, `create foreign data wrapper "dblink_fdw"`) {
		t.Errorf("the wrappers of extensions are never created:\n%s", sql)
	}
	owner := db.Config().ConnConfig.User
	want := map[string]string{
		"postgres_fdw": fmt.Sprintf("handler=postgres_fdw_handler validator=postgres_fdw_validator options= owner=%s comment= acl=%s extension=postgres_fdw", owner, aclOf(owner, app+"=U", owner+"=U")),
		"file_fdw":     fmt.Sprintf("handler=file_fdw_handler validator=file_fdw_validator options= owner=%s comment= acl= extension=file_fdw", owner),
		"dblink_fdw":   fmt.Sprintf("handler=- validator=dblink_fdw_validator options= owner=%s comment=links acl= extension=dblink", owner),
		"pg2":          fmt.Sprintf("handler=postgres_fdw_handler validator=postgres_fdw_validator options= owner=%s comment=second wrapper acl=%s extension=", boss, aclOf(boss, boss+"=U", app+"=U*")),
		"legacy":       fmt.Sprintf("handler=- validator=- options=debug=true,level=03 owner=%s comment= acl= extension=", owner),
		"checked":      fmt.Sprintf("handler=- validator=postgresql_fdw_validator options= owner=%s comment= acl= extension=", owner),
	}
	if got := wrapperStates(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("wrappers after create:\n%v\nwant:\n%v", got, want)
	}

	sql = convergeLive(t, db, y(`
extensions:
  - {name: postgres_fdw, schema: public}
  - {name: file_fdw, schema: public}
  - {name: dblink, schema: public}
foreignDataWrappers:
  postgres_fdw:
    grants: {}
    comment: remote PostgreSQL
  pg2:
    handler: ""
    validator: postgresql_fdw_validator
    owner: BOSS
    grants: {APP: [usage], public: [usage]}
    comment: second wrapper, without a handler
  old_one:
    renamedFrom: legacy
    options: {level: 4, more: yes}
  checked:
    handler: postgres_fdw_handler
    validator: ""
servers:
  on_pg2: {wrapper: pg2, options: {dbname: x}}
  on_legacy: {wrapper: old_one}
`), opts)
	for _, want := range []string{
		`alter foreign data wrapper "legacy" rename to "old_one";`,
		`alter foreign data wrapper "checked" handler "postgres_fdw_handler" no validator;`,
		`alter foreign data wrapper "old_one" options (set "level" '4', add "more" 'yes', drop "debug");`,
		`alter foreign data wrapper "pg2" no handler validator "postgresql_fdw_validator";`,
		fmt.Sprintf(`revoke grant option for usage on foreign data wrapper "pg2" from %q;`, app),
		`grant usage on foreign data wrapper "pg2" to public;`,
		fmt.Sprintf(`revoke usage on foreign data wrapper "postgres_fdw" from %q;`, app),
		`comment on foreign data wrapper "postgres_fdw" is 'remote PostgreSQL';`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "dblink_fdw") || strings.Contains(sql, "server") {
		t.Errorf("an undeclared wrapper of an extension and the servers of a renamed wrapper are left alone:\n%s", sql)
	}
	want = map[string]string{
		"postgres_fdw": fmt.Sprintf("handler=postgres_fdw_handler validator=postgres_fdw_validator options= owner=%s comment=remote PostgreSQL acl=%s extension=postgres_fdw", owner, aclOf(owner, owner+"=U")),
		"file_fdw":     want["file_fdw"],
		"dblink_fdw":   want["dblink_fdw"],
		"pg2":          fmt.Sprintf("handler=- validator=postgresql_fdw_validator options= owner=%s comment=second wrapper, without a handler acl=%s extension=", boss, aclOf(boss, "=U", boss+"=U", app+"=U")),
		"old_one":      fmt.Sprintf("handler=- validator=- options=level=4,more=yes owner=%s comment= acl= extension=", owner),
		"checked":      fmt.Sprintf("handler=postgres_fdw_handler validator=- options= owner=%s comment= acl= extension=", owner),
	}
	if got := wrapperStates(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("wrappers after alter:\n%v\nwant:\n%v", got, want)
	}

	pruned := y(`
extensions:
  - {name: postgres_fdw, schema: public}
  - {name: file_fdw, schema: public}
  - {name: dblink, schema: public}
foreignDataWrappers:
  checked:
    handler: postgres_fdw_handler
servers:
  on_pg2: {wrapper: pg2, options: {dbname: x}}
`)
	plan := planExtensionsLive(t, db, pruned, opts)
	if got := strings.TrimSpace(diff.Render(plan)); got != "drop server \"on_legacy\";\ndrop foreign data wrapper \"old_one\";" {
		t.Errorf("want the unlisted wrapper dropped after its server, got:\n%s", got)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0].Message, "foreign-data wrapper pg2 is not in the foreignDataWrappers block but servers this plan keeps still use it (on_pg2)") {
		t.Errorf("want a warning about the wrapper a kept server uses, got %v", plan.Warnings)
	}
	applyPlan(t, db, plan)
	if sql := diff.Render(planExtensionsLive(t, db, pruned, diff.Options{})); sql != "" {
		t.Errorf("without --prune nothing is dropped:\n%s", sql)
	}
	got := wrapperStates(t, db)
	for _, name := range []string{"postgres_fdw", "file_fdw", "dblink_fdw", "pg2", "checked"} {
		if got[name] == "" {
			t.Errorf("wrapper %s must be kept, have %v", name, got)
		}
	}
	if len(got) != 5 {
		t.Errorf("wrappers after prune: %v", got)
	}
}

func TestIntegrationWrapperOfExtension(t *testing.T) {
	admin := connect(t)
	app, boss := freshRole(t, admin, "app"), superuserRole(t, admin, "boss")
	db := freshDatabase(t, admin, "pgytest_fdw_member")
	ctx := context.Background()
	if _, err := db.Exec(ctx, strings.ReplaceAll(`
		create extension hstore schema public;
		create foreign data wrapper catalogue options (kept '1', changed 'old', dropped 'x');
		grant usage on foreign data wrapper catalogue to "APP";
		alter extension hstore add foreign data wrapper catalogue;
		create table public.t (id integer not null);`, "APP", app)); err != nil {
		t.Fatal(err)
	}
	y := strings.NewReplacer("APP", app, "BOSS", boss).Replace(`
extensions:
  - {name: hstore, schema: public}
foreignDataWrappers:
  catalogue:
    options: {kept: 1, changed: new, added: 2}
    owner: BOSS
    grants: {BOSS: [usage], pg_monitor: [usage]}
    comment: connection settings
schema public:
  table t:
    columns:
      id: {type: int}
`)
	sql := convergeLive(t, db, y, diff.Options{Unsafe: true, Prune: true})
	wantLines := []string{
		`alter foreign data wrapper "catalogue" options (add "added" '2', set "changed" 'new', drop "dropped");`,
		fmt.Sprintf(`alter foreign data wrapper "catalogue" owner to %q;`, boss),
		`grant usage on foreign data wrapper "catalogue" to "pg_monitor";`,
		`comment on foreign data wrapper "catalogue" is 'connection settings';`,
	}
	if strings.TrimSpace(sql) != strings.Join(wantLines, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", sql, strings.Join(wantLines, "\n"))
	}
	want := fmt.Sprintf("handler=- validator=- options=kept=1,changed=new,added=2 owner=%s comment=connection settings acl=%s extension=hstore", boss, aclOf(boss, boss+"=U", app+"=U", "pg_monitor=U"))
	if got := wrapperStates(t, db)["catalogue"]; got != want {
		t.Errorf("the extension's wrapper:\n%s\nwant:\n%s", got, want)
	}

	plan := planExtensionsLive(t, db, "extensions: []\nforeignDataWrappers:\n  catalogue: {comment: connection settings}\nschema public:\n  table t:\n    columns:\n      id: {type: int}\n", diff.Options{Unsafe: true, Prune: true})
	if sql := diff.Render(plan); sql != "" {
		t.Errorf("an extension whose wrapper the YAML declares must be kept, got:\n%s", sql)
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0].Kind != "dependents" || !strings.Contains(plan.Warnings[0].Message, "foreign-data wrapper catalogue, which the YAML declares") {
		t.Errorf("want a warning naming the declaration, got %v", plan.Warnings)
	}
	plan = planExtensionsLive(t, db, "extensions: [{name: hstore, schema: public}]\nforeignDataWrappers: {}\nschema public:\n  table t:\n    columns:\n      id: {type: int}\n", diff.Options{Unsafe: true, Prune: true})
	if sql := diff.Render(plan); sql != "" {
		t.Errorf("the wrapper of an extension is never dropped, got:\n%s", sql)
	}

	res, err := pull.Build(introspectAll(t, db), pull.Options{Single: true, Owners: true})
	if err != nil {
		t.Fatal(err)
	}
	pulled := string(res.Files[0].Content)
	wantYAML := fmt.Sprintf("foreignDataWrappers:\n  catalogue:\n    options: {added: \"2\", changed: new, kept: \"1\"}\n    owner: %s\n    grants: {pg_monitor: [usage]}\n    comment: connection settings\n", boss)
	if !strings.Contains(pulled, wantYAML) {
		t.Errorf("want only what changed since the wrapper joined its extension:\n%s\nin:\n%s", wantYAML, pulled)
	}
}

func TestIntegrationWrapperPrunedWithExtension(t *testing.T) {
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_fdw_prune_ext")
	if _, err := db.Exec(context.Background(), `
		create extension postgres_fdw schema public;
		create foreign data wrapper pg2 handler postgres_fdw_handler;
		create server on_pg2 foreign data wrapper pg2;
		create table public.t (id integer not null);`); err != nil {
		t.Fatal(err)
	}
	const table = "schema public:\n  table t:\n    columns:\n      id: {type: int}\n"
	opts := diff.Options{Unsafe: true, Prune: true}
	plan := planExtensionsLive(t, db, "extensions: []\nforeignDataWrappers:\n  pg2: {handler: postgres_fdw_handler}\n"+table, opts)
	if sql := diff.Render(plan); sql != "" {
		t.Errorf("an extension whose handler a declared wrapper uses must be kept, got:\n%s", sql)
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0].Kind != "dependents" || !strings.Contains(plan.Warnings[0].Message, "foreign-data wrapper pg2") {
		t.Errorf("want a warning naming the wrapper, got %v", plan.Warnings)
	}
	plan = planExtensionsLive(t, db, "extensions: []\nforeignDataWrappers: {}\nservers: {}\n"+table, opts)
	if got := strings.TrimSpace(diff.Render(plan)); got != "drop server \"on_pg2\";\ndrop foreign data wrapper \"pg2\";\ndrop extension \"postgres_fdw\";" {
		t.Errorf("want the server, its wrapper and then the extension dropped, got:\n%s", got)
	}
	applyPlan(t, db, plan)
	if got := wrapperStates(t, db); len(got) != 0 {
		t.Errorf("wrappers left: %v", got)
	}
}

func TestIntegrationWrapperFollowsMovedExtension(t *testing.T) {
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_fdw_ext_move")
	if _, err := db.Exec(context.Background(), `
		create extension postgres_fdw schema public;
		create foreign data wrapper pg2 handler postgres_fdw_handler validator postgres_fdw_validator;`); err != nil {
		t.Fatal(err)
	}
	sql := convergeLive(t, db, `
extensions:
  - {name: postgres_fdw, schema: ext}
foreignDataWrappers:
  pg2: {handler: ext.postgres_fdw_handler, validator: ext.postgres_fdw_validator}
`, diff.Options{Unsafe: true, Prune: true})
	if want := "create schema if not exists \"ext\";\nalter extension \"postgres_fdw\" set schema \"ext\";"; strings.TrimSpace(sql) != want {
		t.Errorf("the wrapper's handler moves with its extension, got:\n%s\nwant:\n%s", sql, want)
	}
	if got := wrapperStates(t, db)["pg2"]; !strings.Contains(got, "handler=ext.postgres_fdw_handler validator=ext.postgres_fdw_validator") {
		t.Errorf("pg2 after the move: %s", got)
	}
}

func TestIntegrationWrapperFunctionOnSearchPath(t *testing.T) {
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_fdw_search_path")
	if _, err := db.Exec(context.Background(), `
		create schema ext;
		create extension postgres_fdw schema ext;`); err != nil {
		t.Fatal(err)
	}
	d := loadSpellingYAML(t, `
extensions:
  - {name: postgres_fdw, schema: ext}
foreignDataWrappers:
  pg2: {handler: postgres_fdw_handler, validator: postgres_fdw_validator}
`)
	onPath := sessionPool(t, "pgytest_fdw_search_path", map[string]string{"search_path": "ext, public"})
	_, plan := planLikeDiff(t, onPath, d)
	if sql := strings.TrimSpace(diff.Render(plan)); sql != `create foreign data wrapper "pg2" handler "postgres_fdw_handler" validator "postgres_fdw_validator";` {
		t.Fatalf("got:\n%s", sql)
	}
	applyPlan(t, onPath, plan)
	_, plan = planLikeDiff(t, onPath, d)
	requireNoChanges(t, "with the functions' schema on the search_path", plan)

	_, plan = planLikeDiff(t, db, d)
	if sql := strings.TrimSpace(diff.Render(plan)); sql != `alter foreign data wrapper "pg2" handler "postgres_fdw_handler" validator "postgres_fdw_validator";` {
		t.Errorf("a function in a schema that is not on the search_path has to be qualified, got:\n%s", sql)
	}
	_, plan = planLikeDiff(t, db, loadSpellingYAML(t, `
extensions:
  - {name: postgres_fdw, schema: ext}
foreignDataWrappers:
  pg2: {handler: ext.postgres_fdw_handler, validator: ext.postgres_fdw_validator}
`))
	requireNoChanges(t, "qualified", plan)
}

func TestIntegrationWrappersPulled(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	app, boss := freshRole(t, admin, "app"), superuserRole(t, admin, "boss")
	src := freshDatabase(t, admin, "pgytest_fdw_pull")
	dst := freshDatabase(t, admin, "pgytest_fdw_pull_copy")
	if _, err := src.Exec(ctx, strings.NewReplacer("APP", app, "BOSS", boss).Replace(`
		create extension postgres_fdw;
		create extension file_fdw;
		create extension dblink;
		create foreign data wrapper pg2 handler postgres_fdw_handler validator postgres_fdw_validator;
		comment on foreign data wrapper pg2 is 'second wrapper';
		alter foreign data wrapper pg2 owner to "BOSS";
		grant usage on foreign data wrapper pg2 to "APP" with grant option;
		create foreign data wrapper legacy options (debug 'true', level '03');
		create foreign data wrapper checked validator postgresql_fdw_validator;
		create server on_pg2 foreign data wrapper pg2 options (dbname 'x');
		create server on_legacy foreign data wrapper legacy;
		grant usage on foreign data wrapper postgres_fdw to "APP";
		comment on foreign data wrapper dblink_fdw is 'links';`)); err != nil {
		t.Fatal(err)
	}
	live := introspectAll(t, src)
	res, err := pull.Build(live, pull.Options{Single: true, Owners: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "foreign-data wrapper") {
			t.Errorf("wrappers are pulled, not reported: %s", w)
		}
	}
	y := string(res.Files[0].Content)
	wantYAML := strings.NewReplacer("APP", app, "BOSS", boss).Replace(`foreignDataWrappers:
  checked:
    handler: ""
    validator: postgresql_fdw_validator
    options: {}
  dblink_fdw:
    comment: links
  legacy:
    handler: ""
    validator: ""
    options: {debug: "true", level: "03"}
  pg2:
    handler: public.postgres_fdw_handler
    validator: public.postgres_fdw_validator
    options: {}
    owner: BOSS
    grants: {APP: [usage with grant option]}
    comment: second wrapper
  postgres_fdw:
    grants: {APP: [usage]}
`)
	if !strings.Contains(y, wantYAML) {
		t.Errorf("want:\n%s\nin the pulled YAML:\n%s", wantYAML, y)
	}
	for _, issue := range validate.Files([]string{writeYAML(t, y)}) {
		if issue.Severity == validate.Error {
			t.Errorf("pgy validate rejects the pulled YAML: %s", issue)
		}
	}
	opts := diff.Options{Unsafe: true, Prune: true}
	if sql := diff.Render(planExtensionsLive(t, src, y, opts)); sql != "" {
		t.Fatalf("pulled YAML must diff clean against its source:\n%s\n%s", sql, y)
	}
	applyPlan(t, dst, planExtensionsLive(t, dst, y, diff.Options{}))
	if sql := diff.Render(planExtensionsLive(t, dst, y, opts)); sql != "" {
		t.Errorf("rebuilt database differs from the pulled YAML:\n%s", sql)
	}
	if want, got := wrapperStates(t, src), wrapperStates(t, dst); !reflect.DeepEqual(got, want) {
		t.Errorf("wrappers were rebuilt as:\n%v\nwant:\n%v\n%s", got, want, y)
	}
	if a := pgDump(t, "pgytest_fdw_pull"); a != "" {
		if d := dumpDifferences(a, pgDump(t, "pgytest_fdw_pull_copy")); len(d) > 0 {
			t.Errorf("pg_dump of the rebuilt database differs from the original:\n%s\n%s", strings.Join(d, "\n"), y)
		}
	}

	if _, err := dst.Exec(ctx, `
		alter foreign data wrapper legacy handler postgres_fdw_handler;
		alter foreign data wrapper checked no validator;`); err != nil {
		t.Fatal(err)
	}
	repair := planExtensionsLive(t, dst, y, opts)
	if got := strings.TrimSpace(diff.Render(repair)); got != "alter foreign data wrapper \"checked\" validator \"postgresql_fdw_validator\";\nalter foreign data wrapper \"legacy\" no handler;" {
		t.Errorf("the pulled YAML states that a wrapper has no handler or validator, so drift is undone, got:\n%s", got)
	}
	applyPlan(t, dst, repair)
	if want, got := wrapperStates(t, src), wrapperStates(t, dst); !reflect.DeepEqual(got, want) {
		t.Errorf("wrappers after undoing the drift:\n%v\nwant:\n%v", got, want)
	}
}

func TestIntegrationWrapperAttributesOnly(t *testing.T) {
	admin := connect(t)
	app := freshRole(t, admin, "app")
	db := freshDatabase(t, admin, "pgytest_fdw_attributes")
	if _, err := db.Exec(context.Background(), `
		create extension postgres_fdw;
		create foreign data wrapper mine handler postgres_fdw_handler validator postgres_fdw_validator;
		create server s foreign data wrapper mine;
		create foreign table remote (id integer) server s;`); err != nil {
		t.Fatal(err)
	}
	entry := strings.ReplaceAll("foreignDataWrappers:\n  mine:\n    grants: {APP: [usage]}\n    comment: made by hand\n", "APP", app)
	sql := convergeLive(t, db, entry, diff.Options{})
	wantLines := []string{
		fmt.Sprintf(`grant usage on foreign data wrapper "mine" to %q;`, app),
		`comment on foreign data wrapper "mine" is 'made by hand';`,
	}
	if strings.TrimSpace(sql) != strings.Join(wantLines, "\n") {
		t.Errorf("an entry without handler and validator leaves both alone, got:\n%s\nwant:\n%s", sql, strings.Join(wantLines, "\n"))
	}
	if got := wrapperStates(t, db)["mine"]; !strings.HasPrefix(got, "handler=postgres_fdw_handler validator=postgres_fdw_validator ") {
		t.Errorf("the wrapper kept its functions: %s", got)
	}

	plan := planExtensionsLive(t, db, entry+"    handler: \"\"\n", diff.Options{})
	if got := strings.TrimSpace(diff.Render(plan)); got != `alter foreign data wrapper "mine" no handler;` {
		t.Errorf("an empty handler removes it and nothing else, got:\n%s", got)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0].Message, "cannot be read until the wrapper has a handler again: public.remote") {
		t.Errorf("want a warning naming the foreign table that loses its handler, got %v", plan.Warnings)
	}
	applyPlan(t, db, plan)
	if got := wrapperStates(t, db)["mine"]; !strings.HasPrefix(got, "handler=- validator=postgres_fdw_validator ") {
		t.Errorf("the wrapper after NO HANDLER: %s", got)
	}
}

func TestIntegrationLanguageGrants(t *testing.T) {
	admin := connect(t)
	dev, lead := freshRole(t, admin, "dev"), freshRole(t, admin, "lead")
	db := freshDatabase(t, admin, "pgytest_language_grants")
	owner := db.Config().ConnConfig.User
	y := func(s string) string { return strings.NewReplacer("DEV", dev, "LEAD", lead).Replace(s) }
	opts := diff.Options{Unsafe: true, Prune: true}

	sql := convergeLive(t, db, y(`
languages:
  plpgsql:
    grants: {public: [], DEV: [usage]}
  sql:
    grants: {LEAD: [usage with grant option]}
schema public:
  function one():
    returns: integer
    language: plpgsql
    body: begin return 1; end
`), opts)
	wantLines := []string{
		`revoke usage on language "plpgsql" from public;`,
		fmt.Sprintf(`grant usage on language "plpgsql" to %q;`, dev),
		fmt.Sprintf(`grant usage on language "sql" to %q with grant option;`, lead),
		`create function "public"."one"() returns integer language plpgsql as $$begin return 1; end$$;`,
	}
	if strings.TrimSpace(sql) != strings.Join(wantLines, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", sql, strings.Join(wantLines, "\n"))
	}
	acls := languageACLs(t, db)
	if want := aclOf(owner, owner+"=U", dev+"=U"); acls["plpgsql"] != want {
		t.Errorf("plpgsql ACL = %q, want %q", acls["plpgsql"], want)
	}
	if want := aclOf(owner, "=U", owner+"=U", lead+"=U*"); acls["sql"] != want {
		t.Errorf("sql ACL = %q, want %q", acls["sql"], want)
	}

	res, err := pull.Build(introspectAll(t, db), pull.Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	wantYAML := y("languages:\n  plpgsql:\n    grants: {DEV: [usage], public: []}\n  sql:\n    grants: {LEAD: [usage with grant option]}\n")
	if pulled := string(res.Files[0].Content); !strings.Contains(pulled, wantYAML) {
		t.Errorf("want:\n%s\nin the pulled YAML:\n%s", wantYAML, pulled)
	}

	sql = convergeLive(t, db, y(`
languages:
  plpgsql:
    grants: {public: [usage]}
  sql:
    grants: {LEAD: [usage]}
`), diff.Options{})
	wantLines = []string{
		fmt.Sprintf(`revoke usage on language "plpgsql" from %q;`, dev),
		fmt.Sprintf(`revoke grant option for usage on language "sql" from %q;`, lead),
		`grant usage on language "plpgsql" to public;`,
	}
	if strings.TrimSpace(sql) != strings.Join(wantLines, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", sql, strings.Join(wantLines, "\n"))
	}

	plan := planExtensionsLive(t, db, y("languages:\n  plnone: {grants: {DEV: [usage]}}\n  c: {grants: {DEV: [usage]}}\n"), diff.Options{})
	if sql := diff.Render(plan); sql != "" {
		t.Errorf("nothing can be planned for a language that is missing or not trusted, got:\n%s", sql)
	}
	if got := warningMessages(plan); !strings.Contains(got, "language plnone is declared under languages") || !strings.Contains(got, "language c is not trusted") {
		t.Errorf("want a warning for each declaration, got:\n%s", got)
	}
}

func TestIntegrationLanguageGrantsPulled(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	dev := freshRole(t, admin, "dev")
	src := freshDatabase(t, admin, "pgytest_language_pull")
	dst := freshDatabase(t, admin, "pgytest_language_pull_copy")
	if _, err := src.Exec(ctx, strings.ReplaceAll(`
		revoke usage on language plpgsql from public;
		grant usage on language plpgsql to "DEV";
		grant usage on language sql to "DEV" with grant option;
		create function public.f() returns integer language plpgsql as $$ begin return 1; end $$;`, "DEV", dev)); err != nil {
		t.Fatal(err)
	}
	y := pullAndRebuild(t, src, dst)
	want := strings.ReplaceAll("languages:\n  plpgsql:\n    grants: {DEV: [usage], public: []}\n  sql:\n    grants: {DEV: [usage with grant option]}\n", "DEV", dev)
	if !strings.Contains(y, want) {
		t.Errorf("want:\n%s\nin the pulled YAML:\n%s", want, y)
	}
	for _, issue := range validate.Files([]string{writeYAML(t, y)}) {
		if issue.Severity == validate.Error {
			t.Errorf("pgy validate rejects the pulled YAML: %s", issue)
		}
	}
	if a, b := languageACLs(t, src), languageACLs(t, dst); !reflect.DeepEqual(a, b) {
		t.Errorf("language ACLs %v were rebuilt as %v\n%s", a, b, y)
	}
	if a := pgDump(t, "pgytest_language_pull"); a != "" {
		if d := dumpDifferences(a, pgDump(t, "pgytest_language_pull_copy")); len(d) > 0 {
			t.Errorf("pg_dump of the rebuilt database differs from the original:\n%s\n%s", strings.Join(d, "\n"), y)
		}
	}
}

func TestIntegrationLanguageOfNewExtension(t *testing.T) {
	admin := connect(t)
	dev := freshRole(t, admin, "dev")
	db := freshDatabase(t, admin, "pgytest_language_plperl")
	copy := freshDatabase(t, admin, "pgytest_language_plperl_copy")
	requireLanguage(t, copy, "plperl")
	y := strings.ReplaceAll(`
extensions: [plperl]
languages:
  plperl:
    grants: {public: [], DEV: [usage]}
schema public:
  function two():
    returns: integer
    language: plperl
    body: return 2;
`, "DEV", dev)
	sql := convergeLive(t, db, y, diff.Options{Unsafe: true, Prune: true})
	wantLines := []string{
		`create extension if not exists "plperl";`,
		`revoke usage on language "plperl" from public;`,
		fmt.Sprintf(`grant usage on language "plperl" to %q;`, dev),
		`set check_function_bodies = off;`,
		`create function "public"."two"() returns integer language plperl as $$return 2;$$;`,
		`reset check_function_bodies;`,
	}
	if strings.TrimSpace(sql) != strings.Join(wantLines, "\n") {
		t.Errorf("the probe knows the trusted language plperl brings, got:\n%s\nwant:\n%s", sql, strings.Join(wantLines, "\n"))
	}
	owner := db.Config().ConnConfig.User
	granted := aclOf(owner, owner+"=U", dev+"=U")
	if got := languageACLs(t, db)["plperl"]; got != granted {
		t.Errorf("plperl ACL = %q, want %q", got, granted)
	}

	pulled := pullAndRebuild(t, db, copy)
	if want := strings.ReplaceAll("languages:\n  plperl:\n    grants: {DEV: [usage], public: []}\n", "DEV", dev); !strings.Contains(pulled, want) {
		t.Errorf("want:\n%s\nin the pulled YAML:\n%s", want, pulled)
	}
	if a, b := languageACLs(t, db), languageACLs(t, copy); !reflect.DeepEqual(a, b) {
		t.Errorf("language ACLs %v were rebuilt as %v", a, b)
	}

	guardedLines := append([]string{
		wantLines[0],
		fmt.Sprintf(`do $$ begin if exists (select from pg_catalog.pg_language where lanname = 'plperl' and lanpltrusted) then revoke all on language "plperl" from public; grant usage on language "plperl" to %q; end if; end $$;`, dev),
	}, wantLines[3:]...)
	for _, how := range unprobedPlans {
		guarded := freshDatabase(t, admin, "pgytest_language_plperl_"+how.name)
		plan := planUnprobed(t, guarded, y, how.offline)
		if msgs := warningMessages(plan); msgs != "" {
			t.Errorf("%s: unexpected warnings:\n%s", how.name, msgs)
		}
		if sql := strings.TrimSpace(diff.Render(plan)); sql != strings.Join(guardedLines, "\n") {
			t.Errorf("%s: the grants wait in a DO block for the language the extension may bring, got:\n%s\nwant:\n%s", how.name, sql, strings.Join(guardedLines, "\n"))
		}
		applyPlan(t, guarded, plan)
		if got := languageACLs(t, guarded)["plperl"]; got != granted {
			t.Errorf("%s: plperl ACL = %q, want %q", how.name, got, granted)
		}
		if sql := diff.Render(planExtensionsLive(t, guarded, y, diff.Options{Unsafe: true, Prune: true})); sql != "" {
			t.Errorf("%s: one migration must converge, the second diff has:\n%s", how.name, sql)
		}
	}
}

// unprobedPlans are the ways pgy diff plans without learning what the
// extensions a plan creates bring: with --from-empty, and with a connection
// when the probe fails.
var unprobedPlans = []struct {
	name    string
	offline bool
}{{"from_empty", true}, {"probe_failed", false}}

// planUnprobed plans y for db without the extension probe: against the
// snapshot pgy diff --from-empty uses when offline is set, and otherwise
// against db the way pgy diff does when the probe fails.
func planUnprobed(t *testing.T, db *pgxpool.Pool, y string, offline bool) *diff.PlanDiff {
	t.Helper()
	ctx := context.Background()
	d, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
	if err != nil {
		t.Fatal(err)
	}
	if offline {
		return diff.PlanWith(offlineLive(), d, diff.Options{})
	}
	live, err := diff.Introspect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectExtensions(ctx, db, live, d, diff.Options{}); err != nil {
		t.Fatal(err)
	}
	return diff.PlanWith(live, d, diff.Options{})
}

// TestIntegrationLanguagesOfPendingExtensions grants on languages an
// extension of the same plan brings: one not named like the extension that
// brings it, here through cascade, in a single migration, and an untrusted
// one, which a plan that knows what the extensions bring reports and leaves
// out, and a migration planned without knowing skips instead of failing.
func TestIntegrationLanguagesOfPendingExtensions(t *testing.T) {
	admin := connect(t)
	dev := freshRole(t, admin, "dev")
	db := freshDatabase(t, admin, "pgytest_language_pending")
	requireLanguage(t, db, "plperl")
	y := strings.ReplaceAll(`
extensions:
  - {name: bool_plperl, schema: public, cascade: true}
  - {name: plperlu, schema: pg_catalog}
languages:
  plperl:
    grants: {DEV: [usage]}
  plperlu:
    grants: {DEV: [usage]}
`, "DEV", dev)
	const untrusted = "language plperlu is not trusted: only superusers can use it and PostgreSQL rejects GRANT and REVOKE on it; nothing is planned for it"
	owner := db.Config().ConnConfig.User
	granted := aclOf(owner, "="+"U", owner+"=U", dev+"=U")
	checkACLs := func(how string, db *pgxpool.Pool) {
		t.Helper()
		acls := languageACLs(t, db)
		if acls["plperl"] != granted {
			t.Errorf("%s: plperl, which bool_plperl brings, is granted in the same migration: ACL %q, want %q", how, acls["plperl"], granted)
		}
		if acls["plperlu"] != "" {
			t.Errorf("%s: nothing is granted on the untrusted plperlu, got ACL %q", how, acls["plperlu"])
		}
	}

	plan := planExtensionsLive(t, db, y, diff.Options{})
	if msgs := warningMessages(plan); msgs != untrusted {
		t.Errorf("the probe finds the untrusted language, which is reported, got:\n%s", msgs)
	}
	wantLines := []string{
		`create extension "bool_plperl" schema "public" cascade;`,
		`create extension if not exists "plperlu" schema "pg_catalog";`,
		fmt.Sprintf(`grant usage on language "plperl" to %q;`, dev),
	}
	if sql := strings.TrimSpace(diff.Render(plan)); sql != strings.Join(wantLines, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", sql, strings.Join(wantLines, "\n"))
	}
	applyPlan(t, db, plan)
	checkACLs("connected", db)
	plan = planExtensionsLive(t, db, y, diff.Options{})
	if sql := diff.Render(plan); sql != "" {
		t.Errorf("a second diff must be empty, got:\n%s", sql)
	}
	if msgs := warningMessages(plan); msgs != untrusted {
		t.Errorf("the untrusted language is still reported, got:\n%s", msgs)
	}

	for _, how := range unprobedPlans {
		guarded := freshDatabase(t, admin, "pgytest_language_pending_"+how.name)
		plan := planUnprobed(t, guarded, y, how.offline)
		if msgs := warningMessages(plan); msgs != "" {
			t.Errorf("%s: languages the plan's extensions may bring are not reported, got:\n%s", how.name, msgs)
		}
		sql := diff.Render(plan)
		for _, lang := range []string{"plperl", "plperlu"} {
			if want := fmt.Sprintf(`do $$ begin if exists (select from pg_catalog.pg_language where lanname = '%s' and lanpltrusted) then grant usage on language %q to %q; end if; end $$;`, lang, lang, dev); !strings.Contains(sql, want) {
				t.Errorf("%s: want\n%s\nin:\n%s", how.name, want, sql)
			}
		}
		applyPlan(t, guarded, plan)
		checkACLs(how.name, guarded)
		if msgs := warningMessages(planExtensionsLive(t, guarded, y, diff.Options{})); msgs != untrusted {
			t.Errorf("%s: the untrusted language is reported once it exists, got:\n%s", how.name, msgs)
		}
	}
}

// TestIntegrationExtensionRevokedPublicDrift repairs PUBLIC regaining
// EXECUTE on a function whose extension's script revoked it
// (pg_buffercache_pages), although the grants block doesn't list public.
func TestIntegrationExtensionRevokedPublicDrift(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	app := freshRole(t, admin, "app")
	db := freshDatabase(t, admin, "pgytest_ext_public_drift")
	y := strings.ReplaceAll(`
extensions:
  - {name: pg_buffercache, schema: public}
schema public:
  function pg_buffercache_pages():
    grants: {APP: [execute]}
`, "APP", app)
	applyPlan(t, db, planExtensionsLive(t, db, y, diff.Options{}))
	if sql := diff.Render(planExtensionsLive(t, db, y, diff.Options{})); sql != "" {
		t.Fatalf("a second diff must be empty, got:\n%s", sql)
	}
	if _, err := db.Exec(ctx, `grant execute on function pg_buffercache_pages() to public`); err != nil {
		t.Fatal(err)
	}
	plan := planExtensionsLive(t, db, y, diff.Options{})
	if sql := strings.TrimSpace(diff.Render(plan)); sql != `revoke execute on function "public"."pg_buffercache_pages"() from public;` {
		t.Errorf("PUBLIC's EXECUTE, which the extension revoked, is revoked again, got:\n%s", sql)
	}
	if msgs := warningMessages(plan); !strings.Contains(msgs, "the script of extension pg_buffercache left PUBLIC without them") {
		t.Errorf("want a warning naming the extension, got:\n%s", msgs)
	}
	applyPlan(t, db, plan)
	if sql := diff.Render(planExtensionsLive(t, db, y, diff.Options{})); sql != "" {
		t.Errorf("a diff after the repair must be empty, got:\n%s", sql)
	}
	if sql := diff.Render(planExtensionsLive(t, db, y, diff.Options{PublicGrants: diff.PublicExplicit})); sql != "" {
		t.Errorf("policy.public_grants: explicit leaves PUBLIC alone, got:\n%s", sql)
	}
}
