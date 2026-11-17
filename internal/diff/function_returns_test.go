package diff

import (
	"slices"
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

func TestRoutineBodiesUncheckedWithNewObjects(t *testing.T) {
	d := &schema.Database{
		Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}}},
		Functions: map[string]*schema.Function{
			"app.f()": {Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select count(*)::int from app.t"},
			"app.g()": {Name: "g", ArgsSig: "()", Returns: "int", Language: "plpgsql", Body: "declare r app.t%rowtype; begin return 1; end"},
			"app.h()": {Name: "h", ArgsSig: "()", Returns: "int", Language: "sql", Body: "return 1"},
			"app.j()": {Name: "j", ArgsSig: "()", Returns: "trigger", Language: "java", Body: "org.example.Triggers.j"},
		},
		Procedures: map[string]*schema.Procedure{
			"app.p()": {Name: "p", ArgsSig: "()", Language: "plperl", Body: "return;"},
		},
	}
	creates := Plan(emptyLive(), d, false).Creates
	wantStatements(t, creates[:10], []string{
		`create schema if not exists "app";`,
		`set check_function_bodies = off;`,
		`create function "app"."f"() returns int language sql as $$select count(*)::int from app.t$$;`,
		`create function "app"."g"() returns int language plpgsql as $$declare r app.t%rowtype; begin return 1; end$$;`,
		`reset check_function_bodies;`,
		`create function "app"."h"() returns int language sql return 1;`,
		`set check_function_bodies = off;`,
		`create function "app"."j"() returns trigger language java as $$org.example.Triggers.j$$;`,
		`create procedure "app"."p"() language plperl as $$return;$$;`,
		`reset check_function_bodies;`,
	})
}

func bodyCheckLive() *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	live.Tables["app.t"] = lt
	for _, name := range []string{"f", "g"} {
		sig := "app." + name + "()"
		live.Functions[sig] = true
		live.FunctionDefs[sig] = &LiveFunction{Signature: sig, Kind: "f", Body: "select 1", Returns: "integer", Language: "sql", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100}
		live.FunctionPublicExec[sig] = true
	}
	return live
}

func bodyCheckDB(bodies map[string]string) *schema.Database {
	d := &schema.Database{
		Tables:    map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}}},
		Functions: map[string]*schema.Function{},
	}
	for name, body := range bodies {
		d.Functions["app."+name+"()"] = &schema.Function{Name: name, ArgsSig: "()", Returns: "int", Language: "plpgsql", Body: body}
	}
	return d
}

func TestRoutineBodyCheckedWhenOnlyRoutineChanges(t *testing.T) {
	d := bodyCheckDB(map[string]string{"f": "begin return 2; end", "g": "select 1"})
	d.Functions["app.g()"].Language = "sql"
	f := d.Functions["app.f()"]
	f.Comment, f.Owner, f.Grants = "two", "app_owner", map[string][]string{"app_user": {"execute"}}
	f.Volatility = "stable"
	p := Plan(bodyCheckLive(), d, false)
	if sql := Render(p); strings.Contains(sql, "check_function_bodies") || !strings.Contains(sql, `create or replace function "app"."f"()`) {
		t.Errorf("a plan that replaces one routine and changes only its privileges, comment and owner keeps body checks:\n%s", sql)
	}
}

func TestRoutineBodiesUncheckedWithSeveralRoutines(t *testing.T) {
	d := bodyCheckDB(map[string]string{"f": "begin return 2; end", "g": "begin return app.f(); end"})
	wantStatements(t, Plan(bodyCheckLive(), d, false).Creates, []string{
		`set check_function_bodies = off;`,
		`create or replace function "app"."f"() returns int language plpgsql as $$begin return 2; end$$;`,
		`create or replace function "app"."g"() returns int language plpgsql as $$begin return app.f(); end$$;`,
		`reset check_function_bodies;`,
	})
}

func TestRoutineBodyUncheckedWithOtherChanges(t *testing.T) {
	for name, change := range map[string]func(*schema.Database){
		"new table": func(d *schema.Database) {
			d.Tables["app.u"] = &schema.Table{Name: "u", Columns: map[string]*schema.Column{"id": {Type: "int"}}}
		},
		"new column": func(d *schema.Database) {
			d.Tables["app.t"].Columns["note"] = &schema.Column{Type: "text", Nullable: true}
		},
		"new type": func(d *schema.Database) {
			d.Types = map[string]*schema.TypeDef{"app.mood": {Kind: "enum", Labels: []string{"ok"}}}
		},
		"new view":      func(d *schema.Database) { d.Views = map[string]*schema.View{"app.v": {Query: "select id from app.t"}} },
		"new extension": func(d *schema.Database) { d.Extensions = []*schema.Extension{{Name: "hstore"}} },
		"SQL-standard routine": func(d *schema.Database) {
			d.Functions["app.h()"] = &schema.Function{Name: "h", ArgsSig: "()", Returns: "int", Language: "sql", Body: "return 1"}
		},
	} {
		d := bodyCheckDB(map[string]string{"f": "declare r app.t%rowtype; begin return 2; end", "g": "select 1"})
		d.Functions["app.g()"].Language = "sql"
		change(d)
		creates := Plan(bodyCheckLive(), d, false).Creates
		at := slices.Index(creates, `create or replace function "app"."f"() returns int language plpgsql as $$declare r app.t%rowtype; begin return 2; end$$;`)
		if at < 1 || creates[at-1] != "set check_function_bodies = off;" || at+1 >= len(creates) || creates[at+1] != "reset check_function_bodies;" {
			t.Errorf("%s: the replaced routine must be created without body checks:\n%s", name, strings.Join(creates, "\n"))
		}
		if strings.Count(strings.Join(creates, "\n"), "check_function_bodies = off") != 1 {
			t.Errorf("%s: only the string body is unchecked:\n%s", name, strings.Join(creates, "\n"))
		}
	}
}

func TestBodyNeutralStatements(t *testing.T) {
	for stmt, want := range map[string]bool{
		`comment on function "app"."f"() is 'x';`:                        true,
		`grant execute on function "app"."f"() to "r";`:                  true,
		`revoke all on function "app"."f"() from public;`:                true,
		`alter function "app"."f"() owner to "o";`:                       true,
		`alter table "app"."t" owner to "o";`:                            true,
		`alter function "app"."f"() stable;`:                             true,
		`drop view "app"."v";`:                                           true,
		`create table "app"."u" ("id" int);`:                             false,
		`alter table "app"."t" add column "note" text;`:                  false,
		`create or replace view "app"."v" as select 1;`:                  false,
		`alter type "app"."mood" add value 'sad';`:                       false,
		`create function "app"."h"() returns int language sql return 1;`: false,
	} {
		if got := bodyNeutral(stmt); got != want {
			t.Errorf("bodyNeutral(%q) = %v, want %v", stmt, got, want)
		}
	}
}

func TestSQLStandardBodies(t *testing.T) {
	for body, want := range map[string]bool{
		"begin atomic select 1; end":    true,
		"BEGIN\n  ATOMIC select 1; END": true,
		"return x * 2":                  true,
		"select 1":                      false,
		"begin return 1; end":           false,
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
	if got := routineBodySQL("sql", "select 1"); got != "as $$select 1$$" {
		t.Errorf("routineBodySQL = %q", got)
	}
	if got := routineBodySQL("sql", "select 1\n"); got != "as $$\nselect 1\n$$" {
		t.Errorf("routineBodySQL = %q", got)
	}
	cases := []struct {
		body, live       string
		replace, liveStd bool
		want             bool
	}{
		{"begin atomic select 1; end", "BEGIN ATOMIC\n SELECT 1;\nEND", false, true, false},
		{"begin atomic select 2; end", "BEGIN ATOMIC\n SELECT 1;\nEND", true, true, true},
		{"select 1", "BEGIN ATOMIC\n SELECT 1;\nEND", false, true, true},
		{"begin atomic select 1; end", "select 1", false, false, true},
		{"select 1", "select 1", false, false, false},
		{"", "anything", false, true, false},
	}
	for _, c := range cases {
		if got := routineBodyChanged("sql", c.body, c.body != "", c.replace, c.live, c.liveStd); got != c.want {
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
