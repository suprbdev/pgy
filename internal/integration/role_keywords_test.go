package integration

import (
	"context"
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

func TestIntegrationRoleSettings(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	role := sch + "_settings"
	t.Cleanup(func() { pool.Exec(context.Background(), `drop role if exists "`+role+`"`) }) //nolint
	y := `
roles:
  ROLE:
    set: {statement_timeout: 5s, timezone: America/New_York, search_path: "SCH, public"}
schema SCH:
  table t:
    columns: {id: {type: int}}
`
	convergeYAML(t, pool, sch, strings.ReplaceAll(y, "ROLE", role))
	sql := convergeYAML(t, pool, sch, strings.ReplaceAll(strings.ReplaceAll(y, "ROLE", role), "statement_timeout: 5s, ", ""))
	if !contains(sql, `alter role "`+role+`" reset statement_timeout;`) {
		t.Errorf("expected a reset, got:\n%s", sql)
	}
}
