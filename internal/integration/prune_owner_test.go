package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
)

// TestIntegrationPruneLeavesObjectsOfOtherOwners prunes as a role that is
// not a superuser in a database where a platform role owns an event trigger,
// a publication, an extension and a table: the plan leaves them alone with
// a warning and applies, and the event trigger whose function is in a schema
// the YAML doesn't use is not pruned at all.
func TestIntegrationPruneLeavesObjectsOfOtherOwners(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	const app, platform = "pgytest_prune_app", "pgytest_prune_platform"
	dropRoles := func() {
		for _, role := range []string{app, platform} {
			admin.Exec(context.Background(), `drop role if exists `+role) //nolint
		}
	}
	dropRoles()
	t.Cleanup(dropRoles)
	for _, stmt := range []string{`create role ` + platform + ` superuser nologin`, `create role ` + app + ` login password 'pgy'`} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db := freshDatabase(t, admin, "pgytest_prune_owner")
	if _, err := admin.Exec(ctx, `alter database pgytest_prune_owner owner to `+app); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		set role `+platform+`;
		create schema platform_fns;
		create function platform_fns.ddl_watch() returns event_trigger language plpgsql as $$ begin null; end $$;
		create event trigger pgytest_platform_watch on ddl_command_end execute function platform_fns.ddl_watch();
		create function public.platform_audit() returns event_trigger language plpgsql as $$ begin null; end $$;
		create event trigger pgytest_platform_audit on sql_drop execute function public.platform_audit();
		create publication pgytest_platform_pub;
		create extension hstore schema public;
		create table public.platform_t (id int);
		reset role;
		set role `+app+`;
		create extension citext schema public;
		create table public.t (id int primary key, e citext);
		create table public.gone (id int);
		reset role;`); err != nil {
		t.Fatal(err)
	}
	cfg := db.Config().Copy()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = app, "pgy"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	const y = `
extensions:
  - {name: citext, schema: public}
eventTriggers: {}
publications: {}
schema public:
  table t:
    columns:
      id: {type: integer}
      e: {type: citext, nullable: true}
    primaryKey: [id]
`
	plan := planExtensionsLive(t, pool, y, diff.Options{Unsafe: true, Prune: true})
	if got := strings.TrimSpace(diff.Render(plan)); got != `drop table "public"."gone";` {
		t.Errorf("only the app's own table is pruned, got:\n%s", got)
	}
	notOwned := []string{}
	for _, w := range plan.Warnings {
		if w.Kind == "not_owner" {
			notOwned = append(notOwned, w.Message)
		}
	}
	for i, want := range []string{"event trigger pgytest_platform_audit", "publication pgytest_platform_pub", "table public.platform_t", "function public.platform_audit()", "extension hstore"} {
		if i >= len(notOwned) || !strings.HasPrefix(notOwned[i], want+" is owned by "+platform+", and "+app+", the connecting role") {
			t.Errorf("want a not_owner warning for %s, got %v", want, notOwned)
		}
	}
	if len(notOwned) != 5 {
		t.Errorf("want 5 not_owner warnings, got %v", notOwned)
	}
	applyPlan(t, pool, plan)
	if sql := diff.Render(planExtensionsLive(t, pool, y, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("a second diff must be empty, got:\n%s", sql)
	}
	if got := diff.Render(planExtensionsLive(t, db, y, diff.Options{Unsafe: true, Prune: true})); !strings.Contains(got, `drop event trigger "pgytest_platform_audit";`) || strings.Contains(got, "pgytest_platform_watch") {
		t.Errorf("a superuser prunes the platform's objects, but not the event trigger whose function is in a schema the YAML doesn't use:\n%s", got)
	}
}

// TestIntegrationPruneKeepsRoutineAnEventTriggerUses keeps a function an
// unmanaged event trigger executes, which DROP FUNCTION would refuse to
// drop, and drops both once the eventTriggers block manages the trigger.
func TestIntegrationPruneKeepsRoutineAnEventTriggerUses(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_prune_et_function")
	if _, err := db.Exec(ctx, `
		create function public.ddl_watch() returns event_trigger language plpgsql as $$ begin null; end $$;
		create event trigger pgytest_ddl_watch on ddl_command_end execute function public.ddl_watch();
		create table public.t (id int primary key);
		create table public.audit (id int, at timestamptz default now());
		create function public.stamp() returns timestamptz language sql as 'select now()';
		alter table public.audit alter column at set default public.stamp();`); err != nil {
		t.Fatal(err)
	}
	const y = `
schema public:
  table t:
    columns:
      id: {type: integer}
    primaryKey: [id]
`
	prune := diff.Options{Unsafe: true, Prune: true}
	plan := planExtensionsLive(t, db, y, prune)
	if got := strings.TrimSpace(diff.Render(plan)); got != `drop table "public"."audit";`+"\n"+`drop function "public"."stamp"();` {
		t.Errorf("the function the event trigger uses is kept, the one only a pruned table uses dropped after it, got:\n%s", got)
	}
	if msgs := warningMessages(plan); !strings.Contains(msgs, "function public.ddl_watch() is not declared but still in use (event trigger pgytest_ddl_watch), so --prune keeps it") {
		t.Errorf("want a warning naming the event trigger, got:\n%s", msgs)
	}
	applyPlan(t, db, plan)

	plan = planExtensionsLive(t, db, "eventTriggers: {}\n"+y, prune)
	if got := strings.TrimSpace(diff.Render(plan)); got != `drop event trigger "pgytest_ddl_watch";`+"\n"+`drop function "public"."ddl_watch"();` {
		t.Errorf("with the event trigger pruned, its function goes too, got:\n%s", got)
	}
	applyPlan(t, db, plan)
}
