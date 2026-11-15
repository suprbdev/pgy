package integration

import (
	"strings"
	"testing"
)

func TestIntegrationStatisticsTargetAndUnloggedSequenceChanges(t *testing.T) {
	pool := connect(t)
	base := `
schema SCH:
  table t:
    columns: {id: {type: int, primaryKey: true}, a: {type: int, nullable: true}, b: {type: int, nullable: true}}
    statistics: {t_ab: {columns: [a, b]%s}}
  sequence s: {start: 1%s}
`
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, strings.ReplaceAll(base, "%s", ""))
	for _, step := range []struct{ stats, seq, want string }{
		{", statisticsTarget: 250", ", unlogged: true", "set statistics 250"},
		{", statisticsTarget: -1", ", unlogged: false", "set statistics -1"},
	} {
		y := strings.Replace(strings.Replace(base, "%s", step.stats, 1), "%s", step.seq, 1)
		sql := convergeYAML(t, pool, sch, y)
		if !contains(sql, step.want) || !contains(sql, "alter sequence") || contains(sql, "drop statistics") {
			t.Errorf("want %q and an in-place sequence change, got:\n%s", step.want, sql)
		}
	}
}
