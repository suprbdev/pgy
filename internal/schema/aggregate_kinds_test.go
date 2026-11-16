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
