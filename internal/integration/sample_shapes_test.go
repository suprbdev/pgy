package integration

import "testing"

func TestIntegrationSampleShapesIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"shapes": `
schema SCH:
  function f8diff(a float8, b float8):
    returns: float8
    language: sql
    immutable: true
    body: select a - b
  type f8range:
    type: range
    subtype: float8
    subtypeDiff: SCH.f8diff
  domain bıgınt:
    type: bigint
    comment: non-ASCII name
  table film:
    columns:
      id: {type: int}
      title: {type: text}
      description: {type: text, nullable: true}
      big: {type: SCH.bıgınt, nullable: true}
      fulltext: {type: tsvector}
    primaryKey: [id]
    indexes:
      film_fulltext_idx: {columns: [fulltext], using: gist}
    triggers:
      film_fulltext_trigger:
        timing: before
        events: [insert, update]
        level: row
        procedure: tsvector_update_trigger('fulltext', 'pg_catalog.english', 'title', 'description')
  table payment:
    columns:
      id: {type: int}
      paid_at: {type: timestamptz}
    primaryKey: [paid_at, id]
    partitionBy: {type: range, columns: [paid_at]}
  table payment_2022_04:
    partitionOf: SCH.payment
    forValues: {from: ["'2022-04-01 01:00:00+01'"], to: ["'2022-05-01 01:00:00+01'"]}
`,
	})
}
