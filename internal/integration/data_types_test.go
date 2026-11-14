package integration

import "testing"

func TestIntegrationDataTrickyTypesIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"types": `
extensions:
  - {name: citext, ifNotExists: true}
schema SCH:
  type mood:
    type: enum
    labels: [ok, meh]
  type pair:
    type: composite
    attributes: {a: int, b: text}
  domain short_code:
    type: text
    check: length(VALUE) <= 8
  table Odd Things:
    columns:
      Region: {type: text}
      n: {type: int}
      id: {type: uuid, nullable: true}
      at: {type: timestamptz, nullable: true}
      span: {type: interval, nullable: true}
      raw: {type: bytea, nullable: true}
      ip: {type: inet, nullable: true}
      ci: {type: citext, nullable: true}
      code: {type: SCH.short_code, nullable: true}
      moods: {type: "SCH.mood[]", nullable: true}
      p: {type: SCH.pair, nullable: true}
      r: {type: int4range, nullable: true}
      j: {type: json, nullable: true}
      big: {type: bigint, nullable: true}
      flag: {type: boolean, default: "false"}
      Space Col: {type: text, nullable: true}
    primaryKey: [Region, n]
    data:
      rows:
        - Region: eu
          n: 1
          id: 6f1c2d4e-1b2a-4c3d-9e8f-001122334455
          at: "2026-03-01 12:00:00+01"
          span: 1 day 02:00:00
          raw: '\xdeadbeef'
          ip: 10.0.0.1/32
          ci: MiXeD
          code: abc
          moods: [ok, meh]
          p: {a: 1, b: x}
          r: "[1,5)"
          j: {k: [1, 2]}
          big: 9223372036854775807
          Space Col: hello
        - {Region: us, n: 2}
`,
	})
}
