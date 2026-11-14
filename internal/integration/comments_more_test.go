package integration

import (
	"context"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationCommentsOnConstraintsTriggersPolicies(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	desired := func(check, using string, events []string) *schema.Database {
		return &schema.Database{
			Functions: map[string]*schema.Function{sch + ".touch()": {Schema: sch, Name: "touch", ArgsSig: "()", Returns: "trigger", Language: "plpgsql", Body: "begin return new; end;"}},
			Tables: map[string]*schema.Table{sch + ".t": {
				Name:             "t",
				Columns:          map[string]*schema.Column{"a": {Type: "int"}},
				Constraints:      []*schema.Constraint{{Name: "t_a_check", Type: "check", Expression: check, Comment: "@name positive"}},
				Triggers:         []*schema.Trigger{{Name: "t_trg", Timing: "before", Events: events, Level: "row", Procedure: sch + ".touch()", Comment: "touch rows"}},
				RowLevelSecurity: schema.BoolPtr(true),
				Policies:         []*schema.Policy{{Name: "t_pol", Using: using, Comment: "visible rows"}},
			}},
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
	for i, d := range []*schema.Database{
		desired("a > 0", "true", []string{"insert"}),
		desired("a > 1", "a > 0", []string{"update"}),
	} {
		applyPlan(t, pool, plan(d))
		if sql := diff.Render(plan(d)); sql != "" {
			t.Fatalf("step %d: comments must survive recreation, got:\n%s", i, sql)
		}
	}
}
