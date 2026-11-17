package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func typmodRoutines() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"app.f(x varchar(10))": {Name: "f", ArgsSig: "(x varchar(10))", Returns: "int", Language: "sql", Body: "select 1"},
			"app.g(n numeric(6,2), out a timestamp(0), inout b bit(3))": {Name: "g", ArgsSig: "(n numeric(6,2), out a timestamp(0), inout b bit(3))", Returns: "record", Language: "sql", Body: "select now()::timestamp, b"},
			"app.h(char(5), interval day to second(3))":                 {Name: "h", ArgsSig: "(char(5), interval day to second(3))", Returns: "int", Language: "sql", Body: "select 1", Comment: "aitch"},
		},
		Procedures: map[string]*schema.Procedure{
			"app.p(x numeric(6,2), out y varchar(3))": {Name: "p", ArgsSig: "(x numeric(6,2), out y varchar(3))", Language: "sql", Body: "select 'a'"},
		},
		Aggregates: map[string]*schema.Aggregate{
			"app.agg(numeric(6,2))": {Name: "agg", ArgsSig: "(numeric(6,2))", SFunc: "numeric_add", SType: "numeric"},
		},
	}
}

func liveTypmodRoutines() *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	live.AggregateDefs = map[string]*LiveAggregate{}
	fn := func(sig, body, returns string) {
		live.Functions[sig] = true
		live.FunctionDefs[normalizeFunctionSignature(sig)] = &LiveFunction{Signature: sig, Kind: "f", Body: body, Returns: returns, Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100, Language: "sql", Args: sig[strings.IndexByte(sig, '(')+1 : len(sig)-1]}
	}
	fn("app.f(x character varying)", "select 1", "integer")
	fn("app.g(n numeric, OUT a timestamp without time zone, INOUT b bit)", "select now()::timestamp, b", "record")
	fn("app.h(character, interval)", "select 1", "integer")
	live.FunctionComments[normalizeFunctionSignature("app.h(character, interval)")] = "aitch"
	psig := "app.p(IN x numeric, OUT y character varying)"
	live.Procedures = map[string]bool{normalizeFunctionSignature(psig): true}
	live.ProcedureDefs = map[string]*LiveProcedure{normalizeFunctionSignature(psig): {Signature: psig, Args: "IN x numeric, OUT y character varying", Body: "select 'a'", Security: "invoker", Language: "sql"}}
	asig := "app.agg(numeric)"
	live.FunctionDefs[normalizeFunctionSignature(asig)] = &LiveFunction{Kind: "a", Signature: asig}
	live.AggregateDefs[normalizeFunctionSignature(asig)] = &LiveAggregate{SFunc: "numeric_add", SType: "numeric", Parallel: "unsafe"}
	return live
}

func TestRoutineArgTypmodsMatchLive(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		p := PlanWith(liveTypmodRoutines(), typmodRoutines(), Options{Unsafe: unsafe, Prune: unsafe})
		if sql := Render(p); sql != "" {
			t.Errorf("unsafe=%v: argument type modifiers PostgreSQL discards must not diff, got:\n%s", unsafe, sql)
		}
		for _, w := range p.Warnings {
			t.Errorf("unsafe=%v: unexpected warning %q", unsafe, w.Message)
		}
	}
}

func TestRoutineArgTypmodsKeptInCreate(t *testing.T) {
	sql := Render(Plan(emptyLiveWithSchema("app"), typmodRoutines(), false))
	for _, want := range []string{
		`create function "app"."f"(x varchar(10)) returns int`,
		`create function "app"."g"(n numeric(6,2), out a timestamp(0), inout b bit(3)) returns record`,
		`create function "app"."h"(char(5), interval day to second(3)) returns int`,
		`create procedure "app"."p"(x numeric(6,2), out y varchar(3))`,
		`aggregate "app"."agg"(numeric(6,2))`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("want %q, got:\n%s", want, sql)
		}
	}
}

func TestRoutineArgTypmodsRenameStillRecreated(t *testing.T) {
	d := typmodRoutines()
	d.Functions = map[string]*schema.Function{"app.f(y varchar(10))": {Name: "f", ArgsSig: "(y varchar(10))", Returns: "int", Language: "sql", Body: "select 1"}}
	d.Procedures, d.Aggregates = nil, nil
	sql := Render(Plan(liveTypmodRoutines(), d, true))
	if !strings.Contains(sql, `drop function "app"."f"(x character varying);`) || !strings.Contains(sql, `create function "app"."f"(y varchar(10))`) {
		t.Errorf("a renamed argument is still recreated, got:\n%s", sql)
	}
}

func TestRoutineIdentityTypmods(t *testing.T) {
	for sig, want := range map[string]string{
		"app.f(x interval day to second(3), c char(5))": "app.f(interval, char)",
		"app.f(x interval, c character)":                "app.f(interval, char)",
	} {
		if got := routineIdentity(sig, false); got != want {
			t.Errorf("routineIdentity(%q) = %q, want %q", sig, got, want)
		}
	}
}
