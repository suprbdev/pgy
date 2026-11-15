package integration

import "testing"

func TestIntegrationConfigValuesIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"set": `
schema SCH:
  function f():
    returns: int
    language: sql
    set:
      statement_timeout: 5s
      timezone: America/New_York
      search_path: SCH, public
      work_mem: 64MB
    body: select 1
`,
	})
}
