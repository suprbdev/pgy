package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestArgDefaultsDiff(t *testing.T) {
	live := "a integer DEFAULT 1, t text DEFAULT 'x'::text, xs integer[] DEFAULT ARRAY[]::integer[], n numeric DEFAULT NULL::numeric, c text DEFAULT 'a,b'::text, OUT r integer"
	for _, c := range []struct {
		desired          string
		changed, removed bool
	}{
		{"(a int default 1, t text default 'x', xs int[] default array[]::int[], n numeric default null, c text = 'a,b', out r int)", false, false},
		{"(a int default 2, t text default 'x', xs int[] default array[]::int[], n numeric default null, c text = 'a,b')", true, false},
		{"(a int, t text default 'x', xs int[] default array[]::int[], n numeric default null, c text default 'a,b')", false, true},
		{"(a int default 1, t text default 'y', xs int[] default array[]::int[], n numeric default null, c text = 'a,b')", true, false},
		{"(a int default 1, t text)", false, false},
	} {
		changed, removed := argDefaultsDiff(c.desired, live)
		if changed != c.changed || removed != c.removed {
			t.Errorf("argDefaultsDiff(%s) = %v, %v, want %v, %v", c.desired, changed, removed, c.changed, c.removed)
		}
	}
	if changed, _ := argDefaultsDiff("(a int, b int default 1)", "a integer, b integer"); !changed {
		t.Error("an added default is a change")
	}
}

func TestSignatureDefaultForms(t *testing.T) {
	live := normalizeFunctionSignature("app.f(a integer, c text)")
	for _, yaml := range []string{"app.f(a int = 1, c text = 'a,b')", "app.f(a int default 1, c text default 'x, y')", "app.f(a int DEFAULT (1 + 2), c text)"} {
		if got := normalizeFunctionSignature(yaml); got != live {
			t.Errorf("normalizeFunctionSignature(%q) = %q, want %q", yaml, got, live)
		}
	}
	if got := grantFuncArgs("(a int = 1, c text default 'a,b')"); got != "(a int, c text)" {
		t.Errorf("grantFuncArgs = %q", got)
	}
}

func liveFunctionWithDefaults(args string) *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	sig := "app.f(a integer, b integer)"
	live.Functions[sig] = true
	live.FunctionDefs[normalizeFunctionSignature(sig)] = &LiveFunction{Signature: sig, Args: args, Kind: "f", Body: "select a", Returns: "integer", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100}
	return live
}

func functionWithArgs(args string) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, Functions: map[string]*schema.Function{
		"app.f" + args: {Name: "f", ArgsSig: args, Returns: "int", Language: "sql", Body: "select a"},
	}}
}

func TestFunctionDefaultChangeReplaces(t *testing.T) {
	sql := Render(Plan(liveFunctionWithDefaults("a integer, b integer DEFAULT 1"), functionWithArgs("(a int, b int default 2)"), false))
	if !strings.Contains(sql, `create or replace function "app"."f"(a int, b int default 2)`) {
		t.Errorf("a changed default must replace the function, got:\n%s", sql)
	}
	if sql := Render(Plan(liveFunctionWithDefaults("a integer, b integer DEFAULT 1"), functionWithArgs("(a int, b int default 1)"), false)); sql != "" {
		t.Errorf("an unchanged default must not, got:\n%s", sql)
	}
}

func TestFunctionDefaultRemovedRecreates(t *testing.T) {
	live := liveFunctionWithDefaults("a integer, b integer DEFAULT 1")
	safe := Plan(live, functionWithArgs("(a int, b int)"), false)
	if sql := Render(safe); sql != "" || len(safe.Warnings) != 1 || !strings.Contains(safe.Warnings[0].Message, "an argument default was removed") {
		t.Errorf("without --unsafe: want only a warning, got:\n%s\n%v", sql, safe.Warnings)
	}
	sql := Render(Plan(live, functionWithArgs("(a int, b int)"), true))
	drop, create := strings.Index(sql, `drop function "app"."f"(a int, b int);`), strings.Index(sql, `create function "app"."f"(a int, b int)`)
	if drop < 0 || create < drop {
		t.Errorf("with --unsafe: want DROP then CREATE, got:\n%s", sql)
	}
}
