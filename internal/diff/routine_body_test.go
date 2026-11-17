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

func TestRoutineSourcePlrust(t *testing.T) {
	stored := `{"src":"\nOk(Some(a + b))\n","trusted_pgrx_version":"=1.2.8","lib":{"x86_64-unknown-linux-gnu":{"encoding":"GzBase64","symbol":"plrust_fn_oid_16415_16423","encoded":"H4sI"}}}`
	for _, c := range []struct{ language, prosrc, want string }{
		{"plrust", stored, "\nOk(Some(a + b))\n"},
		{"plrust", `{"src":""}`, ""},
		{"plrust", "\nOk(Some(a + b))\n", "\nOk(Some(a + b))\n"},
		{"plrust", "{ Ok(Some(1)) }", "{ Ok(Some(1)) }"},
		{"plrust", `{"lib":{}}`, `{"lib":{}}`},
		{"plrust", `{"src":1}`, `{"src":1}`},
		{"plv8", stored, stored},
		{"sql", "select 1", "select 1"},
	} {
		if got := routineSource(c.language, c.prosrc); got != c.want {
			t.Errorf("routineSource(%q, %q) = %q, want %q", c.language, c.prosrc, got, c.want)
		}
	}
	if routineBodyChanged("plrust", "Ok(Some(a + b))\n", true, false, routineSource("plrust", stored), false) {
		t.Error("a plrust body is compared with the source in plrust's JSON document")
	}
}

func attributeLive() *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Functions["app.f()"] = true
	live.FunctionDefs["app.f()"] = &LiveFunction{
		Signature: "app.f()", Kind: "f", Body: "select 1", Returns: "SETOF integer", Language: "sql",
		Security: "definer", Volatility: "stable", Strict: true, Leakproof: true, Parallel: "safe", Cost: 5, Rows: 50,
		Config: map[string]string{"work_mem": "1MB", "search_path": `"$user", public`},
	}
	live.Procedures["app.p()"] = true
	live.ProcedureDefs["app.p()"] = &LiveProcedure{Signature: "app.p()", Body: "select 1", Language: "sql", Security: "definer", Config: map[string]string{"statement_timeout": "5s"}}
	return live
}

func attributeDB() *schema.Database {
	return &schema.Database{
		Functions:  map[string]*schema.Function{"app.f()": {Schema: "app", Name: "f", ArgsSig: "()", Returns: "setof int", Language: "sql", Body: "select 2"}},
		Procedures: map[string]*schema.Procedure{"app.p()": {Schema: "app", Name: "p", ArgsSig: "()", Language: "sql", Body: "select 2"}},
	}
}

func TestRoutineReplaceKeepsUnmanagedAttributes(t *testing.T) {
	p := Plan(attributeLive(), attributeDB(), false)
	for _, want := range []string{
		`create or replace function "app"."f"() returns setof int language sql security definer stable strict leakproof parallel safe cost 5 rows 50 set search_path = "$user", public set work_mem = '1MB' as $$select 2$$;`,
		`create or replace procedure "app"."p"() language sql security definer set statement_timeout = '5s' as $$select 2$$;`,
	} {
		if !findCreate(p, want) {
			t.Errorf("missing %s in:\n%s", want, Render(p))
		}
	}
	sim := Simulate(attributeLive(), attributeDB())
	if lf := sim.FunctionDefs["app.f()"]; lf.Body != "select 2" || lf.Security != "definer" || lf.Volatility != "stable" || !lf.Strict || lf.Parallel != "safe" || lf.Cost != 5 || lf.Rows != 50 || lf.Config["work_mem"] != "1MB" {
		t.Errorf("a simulated replace keeps the unmanaged function attributes: %+v", lf)
	}
	if lp := sim.ProcedureDefs["app.p()"]; lp.Body != "select 2" || lp.Security != "definer" || lp.Config["statement_timeout"] != "5s" {
		t.Errorf("a simulated replace keeps the unmanaged procedure attributes: %+v", lp)
	}
}

func TestRoutineReplaceDeclaredAttributesWin(t *testing.T) {
	d := attributeDB()
	no := false
	f := d.Functions["app.f()"]
	f.Security, f.Volatility, f.Strict, f.Leakproof, f.Parallel, f.Set = "invoker", "volatile", &no, &no, "unsafe", map[string]string{}
	f.Cost, f.Rows = ptr(100.0), ptr(1000.0)
	d.Procedures["app.p()"].Security, d.Procedures["app.p()"].Set = "invoker", map[string]string{}
	p := Plan(attributeLive(), d, false)
	for _, want := range []string{
		`create or replace function "app"."f"() returns setof int language sql security invoker volatile parallel unsafe cost 100 rows 1000 as $$select 2$$;`,
		`create or replace procedure "app"."p"() language sql security invoker as $$select 2$$;`,
	} {
		if !findCreate(p, want) {
			t.Errorf("missing %s in:\n%s", want, Render(p))
		}
	}
}

func TestRoutineReplaceLeavesDefaultAttributesOut(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Functions["app.f()"] = true
	live.FunctionDefs["app.f()"] = &LiveFunction{Signature: "app.f()", Kind: "f", Body: "select 1", Returns: "integer", Language: "sql", Security: "invoker", Volatility: "volatile", Parallel: "unsafe", Cost: 100}
	d := &schema.Database{Functions: map[string]*schema.Function{"app.f()": {Schema: "app", Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select 2"}}}
	if p := Plan(live, d, false); !findCreate(p, `create or replace function "app"."f"() returns int language sql as $$select 2$$;`) {
		t.Errorf("default live attributes add nothing to the replace:\n%s", Render(p))
	}
}
