package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func aggDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{"app.add2(a int, b int)": {Schema: "app", Name: "add2", ArgsSig: "(a int, b int)", Returns: "int", Language: "sql", Body: "select a + b"}},
		Aggregates: map[string]*schema.Aggregate{
			"app.mysum(int)": {Name: "mysum", ArgsSig: "(int)", SFunc: "app.add2", SType: "int", InitCond: "0", CombineFunc: "app.add2", Parallel: "safe", Comment: "sum"},
			"app.cnt(*)":     {Name: "cnt", ArgsSig: "(*)", SFunc: "int8inc", SType: "bigint", InitCond: "0"},
		}}
}

func TestAggregateCreateAfterItsFunction(t *testing.T) {
	p := Plan(emptyLive(), aggDB(), false)
	fn, agg := -1, -1
	for i, s := range p.Creates {
		switch s {
		case `create or replace aggregate "app"."mysum"(int) (sfunc = app.add2, stype = int, combinefunc = app.add2, initcond = '0', parallel = safe);`:
			agg = i
		}
		if len(s) > 15 && s[:15] == "create function" {
			fn = i
		}
	}
	if fn < 0 || agg < 0 || fn > agg {
		t.Errorf("aggregate must be created after its state function: %v", p.Creates)
	}
	if !findCreate(p, `create or replace aggregate "app"."cnt"(*) (sfunc = int8inc, stype = bigint, initcond = '0');`) {
		t.Errorf("missing count(*)-style aggregate in %v", p.Creates)
	}
	if !findAlter(p, `comment on aggregate "app"."mysum"(int) is 'sum';`) {
		t.Errorf("missing aggregate comment in %v", p.Alters)
	}
}

func TestAggregateSkippedReplacedPruned(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Functions["app.add2(a integer, b integer)"] = true
	live.FunctionDefs[normalizeFunctionSignature("app.add2(a integer, b integer)")] = &LiveFunction{Kind: "f", Body: "select a + b", Volatility: "volatile", Security: "invoker", Signature: "app.add2(a integer, b integer)"}
	live.AggregateDefs = map[string]*LiveAggregate{
		normalizeFunctionSignature("app.mysum(integer)"): {SFunc: "app.add2", SType: "integer", InitCond: "0", CombineFunc: "app.add2", Parallel: "safe"},
		normalizeFunctionSignature("app.cnt()"):          {SFunc: "int8inc", SType: "bigint", InitCond: "0", Parallel: "unsafe"},
		normalizeFunctionSignature("app.old(text)"):      {SFunc: "textcat", SType: "text", Parallel: "unsafe"},
	}
	for _, sig := range []string{"app.mysum(integer)", "app.cnt()", "app.old(text)"} {
		live.FunctionDefs[normalizeFunctionSignature(sig)] = &LiveFunction{Kind: "a", Signature: sig}
	}
	live.FunctionComments = map[string]string{normalizeFunctionSignature("app.mysum(integer)"): "sum"}
	if sql := Render(Plan(live, aggDB(), false)); sql != "" {
		t.Errorf("matching aggregates must not diff, got:\n%s", sql)
	}
	p := PlanWith(live, aggDB(), Options{Unsafe: true, Prune: true})
	if len(p.Drops) != 1 || p.Drops[0] != `drop aggregate "app"."old"(text);` {
		t.Errorf("prune should drop the undeclared aggregate, got %v", p.Drops)
	}
	d := aggDB()
	d.Aggregates["app.mysum(int)"].InitCond = "10"
	if p := Plan(live, d, false); !findCreate(p, `create or replace aggregate "app"."mysum"(int)`) {
		t.Errorf("changed initcond must replace the aggregate, got %v", p.Creates)
	}
}
