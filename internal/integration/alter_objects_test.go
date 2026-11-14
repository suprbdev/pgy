package integration

import (
	"context"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationSequenceAndDomainAltered(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	db := func(sq *schema.Sequence, dm *schema.Domain) *schema.Database {
		return &schema.Database{
			Tables: map[string]*schema.Table{
				sch + ".orders": {Name: "orders", Columns: map[string]*schema.Column{
					"id":    {Type: "bigint"},
					"email": {Type: sch + ".email", Nullable: true},
				}, DependsOn: []string{"domain " + sch + ".email"}},
			},
			Sequences: map[string]*schema.Sequence{sch + ".order_no": sq},
			Domains:   map[string]*schema.Domain{sch + ".email": dm},
		}
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
		db(&schema.Sequence{Name: "order_no", As: "integer"},
			&schema.Domain{Name: "email", Type: "text", Check: "value like '%@%'"}),
		db(&schema.Sequence{Name: "order_no", As: "bigint", Increment: "10", MinValue: "1", MaxValue: "99999", Start: "5", Cache: "4", Cycle: schema.BoolPtr(true), OwnedBy: sch + ".orders.id"},
			&schema.Domain{Name: "email", Type: "text", Default: "'none@example.com'", NotNull: true, Check: "value ~ '@'"}),
		db(&schema.Sequence{Name: "order_no", Cycle: schema.BoolPtr(false), OwnedBy: "none"},
			&schema.Domain{Name: "email", Type: "text", ConstraintName: "email_has_at", Check: "position('@' in value) > 0"}),
	}
	for i, d := range steps {
		applyPlan(t, pool, plan(d))
		if sql := diff.Render(plan(d)); sql != "" {
			t.Fatalf("step %d: expected no changes after apply, got:\n%s", i, sql)
		}
	}
}
