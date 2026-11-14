package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationViewInsteadOfTrigger(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	d := &schema.Database{
		Tables: map[string]*schema.Table{sch + ".items": {Name: "items", Columns: map[string]*schema.Column{"name": {Type: "text"}}}},
		Functions: map[string]*schema.Function{sch + ".items_insert()": {Schema: sch, Name: "items_insert", ArgsSig: "()", Returns: "trigger", Language: "plpgsql",
			Body: fmt.Sprintf("begin insert into %q.items values (upper(new.name)); return new; end;", sch)}},
		Views: map[string]*schema.View{sch + ".items_v": {Name: "items_v", Query: fmt.Sprintf("select name from %q.items", sch), DependsOn: []string{"table " + sch + ".items"},
			Triggers: []*schema.Trigger{{Name: "items_v_ins", Timing: "instead of", Events: []string{"insert"}, Procedure: sch + ".items_insert()", Comment: "writable"}}}},
	}
	plan := func() *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, d, true)
	}
	applyPlan(t, pool, plan())
	if sql := diff.Render(plan()); sql != "" {
		t.Fatalf("expected no changes after apply, got:\n%s", sql)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`insert into %q.items_v values ('abc')`, sch)); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := pool.QueryRow(ctx, fmt.Sprintf(`select name from %q.items`, sch)).Scan(&name); err != nil || name != "ABC" {
		t.Errorf("INSTEAD OF trigger should have inserted ABC, got %q (%v)", name, err)
	}
}
