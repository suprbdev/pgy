package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationPublications(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	name := sch + "_pub"
	t.Cleanup(func() { pool.Exec(context.Background(), fmt.Sprintf("drop publication if exists %q", name)) }) //nolint
	tables := map[string]*schema.Table{
		sch + ".a": {Name: "a", Columns: map[string]*schema.Column{"id": {Type: "int", PrimaryKey: true}, "x": {Type: "int"}, "y": {Type: "text"}}},
		sch + ".b": {Name: "b", Columns: map[string]*schema.Column{"id": {Type: "int", PrimaryKey: true}}},
	}
	desired := func(p *schema.Publication) *schema.Database {
		p.Name = name
		return &schema.Database{Tables: tables, Publications: map[string]*schema.Publication{name: p}}
	}
	plan := func(d *schema.Database) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, d, false)
	}
	steps := []*schema.Database{
		desired(&schema.Publication{Tables: []*schema.PublicationTable{{Table: sch + ".a", Columns: []string{"x", "id"}, Where: "x > 0"}, {Table: sch + ".b"}},
			Publish: []string{"insert", "update"}, ViaPartitionRoot: schema.BoolPtr(true), Comment: "cdc"}),
		desired(&schema.Publication{Tables: []*schema.PublicationTable{{Table: sch + ".b"}}, Publish: []string{"insert", "update", "delete"}, Comment: "cdc v2"}),
		desired(&schema.Publication{Schemas: []string{sch}}),
		desired(&schema.Publication{}),
	}
	for i, d := range steps {
		applyPlan(t, pool, plan(d))
		if sql := diff.Render(plan(d)); sql != "" {
			t.Fatalf("step %d: expected no changes after apply, got:\n%s", i, sql)
		}
	}
}
