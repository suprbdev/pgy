package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
)

// grantedByACLs returns the ACLs of app.t, its column c, app.f(), app.s,
// app.e and schema app, joined with " | ".
func grantedByACLs(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var acls string
	if err := db.QueryRow(context.Background(), `
		select concat_ws(' | ',
			(select array_to_string(relacl, ',') from pg_class where oid = 'app.t'::regclass),
			(select array_to_string(attacl, ',') from pg_attribute where attrelid = 'app.t'::regclass and attname = 'c'),
			(select array_to_string(proacl, ',') from pg_proc where oid = 'app.f()'::regprocedure),
			(select array_to_string(relacl, ',') from pg_class where oid = 'app.s'::regclass),
			(select array_to_string(typacl, ',') from pg_type where oid = 'app.e'::regtype),
			(select array_to_string(nspacl, ',') from pg_namespace where nspname = 'app'))`).Scan(&acls); err != nil {
		t.Fatal(err)
	}
	return acls
}

func TestIntegrationGrantedByConverges(t *testing.T) {
	admin := connect(t)
	adm, lead, rd := freshRole(t, admin, "adm"), freshRole(t, admin, "lead"), freshRole(t, admin, "rd")
	db := freshDatabase(t, admin, "pgytest_granted_by")
	ctx := context.Background()
	roles := strings.NewReplacer("ADM", adm, "LEAD", lead, "RD", rd)
	if _, err := db.Exec(ctx, `
		create schema app;
		create table app.t (id int, c int);
		create function app.f() returns int language sql return 1;
		create sequence app.s;
		create type app.e as enum ('x');
	`); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(ctx, "select current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	y := roles.Replace(`
schema app:
  grants: {ADM: [usage with grant option], LEAD: [usage]}
  grantedBy: {ADM: {RD: [usage]}}
  table t:
    columns:
      id: {type: integer, nullable: true}
      c: {type: integer, nullable: true, grantedBy: {ADM: {RD: [update]}}}
    grants: {ADM: [select with grant option, update with grant option]}
    grantedBy:
      ADM: {LEAD: [select with grant option]}
      LEAD: {RD: [select]}
  function f():
    returns: integer
    language: sql
    body: return 1
    revokePublic: true
    grants: {ADM: [execute with grant option]}
    grantedBy: {ADM: {RD: [execute]}}
  sequence s:
    grants: {ADM: [usage with grant option]}
    grantedBy: {ADM: {RD: [usage]}}
  type e:
    type: enum
    labels: [x]
    revokePublic: true
    grants: {ADM: [usage with grant option]}
    grantedBy: {ADM: {RD: [usage]}}
`)
	sql := convergeLive(t, db, y, diff.Options{})
	named := fmt.Sprintf(`granted by %q`, adm)
	if version < 190000 {
		named = fmt.Sprintf(`set role %q`, adm)
	}
	if !strings.Contains(sql, named) {
		t.Errorf("the grants run as their grantor (%s):\n%s", named, sql)
	}
	acls := grantedByACLs(t, db)
	for _, want := range []string{lead + "=r*/" + adm, rd + "=r/" + lead, rd + "=w/" + adm, rd + "=X/" + adm, rd + "=U/" + adm} {
		if !strings.Contains(acls, want) {
			t.Errorf("want %s in %s", want, acls)
		}
	}

	if _, err := db.Exec(ctx, roles.Replace(`set role "ADM"; grant update on app.t to "RD"; grant usage on sequence app.s to "LEAD"; reset role;`)); err != nil {
		t.Fatal(err)
	}
	sql = convergeLive(t, db, y, diff.Options{})
	for _, want := range []string{fmt.Sprintf("revoke update on table \"app\".\"t\" from %q", rd), fmt.Sprintf("revoke usage on sequence \"app\".\"s\" from %q", lead)} {
		if !strings.Contains(sql, want) {
			t.Errorf("a grant of a listed grantor the block doesn't declare is revoked, want %q in:\n%s", want, sql)
		}
	}
	if after := grantedByACLs(t, db); after != acls {
		t.Errorf("the revokes restore the declared grants:\n got %s\nwant %s", after, acls)
	}

	res, err := pull.Build(introspectAll(t, db), pull.Options{Schemas: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "granted by") {
			t.Errorf("pull warns about a grant its grantedBy blocks write: %s", w)
		}
	}
	var pulled strings.Builder
	for _, f := range res.Files {
		pulled.Write(f.Content)
	}
	if again := diff.Render(planExtensionsLive(t, db, pulled.String(), diff.Options{})); again != "" {
		t.Errorf("the pulled YAML diffs clean:\n%s\n%s", again, pulled.String())
	}
}

func TestIntegrationGrantedByMember(t *testing.T) {
	admin := connect(t)
	app, adm, lead, rd := freshRole(t, admin, "app"), freshRole(t, admin, "adm"), freshRole(t, admin, "lead"), freshRole(t, admin, "rd")
	db := freshDatabase(t, admin, "pgytest_granted_by_member")
	ctx := context.Background()
	roles := strings.NewReplacer("APP", app, "ADM", adm, "LEAD", lead, "RD", rd)
	if _, err := admin.Exec(ctx, roles.Replace(`alter role "APP" login password 'pgy'; grant "ADM" to "APP";`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, roles.Replace(`
		create schema app authorization "APP";
		create table app.t (id int);
		alter table app.t owner to "APP";
		grant usage on schema app to "ADM", "LEAD";
	`)); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(ctx, "select current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	cfg := db.Config().Copy()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = app, "pgy"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	y := roles.Replace(`
schema app:
  table t:
    columns:
      id: {type: integer, nullable: true}
    grants: {ADM: [select with grant option], LEAD: [select with grant option]}
    grantedBy:
      ADM: {RD: [select]}
      LEAD: {RD: [insert]}
`)
	plan := planExtensionsLive(t, pool, y, diff.Options{})
	if len(plan.Warnings) != 1 || plan.Warnings[0].Kind != "not_owner" || !strings.Contains(plan.Warnings[0].Message, fmt.Sprintf("grantedBy declares %s's grant of insert to %s", lead, rd)) {
		t.Errorf("a grant as a role the connecting role is no member of is left out with a warning: %v", plan.Warnings)
	}
	applyPlan(t, pool, plan)
	sql := diff.Render(plan)
	named := fmt.Sprintf(`granted by %q`, adm)
	if version < 190000 {
		named = fmt.Sprintf(`set role %q`, adm)
	}
	if !strings.Contains(sql, named) || strings.Contains(sql, lead+`";`+"\n"+`grant insert`) {
		t.Errorf("a member of the grantor grants as it (%s):\n%s", named, sql)
	}
	var acl string
	if err := db.QueryRow(ctx, `select array_to_string(relacl, ',') from pg_class where oid = 'app.t'::regclass`).Scan(&acl); err != nil {
		t.Fatal(err)
	}
	if want := rd + "=r/" + adm; !strings.Contains(acl, want) {
		t.Errorf("want %s in %s", want, acl)
	}
	again := planExtensionsLive(t, pool, y, diff.Options{})
	if s := diff.Render(again); s != "" {
		t.Errorf("the second diff is empty, the left-out grant aside:\n%s", s)
	}
}
