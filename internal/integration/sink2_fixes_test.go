package integration

import "testing"

func TestIntegrationSubPartitionsAndRangeCollation(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{"subparts": `
schema SCH:
  type textrange: {type: range, subtype: text, collation: C}
  table r:
    columns: {id: {type: int, nullable: true}, at: {type: date}, region: {type: text}}
    partitionBy: {range: [at]}
  table r2025:
    partitionOf: SCH.r
    forValues: {from: ["2025-01-01"], to: ["2026-01-01"]}
    partitionBy: {list: [region]}
  table r2025_eu:
    partitionOf: SCH.r2025
    forValues: {in: [eu]}
  table r2025_rest:
    partitionOf: SCH.r2025
    default: true
`})
}
