package integration

import "testing"

func TestIntegrationAggregatesIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"aggregates": `
schema SCH:
  function add2(a int, b int):
    returns: int
    language: sql
    immutable: true
    body: select a + b
  aggregate mysum(int):
    sfunc: SCH.add2
    stype: int
    initcond: "0"
    combinefunc: SCH.add2
    parallel: safe
    comment: sum of ints
  aggregate cnt(*):
    sfunc: int8inc
    stype: bigint
    initcond: "0"
  aggregate concat_all(text):
    sfunc: textcat
    stype: text
    initcond: ""
`,
	})
}
