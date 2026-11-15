package integration

import (
	"sort"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

const objectMatrixBase = `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      a: {type: int, nullable: true}
      b: {type: text, nullable: true}
`

// objectChangeCases maps a case name to the YAML of one object before and
// after a change. Both versions must build from scratch and survive a pull
// round trip, and creating the object, changing it and pruning it must each
// converge in one migration.
var objectChangeCases = map[string][2]string{
	"sequence": {
		`  sequence s: {start: 1}`,
		`  sequence s: {as: bigint, increment: 5, minValue: 1, maxValue: 99999, start: 1, cache: 20, cycle: true, comment: counter}`,
	},
	"sequence_unlogged": {
		`  sequence s: {start: 1}`,
		`  sequence s: {start: 1, unlogged: true}`,
	},
	"sequence_owned": {
		`  sequence s: {start: 1}`,
		"  sequence s: {start: 1, ownedBy: t.a, dependsOn: [table SCH.t]}",
	},
	"domain": {
		`  domain d: {type: int}`,
		`  domain d: {type: int, notNull: true, default: 0, checks: {d_min: VALUE >= 0, d_max: VALUE <= 100}, comment: score}`,
	},
	"domain_check_changed": {
		`  domain d: {type: int, checks: {d_min: VALUE >= 0}}`,
		`  domain d: {type: int, checks: {d_min: VALUE >= 1}}`,
	},
	"enum_append": {
		`  type e: {type: enum, labels: [a, b]}`,
		`  type e: {type: enum, labels: [a, b, c], comment: letters}`,
	},
	"enum_insert": {
		`  type e: {type: enum, labels: [a, b]}`,
		`  type e: {type: enum, labels: [x, a, y, b]}`,
	},
	"type_revoke_public": {
		`  type e: {type: enum, labels: [a]}`,
		`  type e: {type: enum, labels: [a], revokePublic: true}`,
	},
	"composite_add": {
		`  type c: {type: composite, attributes: {x: {type: int}}}`,
		`  type c: {type: composite, attributes: {x: {type: int}, y: {type: text}}, comment: pair}`,
	},
	"composite_comments": {
		`  type c: {type: composite, attributes: {x: {type: int}, y: {type: text, comment: old}}}`,
		`  type c: {type: composite, attributes: {x: {type: int, comment: "it's x"}, y: {type: text, comment: new}}}`,
	},
	"composite_retype": {
		`  type c: {type: composite, attributes: {x: {type: int}}}`,
		`  type c: {type: composite, attributes: {x: {type: bigint}}}`,
	},
	"range": {
		`  type r: {type: range, subtype: float8}`,
		`  type r: {type: range, subtype: float8, subtypeDiff: float8mi, comment: floats}`,
	},
	"function": {
		"  function f(x int):\n    returns: int\n    language: sql\n    body: select x",
		"  function f(x int):\n    returns: int\n    language: sql\n    immutable: true\n    strict: true\n    parallel: safe\n    cost: 5\n    set: {search_path: public}\n    comment: plus one\n    body: select x + 1",
	},
	"function_security": {
		"  function f():\n    returns: int\n    language: plpgsql\n    body: begin return 1; end",
		"  function f():\n    returns: int\n    language: plpgsql\n    security: definer\n    revokePublic: true\n    body: begin return 2; end",
	},
	"function_signature": {
		"  function f(x int):\n    returns: int\n    language: sql\n    body: select x",
		"  function f(x int, y int default 0):\n    returns: int\n    language: sql\n    body: select x + y",
	},
	"function_returns": {
		"  function f():\n    returns: int\n    language: sql\n    body: select 1",
		"  function f():\n    returns: bigint\n    language: sql\n    body: select 1::bigint",
	},
	"function_setof": {
		"  function f():\n    returns: setof int\n    language: sql\n    body: select 1",
		"  function f():\n    returns: setof int\n    language: sql\n    rows: 50\n    stable: true\n    body: select id from SCH.t\n    dependsOn: [table SCH.t]",
	},
	"procedure": {
		"  procedure p(n int):\n    language: plpgsql\n    body: begin perform n; end",
		"  procedure p(n int):\n    language: plpgsql\n    security: definer\n    set: {work_mem: 64MB}\n    comment: does nothing\n    body: begin perform n + 1; end",
	},
	"view": {
		"  view v:\n    query: select id from SCH.t\n    dependsOn: [table SCH.t]",
		"  view v:\n    query: select id from SCH.t\n    securityBarrier: true\n    comment: ids\n    columnComments: {id: the id}\n    dependsOn: [table SCH.t]",
	},
	"view_options": {
		"  view v:\n    query: select id, a from SCH.t\n    dependsOn: [table SCH.t]",
		"  view v:\n    query: select id, a from SCH.t\n    securityInvoker: true\n    checkOption: local\n    grants: {public: [select]}\n    dependsOn: [table SCH.t]",
	},
	"matview": {
		"  materialized view m:\n    query: select id from SCH.t\n    dependsOn: [table SCH.t]",
		"  materialized view m:\n    query: select id from SCH.t\n    with: {fillfactor: 70}\n    comment: snapshot\n    indexes: {m_id: {columns: [id], unique: true}}\n    dependsOn: [table SCH.t]",
	},
	"aggregate": {
		"  function add2(a int, b int):\n    returns: int\n    language: sql\n    immutable: true\n    body: select a + b\n  aggregate total(int):\n    sfunc: SCH.add2\n    stype: int\n    initcond: \"0\"",
		"  function add2(a int, b int):\n    returns: int\n    language: sql\n    immutable: true\n    body: select a + b\n  aggregate total(int):\n    sfunc: SCH.add2\n    stype: int\n    initcond: \"0\"\n    combinefunc: SCH.add2\n    parallel: safe\n    comment: sum",
	},
	"aggregate_moving": {
		"  function add2(a int, b int):\n    returns: int\n    language: sql\n    immutable: true\n    body: select a + b\n  function sub2(a int, b int):\n    returns: int\n    language: sql\n    immutable: true\n    body: select a - b\n  aggregate total(int):\n    sfunc: SCH.add2\n    stype: int\n    initcond: \"0\"",
		"  function add2(a int, b int):\n    returns: int\n    language: sql\n    immutable: true\n    body: select a + b\n  function sub2(a int, b int):\n    returns: int\n    language: sql\n    immutable: true\n    body: select a - b\n  aggregate total(int):\n    sfunc: SCH.add2\n    stype: int\n    initcond: \"0\"\n    sspace: 8\n    msfunc: SCH.add2\n    minvfunc: SCH.sub2\n    mstype: int\n    minitcond: \"0\"\n    mfinalfuncModify: shareable\n    sortop: \">\"",
	},
}

func TestIntegrationObjectChangeMatrix(t *testing.T) {
	names := make([]string, 0, len(objectChangeCases))
	for n := range objectChangeCases {
		names = append(names, n)
	}
	sort.Strings(names)
	built := map[string]string{}
	for _, n := range names {
		built[n+"_before"] = objectMatrixBase + objectChangeCases[n][0] + "\n"
		built[n+"_after"] = objectMatrixBase + objectChangeCases[n][1] + "\n"
	}
	assertYAMLIdempotent(t, built)

	pool := connect(t)
	opts := diff.Options{Unsafe: true, Prune: true}
	for _, n := range names {
		t.Run("changed_"+n, func(t *testing.T) {
			sch := freshSchema(t, pool)
			convergeYAMLWith(t, pool, sch, objectMatrixBase, opts)
			convergeYAMLWith(t, pool, sch, objectMatrixBase+objectChangeCases[n][0]+"\n", opts)
			convergeYAMLWith(t, pool, sch, objectMatrixBase+objectChangeCases[n][1]+"\n", opts)
			convergeYAMLWith(t, pool, sch, objectMatrixBase, opts)
		})
	}
}
