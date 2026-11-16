package schema

import "testing"

func TestOrderedSetAggregateParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema x:
  aggregate my_rank(variadic "any" order by variadic "any"):
    sfunc: ordered_set_transition_multi
    stype: internal
    finalfunc: rank_final
    finalfuncExtra: true
    hypothetical: true
`))
	if err != nil {
		t.Fatal(err)
	}
	ag := db.Aggregates[`x.my_rank(variadic "any" order by variadic "any")`]
	if ag == nil {
		t.Fatalf("aggregate not keyed by its ordered-set signature: %v", db.Aggregates)
	}
	if ag.ArgsSig != `(variadic "any" order by variadic "any")` || ag.Options["hypothetical"] != "true" || ag.Options["finalfunc_extra"] != "true" {
		t.Errorf("parsed: %+v", ag)
	}
}

func TestTablespaceParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  table t:
    columns: {id: {type: int}, v: {type: int}}
    primaryKey: {t_pkey: {columns: [id], tablespace: fast}}
    tablespace: fast
    indexes: {t_v: {columns: [v], tablespace: archive}}
    constraints: {t_v_uq: {type: unique, columns: [v], tablespace: fast}}
  materialized view mv:
    query: select 1
    tablespace: archive
`))
	if err != nil {
		t.Fatal(err)
	}
	tb := db.Tables["app.t"]
	if tb == nil || tb.Tablespace != "fast" || tb.PrimaryKeyTablespace != "fast" || tb.Indexes[0].Tablespace != "archive" || tb.Constraints[0].Tablespace != "fast" {
		t.Errorf("table tablespaces: %+v", tb)
	}
	if mv := db.Views["app.mv"]; mv == nil || mv.Tablespace != "archive" {
		t.Errorf("matview tablespace: %+v", mv)
	}
}
