package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationStatisticsInOtherSchema(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	stat := sch + "_s"
	y := `
schema SCH:
  table t:
    columns: {a: {type: int, nullable: true}, b: {type: int, nullable: true}}
    statistics:
      STAT: {schema: public, kinds: [dependencies], columns: [a, b], comment: elsewhere}
`
	sql := convergeYAML(t, pool, sch, strings.ReplaceAll(y, "STAT", stat))
	if !strings.Contains(sql, `create statistics "public"."`+stat+`" (dependencies) on "a", "b" from "`+sch+`"."t";`) {
		t.Errorf("statistics are created in the schema they declare:\n%s", sql)
	}
	var nsp string
	if err := pool.QueryRow(ctx, `select stxnamespace::regnamespace::text from pg_statistic_ext where stxname = $1`, stat).Scan(&nsp); err != nil || nsp != "public" {
		t.Fatalf("statistics schema = %q, %v", nsp, err)
	}

	d, err := schema.LoadAndMerge([]string{writeYAML(t, strings.ReplaceAll("\nschema SCH:\n  table t:\n    columns: {a: {type: int, nullable: true}, b: {type: int, nullable: true}}\n", "SCH", sch))})
	if err != nil {
		t.Fatal(err)
	}
	p := diff.PlanWith(introspect(t, pool, d), d, diff.Options{Unsafe: true, Prune: true})
	if want := `drop statistics "public"."` + stat + `";`; !strings.Contains(diff.Render(p), want) {
		t.Fatalf("prune drops the statistics under their own schema; want %s in:\n%s", want, diff.Render(p))
	}
	applyPlan(t, pool, p)
	var left int
	if err := pool.QueryRow(ctx, `select count(*) from pg_statistic_ext where stxname = $1`, stat).Scan(&left); err != nil || left != 0 {
		t.Errorf("statistics left after prune: %d, %v", left, err)
	}
}
