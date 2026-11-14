package integration

import "testing"

func TestIntegrationStatisticsReplicaIdentityIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"statistics": `
schema SCH:
  table t:
    columns:
      a: {type: int}
      b: {type: int}
      c: {type: text}
    indexes:
      t_a: {columns: [a], unique: true}
    replicaIdentity: index t_a
    statistics:
      t_ab: {kinds: [ndistinct, dependencies], columns: [b, a], comment: correlated}
      t_expr: {columns: [lower(c), b]}
  table u:
    columns:
      x: {type: int, nullable: true}
    replicaIdentity: full
`,
	})
}
