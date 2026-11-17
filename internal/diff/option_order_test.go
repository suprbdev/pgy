package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestRoutineSettingsInYAMLOrder(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	d := &schema.Database{
		Functions: map[string]*schema.Function{"app.f()": {Schema: "app", Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select 1",
			Set: map[string]string{"work_mem": "1MB", "search_path": "app, public", "statement_timeout": "5s"}, SetOrder: []string{"work_mem", "search_path"}}},
		Procedures: map[string]*schema.Procedure{"app.p()": {Schema: "app", Name: "p", ArgsSig: "()", Language: "sql", Body: "select 1",
			Set: map[string]string{"statement_timeout": "5s", "lock_timeout": "1s"}, SetOrder: []string{"statement_timeout", "lock_timeout"}}},
	}
	p := Plan(live, d, false)
	for _, want := range []string{
		`create function "app"."f"() returns int language sql set work_mem = '1MB' set search_path = app, public set statement_timeout = '5s' as $$select 1$$;`,
		`create procedure "app"."p"() language sql set statement_timeout = '5s' set lock_timeout = '1s' as $$select 1$$;`,
	} {
		if !findCreate(p, want) {
			t.Errorf("missing %s in:\n%s", want, Render(p))
		}
	}
}

func TestRoutineReplaceKeepsLiveSettingOrder(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Functions["app.f()"] = true
	live.FunctionDefs["app.f()"] = &LiveFunction{Signature: "app.f()", Kind: "f", Body: "select 1", Returns: "integer", Language: "sql", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100,
		Config: map[string]string{"work_mem": "1MB", "lock_timeout": "1s"}, ConfigOrder: []string{"work_mem", "lock_timeout"}}
	d := &schema.Database{Functions: map[string]*schema.Function{"app.f()": {Schema: "app", Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select 2"}}}
	if p := Plan(live, d, false); !findCreate(p, `create or replace function "app"."f"() returns int language sql set work_mem = '1MB' set lock_timeout = '1s' as $$select 2$$;`) {
		t.Errorf("a replace keeps unmanaged settings in their live order:\n%s", Render(p))
	}
}

func TestDictionaryOptionsInYAMLOrder(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	d := &schema.Database{TSDictionaries: map[string]*schema.TSDictionary{"app.d": {Schema: "app", Name: "d", Template: "simple",
		Options: map[string]string{"stopwords": "english", "accept": "false"}, OptionOrder: []string{"stopwords", "accept"}}}}
	if p := Plan(live, d, false); !findCreate(p, `create text search dictionary "app"."d" (template = simple, stopwords = english, accept = false);`) {
		t.Errorf("options are emitted in YAML order:\n%s", Render(p))
	}

	live.TSDictionaries = map[string]bool{"app.d": true}
	live.TSDictionaryDefs = map[string]*schema.TSDictionary{"app.d": {Template: "simple", Options: map[string]string{"stopwords": "'russian'", "b": "1", "a": "2"}, OptionOrder: []string{"stopwords", "b", "a"}}}
	d.TSDictionaries["app.d"].Options["zz"] = "1"
	d.TSDictionaries["app.d"].OptionOrder = []string{"zz", "stopwords", "accept"}
	stmts, _ := tsDictionaryAlterStmts("app.d", d.TSDictionaries["app.d"], live.TSDictionaryDefs["app.d"])
	if want := []string{`alter text search dictionary "app"."d" (zz = 1, stopwords = english, accept = false, b, a);`}; !reflect.DeepEqual(stmts, want) {
		t.Errorf("alter = %v, want %v", stmts, want)
	}
	live.TSDictionaryDefs["app.d"] = &schema.TSDictionary{Template: "simple", Options: map[string]string{"accept": "false", "stopwords": "english", "zz": "1"}, OptionOrder: []string{"accept", "zz", "stopwords"}}
	if sql := Render(Plan(live, d, false)); strings.Contains(sql, "dictionary") {
		t.Errorf("the comparison ignores the order:\n%s", sql)
	}
}

func TestExtensionDictionaryOptionsInYAMLOrder(t *testing.T) {
	live := emptyLive()
	live.Schemas["public"] = true
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "dict_int", Schema: "public"}},
		TSDictionaries: map[string]*schema.TSDictionary{"public.intdict": {Schema: "public", Name: "intdict",
			Options: map[string]string{"rejectlong": "true", "maxlen": "4"}, OptionOrder: []string{"rejectlong", "maxlen"}}},
	}
	if p := Plan(live, d, false); !findAlter(p, `alter text search dictionary "public"."intdict" (rejectlong = true, maxlen = 4);`) {
		t.Errorf("the options of an extension's dictionary are set in YAML order:\n%s", Render(p))
	}
}

func TestParseDictOptionsOrder(t *testing.T) {
	opts, order := parseDictOptions(`stopwords = 'english', accept = 'false', "Odd" = 'a, b'`)
	if !reflect.DeepEqual(order, []string{"stopwords", "accept", `"Odd"`}) || opts["accept"] != "'false'" || opts[`"Odd"`] != "'a, b'" {
		t.Errorf("parseDictOptions = %v %v", opts, order)
	}
}
