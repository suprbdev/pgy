package integration

import "testing"

func TestIntegrationPartitionStorageParameters(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	base := `
schema SCH:
  table m:
    columns: {a: {type: int}}
    partitionBy: {list: [a]}
  table m1:
    partitionOf: SCH.m
    forValues: {in: [1]}
`
	for _, step := range []struct{ extra, want string }{
		{"    unlogged: true\n    with: {fillfactor: 90}\n", "with (fillfactor = 90)"},
		{"    unlogged: true\n    with: {fillfactor: 70, autovacuum_enabled: false}\n", "set (autovacuum_enabled = false, fillfactor = 70)"},
	} {
		if sql := convergeYAML(t, pool, sch, base+step.extra); !contains(sql, step.want) {
			t.Errorf("want %q, got:\n%s", step.want, sql)
		}
	}
}
