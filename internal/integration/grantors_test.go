package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
)

func TestIntegrationGrantorRevokesConverge(t *testing.T) {
	admin := connect(t)
	grantor := freshRole(t, admin, "adm")
	reader := freshRole(t, admin, "rd")
	db := freshDatabase(t, admin, "pgytest_grantors")
	ctx := context.Background()
	setup := strings.NewReplacer("ADM", grantor, "RD", reader).Replace(`
		create schema app;
		create table app.t (id int, c int);
		create function app.f() returns int language sql return 1;
		revoke execute on function app.f() from public;
		grant usage on schema app to "ADM" with grant option;
		grant select, update on app.t to "ADM" with grant option;
		grant execute on function app.f() to "ADM" with grant option;
		set role "ADM";
		grant usage on schema app to "RD" with grant option;
		grant select on app.t to "RD" with grant option;
		grant update (c) on app.t to "RD";
		grant execute on function app.f() to "RD";
		reset role;
		set role "RD";
		grant usage on schema app to public;
		reset role;
	`)
	if _, err := db.Exec(ctx, setup); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(ctx, "select current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}

	res, err := pull.Build(introspectAll(t, db), pull.Options{Schemas: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	var pulled strings.Builder
	for _, f := range res.Files {
		pulled.Write(f.Content)
	}
	for _, want := range []string{
		fmt.Sprintf("grantedBy: {%s: {%s: [select with grant option]}}", grantor, reader),
		fmt.Sprintf("grantedBy: {%s: {%s: [update]}}", grantor, reader),
		fmt.Sprintf("grantedBy: {%s: {%s: [usage with grant option]}, %s: {public: [usage]}}", grantor, reader, reader),
		fmt.Sprintf("grantedBy: {%s: {%s: [execute]}}", grantor, reader),
	} {
		if !strings.Contains(pulled.String(), want) {
			t.Errorf("pull writes %q:\n%s", want, pulled.String())
		}
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "granted by") {
			t.Errorf("pull warns about a grant its grantedBy blocks write: %s", w)
		}
	}

	y := strings.ReplaceAll(`
schema app:
  grants: {ADM: [usage with grant option]}
  table t:
    columns:
      id: {type: integer, nullable: true}
      c: {type: integer, nullable: true, grants: {}}
    grants: {ADM: [select with grant option, update with grant option]}
  function f():
    returns: integer
    language: sql
    body: return 1
    revokePublic: true
    grants: {ADM: [execute with grant option]}
`, "ADM", grantor)
	plan := planExtensionsLive(t, db, y, diff.Options{})
	for _, w := range plan.Warnings {
		if w.Kind != "public_revoke" {
			t.Errorf("unexpected warning: %s", w.Message)
		}
	}
	applyPlan(t, db, plan)
	sql := diff.Render(plan)
	if again := diff.Render(planExtensionsLive(t, db, y, diff.Options{})); again != "" {
		t.Fatalf("one migration must converge, the second diff has:\n%s\nafter:\n%s", again, sql)
	}
	named := fmt.Sprintf(`granted by %q`, grantor)
	if version < 190000 {
		named = fmt.Sprintf(`set role %q`, grantor)
	}
	if !strings.Contains(sql, named) {
		t.Errorf("the revokes name the grantor (%s):\n%s", named, sql)
	}
	var acls string
	if err := db.QueryRow(ctx, `
		select concat_ws(' | ',
			(select array_to_string(relacl, ',') from pg_class where oid = 'app.t'::regclass),
			(select array_to_string(attacl, ',') from pg_attribute where attrelid = 'app.t'::regclass and attname = 'c'),
			(select array_to_string(proacl, ',') from pg_proc where oid = 'app.f()'::regprocedure),
			(select array_to_string(nspacl, ',') from pg_namespace where nspname = 'app'))`).Scan(&acls); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(acls, reader) || strings.Contains(acls, "=U/") {
		t.Errorf("the grants of %s are revoked: %s", grantor, acls)
	}
}

func TestIntegrationGrantorCascadeKeepsGrants(t *testing.T) {
	admin := connect(t)
	grantor := freshRole(t, admin, "adm")
	reader := freshRole(t, admin, "rd")
	db := freshDatabase(t, admin, "pgytest_grantor_cascade")
	ctx := context.Background()
	roles := strings.NewReplacer("ADM", grantor, "RD", reader)
	if _, err := db.Exec(ctx, roles.Replace(`
		create schema app;
		create table app.t (id int);
		grant usage on schema app to "ADM";
		grant select on app.t to "ADM" with grant option;
		set role "ADM";
		grant select on app.t to "RD";
		reset role;
	`)); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := db.QueryRow(ctx, "select current_user").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	sql := convergeLive(t, db, roles.Replace(`
schema app:
  grants: {ADM: [usage]}
  table t:
    columns:
      id: {type: integer, nullable: true}
    grants: {ADM: [select], RD: [select]}
`), diff.Options{})
	if !strings.Contains(sql, "cascade;") {
		t.Errorf("the grant option is revoked with CASCADE:\n%s", sql)
	}
	var acl string
	if err := db.QueryRow(ctx, `select array_to_string(relacl, ',') from pg_class where oid = 'app.t'::regclass`).Scan(&acl); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{grantor + "=r/" + owner, reader + "=r/" + owner} {
		if !strings.Contains(acl, want) {
			t.Errorf("want %s in %s", want, acl)
		}
	}
}
