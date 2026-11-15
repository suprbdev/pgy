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

func TestIntegrationDefaultPrivilegesDoNotOverGrant(t *testing.T) {
	pool := connect(t)
	role := freshRole(t, pool, "defpriv")
	assertYAMLIdempotent(t, map[string]string{"defaults": strings.ReplaceAll(`
defaultPrivileges:
  - in: [SCH]
    tables: {ROLE: [select]}
    sequences: {ROLE: [usage]}
schema SCH:
  table older:
    columns: {id: {type: int, primaryKey: true}}
    grants: {}
  table newer:
    columns: {id: {type: bigserial, primaryKey: true}}
    grants: {ROLE: [select, insert]}
  sequence newer_id_seq:
    grants: {}
    dependsOn: [table SCH.newer]
  table loose:
    columns: {id: {type: int}}
`, "ROLE", role)})
}
