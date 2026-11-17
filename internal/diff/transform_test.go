package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func transformDB(language string, transform []string) *schema.Database {
	return &schema.Database{
		Functions: map[string]*schema.Function{
			"app.f(h hstore)": {Schema: "app", Name: "f", ArgsSig: "(h hstore)", Returns: "integer", Language: language, Transform: transform, Body: "return 1"},
		},
		Procedures: map[string]*schema.Procedure{
			"app.p(h hstore)": {Schema: "app", Name: "p", ArgsSig: "(h hstore)", Language: language, Transform: transform, Body: "return"},
		},
	}
}

func transformLive(language string, transform []string) *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Functions["app.f(h hstore)"] = true
	live.FunctionDefs["app.f(h hstore)"] = &LiveFunction{Signature: "app.f(h hstore)", Kind: "f", Body: "return 1", Returns: "integer", Language: language, Transform: transform, Volatility: "volatile", Security: "invoker"}
	live.Procedures["app.p(h hstore)"] = true
	live.ProcedureDefs["app.p(h hstore)"] = &LiveProcedure{Signature: "app.p(h hstore)", Body: "return", Language: language, Transform: transform, Security: "invoker"}
	return live
}

func TestTransformCreate(t *testing.T) {
	p := Plan(emptyLive(), transformDB("plpython3u", []string{"hstore", "public.jsonb"}), false)
	for _, want := range []string{
		`create function "app"."f"(h hstore) returns integer language plpython3u transform for type hstore, for type public.jsonb as $$return 1$$;`,
		`create procedure "app"."p"(h hstore) language plpython3u transform for type hstore, for type public.jsonb as $$return$$;`,
	} {
		if !findCreate(p, want) {
			t.Errorf("missing %q in:\n%s", want, Render(p))
		}
	}
}

func TestTransformSkippedIfExists(t *testing.T) {
	for _, declared := range [][]string{{"hstore", "jsonb"}, {"jsonb", "public.hstore"}, {"JSONB", "hstore"}, nil} {
		if sql := Render(Plan(transformLive("plpython3u", []string{"hstore", "jsonb"}), transformDB("plpython3u", declared), false)); sql != "" {
			t.Errorf("transform %v matches live [hstore jsonb]:\n%s", declared, sql)
		}
	}
	if sql := Render(Plan(transformLive("plpython3u", nil), transformDB("plpython3u", []string{}), false)); sql != "" {
		t.Errorf("an empty list matches a routine without transforms:\n%s", sql)
	}
}

func TestTransformChangeReplaces(t *testing.T) {
	cases := []struct {
		live, declared []string
		clause         string
	}{
		{[]string{"hstore"}, []string{"hstore", "jsonb"}, "language plpython3u transform for type hstore, for type jsonb as"},
		{[]string{"hstore", "jsonb"}, []string{"jsonb"}, "language plpython3u transform for type jsonb as"},
		{nil, []string{"hstore"}, "language plpython3u transform for type hstore as"},
		{[]string{"hstore"}, []string{}, "language plpython3u as"},
	}
	for _, c := range cases {
		p := Plan(transformLive("plpython3u", c.live), transformDB("plpython3u", c.declared), false)
		if !findCreate(p, `create or replace function "app"."f"(h hstore) returns integer `+c.clause) || !findCreate(p, `create or replace procedure "app"."p"(h hstore) `+c.clause) {
			t.Errorf("live %v, declared %v: want %q in:\n%s", c.live, c.declared, c.clause, Render(p))
		}
	}
}

func TestTransformKeptWhenUnmanaged(t *testing.T) {
	live := transformLive("plpython3u", []string{"hstore", "ext.ltree"})
	desired := transformDB("plpython3u", nil)
	desired.Functions["app.f(h hstore)"].Body = "return 2"
	desired.Procedures["app.p(h hstore)"].Body = "return None"
	p := Plan(live, desired, false)
	for _, want := range []string{
		`create or replace function "app"."f"(h hstore) returns integer language plpython3u transform for type hstore, for type ext.ltree as $$return 2$$;`,
		`create or replace procedure "app"."p"(h hstore) language plpython3u transform for type hstore, for type ext.ltree as $$return None$$;`,
	} {
		if !findCreate(p, want) {
			t.Errorf("replacing a routine that declares no transforms must keep the live ones; missing %q in:\n%s", want, Render(p))
		}
	}
}

func TestTransformInLanguageSpelling(t *testing.T) {
	spelled := "plpython3u transform for type hstore, for type jsonb"
	if sql := Render(Plan(transformLive("plpython3u", []string{"hstore", "jsonb"}), transformDB(spelled, nil), false)); sql != "" {
		t.Errorf("a transform clause written in language matches live:\n%s", sql)
	}
	p := Plan(transformLive("plpython3u", []string{"hstore"}), transformDB(spelled, nil), false)
	if !findCreate(p, `create or replace function "app"."f"(h hstore) returns integer language `+spelled+` as`) {
		t.Errorf("a changed clause in language must replace the function:\n%s", Render(p))
	}
	if strings.Count(Render(p), "transform for type hstore, for type jsonb") != 2 {
		t.Errorf("the clause must be emitted once per routine:\n%s", Render(p))
	}
	p = Plan(emptyLive(), transformDB(spelled, []string{"jsonb"}), false)
	if !findCreate(p, `language plpython3u transform for type jsonb as`) {
		t.Errorf("the transform list wins over a clause in language:\n%s", Render(p))
	}
}

func TestTransformSimulated(t *testing.T) {
	desired := transformDB("plpython3u", []string{"hstore"})
	after := Simulate(transformLive("plperl", []string{"jsonb"}), desired)
	if sql := Render(Plan(after, desired, false)); sql != "" {
		t.Errorf("plan after simulating the first one:\n%s", sql)
	}
	if lf := after.FunctionDefs["app.f(h hstore)"]; lf.Language != "plpython3u" || len(lf.Transform) != 1 || lf.Transform[0] != "hstore" {
		t.Errorf("simulated function: %+v", lf)
	}
}

func TestPruneKeepsCompiledRoutines(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Tables["app.t"] = newLiveTable()
	for sig, language := range map[string]string{"app.handler()": "c", "app.to_pl(internal)": "internal", "app.stray()": "sql", "app.unknown()": ""} {
		live.Functions[sig] = true
		live.FunctionDefs[sig] = &LiveFunction{Signature: sig, Kind: "f", Language: language}
	}
	live.Procedures["app.cproc()"] = true
	live.ProcedureDefs["app.cproc()"] = &LiveProcedure{Signature: "app.cproc()", Language: "c"}
	live.Procedures["app.sproc()"] = true
	live.ProcedureDefs["app.sproc()"] = &LiveProcedure{Signature: "app.sproc()", Language: "sql"}
	desired := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{}}}}
	drops := strings.Join(PlanWith(live, desired, Options{Unsafe: true, Prune: true}).Drops, "\n")
	for _, want := range []string{`drop function "app"."stray"(), "app"."unknown"();`, `drop procedure "app"."sproc"();`} {
		if !strings.Contains(drops, want) {
			t.Errorf("missing %q in:\n%s", want, drops)
		}
	}
	for _, kept := range []string{"handler", "to_pl", "cproc"} {
		if strings.Contains(drops, kept) {
			t.Errorf("%s is written in a language YAML cannot declare and must be kept:\n%s", kept, drops)
		}
	}
}

func TestAggregateEmptyInitCond(t *testing.T) {
	desired := &schema.Database{Aggregates: map[string]*schema.Aggregate{
		"app.cat_all(text)": {Schema: "app", Name: "cat_all", ArgsSig: "(text)", SFunc: "app.cat", SType: "text", InitCondSet: true,
			Options: map[string]string{"msfunc": "app.cat", "minvfunc": "app.uncat", "mstype": "text", "minitcond": ""}},
	}}
	live := emptyLive()
	live.Schemas["app"] = true
	live.AggregateDefs = map[string]*LiveAggregate{}
	p := Plan(live, desired, false)
	want := `create or replace aggregate "app"."cat_all"(text) (sfunc = app.cat, stype = text, initcond = '', msfunc = app.cat, minvfunc = app.uncat, mstype = text, minitcond = '');`
	if !findCreate(p, want) {
		t.Fatalf("missing %q in:\n%s", want, Render(p))
	}
	options := func(minitcond string) map[string]string {
		return map[string]string{"sspace": "0", "finalfunc_extra": "false", "finalfunc_modify": "read_only", "serialfunc": "", "deserialfunc": "", "msfunc": "app.cat", "minvfunc": "app.uncat",
			"mstype": "text", "msspace": "0", "mfinalfunc": "", "mfinalfunc_extra": "false", "mfinalfunc_modify": "read_only", "minitcond": minitcond, "sortop": "", "hypothetical": "false"}
	}
	live.Functions["app.cat_all(text)"] = true
	live.FunctionDefs["app.cat_all(text)"] = &LiveFunction{Signature: "app.cat_all(text)", Kind: "a"}
	live.AggregateDefs["app.cat_all(text)"] = &LiveAggregate{SFunc: "app.cat", SType: "text", Parallel: "unsafe", Options: options("")}
	if p := Plan(live, desired, false); !findCreate(p, want) {
		t.Errorf("an empty initial condition differs from none:\n%s", Render(p))
	}
	live.AggregateDefs["app.cat_all(text)"].HasInitCond = true
	if p := Plan(live, desired, false); !findCreate(p, want) {
		t.Errorf("an empty moving initial condition differs from none:\n%s", Render(p))
	}
	live.AggregateDefs["app.cat_all(text)"].HasMInitCond = true
	if sql := Render(Plan(live, desired, false)); sql != "" {
		t.Errorf("empty initial conditions match live empty ones:\n%s", sql)
	}
	undeclared := &schema.Database{Aggregates: map[string]*schema.Aggregate{
		"app.cat_all(text)": {Schema: "app", Name: "cat_all", ArgsSig: "(text)", SFunc: "app.cat", SType: "text"},
	}}
	if sql := Render(Plan(live, undeclared, false)); sql != "" {
		t.Errorf("an undeclared initial condition leaves a live empty one alone:\n%s", sql)
	}
	if after := Simulate(emptyLiveWithSchema("app"), desired); Render(Plan(after, desired, false)) != "" {
		t.Errorf("plan after simulating the first one:\n%s", Render(Plan(after, desired, false)))
	}
}

func emptyLiveWithSchema(name string) *Live {
	live := emptyLive()
	live.Schemas[name] = true
	live.AggregateDefs = map[string]*LiveAggregate{}
	return live
}
