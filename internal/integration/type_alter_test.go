package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationRangeTypeIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"range": `
schema SCH:
  type floatrange:
    type: range
    subtype: float8
    subtypeDiff: float8mi
  table readings:
    dependsOn: [type SCH.floatrange]
    columns:
      span: {type: SCH.floatrange}
    constraints:
      readings_no_overlap:
        type: exclude
        expression: using gist (span with &&)
`,
	})
}

func TestIntegrationCompositeTypeAltered(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	typ := func(order []string, attrs map[string]string) *schema.Database {
		return &schema.Database{Tables: map[string]*schema.Table{}, Types: map[string]*schema.TypeDef{
			sch + ".addr": {Name: "addr", Kind: "composite", AttributeOrder: order, Attributes: attrs},
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
		typ([]string{"street", "zip"}, map[string]string{"street": "text", "zip": "int"}),
		typ([]string{"street", "zip", "city"}, map[string]string{"street": "text", "zip": "text", "city": "varchar(80)"}),
		typ([]string{"street", "city"}, map[string]string{"street": "text", "city": "varchar(80)"}),
	}
	for i, d := range steps {
		applyPlan(t, pool, plan(d))
		if sql := diff.Render(plan(d)); sql != "" {
			t.Fatalf("step %d: expected no changes after apply, got:\n%s", i, sql)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, fmt.Sprintf(`select count(*) from pg_attribute where attrelid = (select typrelid from pg_type where oid = '%q.addr'::regtype) and attnum > 0 and not attisdropped`, sch)).Scan(&n); err != nil || n != 2 {
		t.Errorf("expected 2 attributes, got %d (%v)", n, err)
	}
}
