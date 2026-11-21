package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func movingAggregate() *schema.Aggregate {
	return &schema.Aggregate{Schema: "app", Name: "total", ArgsSig: "(int)", SFunc: "app.add2", SType: "int", Options: map[string]string{
		"msfunc": "app.add2", "minvfunc": "app.sub2", "mstype": "int", "minitcond": "0",
		"finalfunc_extra": "false", "mfinalfunc_extra": "true", "mfinalfunc_modify": "shareable", "sspace": "8", "sortop": "pg_catalog.>",
	}}
}

func TestAggregateOptionsRender(t *testing.T) {
	got := renderAggregate("app.total", movingAggregate())
	want := `create or replace aggregate "app"."total"(int) (sfunc = app.add2, stype = int, sspace = 8, msfunc = app.add2, minvfunc = app.sub2, mstype = int, mfinalfunc_extra, mfinalfunc_modify = shareable, minitcond = '0', sortop = operator(pg_catalog.>));`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestAggregateOptionsMatchLive(t *testing.T) {
	live := &LiveAggregate{SFunc: "app.add2", SType: "integer", Parallel: "unsafe", Options: map[string]string{
		"sspace": "8", "finalfunc_extra": "false", "finalfunc_modify": "read_only", "msfunc": "app.add2", "minvfunc": "app.sub2",
		"mstype": "integer", "msspace": "0", "mfinalfunc_extra": "true", "mfinalfunc_modify": "shareable", "minitcond": "0", "sortop": "pg_catalog.>",
		"serialfunc": "app.ser",
	}}
	ag := movingAggregate()
	if !aggregateMatchesLive(ag, live) {
		t.Error("matching options (serialfunc unmanaged) must match")
	}
	for k, v := range map[string]string{"mfinalfunc_modify": "read_write", "minitcond": "1", "sortop": "<", "mstype": "bigint", "sspace": "16", "mfinalfunc_extra": "false"} {
		changed := movingAggregate()
		changed.Options[k] = v
		if aggregateMatchesLive(changed, live) {
			t.Errorf("changing %s to %s must not match", k, v)
		}
	}
	ag.Options["sortop"] = ">"
	ag.Options["finalfunc_modify"] = ""
	if !aggregateMatchesLive(ag, live) {
		t.Error("an unqualified sortop and a default modify must match")
	}
}

func TestSimulateAggregateOptions(t *testing.T) {
	ag := movingAggregate()
	d := &schema.Database{Aggregates: map[string]*schema.Aggregate{"app.total(int)": ag}}
	if sql := Render(Plan(emptyLive(), d, false)); !strings.Contains(sql, "msfunc = app.add2") {
		t.Fatalf("the aggregate must be planned from empty, got:\n%s", sql)
	}
	if sql := Render(Plan(Simulate(emptyLive(), d), d, false)); strings.Contains(sql, "aggregate") {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}

func supportAggregate(support string) *schema.Database {
	return &schema.Database{Aggregates: map[string]*schema.Aggregate{
		"app.total(int8)": {Schema: "app", Name: "total", ArgsSig: "(int8)", SFunc: "int8pl", SType: "int8", Options: map[string]string{"support": support}},
	}}
}

func supportLive(support string) *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	norm := normalizeFunctionSignature("app.total(bigint)")
	live.Functions["app.total(bigint)"] = true
	live.FunctionDefs[norm] = &LiveFunction{Signature: "app.total(bigint)", Kind: "a"}
	live.FunctionPublicExec[norm] = true
	live.AggregateDefs = map[string]*LiveAggregate{}
	live.AggregateDefs[norm] = &LiveAggregate{SFunc: "int8pl", SType: "bigint", Parallel: "unsafe", Options: map[string]string{"support": support}}
	return live
}

func TestAggregateSupportCreate(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	p := Plan(live, supportAggregate("app.total_support"), false)
	want := `create or replace aggregate "app"."total"(int8) (sfunc = int8pl, stype = int8, support = app.total_support);`
	if !findCreate(p, want) {
		t.Errorf("missing %q in:\n%s", want, Render(p))
	}
}

func TestAggregateSupportSkippedIfExists(t *testing.T) {
	for _, have := range []string{"app.total_support", "total_support"} {
		live := supportLive(have)
		live.SearchPath = []string{"app", "public"}
		if sql := Render(Plan(live, supportAggregate("app.total_support"), false)); sql != "" {
			t.Errorf("live support %s matches, got:\n%s", have, sql)
		}
	}
	if sql := Render(Plan(supportLive(""), supportAggregate(""), false)); sql != "" {
		t.Errorf("an empty support matches an aggregate without one, got:\n%s", sql)
	}
	d := supportAggregate("")
	delete(d.Aggregates["app.total(int8)"].Options, "support")
	if sql := Render(Plan(supportLive("app.total_support"), d, false)); sql != "" {
		t.Errorf("a support left out is unmanaged, got:\n%s", sql)
	}
	if sql := Render(Plan(Simulate(emptyLive(), supportAggregate("app.total_support")), supportAggregate("app.total_support"), false)); strings.Contains(sql, "aggregate") {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}

func TestAggregateSupportReplaced(t *testing.T) {
	for name, c := range map[string]struct{ have, want, sql string }{
		"added":   {"", "app.total_support", `create or replace aggregate "app"."total"(int8) (sfunc = int8pl, stype = int8, support = app.total_support);`},
		"changed": {"app.old_support", "app.total_support", `create or replace aggregate "app"."total"(int8) (sfunc = int8pl, stype = int8, support = app.total_support);`},
		"removed": {"app.old_support", "", `create or replace aggregate "app"."total"(int8) (sfunc = int8pl, stype = int8);`},
	} {
		if p := Plan(supportLive(c.have), supportAggregate(c.want), false); !findCreate(p, c.sql) {
			t.Errorf("%s: missing %q in:\n%s", name, c.sql, Render(p))
		}
	}
}

func TestAggregateCreatedAfterItsSupportFunction(t *testing.T) {
	d := supportAggregate("app.zz_support")
	d.Functions = map[string]*schema.Function{"app.zz_support(internal)": {Schema: "app", Name: "zz_support", ArgsSig: "(internal)", Returns: "internal", Language: "internal", Body: "int8inc_support"}}
	live := emptyLive()
	live.Schemas["app"] = true
	sql := Render(Plan(live, d, false))
	fn, agg := strings.Index(sql, `create function "app"."zz_support"`), strings.Index(sql, `create or replace aggregate "app"."total"`)
	if fn < 0 || agg < fn {
		t.Errorf("the support function must be created before the aggregate:\n%s", sql)
	}
}
