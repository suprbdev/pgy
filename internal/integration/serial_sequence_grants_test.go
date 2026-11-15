package integration

import (
	"strings"
	"testing"
)

func TestIntegrationSerialSequenceGrants(t *testing.T) {
	pool := connect(t)
	role := freshRole(t, pool, "seqgrant")
	assertYAMLIdempotent(t, map[string]string{"serial_grants": strings.ReplaceAll(`
schema SCH:
  table audit:
    columns:
      id: {type: bigserial, primaryKey: true}
      note: {type: text, nullable: true}
    grants: {ROLE: [insert]}
  sequence audit_id_seq:
    grants: {ROLE: [usage]}
    comment: audit ids
    dependsOn: [table SCH.audit]
`, "ROLE", role)})
}
