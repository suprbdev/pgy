package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationViewColumnGrants(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	reader := freshRole(t, pool, "reader")
	y := strings.ReplaceAll(`
schema SCH:
  table t:
    columns: {id: {type: int, primaryKey: true}, a: {type: text, nullable: true}, b: {type: int, nullable: true}}
  view v:
    query: select id, a, b from SCH.t
    columnGrants:
      a: {READER: [select]}
      b: {READER: [update with grant option]}
  materialized view mv:
    query: select id, b from SCH.t
    columnGrants:
      b: {READER: [select]}
`, "READER", reader)
	convergeYAML(t, pool, sch, y)
	privileges := func() string {
		var out string
		if err := pool.QueryRow(ctx, `
			select string_agg(format('%s.%s %s', c.relname, a.attname, a.attacl), ' ' order by c.relname, a.attname)
			from pg_attribute a join pg_class c on c.oid = a.attrelid
			where c.relnamespace = $1::regnamespace and c.relkind in ('v', 'm') and a.attacl is not null`, sch).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := privileges(); !strings.Contains(got, "mv.b {"+reader+"=r/") || !strings.Contains(got, "v.a {"+reader+"=r/") || !strings.Contains(got, "v.b {"+reader+"=w*/") {
		t.Errorf("declared view column grants: %s", got)
	}

	if _, err := pool.Exec(ctx, strings.NewReplacer("SCH", sch, "READER", reader).Replace(`
		grant insert (a) on SCH.v to "READER";
		grant select (id) on SCH.v to "READER";`)); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{writeYAML(t, strings.ReplaceAll(y, "SCH", sch))})
	if err != nil {
		t.Fatal(err)
	}
	sql := diff.Render(diff.Plan(introspect(t, pool, d), d, false))
	if !strings.Contains(sql, `revoke insert ("a") on table "`+sch+`"."v" from "`+reader+`";`) || strings.Contains(sql, `("id")`) {
		t.Errorf("drift on a declared column is revoked, an undeclared column is left alone:\n%s", sql)
	}

	if _, err := pool.Exec(ctx, strings.NewReplacer("SCH", sch, "READER", reader).Replace(`revoke insert (a) on SCH.v from "READER";`)); err != nil {
		t.Fatal(err)
	}
	recreated, err := schema.LoadAndMerge([]string{writeYAML(t, strings.ReplaceAll(strings.Replace(y, "select id, a, b from SCH.t", "select id, b, a from SCH.t", 1), "SCH", sch))})
	if err != nil {
		t.Fatal(err)
	}
	probed := func() *diff.Live {
		live := introspect(t, pool, recreated)
		if err := diff.ProbeViews(ctx, pool, live, recreated); err != nil {
			t.Fatal(err)
		}
		return live
	}
	before := privileges()
	p := diff.Plan(probed(), recreated, true)
	if !strings.Contains(diff.Render(p), `drop view if exists "`+sch+`"."v";`) {
		t.Fatalf("the view is recreated for its columns:\n%s", diff.Render(p))
	}
	applyPlan(t, pool, p)
	if again := diff.Render(diff.Plan(probed(), recreated, true)); again != "" {
		t.Errorf("plan did not converge:\n%s", again)
	}
	if got := privileges(); got != before {
		t.Errorf("a recreated view keeps its declared and unmanaged column grants:\n got %s\nwant %s\nplan:\n%s", got, before, diff.Render(p))
	}
}
