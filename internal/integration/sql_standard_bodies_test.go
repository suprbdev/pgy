package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

const sqlStandardBodies = `
schema SCH:
  table t:
    columns: {id: {type: int, primaryKey: true}, a: {type: int, nullable: true}}
  function plus(x int):
    returns: int
    language: sql
    immutable: true
    body: begin atomic select x + 1; end
  function total():
    returns: bigint
    language: sql
    stable: true
    body: |
      begin atomic
        select count(*) from SCH.t where case when a > 0 then true else false end;
      end
    dependsOn: [table SCH.t]
  function twice(x int):
    returns: int
    language: sql
    body: return SCH.plus(x) + SCH.plus(x)
    dependsOn: [function SCH.plus(int)]
  procedure purge():
    language: sql
    body: begin atomic delete from SCH.t where a is null; end
    dependsOn: [table SCH.t]
`

func TestIntegrationSQLStandardBodies(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{"sql_standard": sqlStandardBodies})
}

func TestIntegrationSQLStandardBodyReplace(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	opts := diff.Options{Unsafe: true}
	convergeYAMLWith(t, pool, sch, sqlStandardBodies, opts)
	changed := strings.Replace(sqlStandardBodies, "body: begin atomic select x + 1; end", "body: begin atomic select x + 2; end", 1)
	if sql := convergeYAMLWith(t, pool, sch, changed, opts); sql != "" {
		t.Errorf("without replace a SQL-standard body is not compared:\n%s", sql)
	}
	replaced := strings.Replace(changed, "    immutable: true\n", "    immutable: true\n    replace: true\n", 1)
	d := loadYAML(t, sch, replaced)
	sql := diff.Render(diff.PlanWith(introspect(t, pool, d), d, opts))
	if !strings.Contains(sql, `create or replace function "`+sch+`"."plus"(x int) returns int language sql immutable begin atomic select x + 2; end;`) {
		t.Fatalf("replace: true must re-emit the body:\n%s", sql)
	}
	applyPlan(t, pool, diff.PlanWith(introspect(t, pool, d), d, opts))
	var v int
	if err := pool.QueryRow(context.Background(), `select `+sch+`.plus(1)`).Scan(&v); err != nil || v != 3 {
		t.Errorf("plus(1) = %d, %v", v, err)
	}
}
