package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func fptr(v float64) *float64 { return &v }

func parallelDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, Functions: map[string]*schema.Function{
		"app.f()": {Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select 1", Parallel: "safe", Cost: fptr(5)},
		"app.g()": {Name: "g", ArgsSig: "()", Returns: "setof int", Language: "sql", Body: "select 1", Rows: fptr(10), Cost: fptr(0.5)},
	}}
}

func TestFunctionParallelCostRowsCreate(t *testing.T) {
	sql := Render(Plan(emptyLive(), parallelDB(), false))
	for _, want := range []string{
		`create function "app"."f"() returns int language sql parallel safe cost 5 as $$`,
		`create function "app"."g"() returns setof int language sql cost 0.5 rows 10 as $$`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
}

func TestFunctionParallelCostRowsAlterAndSkip(t *testing.T) {
	live := Simulate(emptyLive(), parallelDB())
	if sql := Render(Plan(live, parallelDB(), false)); sql != "" {
		t.Fatalf("simulated state must match, got:\n%s", sql)
	}
	live.FunctionDefs[normalizeFunctionSignature("app.g()")].Cost = 0.5000000001
	if sql := Render(Plan(live, parallelDB(), false)); sql != "" {
		t.Fatalf("float4 rounding must not diff, got:\n%s", sql)
	}
	d := parallelDB()
	d.Functions["app.f()"].Parallel = "restricted"
	d.Functions["app.f()"].Cost = fptr(50)
	d.Functions["app.g()"].Rows = fptr(100)
	p := Plan(live, d, false)
	for _, want := range []string{
		`alter function "app"."f"() parallel restricted cost 50;`,
		`alter function "app"."g"() rows 100;`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
	d.Functions["app.f()"].Parallel, d.Functions["app.f()"].Cost, d.Functions["app.g()"].Rows, d.Functions["app.g()"].Cost = "", nil, nil, nil
	live.FunctionDefs[normalizeFunctionSignature("app.f()")].Parallel = "restricted"
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("unmanaged attributes must not diff, got:\n%s", sql)
	}
}
