package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestRoutineBodyStoredForms(t *testing.T) {
	for body, want := range map[string]string{
		"select 1":                          "select 1",
		" select 1 ":                        " select 1 ",
		"":                                  "",
		"select 1\n":                        "\nselect 1\n",
		"begin\n  return 1;\nend;":          "\nbegin\n  return 1;\nend;\n",
		"begin\n  return 1;\nend;\n":        "\nbegin\n  return 1;\nend;\n",
		"begin\n  return 1;\nend;\n\n":      "\nbegin\n  return 1;\nend;\n\n",
		"begin\n  return 1;\nend;\n\n\n":    "begin\n  return 1;\nend;",
		"select 1\n\n\n\n":                  "select 1\n",
		"    if a > 0:\n        return 1\n": "\n    if a > 0:\n        return 1\n",
	} {
		if got := StoredRoutineBody(body); got != want {
			t.Errorf("StoredRoutineBody(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestRoutineBodyRoundTrip(t *testing.T) {
	for _, src := range []string{
		"", " ", "\n", "\n\n", "\n\n\n", "\n\n\n\n", "select 1", " select 1 ", "return a", "\tselect 1",
		"\nselect 1\n", "\nselect 1\n\n", "\nselect 1\n\n\n", "select 1\n", "select 1\n\n", "\nselect 1",
		"\nbegin\n  return 1;\nend;\n", "\nbegin\n  return 1;\nend;\n\n", "begin\n  return 1;\nend;",
		"begin\n  return 1;\nend;\n", "\n\nbegin\nend;\n", "\n    if a > 0:\n        return 1\n    return -1\n",
		"\n\tif a > 0:\n\t\treturn 1\n", "\r\nbegin\r\nend;\r\n", "  \n  x\n  ", "a\nb$", "a$$b\n$body$",
	} {
		declared := DeclaredRoutineBody(src)
		if got := StoredRoutineBody(declared); got != src {
			t.Errorf("stored %q -> declared %q -> stored %q", src, declared, got)
		}
		tag := dollarTag(src)
		if sql := routineBodySQL("plpython3u", declared); sql != "as "+tag+src+tag || strings.Index(src+tag, tag) != len(src) {
			t.Errorf("stored %q is quoted as %q", src, sql)
		}
	}
}

func TestRoutineBodyDeclaredForms(t *testing.T) {
	for src, want := range map[string]string{
		"\nbegin\n  return 1;\nend;\n":   "begin\n  return 1;\nend;\n",
		"\nbegin\n  return 1;\nend;\n\n": "begin\n  return 1;\nend;\n\n",
		" select 1 ":                     " select 1 ",
		"begin\n  return 1;\nend;":       "begin\n  return 1;\nend;\n\n\n",
	} {
		if got := DeclaredRoutineBody(src); got != want {
			t.Errorf("DeclaredRoutineBody(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestRoutineBodyFramingNeverDiffs(t *testing.T) {
	for _, body := range []string{"select 1", "select 1\n", "begin\n  return 1;\nend;", "begin\n  return 1;\nend;\n", "x\n\n", "x\n\n\n"} {
		for _, live := range []string{"\n" + body + "\n", StoredRoutineBody(body)} {
			if routineBodyChanged("plpgsql", body, true, false, live, false) {
				t.Errorf("body %q differs from stored %q", body, live)
			}
		}
	}
}

func TestFunctionEmptyBodyCreate(t *testing.T) {
	desired := &schema.Database{Functions: map[string]*schema.Function{
		"app.noop": {Schema: "app", Name: "noop", ArgsSig: "()", Returns: "void", Language: "sql", BodySet: true},
		"app.attr": {Schema: "app", Name: "attr", ArgsSig: "()", Returns: "void", Language: "sql"},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create function "app"."noop"() returns void language sql as $$$$;`) {
		t.Errorf("a function with an empty body must be created:\n%s", Render(p))
	}
	if findCreate(p, `"app"."attr"`) {
		t.Errorf("a function without a body key is attribute-only:\n%s", Render(p))
	}
}

func TestFunctionEmptyBodyReplacesLive(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Functions["app.noop()"] = true
	live.FunctionDefs["app.noop()"] = &LiveFunction{Signature: "app.noop()", Kind: "f", Body: "select 1", Returns: "void", Language: "sql", Volatility: "volatile", Security: "invoker"}
	f := &schema.Function{Schema: "app", Name: "noop", ArgsSig: "()", Returns: "void", Language: "sql", BodySet: true}
	p := Plan(live, &schema.Database{Functions: map[string]*schema.Function{"app.noop": f}}, false)
	if !findCreate(p, `create or replace function "app"."noop"() returns void language sql as $$$$;`) {
		t.Errorf("an empty declared body replaces a live one:\n%s", Render(p))
	}
	live.FunctionDefs["app.noop()"].Body = ""
	if sql := Render(Plan(live, &schema.Database{Functions: map[string]*schema.Function{"app.noop": f}}, false)); sql != "" {
		t.Errorf("an empty body matches an empty live body:\n%s", sql)
	}
}

func TestProcedureEmptyBodyCreate(t *testing.T) {
	desired := &schema.Database{Procedures: map[string]*schema.Procedure{
		"app.noop": {Schema: "app", Name: "noop", ArgsSig: "()", Language: "sql", BodySet: true},
	}}
	if p := Plan(emptyLive(), desired, false); !findCreate(p, `create procedure "app"."noop"() language sql as $$$$;`) {
		t.Errorf("a procedure with an empty body must be created:\n%s", Render(p))
	}
}

func TestFunctionLanguageChangeReplaces(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Functions["app.f()"] = true
	live.FunctionDefs["app.f()"] = &LiveFunction{Signature: "app.f()", Kind: "f", Body: "return 1;", Returns: "integer", Language: "plperlu", Volatility: "volatile", Security: "invoker"}
	f := &schema.Function{Schema: "app", Name: "f", ArgsSig: "()", Returns: "integer", Language: "plperl", Body: "return 1;"}
	desired := &schema.Database{Functions: map[string]*schema.Function{"app.f": f}}
	if p := Plan(live, desired, false); !findCreate(p, `create or replace function "app"."f"() returns integer language plperl as $$return 1;$$;`) {
		t.Errorf("a changed language must replace the function:\n%s", Render(p))
	}
	for _, same := range []string{"plperlu", "PLPerlU", `"plperlu"`, `'plperlu'`, " plperlu "} {
		f.Language = same
		if sql := Render(Plan(live, desired, false)); sql != "" {
			t.Errorf("language %s matches live plperlu:\n%s", same, sql)
		}
	}
	live.FunctionDefs["app.f()"].Language = ""
	f.Language = "plperl"
	if sql := Render(Plan(live, desired, false)); sql != "" {
		t.Errorf("an unknown live language is not compared:\n%s", sql)
	}
}

func TestFunctionLanguageSkippedIfAttributeOnly(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Functions["app.f()"] = true
	live.FunctionDefs["app.f()"] = &LiveFunction{Signature: "app.f()", Kind: "f", Body: "x", Returns: "integer", Language: "c", Volatility: "volatile", Security: "invoker"}
	f := &schema.Function{Schema: "app", Name: "f", ArgsSig: "()", Language: "plpgsql"}
	if sql := Render(Plan(live, &schema.Database{Functions: map[string]*schema.Function{"app.f": f}}, false)); sql != "" {
		t.Errorf("a declaration without a body never replaces the function:\n%s", sql)
	}
}

func TestProcedureLanguageChangeReplaces(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Procedures["app.p()"] = true
	live.ProcedureDefs["app.p()"] = &LiveProcedure{Signature: "app.p()", Body: "select 1", Language: "sql", Security: "invoker"}
	pr := &schema.Procedure{Schema: "app", Name: "p", ArgsSig: "()", Language: "plpgsql", Body: "select 1"}
	desired := &schema.Database{Procedures: map[string]*schema.Procedure{"app.p": pr}}
	if p := Plan(live, desired, false); !findCreate(p, `create or replace procedure "app"."p"() language plpgsql as $$select 1$$;`) {
		t.Errorf("a changed language must replace the procedure:\n%s", Render(p))
	}
	pr.Language = "sql"
	if sql := Render(Plan(live, desired, false)); sql != "" {
		t.Errorf("same language:\n%s", sql)
	}
}
