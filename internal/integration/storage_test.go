package integration

import (
	"context"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationTableStorageIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"storage": `
schema SCH:
  table t:
    unlogged: true
    with: {fillfactor: 70, autovacuum_enabled: false}
    columns:
      a: {type: text, collate: C, compression: pglz}
      b: {type: text, storage: external}
      c: {type: "varchar(10)", collate: POSIX, storage: main}
`,
	})
}

func TestIntegrationTableStorageAltered(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	table := func(unlogged bool, with map[string]string, a *schema.Column) *schema.Database {
		return &schema.Database{Tables: map[string]*schema.Table{
			sch + ".t": {Name: "t", Unlogged: &unlogged, With: with, Columns: map[string]*schema.Column{"a": a}},
		}}
	}
	plan := func(d *schema.Database) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, d, true)
	}
	steps := []*schema.Database{
		table(true, map[string]string{"fillfactor": "70"}, &schema.Column{Type: "text", Collate: "C", Compression: "pglz", Storage: "main"}),
		table(false, map[string]string{"fillfactor": "80", "autovacuum_enabled": "false"}, &schema.Column{Type: "text", Collate: "POSIX", Compression: "default", Storage: "external"}),
		table(true, map[string]string{}, &schema.Column{Type: "text", Storage: "extended"}),
	}
	for i, d := range steps {
		applyPlan(t, pool, plan(d))
		if sql := diff.Render(plan(d)); sql != "" {
			t.Fatalf("step %d: expected no changes after apply, got:\n%s", i, sql)
		}
	}
}
