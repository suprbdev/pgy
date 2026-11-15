package integration

import "testing"

func TestIntegrationIndexCommentsOnConcurrentAndRebuiltIndexes(t *testing.T) {
	pool := connect(t)
	table := `
schema SCH:
  table t:
    columns: {id: {type: int, primaryKey: true}, a: {type: int, nullable: true}}
`
	cases := map[string][]string{
		"concurrent": {table, table + "    indexes: {t_a: {columns: [a], concurrently: true, comment: by a}}\n"},
		"rebuilt": {
			table + "    indexes: {t_a: {columns: [a], comment: by a}}\n",
			table + "    indexes: {t_a: {columns: [a desc], comment: by a}}\n",
			table + "    indexes: {t_a: {columns: [a], concurrently: true, comment: by a}}\n",
		},
		"matview": {`
schema SCH:
  materialized view mv:
    query: select 1 as a
    indexes: {mv_a: {columns: [a], comment: by a}}
`},
	}
	for name, steps := range cases {
		t.Run(name, func(t *testing.T) {
			sch := freshSchema(t, pool)
			for _, y := range steps {
				convergeYAML(t, pool, sch, y)
			}
		})
	}
}
