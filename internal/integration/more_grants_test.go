package integration

import (
	"strings"
	"testing"
)

func TestIntegrationSequenceTypeDomainGrantsIdempotent(t *testing.T) {
	pool := connect(t)
	role := freshRole(t, pool, "rw")
	assertYAMLIdempotent(t, map[string]string{
		"grants": strings.ReplaceAll(`
schema SCH:
  sequence s:
    grants: {ROLE: [select, usage]}
  type mood:
    type: enum
    labels: [ok]
    grants: {ROLE: [usage]}
  domain email:
    type: text
    grants: {ROLE: [usage]}
`, "ROLE", role),
	})
}
