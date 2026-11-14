package integration

import "testing"

func TestIntegrationDomainChecksIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"domains": `
schema SCH:
  domain score:
    type: int
    notNull: true
    checks:
      score_min: VALUE >= 0
      score_max: VALUE <= 100
      score_not_42: VALUE <> 42
  domain code:
    type: text
    check: VALUE ~ '^[A-Z]+$'
    checks:
      code_len: length(VALUE) between 2 and 8
  table t:
    columns:
      s: {type: SCH.score}
      c: {type: SCH.code, nullable: true}
`,
	})
}
