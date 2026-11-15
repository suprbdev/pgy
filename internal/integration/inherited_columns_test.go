package integration

import (
	"context"
	"testing"
)

func TestIntegrationInheritedColumnSettings(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	y := `
schema SCH:
  table m:
    columns: {at: {type: date}, v: {type: int, nullable: true, default: 0}, note: {type: text, nullable: true}}
    partitionBy: {range: [at]}
  table m_2025:
    partitionOf: SCH.m
    forValues: {from: ["2025-01-01"], to: ["2026-01-01"]}
    columns:
      v: {default: 5, nullable: false}
      note: {statisticsTarget: 200, comment: partition note}
  table base:
    columns: {a: {type: int, nullable: true, default: 1}}
  table kid:
    inherits: [SCH.base]
    columns:
      a: {default: 7}
      c: {type: int, nullable: true}
`
	convergeYAML(t, pool, sch, y)
	if _, err := pool.Exec(ctx, `alter table `+sch+`.m_2025 alter column v set default 99; alter table `+sch+`.kid alter column a set default 1`); err != nil {
		t.Fatal(err)
	}
	if sql := convergeYAML(t, pool, sch, y); !contains(sql, "set default 5") || !contains(sql, "set default 7") {
		t.Errorf("want the overrides restored, got:\n%s", sql)
	}
	var partDefault, kidDefault string
	var notNull bool
	if err := pool.QueryRow(ctx, `select pg_get_expr(d.adbin, d.adrelid), a.attnotnull from pg_attribute a join pg_attrdef d on d.adrelid = a.attrelid and d.adnum = a.attnum where a.attrelid = $1::regclass and a.attname = 'v'`, sch+".m_2025").Scan(&partDefault, &notNull); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select pg_get_expr(d.adbin, d.adrelid) from pg_attribute a join pg_attrdef d on d.adrelid = a.attrelid and d.adnum = a.attnum where a.attrelid = $1::regclass and a.attname = 'a'`, sch+".kid").Scan(&kidDefault); err != nil {
		t.Fatal(err)
	}
	if partDefault != "5" || !notNull || kidDefault != "7" {
		t.Errorf("partition default %s not null %v, child default %s", partDefault, notNull, kidDefault)
	}
}
