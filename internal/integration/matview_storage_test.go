package integration

import (
	"strings"
	"testing"
)

func TestIntegrationMaterializedViewStorageParams(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"matview_with": `
schema SCH:
  table t:
    columns: {a: {type: int}, b: {type: text}}
  materialized view mv:
    query: select a, b from SCH.t
    with: {fillfactor: 70, autovacuum_enabled: false, toast.autovacuum_enabled: false}
    dependsOn: [table SCH.t]
`,
		"toast_without_toast_table": `
schema SCH:
  table ints:
    columns: {a: {type: int}}
    with: {toast.autovacuum_enabled: false}
  materialized view mv:
    query: select a from SCH.ints
    with: {toast.autovacuum_enabled: false}
    dependsOn: [table SCH.ints]
`,
	})
}

func TestIntegrationMaterializedViewStorageParamsChange(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	y := `
schema SCH:
  table t:
    columns: {a: {type: int}}
  materialized view mv:
    query: select a from SCH.t
    with: WITH
    dependsOn: [table SCH.t]
`
	convergeYAML(t, pool, sch, strings.Replace(y, "WITH", "{fillfactor: 70, autovacuum_enabled: false}", 1))
	sql := convergeYAML(t, pool, sch, strings.Replace(y, "WITH", "{fillfactor: 50}", 1))
	for _, want := range []string{"set (fillfactor = 50)", "reset (autovacuum_enabled)"} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}
