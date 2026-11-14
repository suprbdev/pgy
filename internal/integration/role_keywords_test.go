package integration

import (
	"strings"
	"testing"
)

func TestIntegrationRoleKeywordsIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"keywords": `
schema SCH:
  table doc:
    columns: {id: {type: int}, owner: {type: text}}
    rowLevelSecurity: true
    policies:
      mine: {to: [current_user], using: owner = current_user}
      theirs: {to: [session_user, public], for: select, using: "true"}
    grants:
      current_user: [select, insert]
`,
	})
}

func TestIntegrationDefaultPrivilegesRoleKeywordsIdempotent(t *testing.T) {
	pool := connect(t)
	reader := freshRole(t, pool, "kwreader")
	assertYAMLIdempotent(t, map[string]string{
		"defaults": strings.ReplaceAll(`
defaultPrivileges:
  - for: current_user
    in: [SCH]
    tables: {READER: [select]}
schema SCH:
  table t:
    columns: {id: {type: int}}
`, "READER", reader),
	})
}
