package integration

import (
	"context"
	"testing"
)

func requireServerVersion(t *testing.T, min int) {
	t.Helper()
	var v int
	if err := connect(t).QueryRow(context.Background(), "select current_setting('server_version_num')::int").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v < min {
		t.Skipf("requires server_version_num >= %d, have %d", min, v)
	}
}

func TestIntegrationPostgres18Idempotent(t *testing.T) {
	requireServerVersion(t, 180000)
	assertYAMLIdempotent(t, map[string]string{
		"pg18": `
extensions: [btree_gist]
schema SCH:
  table q:
    columns:
      id: {type: int, primaryKey: true}
  table p:
    columns:
      id: {type: int}
      valid: {type: tstzrange}
    primaryKey: [id, valid without overlaps]
  table t:
    columns:
      id: {type: int}
      a: {type: int, nullable: true}
      v: {type: int, nullable: true, generated: a * 2, virtual: true}
      pid: {type: int, nullable: true}
      valid: {type: tstzrange, nullable: true}
    foreignKeys:
      t_pid_fkey:
        columns: [pid, period valid]
        references: {table: SCH.p, columns: [id, period valid]}
      t_pid_fkey2:
        columns: [pid]
        references: {table: SCH.q, columns: [id]}
        enforced: false
    constraints:
      t_a_check:
        type: check
        expression: a > 0
        enforced: false
      t_pid_valid_key:
        type: unique
        columns: [pid, valid without overlaps]
`,
	})
}
