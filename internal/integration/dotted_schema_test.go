package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationDottedSchemaLeftAlone(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_dotted_schema")
	if _, err := db.Exec(ctx, `
		create schema app;
		create table app.t (id int);
		create table app."x.y" (id int);
		create schema "app.sub";
		create table "app.sub".u (id int);
		create view "app.sub".w as select 1 as x;
		create function "app.sub".f() returns int language sql as $$select 1$$;
		create type "app.sub".e as enum ('a');`); err != nil {
		t.Fatal(err)
	}
	live := introspectAll(t, db)
	res, err := pull.Build(live, pull.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Warnings, "\n"); !strings.Contains(got, "schema app.sub was left out with its objects") {
		t.Errorf("want a warning for schema app.sub, got:\n%s", got)
	}
	paths := []string{}
	for _, f := range res.Files {
		if strings.Contains(string(f.Content), "sub") {
			t.Errorf("%s must not mention app.sub:\n%s", f.Name, f.Content)
		}
		paths = append(paths, writeYAML(t, string(f.Content)))
	}
	pulled, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}
	if sql := diff.Render(diff.PlanWith(live, pulled, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("the pulled YAML must diff clean, and leave app.sub alone, got:\n%s", sql)
	}
}
