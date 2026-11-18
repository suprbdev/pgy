package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
)

// TestIntegrationEmptySchemaBlocksRebuilt pulls a database with schemas that
// hold nothing but, at most, a comment or grants, and rebuilds it from the
// pulled YAML into an empty database, with an extension the plan creates
// and without: every schema is created in one migration, and a second diff
// is empty. --prune leaves the undeclared table of a schema whose block is
// empty alone.
func TestIntegrationEmptySchemaBlocksRebuilt(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	reader := freshRole(t, admin, "reader")
	src := freshDatabase(t, admin, "pgytest_schema_blocks")
	if _, err := src.Exec(ctx, strings.NewReplacer("READER", reader).Replace(`
		create schema empty_one;
		create schema commented;
		comment on schema commented is 'nothing here yet';
		create schema granted;
		grant usage on schema granted to "READER";
		create schema app;
		create table app.t (id int primary key);`)); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(introspectAll(t, src), pull.Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	y := string(res.Files[0].Content)
	for _, want := range []string{"schema empty_one: {}\n", "schema commented:\n  comment: nothing here yet\n", "schema granted:\n  grants:"} {
		if !strings.Contains(y, want) {
			t.Fatalf("want %q in the pulled YAML:\n%s", want, y)
		}
	}
	for _, tc := range []struct{ name, yaml string }{
		{"pgytest_schema_blocks_rebuild", y},
		{"pgytest_schema_blocks_with_ext", "extensions:\n  - {name: citext, schema: public}\n" + y},
	} {
		dst := freshDatabase(t, admin, tc.name)
		plan := planExtensionsLive(t, dst, tc.yaml, diff.Options{})
		applyPlan(t, dst, plan)
		var n int
		if err := dst.QueryRow(ctx, `select count(*) from pg_namespace where nspname in ('empty_one', 'commented', 'granted', 'app')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 4 {
			t.Errorf("%s: want the 4 schemas after one migration, got %d:\n%s", tc.name, n, diff.Render(plan))
		}
		if sql := diff.Render(planExtensionsLive(t, dst, tc.yaml, diff.Options{Unsafe: true, Prune: true})); sql != "" {
			t.Errorf("%s: a second diff must be empty, got:\n%s", tc.name, sql)
		}
	}

	if _, err := src.Exec(ctx, `create table empty_one.later (id int)`); err != nil {
		t.Fatal(err)
	}
	if sql := diff.Render(planExtensionsLive(t, src, y, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("an empty schema block doesn't put the schema's objects in reach of --prune, got:\n%s", sql)
	}
}
