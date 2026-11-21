package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func unknownSettingDatabase() *schema.Database {
	return &schema.Database{
		Tables:           map[string]*schema.Table{},
		DatabaseSettings: map[string]string{"escape_string_warning": "off", "work_mem": "8MB"},
		DatabaseRoleSettings: map[string]map[string]string{
			"app": {"Escape_String_Warning": "off", "statement_timeout": "5s"},
		},
		Roles: map[string]*schema.Role{
			"app": {Name: "app", ConnectionLimit: -1, Managed: map[string]bool{}, Set: map[string]string{"escape_string_warning": "off", "lock_timeout": "1s"}},
		},
		Functions: map[string]*schema.Function{
			"public.f()": {Schema: "public", Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select 1",
				Set: map[string]string{"escape_string_warning": "off", "search_path": "public"}, SetOrder: []string{"search_path", "escape_string_warning"}},
		},
		Procedures: map[string]*schema.Procedure{
			"public.p()": {Schema: "public", Name: "p", ArgsSig: "()", Language: "sql", Body: "select 1", Set: map[string]string{"escape_string_warning": "off"}},
		},
	}
}

func TestUnknownSettingsLeftOut(t *testing.T) {
	d := unknownSettingDatabase()
	live := emptyLive()
	live.ServerVersion = 190000
	live.UnknownSettings = map[string]bool{"escape_string_warning": true}
	p := Plan(live, d, false)
	sql := Render(p)
	if strings.Contains(strings.ToLower(sql), "escape_string_warning") {
		t.Errorf("a setting the server does not know must be left out:\n%s", sql)
	}
	for _, want := range []string{
		`alter database %I set work_mem = ''8MB''`,
		`alter role "app" in database %I set statement_timeout = ''5s''`,
		`alter role "app" set lock_timeout = '1s';`,
		`create function "public"."f"() returns int language sql set search_path = public as`,
		`create procedure "public"."p"() language sql as`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
	var got []string
	for _, w := range p.Warnings {
		if strings.Contains(w.Message, "is not a parameter PostgreSQL 19 knows") {
			got = append(got, w.Message)
		}
	}
	for _, where := range []string{"database.set: escape_string_warning", "database.roles.app: Escape_String_Warning", "role app's set: escape_string_warning", "function public.f()'s set: escape_string_warning", "procedure public.p()'s set: escape_string_warning"} {
		found := false
		for _, m := range got {
			found = found || strings.HasPrefix(m, where+" ")
		}
		if !found {
			t.Errorf("missing a warning for %s in %q", where, got)
		}
	}
	if len(got) != 5 {
		t.Errorf("want 5 warnings, got %q", got)
	}
	if fresh := unknownSettingDatabase(); len(d.DatabaseSettings) != len(fresh.DatabaseSettings) || len(d.Functions["public.f()"].Set) != 2 || len(d.Functions["public.f()"].SetOrder) != 2 || len(d.Roles["app"].Set) != 2 || len(d.DatabaseRoleSettings["app"]) != 2 || len(d.Procedures["public.p()"].Set) != 1 {
		t.Error("the plan must not change the declared settings")
	}
}

func TestUnknownSettingsUncheckedArePlanned(t *testing.T) {
	sql := Render(Plan(emptyLive(), unknownSettingDatabase(), false))
	for _, want := range []string{
		`alter database %I set escape_string_warning = off`,
		`alter role "app" set escape_string_warning = off;`,
		`set search_path = public set escape_string_warning = off`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("settings must be planned when they were not checked, missing %q in\n%s", want, sql)
		}
	}
}

func TestUnknownSettingsKeepFunctionUnchanged(t *testing.T) {
	d := unknownSettingDatabase()
	live := emptyLive()
	live.ServerVersion = 190000
	live.UnknownSettings = map[string]bool{"escape_string_warning": true}
	live.Functions["public.f()"] = true
	live.FunctionDefs[normalizeFunctionSignature("public.f()")] = &LiveFunction{Signature: "public.f()", Kind: "f", Body: "select 1", Returns: "integer", Language: "sql",
		Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100, Config: map[string]string{"search_path": "public"}, ConfigOrder: []string{"search_path"}}
	sql := Render(Plan(live, d, false))
	if strings.Contains(sql, `"public"."f"`) {
		t.Errorf("a function whose only difference is an unknown setting must be left as it is:\n%s", sql)
	}
}

func TestUnknownSettingsSimulatedKeptQuietly(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 190000
	live.UnknownSettings = map[string]bool{"escape_string_warning": true}
	live.CurrentUser = "deployer"
	p := Plan(Simulate(live, unknownSettingDatabase()), unknownSettingDatabase(), false)
	if sql := Render(p); strings.Contains(strings.ToLower(sql), "escape_string_warning") {
		t.Errorf("a later split step must neither set nor reset the unknown setting:\n%s", sql)
	}
	for _, w := range p.Warnings {
		if strings.Contains(w.Message, "is not a parameter") {
			t.Errorf("a later split step must not warn again: %s", w.Message)
		}
	}
}
