package integration

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

// asOwner returns a copy of live that doesn't know which roles' privileges
// the connecting role has, so a plan made from it keeps the statements that
// need an owner's privileges.
func asOwner(live *diff.Live) *diff.Live {
	c := *live
	c.OwnerRoles = nil
	return &c
}

func notOwnerMessages(p *diff.PlanDiff) []string {
	var out []string
	for _, w := range p.Warnings {
		if w.Kind == "not_owner" {
			out = append(out, w.Message)
		}
	}
	sort.Strings(out)
	return out
}

// TestIntegrationNotOwnerStatementsLeftOut plans, as a role that is not a
// superuser, changes to a table, a function and a view another role owns,
// a column type change a view of that role depends on, and settings of a
// database the role doesn't own: those statements are left out with one
// not_owner warning per object, while the role's own changes, a GRANT it
// holds the grant option for and a trigger it has the TRIGGER privilege for
// are planned, and the plan applies. A superuser plans everything.
func TestIntegrationNotOwnerStatementsLeftOut(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	const app, team, reader = "pgytest_no_app", "pgytest_no_team", "pgytest_no_reader"
	dropRoles := func() {
		for _, role := range []string{app, team, reader} {
			admin.Exec(context.Background(), `drop role if exists `+role) //nolint
		}
	}
	dropRoles()
	t.Cleanup(dropRoles)
	for _, stmt := range []string{`create role ` + app + ` login password 'pgy'`, `create role ` + team, `create role ` + reader} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db := freshDatabase(t, admin, "pgytest_not_owner")
	if _, err := db.Exec(ctx, strings.NewReplacer("APP", app, "TEAM", team).Replace(`
		create schema app authorization APP;
		create table app.mine (id int primary key, ttl interval default '1 hour');
		create table app.theirs (id int primary key, ttl interval default '1 hour');
		create table app.shared (id int primary key);
		create table app.base (id int primary key, b int);
		create view app.team_view as select id, b from app.base;
		create function app.f() returns int language sql as 'select 1';
		create function app.touch() returns trigger language plpgsql as $$ begin return new; end $$;
		alter table app.mine owner to APP;
		alter table app.base owner to APP;
		alter function app.touch() owner to APP;
		alter table app.theirs owner to TEAM;
		alter table app.shared owner to TEAM;
		alter view app.team_view owner to TEAM;
		alter function app.f() owner to TEAM;
		grant select on app.shared to APP with grant option;
		grant trigger on app.shared to APP;`)); err != nil {
		t.Fatal(err)
	}
	cfg := db.Config().Copy()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = app, "pgy"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	y := strings.NewReplacer("APP", app, "READER", reader).Replace(`
database:
  set: {work_mem: 8MB}
schema app:
  table mine:
    columns:
      id: {type: integer}
      ttl: {type: interval, nullable: true, default: "'02:00:00'::interval"}
    primaryKey: [id]
  table theirs:
    columns:
      id: {type: integer}
      ttl: {type: interval, nullable: true, default: "'02:00:00'::interval"}
      note: {type: text, nullable: true}
    primaryKey: [id]
    comment: changed by the app
  table shared:
    columns:
      id: {type: integer}
    primaryKey: [id]
    grants: {APP: ["select*", trigger], READER: [select]}
    triggers:
      shared_touch: {timing: before, events: [update], level: row, procedure: app.touch()}
  table base:
    columns:
      id: {type: integer}
      b: {type: bigint, nullable: true}
    primaryKey: [id]
  view team_view:
    query: select id, b from app.base
  function f():
    returns: integer
    language: sql
    body: select 2
  function touch():
    returns: trigger
    language: plpgsql
    body: begin return new; end
`)
	opts := diff.Options{Unsafe: true}
	plan := planExtensionsLive(t, pool, y, opts)
	sql := diff.Render(plan)
	for _, want := range []string{
		`alter table "app"."mine" alter column "ttl" set default '02:00:00'::interval;`,
		`grant select on table "app"."shared" to "` + reader + `";`,
		`create trigger "shared_touch"`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	for _, unwanted := range []string{`"app"."theirs"`, `"app"."f"`, `"app"."team_view"`, `"app"."base"`, "alter database"} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("%s is left out, got:\n%s", unwanted, sql)
		}
	}
	got := notOwnerMessages(plan)
	for i, want := range []string{
		"function app.f() is owned by " + team,
		"table app.theirs is owned by " + team,
		"the database is owned by pgy",
		"view app.team_view is owned by " + team,
	} {
		if i >= len(got) || !strings.HasPrefix(got[i], want+", and "+app+", the connecting role") {
			t.Errorf("want a not_owner warning for %s, got %q", want, got)
		}
	}
	if len(got) != 4 || !strings.Contains(got[3], "table app.base") {
		t.Errorf("want 4 not_owner warnings, the view's naming the table whose type change needs it, got %q", got)
	}
	external := opts
	external.External = &schema.External{Roles: []string{team}}
	if p := planExtensionsLive(t, pool, y, external); diff.Render(p) != sql || len(notOwnerMessages(p)) != 4 || !strings.Contains(strings.Join(notOwnerMessages(p), "\n"), "table app.theirs is owned by "+team+", which the config's external.roles lists, and "+app) {
		t.Errorf("an external owner's objects get the same plan and warnings, saying the owner is external, got:\n%s%q", diff.Render(p), notOwnerMessages(p))
	}
	applyPlan(t, pool, plan)
	again := planExtensionsLive(t, pool, y, opts)
	if sql := diff.Render(again); sql != "" {
		t.Errorf("a second diff plans nothing, got:\n%s", sql)
	}
	if w := notOwnerMessages(again); len(w) != 4 {
		t.Errorf("and reports the same objects, got %q", w)
	}

	sql = diff.Render(planExtensionsLive(t, db, y, opts))
	for _, want := range []string{`"app"."theirs"`, `create or replace function "app"."f"()`, `drop view "app"."team_view";`, `alter table "app"."base" alter column "b" type bigint`, "alter database"} {
		if !strings.Contains(sql, want) {
			t.Errorf("a superuser plans %q too, got:\n%s", want, sql)
		}
	}
}
