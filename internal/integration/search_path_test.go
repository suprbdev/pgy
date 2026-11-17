package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

// searchPathYAML declares extensions in a schema of their own and
// application objects in schema app, with every reference to either
// schema-qualified.
const searchPathYAML = `
extensions:
  - {name: citext, schema: extensions}
  - {name: hstore, schema: extensions}
  - {name: pg_trgm, schema: extensions}
  - {name: btree_gist, schema: extensions}
  - {name: uuid-ossp, schema: extensions}

schema app:
  type mood:
    type: enum
    labels: [ok, bad]
  domain email_addr:
    type: extensions.citext
    check: "VALUE OPERATOR(extensions.~) '^[^@]+@[^@]+$'"
  type contact:
    type: composite
    attributes:
      email: extensions.citext
      meta: extensions.hstore
  sequence ticket_seq: {}
  function is_ok(m app.mood):
    returns: boolean
    language: sql
    immutable: true
    body: select m = 'ok'
  function touch():
    returns: trigger
    language: plpgsql
    body: |
      begin
        return new;
      end;
  function by_email(p extensions.citext):
    returns: uuid
    language: sql
    stable: true
    body: select id from app.accounts where email::text = p::text
  table rooms:
    columns:
      id: {type: int}
      name: {type: extensions.citext}
      mood: {type: app.mood, default: "'ok'::app.mood"}
    primaryKey: [id]
  table accounts:
    columns:
      id: {type: uuid, default: extensions.uuid_generate_v4()}
      email: {type: extensions.citext}
      alt_email: {type: app.email_addr, nullable: true}
      attrs: {type: extensions.hstore, default: "''::extensions.hstore"}
      contact: {type: app.contact, nullable: true}
      mood: {type: app.mood, default: "'ok'::app.mood"}
      ticket: {type: bigint, default: "nextval('app.ticket_seq')"}
      room_id: {type: int}
      during: {type: tstzrange}
      note: {type: text, nullable: true}
      ok: {type: boolean, generated: "app.is_ok(mood)", nullable: true}
    primaryKey: [id]
    foreignKeys:
      accounts_room_fk: {columns: [room_id], references: {table: app.rooms, columns: [id]}}
    constraints:
      accounts_email_chk: {type: check, expression: "email OPERATOR(extensions.<>) ''"}
      accounts_attrs_chk: {type: check, expression: "not (attrs OPERATOR(extensions.?) 'forbidden')"}
      accounts_mood_chk: {type: check, expression: "app.is_ok(mood)"}
      accounts_no_overlap: {type: exclude, expression: "using gist (room_id with =, during with &&)"}
      accounts_note_excl: {type: exclude, expression: "using gist (note extensions.gist_trgm_ops with OPERATOR(extensions.%))"}
    indexes:
      accounts_note_trgm: {columns: [note], using: gin, opclass: extensions.gin_trgm_ops}
      accounts_plan: {columns: ["(attrs OPERATOR(extensions.->) 'plan')"]}
      accounts_ok: {columns: [email], where: "mood = 'ok'::app.mood"}
    rowLevelSecurity: true
    policies:
      accounts_self: {using: "email OPERATOR(extensions.=) current_setting('app.email', true)::extensions.citext"}
    triggers:
      accounts_touch:
        timing: before
        events: [update]
        level: row
        when: "old.attrs OPERATOR(extensions.<>) new.attrs"
        procedure: app.touch()
  view account_search:
    query: "select a.id, extensions.similarity(a.note, 'x') as sim, a.email, app.is_ok(a.mood) as ok from app.accounts a join app.rooms r on r.id = a.room_id where a.note OPERATOR(extensions.%) 'x'"
`

var searchPathOperatorRe = regexp.MustCompile(`OPERATOR\(extensions\.([^)]+)\)`)

// unqualifiedSearchPathYAML is searchPathYAML the way it is written for a
// database whose search_path lists app and extensions: types, functions,
// operators, operator classes and sequences without their schema.
func unqualifiedSearchPathYAML() string {
	y := searchPathOperatorRe.ReplaceAllString(searchPathYAML, "$1")
	y = strings.ReplaceAll(y, "extensions.", "")
	for _, name := range []string{"mood", "email_addr", "contact", "ticket_seq", "is_ok", "touch"} {
		y = strings.ReplaceAll(y, "app."+name, name)
	}
	return y
}

// sessionPool connects to database name with the given connection
// parameters, such as search_path or options.
func sessionPool(t *testing.T, name string, params map[string]string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	for k, v := range params {
		cfg.ConnConfig.RuntimeParams[k] = v
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect %s: %v", name, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// planLikeDiff introspects pool the way pgy diff does and plans d against
// it with --unsafe --prune.
func planLikeDiff(t *testing.T, pool *pgxpool.Pool, d *schema.Database) (*diff.Live, *diff.PlanDiff) {
	t.Helper()
	ctx := context.Background()
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectExtensions(ctx, pool, live, d); err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectData(ctx, pool, live, d); err != nil {
		t.Fatal(err)
	}
	if err := diff.ProbeViews(ctx, pool, live, d); err != nil {
		t.Fatal(err)
	}
	return live, diff.PlanWith(live, d, diff.Options{Unsafe: true, Prune: true})
}

func requireNoChanges(t *testing.T, session string, p *diff.PlanDiff) {
	t.Helper()
	if sql := diff.Render(p) + diff.RenderConcurrent(p); sql != "" {
		t.Errorf("%s: expected no changes, got:\n%s", session, sql)
	}
	for _, w := range p.Warnings {
		t.Errorf("%s: unexpected warning: %s", session, w.Message)
	}
}

// TestIntegrationSearchPathSameDiff diffs one database through sessions
// whose search_path is set in every way PostgreSQL offers. YAML that
// qualifies everything must be clean in all of them, and YAML that leaves
// the schemas out must be clean wherever the search_path lists them.
func TestIntegrationSearchPathSameDiff(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	const name = "pgytest_search_path"
	db := freshDatabase(t, admin, name)
	qualified, err := schema.LoadAndMerge([]string{writeYAML(t, searchPathYAML)})
	if err != nil {
		t.Fatal(err)
	}
	unqualified, err := schema.LoadAndMerge([]string{writeYAML(t, unqualifiedSearchPathYAML())})
	if err != nil {
		t.Fatal(err)
	}
	_, build := planLikeDiff(t, db, qualified)
	applyPlan(t, db, build)

	sessions := []struct {
		name   string
		setup  string
		params map[string]string
		path   []string
		listed bool
	}{
		{name: "default", path: []string{"public"}},
		{name: "DSN search_path", params: map[string]string{"search_path": "public, extensions"}, path: []string{"public", "extensions"}},
		{name: "DSN options", params: map[string]string{"options": "-csearch_path=extensions,public"}, path: []string{"extensions", "public"}},
		{name: "application schema first", params: map[string]string{"search_path": "app, public"}, path: []string{"app", "public"}},
		{name: "pg_catalog only", params: map[string]string{"search_path": "pg_catalog"}, path: []string{"pg_catalog"}},
		{name: "DSN search_path with every schema", params: map[string]string{"search_path": "app, extensions, public"}, path: []string{"app", "extensions", "public"}, listed: true},
		{name: "role setting", setup: fmt.Sprintf(`alter role current_user in database %q set search_path = extensions, app`, name), path: []string{"extensions", "app"}, listed: true},
		{name: "database setting", setup: fmt.Sprintf(`alter role current_user in database %q reset search_path; alter database %q set search_path = "$user", public, extensions, app`, name, name), path: []string{"public", "extensions", "app"}, listed: true},
	}
	for _, s := range sessions {
		if s.setup != "" {
			if _, err := db.Exec(ctx, s.setup); err != nil {
				t.Fatalf("%s: %v", s.name, err)
			}
		}
		pool := sessionPool(t, name, s.params)
		live, plan := planLikeDiff(t, pool, qualified)
		if !slices.Equal(live.SearchPath, s.path) {
			t.Errorf("%s: session search_path = %v, want %v", s.name, live.SearchPath, s.path)
		}
		requireNoChanges(t, s.name+", qualified YAML", plan)
		if s.listed {
			_, plan := planLikeDiff(t, pool, unqualified)
			requireNoChanges(t, s.name+", unqualified YAML", plan)
		}
	}
}

// TestIntegrationDatabaseSearchPathReachesMigration builds a database from
// YAML that sets the database's search_path and relies on it for every
// unqualified name: the migration that sets it has to run under it.
func TestIntegrationDatabaseSearchPathReachesMigration(t *testing.T) {
	admin := connect(t)
	const name = "pgytest_search_path_set"
	db := freshDatabase(t, admin, name)
	y := "database:\n  set:\n    search_path: '\"$user\", app, public, extensions'\n" + unqualifiedSearchPathYAML()
	d, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
	if err != nil {
		t.Fatal(err)
	}
	_, plan := planLikeDiff(t, db, d)
	sql := diff.Render(plan)
	alter := strings.Index(sql, "alter database %I set search_path")
	set := strings.Index(sql, "\nset search_path = \"$user\", app, public, extensions;\n")
	table := strings.Index(sql, `create table if not exists "app"."accounts"`)
	if alter < 0 || set < alter || table < set {
		t.Fatalf("the session's search_path must be set right after the database's and before the tables:\n%s", sql)
	}
	applyPlan(t, db, plan)
	_, again := planLikeDiff(t, sessionPool(t, name, nil), d)
	requireNoChanges(t, "new session", again)
}

// TestIntegrationPullSearchPathIndependent pulls a database whose
// search_path lists an application schema and an extension schema, through
// that search_path and through others. The YAML must be the same every
// time, qualify everything outside public, and rebuild the database in one
// that has the default search_path.
func TestIntegrationPullSearchPathIndependent(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	const name = "pgytest_search_path_pull"
	src := freshDatabase(t, admin, name)
	if _, err := src.Exec(ctx, fmt.Sprintf(`
		create schema extensions;
		create schema app;
		alter database %q set search_path = app, public, extensions;
		set search_path = app, public, extensions;
		create extension citext schema extensions;
		create extension pg_trgm schema extensions;
		create extension unaccent schema extensions;
		create type mood as enum ('ok', 'bad');
		create domain email_addr as citext check (value <> '');
		create type contact as (label text, address email_addr);
		create sequence ticket_seq;
		create function is_ok(m mood) returns boolean language sql immutable as $$ select m = 'ok' $$;
		create function touch() returns trigger language plpgsql as $$ begin return new; end $$;
		create table rooms (id int primary key, name citext not null, mood mood not null default 'ok');
		create table bookings (
			id int primary key,
			room_id int not null references rooms (id),
			who contact,
			email citext not null,
			ticket bigint not null default nextval('ticket_seq'),
			ok boolean generated always as (is_ok('ok'::mood)) stored,
			sim real generated always as (similarity(email::text, 'admin')) stored,
			constraint bookings_email_chk check (email <> ''),
			constraint bookings_ok_chk check (is_ok('ok'::mood))
		);
		create index bookings_email_trgm on bookings using gin ((email::text) gin_trgm_ops);
		create index bookings_room_ok on bookings (room_id) where is_ok('ok'::mood);
		create trigger bookings_touch before update on bookings for each row when (old.email <> new.email) execute function touch();
		alter table bookings enable row level security;
		create policy bookings_p on bookings using (email = current_user::citext);
		create view booking_names as select b.id, unaccent(r.name::text) as room, is_ok(r.mood) as ok from bookings b join rooms r on r.id = b.room_id;
		create text search dictionary my_unaccent (template = unaccent, rules = 'unaccent');
		create table public.plain (id int primary key, room_id int references rooms (id));`, name)); err != nil {
		t.Fatal(err)
	}
	pulls := map[string]string{}
	var files []pull.File
	for session, params := range map[string]map[string]string{
		"database setting":         nil,
		"DSN search_path public":   {"search_path": "public"},
		"DSN search_path app":      {"search_path": "app"},
		"DSN options, pg_catalog":  {"options": "-csearch_path=pg_catalog"},
		"DSN search_path reversed": {"search_path": "extensions, public, app"},
	} {
		res, err := pull.Build(introspectAll(t, sessionPool(t, name, params)), pull.Options{})
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, f := range res.Files {
			fmt.Fprintf(&b, "# %s\n%s", f.Name, f.Content)
		}
		pulls[session], files = b.String(), res.Files
	}
	want := pulls["database setting"]
	for session, got := range pulls {
		if got != want {
			t.Errorf("pull through %q differs from pull through the database's own search_path:\n%s\n--- want:\n%s", session, got, want)
		}
	}
	for _, s := range []string{
		"references: {table: app.rooms, columns: [id]}",
		"mood: {type: app.mood",
		"name: {type: extensions.citext}",
		"function is_ok(m app.mood):",
		"address: {type: app.email_addr}",
		"extensions.similarity(",
		"extensions.gin_trgm_ops",
		"extensions.unaccent(",
		"app.is_ok(",
		"template: extensions.unaccent",
	} {
		if !strings.Contains(want, s) {
			t.Errorf("pulled YAML lacks %q:\n%s", s, want)
		}
	}
	if t.Failed() {
		return
	}
	dir := t.TempDir()
	paths := []string{}
	for _, f := range files {
		p := filepath.Join(dir, f.Name)
		if err := os.WriteFile(p, f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	pulled, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}
	for session, params := range map[string]map[string]string{"database setting": nil, "DSN search_path public": {"search_path": "public"}} {
		_, plan := planLikeDiff(t, sessionPool(t, name, params), pulled)
		requireNoChanges(t, "pulled YAML against its source through "+session, plan)
	}
	dst := freshDatabase(t, admin, name+"_copy")
	_, plan := planLikeDiff(t, dst, pulled)
	if stmt, err := applyAsMigrations(ctx, dst, plan); err != nil {
		t.Fatalf("rebuilding from pulled YAML: %q: %v\n%s", stmt, err, want)
	}
	_, again := planLikeDiff(t, sessionPool(t, name+"_copy", nil), pulled)
	requireNoChanges(t, "rebuilt database", again)
	if a := pgDump(t, name); a != "" {
		if d := dumpDifferences(a, pgDump(t, name+"_copy")); len(d) > 0 {
			t.Errorf("pg_dump of the rebuilt database differs from the original:\n%s", strings.Join(d, "\n"))
		}
	}
}
