package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationRules(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	d := &schema.Database{Tables: map[string]*schema.Table{
		sch + ".t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}, "deleted": {Type: "bool", Default: "false"}},
			Rules: []*schema.Rule{
				{Name: "t_soft_delete", Event: "delete", Instead: true, Where: "old.id > 0", Do: fmt.Sprintf("update %q.t set deleted = true where id = old.id", sch), Comment: "soft delete"},
				{Name: "t_log", Event: "insert", Do: fmt.Sprintf("(insert into %q.log values (new.id); notify t_changed)", sch)},
			}},
		sch + ".log": {Name: "log", Columns: map[string]*schema.Column{"id": {Type: "int"}}},
	}}
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
	if _, err := pool.Exec(ctx, fmt.Sprintf(`insert into %q.t (id) values (5); delete from %q.t where id = 5`, sch, sch)); err != nil {
		t.Fatal(err)
	}
	var deleted bool
	var logged int
	if err := pool.QueryRow(ctx, fmt.Sprintf(`select deleted, (select count(*) from %q.log) from %q.t where id = 5`, sch, sch)).Scan(&deleted, &logged); err != nil || !deleted || logged != 1 {
		t.Errorf("rules should soft-delete and log: deleted=%v logged=%d err=%v", deleted, logged, err)
	}
}
