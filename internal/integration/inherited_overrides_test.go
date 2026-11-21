package integration

import (
	"context"
	"testing"
)

func TestIntegrationInheritedGeneratedOverrides(t *testing.T) {
	requireServerVersion(t, 170000)
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	y := `
schema SCH:
  table m:
    columns:
      id: {type: int}
      k: {type: text}
      s: {type: int, nullable: true, generated: id * 2}
    partitionBy: {list: [k]}
  table m_a:
    partitionOf: SCH.m
    forValues: {in: [a]}
    columns:
      s: {generated: id * 20}
  table base:
    columns:
      id: {type: int, nullable: true}
      g: {type: int, nullable: true, generated: id + 1}
  table kid:
    inherits: [SCH.base]
    columns:
      g: {generated: id + 100}
`
	convergeYAML(t, pool, sch, y)
	expr := func(table, col string) string {
		var e string
		if err := pool.QueryRow(ctx, `select pg_get_expr(d.adbin, d.adrelid) from pg_attribute a join pg_attrdef d on d.adrelid = a.attrelid and d.adnum = a.attnum where a.attrelid = $1::regclass and a.attname = $2`, sch+"."+table, col).Scan(&e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	if expr("m_a", "s") != "(id * 20)" || expr("kid", "g") != "(id + 100)" {
		t.Errorf("created overrides: m_a.s %s, kid.g %s", expr("m_a", "s"), expr("kid", "g"))
	}
	if _, err := pool.Exec(ctx, `alter table only `+sch+`.m_a alter column s set expression as (id * 2); alter table only `+sch+`.kid alter column g set expression as (id + 1)`); err != nil {
		t.Fatal(err)
	}
	if sql := convergeYAML(t, pool, sch, y); !contains(sql, `alter table only "`+sch+`"."m_a" alter column "s" set expression as (id * 20);`) || !contains(sql, `alter table only "`+sch+`"."kid" alter column "g" set expression as (id + 100);`) {
		t.Errorf("want the overrides restored, got:\n%s", sql)
	}
	changed := `
schema SCH:
  table m:
    columns:
      id: {type: int}
      k: {type: text}
      s: {type: int, nullable: true, generated: id * 3}
    partitionBy: {list: [k]}
  table m_a:
    partitionOf: SCH.m
    forValues: {in: [a]}
    columns:
      s: {generated: id * 20}
`
	convergeYAML(t, pool, sch, changed)
	if expr("m", "s") != "(id * 3)" || expr("m_a", "s") != "(id * 20)" {
		t.Errorf("after the parent's change: m.s %s, m_a.s %s", expr("m", "s"), expr("m_a", "s"))
	}
}

func TestIntegrationNotNullNoInheritAltered(t *testing.T) {
	requireServerVersion(t, 180000)
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	y := `
schema SCH:
  table p:
    columns:
      a: {type: int, notNullNoInherit: true}
  table kid:
    inherits: [SCH.p]
    columns: {}
`
	convergeYAML(t, pool, sch, y)
	var kidNotNull bool
	if err := pool.QueryRow(ctx, `select attnotnull from pg_attribute where attrelid = $1::regclass and attname = 'a'`, sch+".kid").Scan(&kidNotNull); err != nil {
		t.Fatal(err)
	}
	if kidNotNull {
		t.Error("a child created after a NO INHERIT NOT NULL must not get it")
	}
	inheritable := `
schema SCH:
  table p:
    columns:
      a: {type: int}
  table kid:
    inherits: [SCH.p]
    columns: {}
`
	if sql := convergeYAML(t, pool, sch, inheritable); !contains(sql, `alter table "`+sch+`"."p" alter constraint "p_a_not_null" inherit;`) {
		t.Errorf("want the constraint made inheritable in place, got:\n%s", sql)
	}
	if sql := convergeYAML(t, pool, sch, y); !contains(sql, `alter table "`+sch+`"."p" alter constraint "p_a_not_null" no inherit;`) {
		t.Errorf("want the constraint made NO INHERIT in place, got:\n%s", sql)
	}
}
