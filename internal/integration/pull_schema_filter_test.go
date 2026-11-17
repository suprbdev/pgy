package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

// TestIntegrationPullSchemaKeepsWhatTheSchemaNeeds pulls one schema of a
// database whose other schema holds a platform's objects: only the
// extensions, publications and event triggers of the pulled schema are
// written, and a database rebuilt from the YAML matches it.
func TestIntegrationPullSchemaKeepsWhatTheSchemaNeeds(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	src := freshDatabase(t, admin, "pgytest_pull_filter")
	dst := freshDatabase(t, admin, "pgytest_pull_filter_copy")
	if _, err := src.Exec(ctx, `
		create schema platform;
		create schema app;
		create extension pgcrypto with schema platform;
		create extension hstore with schema platform;
		create extension citext with schema platform;
		create extension cube with schema platform;
		create extension earthdistance with schema platform;
		create extension pg_trgm with schema app;
		create table platform.users (id int primary key);
		create function platform.ddl_watch() returns event_trigger language plpgsql as $$ begin null; end $$;
		create event trigger platform_ddl_watch on ddl_command_end execute function platform.ddl_watch();
		create publication platform_realtime;
		create publication everything for all tables;
		create table app.notes (id int primary key, email platform.citext, loc platform.earth);
		create function app.ddl_log() returns event_trigger language plpgsql as $$ begin null; end $$;
		create event trigger app_ddl on sql_drop execute function app.ddl_log();
		create publication app_pub for table app.notes;
		create publication mixed for table app.notes, platform.users;
		alter database pgytest_pull_filter set app.settings.secret = 'not-for-yaml';`); err != nil {
		t.Fatal(err)
	}
	live := introspectAll(t, src)
	for _, opts := range []pull.Options{{Schemas: []string{"app"}}, {ExcludeSchemas: []string{"platform"}}} {
		opts.Single = true
		res, err := pull.Build(live, opts)
		if err != nil {
			t.Fatal(err)
		}
		y := string(res.Files[0].Content)
		for _, want := range []string{"{name: citext,", "{name: cube,", "{name: earthdistance,", "{name: pg_trgm, ifNotExists: true, schema: app}", "app_pub:", "app_ddl:"} {
			if !strings.Contains(y, want) {
				t.Errorf("%+v: want %q in:\n%s", opts, want, y)
			}
		}
		for _, unwanted := range []string{"pgcrypto", "hstore", "platform_realtime", "everything", "mixed", "platform_ddl_watch", "schema platform", "database:", "not-for-yaml"} {
			if strings.Contains(y, unwanted) {
				t.Errorf("%+v: %q must be left out:\n%s", opts, unwanted, y)
			}
		}
		warnings := strings.Join(res.Warnings, "\n")
		for _, want := range []string{"publication mixed also publishes platform.users", "would drop the others: everything, mixed, platform_realtime", "would drop the others: platform_ddl_watch"} {
			if !strings.Contains(warnings, want) {
				t.Errorf("%+v: want a warning with %q, got:\n%s", opts, want, warnings)
			}
		}
		pulled, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
		if err != nil {
			t.Fatalf("%v\n%s", err, y)
		}
		if sql := diff.Render(diff.PlanWith(live, pulled, diff.Options{Unsafe: true})); sql != "" {
			t.Errorf("%+v: the pulled YAML must diff clean against its source:\n%s", opts, sql)
		}
	}

	res, err := pull.Build(live, pull.Options{Schemas: []string{"app"}, DatabaseSettings: true, Single: true})
	if err != nil {
		t.Fatal(err)
	}
	y := string(res.Files[0].Content)
	if !strings.Contains(y, "app.settings.secret: not-for-yaml") {
		t.Errorf("DatabaseSettings must write the settings:\n%s", y)
	}

	res, err = pull.Build(live, pull.Options{Schemas: []string{"app"}, Single: true})
	if err != nil {
		t.Fatal(err)
	}
	y = string(res.Files[0].Content)
	pulled, err := schema.LoadAndMerge([]string{writeYAML(t, y)})
	if err != nil {
		t.Fatal(err)
	}
	if stmt, err := applyAsMigrations(ctx, dst, diff.PlanWith(introspectAll(t, dst), pulled, diff.Options{})); err != nil {
		t.Fatalf("rebuilding from the pulled YAML: %q: %v\n%s", stmt, err, y)
	}
	if sql := diff.Render(diff.PlanWith(introspectAll(t, dst), pulled, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("the rebuilt database differs from the pulled YAML:\n%s", sql)
	}
}
