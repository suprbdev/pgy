package integration

import "testing"

func TestIntegrationTableOptionsIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"options": `
schema SCH:
  function touch():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table t:
    clusterOn: t_a_idx
    columns:
      a: {type: int, statisticsTarget: 500}
      b: {type: int, statisticsTarget: 0}
      c: {type: int, statisticsTarget: -1}
    indexes:
      t_a_idx: {columns: [a]}
    triggers:
      t_off: {timing: before, events: [insert], level: row, procedure: SCH.touch(), enabled: false}
      t_rep: {timing: before, events: [update], level: row, procedure: SCH.touch(), enabled: replica}
      t_all: {timing: before, events: [delete], level: row, procedure: SCH.touch(), enabled: always}
      t_on: {timing: before, events: [truncate], level: statement, procedure: SCH.touch(), enabled: true}
  view v:
    query: select a as first, b as second from SCH.t
    columnComments:
      first: the a column
      second: the b column
  materialized view mv:
    query: select a from SCH.t
    columnComments: {a: from t}
`,
	})
}
