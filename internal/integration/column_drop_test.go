package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationColumnDropKeptWhileDependentsStay(t *testing.T) {
	const after = `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
`
	for name, before := range map[string]string{
		"trigger": `
    triggers:
      t_a: {timing: before, events: ["update of a"], level: row, procedure: suppress_redundant_updates_trigger()}`,
		"view": `
  view v:
    query: select id, a from SCH.t`,
		"policy": `
    rowLevelSecurity: true
    policies:
      t_p: {using: "a is not null"}`,
	} {
		t.Run(name, func(t *testing.T) {
			pool := connect(t)
			ctx := context.Background()
			sch := freshSchema(t, pool)
			convergeYAML(t, pool, sch, after[:len(after)-1]+"\n      a: {type: text, nullable: true}"+before+"\n")
			d, err := schema.LoadAndMerge([]string{writeYAML(t, strings.ReplaceAll(after, "SCH", sch))})
			if err != nil {
				t.Fatal(err)
			}
			p := diff.PlanWith(introspect(t, pool, d), d, diff.Options{Unsafe: true})
			msg := warningMessages(p)
			if strings.Contains(diff.Render(p), "drop column") || !strings.Contains(msg, "dropping column "+sch+".t.a: ") || !strings.Contains(msg, "so the column is kept") {
				t.Fatalf("want the drop left out with a warning, got:\n%s\n%s", msg, diff.Render(p))
			}
			applyPlan(t, pool, p)

			p = diff.PlanWith(introspect(t, pool, d), d, diff.Options{Unsafe: true, Prune: true})
			if name == "policy" {
				if strings.Contains(diff.Render(p), "drop column") || !strings.Contains(warningMessages(p), "a policies block drops the policies it doesn't list") {
					t.Fatalf("--prune leaves policies alone without a policies block, got:\n%s\n%s", warningMessages(p), diff.Render(p))
				}
				d.Tables[sch+".t"].Policies = []*schema.Policy{}
				p = diff.PlanWith(introspect(t, pool, d), d, diff.Options{Unsafe: true})
			}
			applyPlan(t, pool, p)
			var n int
			if err := pool.QueryRow(ctx, `select count(*) from pg_attribute where attrelid = $1::regclass and attname = 'a' and not attisdropped`, sch+".t").Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("column a is still there after the plan that drops its dependent:\n%s", diff.Render(p))
			}
		})
	}
}

func TestIntegrationColumnDropWithGeneratedColumn(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      a: {type: text, nullable: true}
      a_len: {type: int, generated: length(a), nullable: true}
`)
	sql := convergeYAML(t, pool, sch, `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
`)
	if i, j := strings.Index(sql, `drop column "a_len"`), strings.Index(sql, `drop column "a";`); i < 0 || j < i {
		t.Errorf("the generated column must go first, got:\n%s", sql)
	}
}
