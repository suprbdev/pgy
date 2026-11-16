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

func TestSQLFunctionBodyUncheckedOnlyWithNewRelations(t *testing.T) {
	d := &schema.Database{
		Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}}},
		Functions: map[string]*schema.Function{
			"app.f()": {Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select count(*)::int from app.t"},
			"app.g()": {Name: "g", ArgsSig: "()", Returns: "int", Language: "plpgsql", Body: "begin return 1; end"},
		},
	}
	creates := Plan(emptyLive(), d, false).Creates
	joined := strings.Join(creates, "\n")
	if !strings.Contains(joined, "set check_function_bodies = off;\ncreate function \"app\".\"f\"()") || !strings.Contains(joined, "$$;\nreset check_function_bodies;") {
		t.Errorf("a SQL function created alongside tables must skip body checks:\n%s", joined)
	}
	if strings.Count(joined, "check_function_bodies = off") != 1 {
		t.Errorf("only the SQL-language function is wrapped:\n%s", joined)
	}

	live := emptyLive()
	live.Schemas["app"] = true
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	live.Tables["app.t"] = lt
	if sql := Render(Plan(live, d, false)); strings.Contains(sql, "check_function_bodies") {
		t.Errorf("with every relation already present, bodies are checked as usual:\n%s", sql)
	}
}

func TestSQLStandardBodies(t *testing.T) {
	for body, want := range map[string]bool{
		"begin atomic select 1; end": true,
		"BEGIN\n  ATOMIC select 1; END": true,
		"return x * 2":               true,
		"select 1":                   false,
		"begin return 1; end":        false,
	} {
		if got := schema.IsSQLStandardBody("sql", body); got != want {
			t.Errorf("IsSQLStandardBody(%q) = %v", body, got)
		}
	}
	if schema.IsSQLStandardBody("plpgsql", "return 1") {
		t.Error("only language sql has SQL-standard bodies")
	}
	if got := routineBodySQL("sql", "begin atomic select 1; end;\n"); got != "begin atomic select 1; end" {
		t.Errorf("routineBodySQL = %q", got)
	}
	if got := routineBodySQL("sql", "select 1"); got != "as $$\nselect 1\n$$" {
		t.Errorf("routineBodySQL = %q", got)
	}
	cases := []struct {
		body, live      string
		replace, liveStd bool
		want            bool
	}{
		{"begin atomic select 1; end", "BEGIN ATOMIC\n SELECT 1;\nEND", false, true, false},
		{"begin atomic select 2; end", "BEGIN ATOMIC\n SELECT 1;\nEND", true, true, true},
		{"select 1", "BEGIN ATOMIC\n SELECT 1;\nEND", false, true, true},
		{"begin atomic select 1; end", "select 1", false, false, true},
		{"select 1", "select 1", false, false, false},
		{"", "anything", false, true, false},
	}
	for _, c := range cases {
		if got := routineBodyChanged("sql", c.body, c.replace, c.live, c.liveStd); got != c.want {
			t.Errorf("routineBodyChanged(%q, replace=%v, live std=%v) = %v, want %v", c.body, c.replace, c.liveStd, got, c.want)
		}
	}
}

func TestRangeCollationAndSubPartitionRendered(t *testing.T) {
	if got := renderRangeType("app.r", &schema.RangeDef{Subtype: "text", Collation: "C"}); got != `create type "app"."r" as range (subtype = text, collation = "C");` {
		t.Errorf("renderRangeType = %s", got)
	}
	d := &schema.Database{Tables: map[string]*schema.Table{
		"app.r":  {Name: "app.r", Columns: map[string]*schema.Column{"at": {Type: "date"}, "region": {Type: "text"}}, PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"at"}}},
		"app.r1": {Name: "app.r1", PartitionOf: "app.r", Partition: &schema.PartitionSpec{From: []string{"'2025-01-01'"}, To: []string{"'2026-01-01'"}, Modulus: -1, Remainder: -1}, PartitionBy: &schema.PartitionBy{Type: "list", Columns: []string{"region"}}},
	}}
	if sql := Render(Plan(emptyLive(), d, false)); !strings.Contains(sql, `partition of "app"."r" for values from ('2025-01-01') to ('2026-01-01') partition by list ("region");`) {
		t.Errorf("a partition that is itself partitioned needs PARTITION BY:\n%s", sql)
	}
}

func TestKeepExistingOnlyFiltersLockWarnings(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	ws := []Warning{
		{Kind: "lock", Table: "app.t", Message: "existing"},
		{Kind: "lock", Table: "app.new", Message: "created earlier in the plan"},
		{Kind: "public_revoke", Message: "revoke"},
		{Message: "return type changed"},
	}
	got := []string{}
	for _, w := range KeepExisting(ws, live, &schema.Database{}) {
		got = append(got, w.Message)
	}
	if strings.Join(got, "|") != "existing|revoke|return type changed" {
		t.Errorf("KeepExisting kept %q", got)
	}
}
