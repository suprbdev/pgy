package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationIndexOptionsIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"index_options": `
schema SCH:
  table t:
    columns:
      id: {type: int}
      email: {type: text}
      name: {type: text}
      status: {type: text}
      tags: {type: "text[]"}
    indexes:
      t_cover:
        columns: [lower(email) desc nulls last, id]
        unique: true
        include: [name]
        nullsNotDistinct: true
        with: {fillfactor: 70}
        where: "status <> 'b'"
      t_tags:
        columns: [tags]
        using: gin
        with: {fastupdate: false}
      t_status_first:
        columns: [status nulls first]
`,
	})
}

func TestIntegrationIndexChangedRecreatedUnsafe(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	desired := func(ix *schema.Index) *schema.Database {
		return &schema.Database{Tables: map[string]*schema.Table{
			sch + ".t": {Name: "t", Columns: map[string]*schema.Column{
				"a": {Type: "int"}, "b": {Type: "int"}, "c": {Type: "text"},
			}, Indexes: []*schema.Index{ix}},
		}}
	}
	plan := func(d *schema.Database, unsafe bool) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, d, unsafe)
	}
	steps := []*schema.Index{
		{Name: "t_ab", Columns: []string{"a"}},
		{Name: "t_ab", Columns: []string{"a", "b"}},
		{Name: "t_ab", Columns: []string{"a", "b desc"}},
		{Name: "t_ab", Columns: []string{"a", "b desc"}, Unique: true},
		{Name: "t_ab", Columns: []string{"a", "b desc"}, Unique: true, Include: []string{"c"}},
		{Name: "t_ab", Columns: []string{"a", "b desc"}, Unique: true, Include: []string{"c"}, Where: "a > 0"},
		{Name: "t_ab", Columns: []string{"a", "b desc"}, Unique: true, Include: []string{"c"}, Where: "a > 0", With: map[string]string{"fillfactor": "80"}},
		{Name: "t_ab", Columns: []string{"a", "b desc"}, Using: "hash"},
	}
	steps[7].Columns = []string{"a"}
	for i, ix := range steps {
		d := desired(ix)
		if i > 0 {
			if sql := diff.Render(plan(d, false)); sql != "" {
				t.Fatalf("step %d: index changes are gated behind --unsafe, got:\n%s", i, sql)
			}
			p := plan(d, true)
			if len(p.Alters) != 2 {
				t.Fatalf("step %d: expected drop + create, got creates=%v alters=%v", i, p.Creates, p.Alters)
			}
		}
		applyPlan(t, pool, plan(d, true))
		if sql := diff.Render(plan(d, true)); sql != "" {
			t.Fatalf("step %d: expected no changes after apply, got:\n%s", i, sql)
		}
	}
	var def string
	if err := pool.QueryRow(ctx, fmt.Sprintf(`select pg_get_indexdef('%s.t_ab'::regclass)`, sch)).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("CREATE INDEX t_ab ON %s.t USING hash (a)", sch); def != want {
		t.Errorf("final index %q, want %q", def, want)
	}
}
