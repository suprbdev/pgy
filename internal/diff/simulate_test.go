package diff

import (
    "path/filepath"
    "strings"
    "testing"

    "github.com/suprbdev/pgy/internal/schema"
)

func exampleFiles(t *testing.T, patterns ...string) []string {
    t.Helper()
    var out []string
    for _, p := range patterns {
        m, err := filepath.Glob(filepath.Join("..", "..", "examples", p))
        if err != nil { t.Fatal(err) }
        out = append(out, m...)
    }
    if len(out) == 0 { t.Fatalf("no example files for %v", patterns) }
    return out
}

func planSize(p *PlanDiff) int { return len(p.Creates) + len(p.Alters) + len(p.Drops) }

func TestSimulateConvergesOnExamples(t *testing.T) {
    cases := map[string][]string{
        "kitchen-sink":  exampleFiles(t, "schema.yml"),
        "modules+links": exampleFiles(t, "modules/*/*.yml", "links/*.yml"),
    }
    for name, paths := range cases {
        d, err := schema.LoadAndMerge(paths)
        if err != nil { t.Fatalf("%s: %v", name, err) }
        for _, vw := range d.Views { vw.Replace = false }
        for _, unsafe := range []bool{false, true} {
            p := Plan(Simulate(emptyLive(), d), d, unsafe)
            if planSize(p) != 0 {
                t.Errorf("%s (unsafe=%v): expected no changes after simulate, got:\n%s", name, unsafe, Render(p))
            }
        }
    }
}

func TestSimulateNilLiveAndDesired(t *testing.T) {
    d := &schema.Database{Tables: map[string]*schema.Table{
        "app.t": {Columns: map[string]*schema.Column{"id": {Type: "int", PrimaryKey: true}}},
    }}
    sim := Simulate(nil, d)
    if !sim.Schemas["app"] || sim.Tables["app.t"] == nil || !sim.Tables["app.t"].HasPK {
        t.Fatalf("expected schema, table and pk in simulated live: %+v", sim.Tables["app.t"])
    }
    if planSize(Plan(sim, d, false)) != 0 { t.Fatal("expected converged plan") }
    if planSize(Plan(Simulate(nil, nil), d, false)) == 0 { t.Fatal("expected changes against empty simulation") }
}

func TestSimulateDoesNotMutateLive(t *testing.T) {
    live := liveWithTable("public.t", map[string]*LiveColumn{"a": {Type: "int"}})
    d := &schema.Database{Tables: map[string]*schema.Table{
        "public.t": {Columns: map[string]*schema.Column{"a": {Type: "bigint"}, "b": {Type: "text"}}},
        "public.u": {Columns: map[string]*schema.Column{"id": {Type: "int"}}},
    }}
    sim := Simulate(live, d)
    lt := live.Tables["public.t"]
    if lt.Columns["b"] != nil || lt.Columns["a"].Type != "int" || live.Tables["public.u"] != nil {
        t.Fatal("simulate mutated the input live state")
    }
    if sim.Tables["public.t"].Columns["b"] == nil || sim.Tables["public.u"] == nil {
        t.Fatal("simulated live missing desired objects")
    }
}

func TestSimulateKeepsLiveOnlyObjects(t *testing.T) {
    live := liveWithTable("public.t", map[string]*LiveColumn{
        "a":      {Type: "int"},
        "legacy": {Type: "text", Nullable: true},
    })
    live.Tables["public.t"].Policies = map[string]*LivePolicy{"old_p": {Cmd: "all", Using: "true"}}
    d := &schema.Database{Tables: map[string]*schema.Table{
        "public.t": {
            Columns:  map[string]*schema.Column{"a": {Type: "int"}},
            Policies: []*schema.Policy{{Name: "new_p", Using: "a > 0"}},
        },
    }}
    sim := Simulate(live, d)
    st := sim.Tables["public.t"]
    if st.Columns["legacy"] == nil { t.Fatal("live-only column dropped by simulate") }
    if st.Policies["old_p"] == nil || st.Policies["new_p"] == nil { t.Fatalf("expected live and desired policies, got %v", st.Policies) }

    p := Plan(sim, d, true)
    drops := strings.Join(p.Drops, "\n")
    if !strings.Contains(drops, `drop column "legacy"`) || !strings.Contains(drops, `drop policy "old_p"`) {
        t.Fatalf("expected live-only objects to still be dropped, got:\n%s", Render(p))
    }
    if len(p.Creates)+len(p.Alters) != 0 { t.Fatalf("expected only drops, got:\n%s", Render(p)) }
}

func TestSimulateFunctionAttributes(t *testing.T) {
    strict := true
    live := emptyLive()
    live.Functions["public.f()"] = true
    live.FunctionDefs["public.f()"] = &LiveFunction{Body: "select 1", Volatility: "stable", Security: "invoker"}
    d := &schema.Database{Functions: map[string]*schema.Function{
        "public.f":    {Schema: "public", Name: "f", ArgsSig: "()", Strict: &strict},
        "public.g":    {Schema: "public", Name: "g", ArgsSig: "(x int)", Returns: "int", Language: "sql", Body: "select x", RevokePublic: true},
        "public.none": {Schema: "public", Name: "none", ArgsSig: "()", Strict: &strict, RevokePublic: true},
    }}
    sim := Simulate(live, d)
    if f := sim.FunctionDefs["public.f()"]; f.Body != "select 1" || f.Volatility != "stable" || !f.Strict {
        t.Fatalf("attribute-only declaration should keep live body/volatility and apply strict: %+v", f)
    }
    if sim.FunctionDefs[normalizeFunctionSignature("public.none()")] != nil {
        t.Fatal("attribute-only declaration of a missing function must not be simulated as created")
    }
    if sim.FunctionPublicExec[normalizeFunctionSignature("public.g(x int)")] {
        t.Fatal("revokePublic should clear PUBLIC execute")
    }
    if p := Plan(sim, d, false); planSize(p) != 0 {
        t.Fatalf("expected converged plan, got:\n%s", Render(p))
    }
}

func TestSimulateFunctionAndProcedureSetConverge(t *testing.T) {
    d := &schema.Database{
        Tables: map[string]*schema.Table{},
        Functions: map[string]*schema.Function{
            "public.f": {Schema: "public", Name: "f", ArgsSig: "()", Returns: "int", Language: "sql",
                Set: map[string]string{"work_mem": "'64MB'"}, Body: "select 1"},
        },
        Procedures: map[string]*schema.Procedure{
            "public.p": {Schema: "public", Name: "p", ArgsSig: "()", Language: "plpgsql",
                Set: map[string]string{"search_path": "public"}, Body: "begin null; end;"},
        },
    }
    if p := Plan(Simulate(emptyLive(), d), d, false); planSize(p) != 0 {
        t.Fatalf("expected no changes after simulate, got:\n%s", Render(p))
    }

    live := emptyLive()
    live.Functions["public.f()"] = true
    live.FunctionDefs["public.f()"] = &LiveFunction{Body: "select 1", Volatility: "volatile", Security: "invoker",
        Config: map[string]string{"work_mem": "32MB", "search_path": "public"}}
    if p := Plan(Simulate(live, d), d, false); planSize(p) != 0 {
        t.Fatalf("expected simulated SET/RESET to converge, got:\n%s", Render(p))
    }
}

func TestSimulateFunctionArgumentDefaultConverges(t *testing.T) {
    for _, c := range []struct {
        name, liveArgs, args string
        unsafe               bool
    }{
        {"changed", "a integer DEFAULT 1", "(a int default 2)", false},
        {"added", "a integer", "(a int default 3)", false},
        {"removed", "a integer DEFAULT 1", "(a int)", true},
    } {
        norm := normalizeFunctionSignature("app.f(a int)")
        live := emptyLive()
        live.Functions["app.f(a integer)"] = true
        live.FunctionDefs[norm] = &LiveFunction{Signature: "app.f(a integer)", Args: c.liveArgs, Kind: "f",
            Body: "select a", Returns: "integer", Language: "sql", Volatility: "stable", Security: "invoker", Parallel: "safe", Cost: 5}
        d := &schema.Database{Functions: map[string]*schema.Function{
            "app.f" + c.args: {Schema: "app", Name: "f", ArgsSig: c.args, Returns: "int", Language: "sql", Body: "select a"},
        }}
        if p := Plan(live, d, c.unsafe); planSize(p) == 0 {
            t.Fatalf("%s: the argument default has to be planned", c.name)
        }
        sim := Simulate(live, d)
        if p := Plan(sim, d, c.unsafe); planSize(p) != 0 {
            t.Errorf("%s: a plan after the simulated one must be empty, got:\n%s", c.name, Render(p))
        }
        lf := sim.FunctionDefs[norm]
        if lf == nil || lf.Volatility != "volatile" || lf.Parallel != "unsafe" || lf.Cost != 100 {
            t.Errorf("%s: the new definition resets the attributes the YAML leaves out, got %+v", c.name, lf)
        }
    }
}

func TestSimulateProcedureArgumentDefaultConverges(t *testing.T) {
    norm := normalizeFunctionSignature("app.p(a int)")
    live := emptyLive()
    live.Procedures[norm] = true
    live.ProcedureDefs[norm] = &LiveProcedure{Signature: "app.p(a integer)", Args: "a integer DEFAULT 1", Body: "select a", Language: "sql", Security: "definer"}
    d := &schema.Database{Procedures: map[string]*schema.Procedure{
        "app.p(a int default 2)": {Schema: "app", Name: "p", ArgsSig: "(a int default 2)", Language: "sql", Body: "select a"},
    }}
    if p := Plan(live, d, false); planSize(p) == 0 {
        t.Fatal("the argument default has to be planned")
    }
    sim := Simulate(live, d)
    if p := Plan(sim, d, false); planSize(p) != 0 {
        t.Errorf("a plan after the simulated one must be empty, got:\n%s", Render(p))
    }
    if lp := sim.ProcedureDefs[norm]; lp == nil || lp.Args != "a int default 2" || lp.Security != "invoker" {
        t.Errorf("CREATE OR REPLACE sets the arguments and resets security, got %+v", lp)
    }
}
