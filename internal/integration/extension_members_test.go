package integration

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/validate"
)

// memberACLs returns the ACL of every function, relation and type that an
// extension owns in db, as sorted aclitems by object ("" for a default ACL).
func memberACLs(t *testing.T, db *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := db.Query(context.Background(), `
		select o.name, coalesce((select string_agg(a::text, ',' order by a::text) from unnest(o.acl) a), '')
		from (
			select 'function ' || p.oid::regprocedure::text as name, p.proacl as acl, 'pg_proc'::regclass as classid, p.oid from pg_proc p
			union all select 'relation ' || c.oid::regclass::text, c.relacl, 'pg_class'::regclass, c.oid from pg_class c
			union all select 'type ' || t.oid::regtype::text, t.typacl, 'pg_type'::regclass, t.oid from pg_type t
		) o
		join pg_depend d on d.classid = o.classid and d.objid = o.oid and d.deptype = 'e'`)
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

func TestIntegrationExtensionMembersManagedInOneRound(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	app, mon := freshRole(t, admin, "app"), freshRole(t, admin, "mon")
	db := freshDatabase(t, admin, "pgytest_ext_members")
	y := strings.NewReplacer("APP", app, "MON", mon).Replace(`
extensions:
  - {name: citext, schema: public}
  - {name: hstore, schema: public}
  - {name: pgcrypto, schema: public}
  - {name: pg_buffercache, schema: public}
  - {name: pg_stat_statements, schema: public}
  - {name: unaccent, schema: public}
schema public:
  function citext_eq(citext, citext):
    leakproof: true
    comment: equality
  function each(hstore):
    rows: 50
    grants: {APP: [execute]}
  function gen_random_bytes(integer):
    revokePublic: true
    grants: {APP: [execute]}
  function pg_buffercache_pages():
    grants: {MON: [execute]}
  function unaccent(text):
    immutable: true
  view pg_stat_statements:
    grants: {MON: [select], public: []}
  view pg_buffercache:
    grants: {MON: [select]}
  type hstore:
    revokePublic: true
    grants: {APP: [usage]}
  table doc:
    columns:
      id: {type: int, primaryKey: true}
      title: {type: text}
    indexes:
      doc_unaccent: {columns: [unaccent(title)]}
`)
	opts := diff.Options{Unsafe: true, Prune: true}
	plan := planExtensionsLive(t, db, y, opts)
	for _, w := range plan.Warnings {
		if w.Kind != "lock" {
			t.Errorf("unexpected warning: %s", w.Message)
		}
	}
	applyPlan(t, db, plan)
	if sql := diff.Render(planExtensionsLive(t, db, y, opts)); sql != "" {
		t.Fatalf("one migration must converge, the second diff has:\n%s\nafter:\n%s", sql, diff.Render(plan))
	}

	var leakproof bool
	var rows float32
	var volatility string
	if err := db.QueryRow(ctx, `select (select proleakproof from pg_proc where proname = 'citext_eq'),
		(select prorows from pg_proc where proname = 'each' and pronargs = 1),
		(select provolatile::text from pg_proc where proname = 'unaccent' and pronargs = 1)`).Scan(&leakproof, &rows, &volatility); err != nil {
		t.Fatal(err)
	}
	if !leakproof || rows != 50 || volatility != "i" {
		t.Errorf("attributes of the extensions' functions: leakproof=%v rows=%v volatility=%s", leakproof, rows, volatility)
	}
	acls := memberACLs(t, db)
	for object, want := range map[string][]string{
		"function pg_buffercache_pages()":    {"pg_monitor=X/", mon + "=X/"},
		"relation pg_buffercache":            {"pg_monitor=r/", mon + "=r/"},
		"relation pg_stat_statements":        {mon + "=r/"},
		"type hstore":                        {app + "=U/"},
		"function gen_random_bytes(integer)": {app + "=X/"},
	} {
		for _, item := range want {
			if !strings.Contains(acls[object], item) {
				t.Errorf("%s: want %s in its ACL, got %q", object, item, acls[object])
			}
		}
	}
	for _, object := range []string{"relation pg_stat_statements", "type hstore", "function gen_random_bytes(integer)"} {
		if strings.HasPrefix(acls[object], "=") || strings.Contains(acls[object], ",=") {
			t.Errorf("%s: PUBLIC must have lost its privileges, got %q", object, acls[object])
		}
	}

	narrowed := strings.Replace(y, "grants: {"+mon+": [execute]}", "grants: {"+mon+": [execute], pg_monitor: []}", 1)
	sql := diff.Render(planExtensionsLive(t, db, narrowed, opts))
	if strings.TrimSpace(sql) != `revoke execute on function "public"."pg_buffercache_pages"() from "pg_monitor";` {
		t.Errorf("naming a grantee of the extension makes it exact, got:\n%s", sql)
	}
}

func TestIntegrationExtensionMemberAbsentIsReported(t *testing.T) {
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_ext_members_absent")
	if _, err := db.Exec(context.Background(), `create extension hstore schema public`); err != nil {
		t.Fatal(err)
	}
	plan := planExtensionsLive(t, db, `
extensions:
  - {name: hstore, schema: public}
schema public:
  function no_such_function(hstore):
    leakproof: true
    grants: {public: []}
  view no_such_view:
    grants: {public: [select]}
  type no_such_type:
    revokePublic: true
`, diff.Options{})
	if sql := diff.Render(plan); sql != "" {
		t.Errorf("nothing can be planned for objects that do not exist, got:\n%s", sql)
	}
	got := []string{}
	for _, w := range plan.Warnings {
		got = append(got, w.Message[:strings.Index(w.Message, " has no ")])
	}
	if want := []string{"function public.no_such_function(hstore)", "view public.no_such_view", "type public.no_such_type"}; !reflect.DeepEqual(got, want) {
		t.Errorf("warnings name %v, want %v", got, want)
	}
}

func TestIntegrationExtensionMemberPrivilegesPulled(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	app, mon := freshRole(t, admin, "app"), freshRole(t, admin, "mon")
	src := freshDatabase(t, admin, "pgytest_ext_member_acl")
	dst := freshDatabase(t, admin, "pgytest_ext_member_acl_copy")
	if _, err := src.Exec(ctx, strings.NewReplacer("APP", app, "MON", mon).Replace(`
		create extension pg_buffercache;
		create extension pg_stat_statements;
		create schema ext;
		create extension pgcrypto schema ext;
		create extension citext;
		create extension hstore;
		revoke execute on function pg_buffercache_pages() from pg_monitor;
		grant execute on function pg_buffercache_pages() to "MON" with grant option;
		grant execute on function pg_buffercache_pages() to public;
		grant insert on pg_stat_statements to public;
		grant select on pg_stat_statements_info to "MON";
		revoke select on pg_buffercache from pg_monitor;
		revoke execute on function ext.digest(text, text) from public;
		grant execute on function ext.gen_salt(text) to "APP";
		grant execute on function each(hstore) to "APP";
		revoke usage on type citext from public;
		grant usage on type hstore to "APP";
		create view slow as select queryid, calls from pg_stat_statements;
		create function sha(x text) returns bytea language sql immutable return ext.digest(x, 'sha256');`)); err != nil {
		t.Fatal(err)
	}
	y := pullAndRebuild(t, src, dst)
	if strings.Contains(y, "dependsOn") {
		t.Errorf("objects an extension owns need no dependsOn:\n%s", y)
	}
	for _, want := range []string{
		"function digest(text, text):\n    revokePublic: true\n",
		"grants: {pg_monitor: [], " + mon + ": [execute with grant option], public: [execute]}",
		"view pg_buffercache:\n    grants: {pg_monitor: []}\n",
		"view pg_stat_statements:\n    grants: {public: [insert, select]}\n",
		"type citext:\n    revokePublic: true\n",
		"type hstore:\n    grants: {" + app + ": [usage]}\n",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("missing %q in the pulled YAML:\n%s", want, y)
		}
	}
	for _, issue := range validate.Files([]string{writeYAML(t, y)}) {
		if issue.Severity == validate.Error {
			t.Errorf("pgy validate rejects the pulled YAML: %s", issue)
		}
	}
	want, got := memberACLs(t, src), memberACLs(t, dst)
	for object, acl := range want {
		if got[object] != acl {
			t.Errorf("%s: ACL %q was rebuilt as %q\n%s", object, acl, got[object], y)
		}
	}
}

func TestIntegrationInitialPrivilegesIntrospected(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_init_privs")
	if _, err := db.Exec(ctx, `
		create extension pg_buffercache;
		create extension pg_stat_statements;
		create extension hstore;
		create extension postgres_fdw;
		create function mine() returns integer language sql as 'select 1';`); err != nil {
		t.Fatal(err)
	}
	live, err := diff.Introspect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"proc:public.pg_buffercache_pages()", "rel:public.pg_buffercache", "type:public.hstore", "fdw:postgres_fdw"} {
		if !live.ExtensionMembers[key] {
			t.Errorf("ExtensionMembers lacks %s", key)
		}
	}
	if !live.BaseTypes["public.hstore"] || live.Types["public.hstore"] {
		t.Errorf("hstore is a base type: BaseTypes=%v Types=%v", live.BaseTypes["public.hstore"], live.Types["public.hstore"])
	}
	monitor := map[string]map[string]bool{"pg_monitor": {"execute": true}}
	for _, c := range []struct {
		key, publicDefault string
		want               map[string]map[string]bool
		ok                 bool
	}{
		{"proc:public.pg_buffercache_pages()", "execute", monitor, true},
		{"rel:public.pg_buffercache", "", map[string]map[string]bool{"pg_monitor": {"select": true}}, true},
		{"rel:public.pg_stat_statements", "", map[string]map[string]bool{"public": {"select": true}}, true},
		{"proc:public.akeys(hstore)", "execute", map[string]map[string]bool{"public": {"execute": true}}, true},
		{"type:public.hstore", "usage", map[string]map[string]bool{"public": {"usage": true}}, true},
		{"fdw:postgres_fdw", "", nil, true},
		{"proc:public.mine()", "execute", nil, false},
	} {
		got, ok := live.InitialGrants(c.key, c.publicDefault)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("InitialGrants(%q) = %v, %v; want %v, %v", c.key, got, ok, c.want, c.ok)
		}
	}
	res, err := pull.Build(introspectAll(t, db), pull.Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	if y := string(res.Files[0].Content); strings.Contains(y, "pg_buffercache_pages") || strings.Contains(y, "view pg_") || strings.Contains(y, "type hstore") {
		t.Errorf("untouched objects of extensions must not be pulled:\n%s", y)
	}
}
