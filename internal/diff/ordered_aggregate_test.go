package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestOrderedSetAggregateSignatures(t *testing.T) {
	for yaml, live := range map[string]string{
		"x.my_pct(float8 order by float8)":                  "x.my_pct(double precision ORDER BY double precision)",
		"x.my_mode(order by anyelement)":                    "x.my_mode(ORDER BY anyelement)",
		`x.my_rank(variadic "any" order by variadic "any")`: `x.my_rank(VARIADIC "any" ORDER BY VARIADIC "any")`,
		"x.two(int, text order by timestamptz, int)":        "x.two(integer, text ORDER BY timestamp with time zone, integer)",
	} {
		if a, b := normalizeFunctionSignature(yaml), normalizeFunctionSignature(live); a != b {
			t.Errorf("%s -> %s, but %s -> %s", yaml, a, live, b)
		}
	}
	if normalizeFunctionSignature("x.m(order by anyelement)") == normalizeFunctionSignature("x.m(anyelement)") {
		t.Error("an ordered-set aggregate's signature must differ from a plain one's")
	}
}

func orderedAggregates(hypothetical string) *schema.Database {
	ag := &schema.Aggregate{Schema: "x", Name: "my_rank", ArgsSig: `(variadic "any" order by variadic "any")`, SFunc: "ordered_set_transition_multi", SType: "internal", FinalFunc: "rank_final",
		Options: map[string]string{"finalfunc_extra": "true", "hypothetical": hypothetical}}
	return &schema.Database{Aggregates: map[string]*schema.Aggregate{`x.my_rank(variadic "any" order by variadic "any")`: ag}}
}

func liveOrderedAggregate(hypothetical string) *Live {
	live := emptyLive()
	live.AggregateDefs = map[string]*LiveAggregate{normalizeFunctionSignature(`x.my_rank(VARIADIC "any" ORDER BY VARIADIC "any")`): {
		SFunc: "ordered_set_transition_multi", SType: "internal", FinalFunc: "rank_final", Parallel: "unsafe",
		Options: map[string]string{"finalfunc_extra": "true", "hypothetical": hypothetical},
	}}
	return live
}

func TestHypotheticalAggregateCreate(t *testing.T) {
	sql := Render(Plan(emptyLive(), orderedAggregates("true"), false))
	if !strings.Contains(sql, `create or replace aggregate "x"."my_rank"(variadic "any" order by variadic "any") (sfunc = ordered_set_transition_multi, stype = internal, finalfunc = rank_final, finalfunc_extra, hypothetical);`) {
		t.Errorf("want a hypothetical aggregate, got:\n%s", sql)
	}
}

func TestOrderedSetAggregateSkippedIfExists(t *testing.T) {
	if sql := Render(Plan(liveOrderedAggregate("true"), orderedAggregates("true"), false)); strings.Contains(sql, "aggregate") {
		t.Errorf("a matching ordered-set aggregate must be left alone, got:\n%s", sql)
	}
}

func TestHypotheticalAggregateToggle(t *testing.T) {
	p := Plan(liveOrderedAggregate("true"), orderedAggregates("false"), false)
	if sql := Render(p); strings.Contains(sql, "aggregate") || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "rerun with --unsafe") {
		t.Errorf("without --unsafe: want only a warning, got:\n%s\n%v", sql, p.Warnings)
	}
	sql := Render(Plan(liveOrderedAggregate("true"), orderedAggregates("false"), true))
	drop := strings.Index(sql, `drop aggregate "x"."my_rank"(variadic "any" order by variadic "any");`)
	create := strings.Index(sql, `create or replace aggregate "x"."my_rank"`)
	if drop < 0 || create < drop || strings.Contains(sql, "hypothetical") {
		t.Errorf("with --unsafe: want DROP then CREATE without hypothetical, got:\n%s", sql)
	}
}
