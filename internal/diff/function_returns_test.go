package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestReturnsChanged(t *testing.T) {
	for _, c := range []struct {
		desired, live string
		want          bool
	}{
		{"int", "integer", false},
		{"bigint", "integer", true},
		{"setof int", "SETOF integer", false},
		{"int", "SETOF integer", true},
		{"varchar(20)", "character varying", false},
		{"app.status", "status", false},
		{"numeric(12,2)[]", "numeric[]", false},
		{"table(id int)", "TABLE(id bigint)", false},
		{"int", "", false},
		{"trigger", "trigger", false},
	} {
		if got := returnsChanged(c.desired, c.live); got != c.want {
			t.Errorf("returnsChanged(%q, %q) = %v, want %v", c.desired, c.live, got, c.want)
		}
	}
}

func TestFunctionReturnTypeChangeRecreates(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	sig := "app.f()"
	live.Functions[sig] = true
	live.FunctionDefs[normalizeFunctionSignature(sig)] = &LiveFunction{Signature: sig, Kind: "f", Body: "select 1", Returns: "integer", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100}
	live.FunctionComments[normalizeFunctionSignature(sig)] = "one"
	d := &schema.Database{Tables: map[string]*schema.Table{}, Functions: map[string]*schema.Function{
		"app.f()": {Name: "f", ArgsSig: "()", Returns: "bigint", Language: "sql", Body: "select 1::bigint", Comment: "one"},
	}}

	safe := Plan(live, d, false)
	if sql := Render(safe); sql != "" {
		t.Errorf("without --unsafe nothing is emitted, got:\n%s", sql)
	}
	if len(safe.Warnings) != 1 || !strings.Contains(safe.Warnings[0].Message, "return type changed from integer to bigint") {
		t.Errorf("expected a return type warning, got %+v", safe.Warnings)
	}

	sql := Render(Plan(live, d, true))
	drop := strings.Index(sql, `drop function "app"."f"();`)
	create := strings.Index(sql, `create function "app"."f"() returns bigint`)
	if drop < 0 || create < drop {
		t.Errorf("expected DROP FUNCTION then CREATE FUNCTION, got:\n%s", sql)
	}
	if !strings.Contains(sql, `comment on function "app"."f"() is 'one';`) {
		t.Errorf("the recreated function's comment must be restored:\n%s", sql)
	}
	if live.FunctionDefs[normalizeFunctionSignature(sig)] == nil {
		t.Error("planning must not modify the caller's live snapshot")
	}
}
