package diff

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// typeProbeYAML declares types in spellings PostgreSQL prints another way.
const typeProbeYAML = `
schema app:
  type pair:
    type: composite
    attributes:
      a: {type: "numeric(10)"}
      b: {type: text}
  domain d10:
    type: numeric(10)
  table t:
    columns:
      id: {type: bigint}
      n: {type: "numeric(10)"}
      v: {type: "varchar( 5 )"}
      ts: {type: "timestamp (3) with time zone"}
      na: {type: "numeric(10)[]"}
      same: {type: "varchar(5)"}
      p: {type: app.pair, nullable: true}
`

// typeProbeLiveYAML is typeProbeYAML as pgy pull writes it.
const typeProbeLiveYAML = `
schema app:
  type pair:
    type: composite
    attributes:
      a: {type: "numeric(10,0)"}
      b: {type: text}
  domain d10:
    type: numeric(10,0)
  table t:
    columns:
      id: {type: bigint}
      n: {type: "numeric(10,0)"}
      v: {type: "character varying(5)"}
      ts: {type: "timestamp(3) with time zone"}
      na: {type: "numeric(10,0)[]"}
      same: {type: "character varying(5)"}
      p: {type: app.pair, nullable: true}
`

// typeProbeShown is what PostgreSQL prints for each declared type of
// typeProbeYAML that differs as text.
var typeProbeShown = map[string]string{
	"numeric(10)":                  "numeric(10,0)",
	"varchar( 5 )":                 "character varying(5)",
	"timestamp (3) with time zone": "timestamp(3) with time zone",
	"numeric(10)[]":                "numeric(10,0)[]",
}

func loadTypeProbeYAML(t *testing.T, y string) *schema.Database {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.yml")
	if err := os.WriteFile(path, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// typeProbeLive is the database typeProbeYAML was applied to.
func typeProbeLive(t *testing.T) *Live {
	t.Helper()
	live := Simulate(emptyLive(), loadTypeProbeYAML(t, typeProbeLiveYAML))
	live.ServerVersion = 180000
	live.DomainDefs["app.d10"] = &LiveDomain{Type: "numeric(10,0)", Checks: map[string]string{}}
	return live
}

func TestTypeProbeCollectsTextMismatchesOnly(t *testing.T) {
	live, desired := typeProbeLive(t), loadTypeProbeYAML(t, typeProbeYAML)
	want := []string{"numeric(10)", "numeric(10)[]", "timestamp (3) with time zone", "varchar( 5 )"}
	if got := collectExprProbes(live, desired, false).spec.Types; !reflect.DeepEqual(got, want) {
		t.Errorf("want each declared type that differs as text once, got %q", got)
	}
	if got := collectExprProbes(live, loadTypeProbeYAML(t, typeProbeLiveYAML), true).spec.Types; len(got) != 0 {
		t.Errorf("types that match as text are never probed, also with every expression probed, got %q", got)
	}
}

func TestTypeProbeWithoutResultComparesAsText(t *testing.T) {
	live, desired := typeProbeLive(t), loadTypeProbeYAML(t, typeProbeYAML)
	sql := unsafePlanSQL(live, desired)
	for _, want := range []string{
		`alter column "n" type numeric(10);`,
		`alter column "v" type varchar( 5 );`,
		`alter column "ts" type timestamp (3) with time zone;`,
		`alter column "na" type numeric(10)[];`,
		`alter type "app"."pair" alter attribute "a" type numeric(10)`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("without a probe the text comparison reports a change, want %q in:\n%s", want, sql)
		}
	}
	if !hasWarning(Plan(live, desired, false), "base type changed") {
		t.Error("without a probe the domain's base type is reported as changed")
	}
}

func hasWarning(p *PlanDiff, fragment string) bool {
	for _, w := range p.Warnings {
		if strings.Contains(w.Message, fragment) {
			return true
		}
	}
	return false
}

func TestTypeProbeEqualEmitsNothing(t *testing.T) {
	live, desired := typeProbeLive(t), loadTypeProbeYAML(t, typeProbeYAML)
	live.TypeProbes = typeProbeShown
	for _, opts := range []Options{{Unsafe: true}, {Unsafe: true, Prune: true}} {
		if p := PlanWith(live, desired, opts); Render(p)+RenderConcurrent(p) != "" || len(p.Warnings) > 0 {
			t.Errorf("every type is stored as declared, want an empty plan (%+v), got:\n%s%+v", opts, Render(p), p.Warnings)
		}
	}
	if p := PlanReportingUnsafe(live, desired, Options{}); Render(p) != "" || len(p.Warnings) > 0 {
		t.Errorf("want nothing planned or reported without --unsafe, got:\n%s%+v", Render(p), p.Warnings)
	}
	if again := Plan(Simulate(live, desired), desired, true); Render(again) != "" {
		t.Errorf("a later step of a split plan sees the types as they are, got:\n%s", Render(again))
	}
}

func TestTypeProbeKeepsLiveSnapshot(t *testing.T) {
	live, desired := typeProbeLive(t), loadTypeProbeYAML(t, typeProbeYAML)
	live.TypeProbes = typeProbeShown
	Plan(live, desired, true)
	if got := live.Tables["app.t"].Columns["n"].Type; got != "numeric(10,0)" {
		t.Errorf("the snapshot itself is not modified, got column type %q", got)
	}
	if got := live.DomainDefs["app.d10"].Type; got != "numeric(10,0)" {
		t.Errorf("the snapshot itself is not modified, got domain type %q", got)
	}
	if got := live.CompositeAttributes["app.pair"]["a"]; got != "numeric(10,0)" {
		t.Errorf("the snapshot itself is not modified, got attribute type %q", got)
	}
}

func TestTypeProbeDifferentKeepsStatement(t *testing.T) {
	live, desired := typeProbeLive(t), loadTypeProbeYAML(t, typeProbeYAML)
	live.TypeProbes = map[string]string{}
	for declared, shown := range typeProbeShown {
		live.TypeProbes[declared] = shown
	}
	live.TypeProbes["numeric(10)"] = "numeric(10,2)"
	sql := unsafePlanSQL(live, desired)
	for _, want := range []string{`alter column "n" type numeric(10);`, `alter type "app"."pair" alter attribute "a" type numeric(10)`} {
		if !strings.Contains(sql, want) {
			t.Errorf("a type PostgreSQL prints another way than the live one is a change, want %q in:\n%s", want, sql)
		}
	}
	for _, unwanted := range []string{`"v" type`, `"ts" type`, `"na" type`} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("only the type that differs is planned, got %q in:\n%s", unwanted, sql)
		}
	}
	if !hasWarning(Plan(live, desired, false), "base type changed from numeric(10,0) to numeric(10)") {
		t.Error("the domain's base type is still reported as changed")
	}
}

func TestTypeProbeOfAnotherTypeIsIgnored(t *testing.T) {
	live, desired := typeProbeLive(t), loadTypeProbeYAML(t, typeProbeYAML)
	live.TypeProbes = map[string]string{"varchar(5)": "character varying(5)", "numeric(10)": "numeric(10,0)"}
	desired.Tables["app.t"].Columns["n"].Type = "numeric(11)"
	sql := unsafePlanSQL(live, desired)
	if !strings.Contains(sql, `alter column "n" type numeric(11);`) || !strings.Contains(sql, `alter column "v" type varchar( 5 );`) {
		t.Errorf("a result applies to the type it was made from only, got:\n%s", sql)
	}
}

func TestTypeProbeResults(t *testing.T) {
	reply := &probeReply{Types: []probeType{
		{Type: "numeric(10)", Shown: "numeric(10,0)"},
		{Type: "nosuch(1)", Err: `type "nosuch(1)" does not exist`},
	}}
	if got, want := typeResults(reply), map[string]string{"numeric(10)": "numeric(10,0)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("want only the resolved types, got %v", got)
	}
}
