package integration

import "testing"

func TestIntegrationFunctionParallelCostRowsIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"functions": `
schema SCH:
  function safe_one():
    returns: int
    language: sql
    immutable: true
    parallel: safe
    cost: 0.1
    body: select 1
  function restricted_one():
    returns: int
    language: plpgsql
    parallel: restricted
    cost: 5000
    body: begin return 1; end
  function some_rows():
    returns: setof int
    language: sql
    rows: 10
    body: select generate_series(1, 3)
  function table_rows():
    returns: table (n int)
    language: sql
    parallel: safe
    rows: 2.5
    body: select 1
`,
	})
}
