package pull

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestExactScalarsReadBack(t *testing.T) {
	for _, s := range []string{
		"", " ", "select 1", " select 1 ", "null", "1", "true", "~", "a: b", "- x", "# x", "'quoted'", `"quoted"`,
		"begin\n  return 1;\nend;\n", "begin\n  return 1;\nend;", "begin\n  return 1;\nend;\n\n\n",
		"    if a > 0:\n        return 1\n    return -1\n", "\tif a > 0:\n\t\treturn 1\n", "begin\n\treturn 1;\nend;\n",
		"\nselect 1\n", "select 1   \n  + 1\n", "select 1\r\n", "x\n ", " \nx", "a\n\n\nb\n", "é\n☃\n",
	} {
		doc := newMap()
		inner := newMap()
		add(inner, "body", exact(s))
		add(doc, "function f()", inner)
		out, err := render(doc)
		if err != nil {
			t.Fatal(err)
		}
		var back map[string]map[string]any
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Errorf("%q does not parse back: %v\n%s", s, err, out)
			continue
		}
		if got := back["function f()"]["body"]; got != any(s) {
			t.Errorf("%q reads back as %#v from:\n%s", s, got, out)
		}
	}
}

func TestExactPrefersBlockScalars(t *testing.T) {
	for s, want := range map[string]string{
		"begin\n  return 1;\nend;\n":                 "|\n",
		"begin\n  return 1;\nend;":                   "|-\n",
		"    if a > 0:\n        return 1\n":          "|2\n",
		"select 1":                                   "body: select 1\n",
		"if a > 0:\n    return 1\nreturn -1\n\n\n\n": "|+\n",
	} {
		doc := newMap()
		add(doc, "body", exact(s))
		out, err := render(doc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), want) {
			t.Errorf("%q should render with %q:\n%s", s, want, out)
		}
	}
}

func routineLive() *diff.Live {
	return &diff.Live{
		Schemas:            map[string]bool{"app": true},
		Functions:          map[string]bool{},
		FunctionDefs:       map[string]*diff.LiveFunction{},
		Procedures:         map[string]bool{},
		ProcedureDefs:      map[string]*diff.LiveProcedure{},
		AggregateDefs:      map[string]*diff.LiveAggregate{},
		FunctionPublicExec: map[string]bool{},
		Details:            &diff.LiveDetails{Routines: map[string]*diff.LiveRoutine{}, TransformLanguages: map[string]bool{}},
	}
}

func addFunction(live *diff.Live, name, args, language, src string, transform []string) {
	sig := "app." + name + "(" + args + ")"
	live.Functions[sig] = true
	live.FunctionDefs[sig] = &diff.LiveFunction{Signature: sig, Kind: "f", Body: src, Returns: "integer", Language: language, Transform: transform, Volatility: "volatile", Security: "invoker", Parallel: "unsafe"}
	live.FunctionPublicExec[sig] = true
	live.Details.Routines[sig] = &diff.LiveRoutine{Schema: "app", Name: name, Arguments: args, Returns: "integer", Language: language}
}

func buildSingle(t *testing.T, live *diff.Live) (string, *schema.Database, []string) {
	t.Helper()
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Files[0].Content)
	path := filepath.Join(t.TempDir(), "schema.yml")
	if err := os.WriteFile(path, res.Files[0].Content, 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{path})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return out, d, res.Warnings
}

func TestPulledBodiesAreRecreatedByteForByte(t *testing.T) {
	sources := map[string]string{
		"pgdump":     "\nbegin\n  return 1;\nend;\n",
		"oldpgy":     "\nbegin\n  return 1;\nend;\n\n",
		"indented":   "\n    if a > 0:\n        return 1\n    return -1\n",
		"tabbed":     "\n\tif a > 0:\n\t\treturn 1\n",
		"padded":     " select 1 ",
		"oneline":    "select 1",
		"unframed":   "select 1\n  + 1",
		"trailing":   "select 1\n",
		"crlf":       "\r\nselect 1\r\n",
		"empty":      "",
		"blank":      "  ",
		"newline":    "\n",
		"spaces_eol": "\nselect 1   \n  + 1\n",
	}
	live := routineLive()
	for name, src := range sources {
		addFunction(live, name, "", "plpython3u", src, nil)
	}
	out, d, _ := buildSingle(t, live)
	for name, src := range sources {
		f := d.Functions["app."+name+"()"]
		if f == nil {
			t.Fatalf("function %s not pulled:\n%s", name, out)
		}
		if !f.HasBody() {
			t.Errorf("function %s must be pulled with a body:\n%s", name, out)
		}
		if got := diff.StoredRoutineBody(f.Body); got != src {
			t.Errorf("function %s: stored %q, pulled %q, recreated %q", name, src, f.Body, got)
		}
	}
	for _, want := range []string{"function pgdump():\n    returns: integer\n    language: plpython3u\n    body: |\n      begin\n        return 1;\n      end;\n", "body: select 1\n", "body: ' select 1 '\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPulledSQLStandardBodyStaysTrimmed(t *testing.T) {
	live := routineLive()
	addFunction(live, "std", "", "sql", "BEGIN ATOMIC\n SELECT 1;\nEND", nil)
	live.FunctionDefs["app.std()"].SQLBody = true
	out, _, _ := buildSingle(t, live)
	if !strings.Contains(out, "body: |-\n      BEGIN ATOMIC\n       SELECT 1;\n      END\n") {
		t.Errorf("SQL-standard body:\n%s", out)
	}
}

func TestPulledTransforms(t *testing.T) {
	live := routineLive()
	live.Details.TransformLanguages["plpython3u"] = true
	addFunction(live, "both", "", "plpython3u", "return 1", []string{"hstore", "jsonb"})
	addFunction(live, "none", "", "plpython3u", "return 1", nil)
	addFunction(live, "other", "", "plpgsql", "begin return 1; end", nil)
	live.Procedures["app.proc()"] = true
	live.ProcedureDefs["app.proc()"] = &diff.LiveProcedure{Signature: "app.proc()", Body: "return", Language: "plpython3u", Transform: []string{"ext.ltree"}, Security: "invoker"}
	live.FunctionPublicExec["app.proc()"] = true
	live.Details.Routines["app.proc()"] = &diff.LiveRoutine{Schema: "app", Name: "proc", Language: "plpython3u", Procedure: true}
	out, d, _ := buildSingle(t, live)
	for key, want := range map[string]string{"app.both()": "hstore,jsonb", "app.none()": ""} {
		f := d.Functions[key]
		if f == nil || f.Transform == nil || strings.Join(f.Transform, ",") != want {
			t.Errorf("function %s: %+v in:\n%s", key, f, out)
		}
	}
	if f := d.Functions["app.other()"]; f == nil || f.Transform != nil {
		t.Errorf("a language without transforms gets no transform key:\n%s", out)
	}
	if p := d.Procedures["app.proc()"]; p == nil || strings.Join(p.Transform, ",") != "ext.ltree" {
		t.Errorf("procedure transform:\n%s", out)
	}
}

func TestPulledAggregateEmptyInitCond(t *testing.T) {
	live := routineLive()
	options := map[string]string{"sspace": "0", "finalfunc_extra": "false", "finalfunc_modify": "read_only", "msspace": "0", "mfinalfunc_extra": "false", "mfinalfunc_modify": "read_only", "hypothetical": "false"}
	for name, la := range map[string]*diff.LiveAggregate{
		"empty": {SFunc: "app.cat", SType: "text", Parallel: "unsafe", HasInitCond: true, HasMInitCond: true, Options: options},
		"unset": {SFunc: "app.cat", SType: "text", Parallel: "unsafe", Options: options},
	} {
		sig := "app." + name + "(text)"
		live.AggregateDefs[sig] = la
		live.FunctionDefs[sig] = &diff.LiveFunction{Signature: sig, Kind: "a"}
	}
	out, d, _ := buildSingle(t, live)
	empty, unset := d.Aggregates["app.empty(text)"], d.Aggregates["app.unset(text)"]
	if empty == nil || unset == nil {
		t.Fatalf("aggregates not pulled:\n%s", out)
	}
	if _, minitcond := empty.Options["minitcond"]; !empty.InitCondSet || !minitcond {
		t.Errorf("empty initial conditions must be pulled:\n%s", out)
	}
	if _, minitcond := unset.Options["minitcond"]; unset.HasInitCond() || minitcond {
		t.Errorf("no initial condition must stay out of the YAML:\n%s", out)
	}
}

func TestUnsupportedDatabaseWideObjectsReported(t *testing.T) {
	for _, opts := range []Options{{}, {Schemas: []string{"app"}}} {
		live := routineLive()
		live.Details.Unsupported = []diff.UnsupportedObject{
			{Kind: "language", Name: "plsample"},
			{Kind: "transform", Name: "for hstore language plsample"},
			{Kind: "foreign-data wrapper", Name: "pg2"},
			{Kind: "operator", Schema: "other", Name: "===(integer, integer)"},
		}
		res, err := Build(live, opts)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(res.Warnings, "\n")
		for _, want := range []string{
			"language plsample is not supported by pgy and was left out",
			"transform for hstore language plsample is not supported by pgy and was left out",
			"foreign-data wrapper pg2 is not supported by pgy and was left out",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%+v: missing %q in:\n%s", opts, want, got)
			}
		}
		if strings.Contains(got, "operator") != (len(opts.Schemas) == 0) {
			t.Errorf("%+v: schema objects follow the schema filter:\n%s", opts, got)
		}
	}
}

func TestCompiledRoutinesNotPulled(t *testing.T) {
	live := routineLive()
	addFunction(live, "handler", "", "c", "plpgsql_call_handler", nil)
	addFunction(live, "to_pl", "internal", "internal", "int4in", nil)
	out, d, warnings := buildSingle(t, live)
	if len(d.Functions) != 0 {
		t.Errorf("C and internal functions cannot be declared:\n%s", out)
	}
	got := strings.Join(warnings, "\n")
	for _, want := range []string{"function app.handler is written in c and cannot be pulled", "function app.to_pl is written in internal and cannot be pulled"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPulledSecurityDefinerSettings(t *testing.T) {
	live := routineLive()
	addFunction(live, "plain", "", "plpgsql", "begin return 1; end", nil)
	addFunction(live, "definer", "", "plpgsql", "begin return 1; end", nil)
	live.FunctionDefs["app.definer()"].Security = "definer"
	addFunction(live, "pinned", "", "plpgsql", "begin return 1; end", nil)
	live.FunctionDefs["app.pinned()"].Security = "definer"
	live.FunctionDefs["app.pinned()"].Config = map[string]string{"search_path": "app"}
	live.Procedures["app.proc()"] = true
	live.ProcedureDefs["app.proc()"] = &diff.LiveProcedure{Signature: "app.proc()", Body: "select 1", Language: "sql", Security: "definer"}
	live.FunctionPublicExec["app.proc()"] = true
	live.Details.Routines["app.proc()"] = &diff.LiveRoutine{Schema: "app", Name: "proc", Language: "sql", Procedure: true}
	out, d, _ := buildSingle(t, live)
	if f := d.Functions["app.plain()"]; f == nil || f.Set != nil {
		t.Errorf("a SECURITY INVOKER function without settings gets no set key:\n%s", out)
	}
	if f := d.Functions["app.definer()"]; f == nil || f.Set == nil || len(f.Set) != 0 {
		t.Errorf("a SECURITY DEFINER function without settings gets set: {}:\n%s", out)
	}
	if f := d.Functions["app.pinned()"]; f == nil || len(f.Set) != 1 || f.Set["search_path"] != "app" {
		t.Errorf("a SECURITY DEFINER function keeps its settings:\n%s", out)
	}
	if p := d.Procedures["app.proc()"]; p == nil || p.Set == nil || len(p.Set) != 0 {
		t.Errorf("a SECURITY DEFINER procedure without settings gets set: {}:\n%s", out)
	}
	if strings.Count(out, "set: {}\n") != 2 {
		t.Errorf("want set: {} on the two SECURITY DEFINER routines without settings:\n%s", out)
	}
}
