package integration

import (
	"strings"
	"testing"
)

func TestIntegrationColumnNDistinctIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"ndistinct": `
schema SCH:
  table t:
    columns:
      a: {type: int, nDistinct: -0.5, nDistinctInherited: 200}
      b: {type: text, nDistinct: 42}
      c: {type: int, nDistinct: 0}
`,
	})
}

func TestIntegrationAddedColumnSettingsConverge(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, `
schema SCH:
  table t:
    columns: {a: {type: int}}
`)
	sql := convergeYAML(t, pool, sch, `
schema SCH:
  table t:
    columns:
      a: {type: int, nDistinct: -1}
      b: {type: text, statisticsTarget: 300, nDistinct: 25, storage: external}
`)
	for _, want := range []string{"set statistics 300", "set (n_distinct = 25)", `column "a" set (n_distinct = -1)`} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	sql = convergeYAML(t, pool, sch, `
schema SCH:
  table t:
    columns:
      a: {type: int, nDistinct: 0}
      b: {type: text}
`)
	if !strings.Contains(sql, `column "a" reset (n_distinct)`) || strings.Contains(sql, `column "b"`) {
		t.Errorf("expected only a's override reset:\n%s", sql)
	}
}
