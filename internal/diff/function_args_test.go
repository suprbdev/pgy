package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestRoutineIdentity(t *testing.T) {
	for _, c := range []struct {
		sig       string
		procedure bool
		want      string
	}{
		{"app.f(a int default 1, n numeric(10,2) = 0)", false, "app.f(int, numeric)"},
		{"app.f(b integer, m numeric)", false, "app.f(int, numeric)"},
		{"app.f(a int, out x int, inout y text)", false, "app.f(int, text)"},
		{"app.p(a int, out x int)", true, "app.p(int, int)"},
		{"app.f(variadic xs int[])", false, "app.f(int[])"},
		{"app.f(double precision, ts timestamp with time zone)", false, "app.f(float8, timestamptz)"},
		{"App.F()", false, "app.f()"},
	} {
		if got := routineIdentity(c.sig, c.procedure); got != c.want {
			t.Errorf("routineIdentity(%q, %v) = %q, want %q", c.sig, c.procedure, got, c.want)
		}
	}
}

func liveWithRoutines() *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	sig := "app.f(a integer)"
	norm := normalizeFunctionSignature(sig)
	live.Functions[sig] = true
	live.FunctionDefs[norm] = &LiveFunction{Signature: sig, Kind: "f", Body: "select a", Returns: "integer", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100}
	live.FunctionComments[norm] = "eff"
	live.Procedures = map[string]bool{normalizeFunctionSignature("app.p(a integer)"): true}
	return live
}

func renamedRoutines() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{},
		Functions:  map[string]*schema.Function{"app.f(b int)": {Name: "f", ArgsSig: "(b int)", Returns: "int", Language: "sql", Body: "select b", Comment: "eff"}},
		Procedures: map[string]*schema.Procedure{"app.p(b int)": {Name: "p", ArgsSig: "(b int)", Language: "sql", Body: "select 1"}},
	}
}

func TestRoutineArgumentRenameBlocked(t *testing.T) {
	p := Plan(liveWithRoutines(), renamedRoutines(), false)
	if sql := Render(p); sql != "" {
		t.Errorf("without --unsafe nothing is emitted, the comment included, got:\n%s", sql)
	}
	if len(p.Warnings) != 2 || !strings.Contains(p.Warnings[0].Message, "the live app.f(a integer) has the same argument types") {
		t.Errorf("want a warning per routine, got %+v", p.Warnings)
	}
}

func TestRoutineArgumentRenameRecreated(t *testing.T) {
	sql := Render(PlanWith(liveWithRoutines(), renamedRoutines(), Options{Unsafe: true, Prune: true}))
	for _, want := range [][2]string{
		{`drop function "app"."f"(a integer);`, `create function "app"."f"(b int)`},
		{`drop procedure "app"."p"(a int);`, `create procedure "app"."p"(b int)`},
	} {
		drop, create := strings.Index(sql, want[0]), strings.Index(sql, want[1])
		if drop < 0 || create < drop || strings.Count(sql, want[0]) != 1 {
			t.Errorf("want %q once, then %q, got:\n%s", want[0], want[1], sql)
		}
	}
	if !strings.Contains(sql, `comment on function "app"."f"(b int) is 'eff';`) {
		t.Errorf("the comment DROP FUNCTION discards must be restored, got:\n%s", sql)
	}
}

func TestRoutineNewOverloadNotRenamed(t *testing.T) {
	d := renamedRoutines()
	d.Functions["app.f(b int)"].ArgsSig = "(b bigint)"
	delete(d.Functions, "app.f(b int)")
	d.Functions["app.f(b bigint)"] = &schema.Function{Name: "f", ArgsSig: "(b bigint)", Returns: "int", Language: "sql", Body: "select 1"}
	d.Functions["app.f(a integer)"] = &schema.Function{Name: "f", ArgsSig: "(a integer)", Returns: "integer", Language: "sql", Body: "select a", Comment: "eff"}
	d.Procedures = nil
	sql := Render(Plan(liveWithRoutines(), d, true))
	if strings.Contains(sql, "drop function") || !strings.Contains(sql, `create function "app"."f"(b bigint)`) {
		t.Errorf("a new overload with other types is created alongside, got:\n%s", sql)
	}
}
