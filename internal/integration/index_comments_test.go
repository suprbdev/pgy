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

func TestIntegrationIndexStatisticsTargetChanges(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	table := `
schema SCH:
  table t:
    columns: {id: {type: int, primaryKey: true}, b: {type: text, nullable: true}}
`
	for _, step := range []struct{ index, want string }{
		{"    indexes: {t_lb: {columns: [lower(b)], concurrently: true, statisticsTargets: {lower(b): 250}}}\n", "set statistics 250"},
		{"    indexes: {t_lb: {columns: [lower(b)], concurrently: true, statisticsTargets: {lower(b): -1}}}\n", "set statistics -1"},
		{"    indexes: {t_lb: {columns: [lower(b) desc], statisticsTargets: {lower(b) desc: 90}}}\n", "set statistics 90"},
	} {
		if sql := convergeYAML(t, pool, sch, table+step.index); !contains(sql, step.want) {
			t.Errorf("want %q, got:\n%s", step.want, sql)
		}
	}
}
