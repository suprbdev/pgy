package integration

import (
	"context"
	"testing"
)

func TestIntegrationValidateConstraintAfterNotValid(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	steps := []string{`
schema SCH:
  table p:
    columns: {id: {type: int}}
    primaryKey: [id]
  table t:
    columns: {id: {type: int}, p_id: {type: int, nullable: true}}
    constraints:
      t_id_check: {type: check, expression: id > 0, notValid: true}
    foreignKeys:
      t_p_fkey: {columns: [p_id], references: {table: SCH.p, columns: [id]}, notValid: true}
`, `
schema SCH:
  table p:
    columns: {id: {type: int}}
    primaryKey: [id]
  table t:
    columns: {id: {type: int}, p_id: {type: int, nullable: true}}
    constraints:
      t_id_check: {type: check, expression: id > 0}
    foreignKeys:
      t_p_fkey: {columns: [p_id], references: {table: SCH.p, columns: [id]}}
`}
	for i, y := range steps {
		if sql := convergeYAML(t, pool, sch, y); i == 1 && (!contains(sql, `validate constraint "t_id_check"`) || !contains(sql, `validate constraint "t_p_fkey"`)) {
			t.Errorf("removing notValid must validate the constraints, got:\n%s", sql)
		}
		if i == 0 {
			if _, err := pool.Exec(context.Background(), `insert into "`+sch+`".p values (1); insert into "`+sch+`".t values (1, 1)`); err != nil {
				t.Fatal(err)
			}
		}
	}
}
