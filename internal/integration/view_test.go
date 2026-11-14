package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationViewOptionsIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"view_options": `
schema SCH:
  table t:
    columns:
      id: {type: int}
      a: {type: int}
  view v:
    dependsOn: [table SCH.t]
    query: select id, a from SCH.t where a > 0
    securityInvoker: true
    securityBarrier: true
    checkOption: cascaded
  materialized view mv:
    dependsOn: [table SCH.t]
    query: select id, sum(a) as total from SCH.t group by id
    withData: false
    indexes:
      mv_id:
        columns: [id]
        unique: true
        comment: refresh concurrently key
      mv_total:
        columns: [total desc]
`,
	})
}

func TestIntegrationViewOptionsAlteredAndMatviewRefresh(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	desired := func(invoker bool, check string) *schema.Database {
		return &schema.Database{
			Tables: map[string]*schema.Table{sch + ".t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}}},
			Views: map[string]*schema.View{
				sch + ".v": {Name: "v", Query: fmt.Sprintf("select id from %s.t", sch), SecurityInvoker: &invoker, CheckOption: check, DependsOn: []string{"table " + sch + ".t"}},
				sch + ".mv": {Name: "mv", Materialized: true, Query: fmt.Sprintf("select id from %s.t", sch), DependsOn: []string{"table " + sch + ".t"},
					Indexes: []*schema.Index{{Name: "mv_id", Columns: []string{"id"}, Unique: true}}},
			},
		}
	}
	plan := func(d *schema.Database) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, d, false)
	}
	applyPlan(t, pool, plan(desired(true, "local")))
	if sql := diff.Render(plan(desired(true, "local"))); sql != "" {
		t.Fatalf("expected no changes after create, got:\n%s", sql)
	}
	p := plan(desired(false, "none"))
	if len(p.Creates) != 0 || len(p.Alters) != 1 {
		t.Fatalf("expected one ALTER VIEW ... RESET, got creates=%v alters=%v", p.Creates, p.Alters)
	}
	applyPlan(t, pool, p)
	if sql := diff.Render(plan(desired(false, "none"))); sql != "" {
		t.Fatalf("expected no changes after alter, got:\n%s", sql)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("refresh materialized view concurrently %q.mv", sch)); err != nil {
		t.Errorf("declared unique index should allow REFRESH CONCURRENTLY: %v", err)
	}
}
