package integration

import (
	"context"
	"testing"
)

func TestIntegrationInheritanceIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"inherits": `
schema SCH:
  table base:
    columns:
      id: {type: int}
      note: {type: text, nullable: true}
    constraints:
      base_id_check: {type: check, expression: id > 0}
  table tagged:
    columns:
      tag: {type: text, nullable: true}
  table child:
    inherits: [SCH.base, SCH.tagged]
    columns:
      extra: {type: int, nullable: true}
    indexes:
      child_id_idx: {columns: [id]}
`,
	})
}

func TestIntegrationInheritanceAltered(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, `
schema SCH:
  table base:
    columns:
      id: {type: int}
  table other:
    columns:
      id: {type: int}
`)
	if _, err := pool.Exec(context.Background(), `insert into "`+sch+`".other values (7)`); err != nil {
		t.Fatal(err)
	}
	sql := convergeYAML(t, pool, sch, `
schema SCH:
  table base:
    columns:
      id: {type: int}
  table other:
    inherits: [SCH.base]
    columns: {}
`)
	if !contains(sql, `alter table "`+sch+`"."other" inherit "`+sch+`"."base";`) || contains(sql, "drop column") {
		t.Errorf("expected INHERIT without touching the shared column, got:\n%s", sql)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from "`+sch+`".base`).Scan(&n); err != nil || n != 1 {
		t.Errorf("child rows must be visible through the parent: n=%d err=%v", n, err)
	}
	sql = convergeYAML(t, pool, sch, `
schema SCH:
  table base:
    columns:
      id: {type: int}
  table other:
    inherits: []
    columns:
      id: {type: int}
`)
	if !contains(sql, `alter table "`+sch+`"."other" no inherit "`+sch+`"."base";`) {
		t.Errorf("expected NO INHERIT under --unsafe, got:\n%s", sql)
	}
}
