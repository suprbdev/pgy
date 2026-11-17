package diff

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// exprCastYAML declares, in every kind of definition the probe covers, an
// expression that its live counterpart (exprCastLive) casts where the YAML
// doesn't, so the two compare equal as text only because the text
// comparison drops the cast.
const exprCastYAML = `
schema app:
  domain d:
    type: date
    default: "now()"
    check: "value <> ''"
  function f(d date default now()):
    returns: boolean
    language: sql
    body: select true
  procedure p(d date default now()):
    language: sql
    body: select 1
  function trg():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table t:
    columns:
      id: {type: bigint}
      name: {type: "varchar(20)"}
      email: {type: citext}
      at: {type: date, default: "now()"}
      up: {type: text, generated: "upper(name)"}
      r: {type: int4range, nullable: true}
    primaryKey: [id]
    constraints:
      t_name: {type: check, expression: "name <> ''"}
      t_excl: {type: exclude, expression: "using gist (r with &&) where (name <> '')"}
    indexes:
      t_email: {columns: [email], unique: true}
      t_named: {columns: [id], where: "name <> ''"}
    rowLevelSecurity: true
    policies:
      t_p: {using: "name = current_user", withCheck: "email = 'x'"}
    triggers:
      t_t:
        timing: before
        events: [update]
        level: row
        when: "new.name <> ''"
        procedure: app.trg()
  view v:
    query: select id, at from app.t
    columnDefaults:
      at: "now()"
`

// exprCastCase is a definition of exprCastYAML: its key, what the probe
// reads back from the declared definition, which differs from the live
// one, and a fragment of the statement that changes it.
type exprCastCase struct {
	key       ExprKey
	parts     []string
	statement string
}

func exprCastCases() []exprCastCase {
	return []exprCastCase{
		{ExprKey{ExprDefault, "app.t", "at"}, []string{"now()"}, `alter table "app"."t" alter column "at" set default now();`},
		{ExprKey{ExprGenerated, "app.t", "up"}, []string{"upper(name)"}, `alter column "up" set expression as (upper(name));`},
		{ExprKey{ExprConstraint, "app.t", "t_name"}, []string{"CHECK ((name <> ''))"}, `add constraint "t_name" check (name <> '')`},
		{ExprKey{ExprConstraint, "app.t", "t_excl"}, []string{"EXCLUDE USING gist (r WITH &&) WHERE ((name <> ''))"}, `add constraint "t_excl" exclude`},
		{ExprKey{ExprIndex, "app.t", "t_email"}, []string{"email", ""}, `create unique index if not exists "t_email"`},
		{ExprKey{ExprIndex, "app.t", "t_named"}, []string{"id", "name <> ''"}, `create index if not exists "t_named"`},
		{ExprKey{ExprPolicy, "app.t", "t_p"}, []string{"((name)::text = CURRENT_USER)", "(email = 'x'::text)"}, `create policy "t_p"`},
		{ExprKey{ExprTrigger, "app.t", "t_t"}, []string{"CREATE TRIGGER t_t BEFORE UPDATE ON app.t FOR EACH ROW WHEN ((new.name <> '')) EXECUTE FUNCTION app.trg()"}, `create trigger "t_t"`},
		{ExprKey{ExprViewDefault, "app.v", "at"}, []string{"now()"}, `alter view "app"."v" alter column "at" set default now();`},
		{ExprKey{ExprDomainDefault, "app.d", ""}, []string{"now()"}, `alter domain "app"."d" set default now();`},
		{ExprKey{ExprDomainCheck, "app.d", "d_check"}, []string{"CHECK ((VALUE <> ''))"}, `alter domain "app"."d" add constraint "d_check"`},
		{ExprKey{ExprFunctionArgs, "app.f(d date default now())", ""}, []string{"d date DEFAULT now()"}, `create or replace function "app"."f"`},
		{ExprKey{ExprProcedureArgs, "app.p(d date default now())", ""}, []string{"IN d date DEFAULT now()"}, `create or replace procedure "app"."p"`},
	}
}

func exprCastDesired(t *testing.T) *schema.Database {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.yml")
	if err := os.WriteFile(path, []byte(exprCastYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// exprCastLive is a database exprCastYAML matches as text, with a cast in
// every definition that the YAML leaves out.
func exprCastLive() *Live {
	live := emptyLive()
	live.ServerVersion = 170000
	live.Schemas["app"] = true
	live.DomainDefs = map[string]*LiveDomain{}
	live.ViewColumnDefaults = map[string]map[string]string{}

	live.Domains["app.d"] = true
	live.DomainDefs["app.d"] = &LiveDomain{Type: "date", Default: "(now())::date", Checks: map[string]string{"d_check": "CHECK (((VALUE)::text <> ''::text))"}}

	f, trg, proc := normalizeFunctionSignature("app.f(d date)"), normalizeFunctionSignature("app.trg()"), normalizeFunctionSignature("app.p(d date)")
	live.Functions["app.f(d date)"] = true
	live.FunctionDefs[f] = &LiveFunction{Signature: "app.f(d date)", Args: "d date DEFAULT (now())::date", Kind: "f", Body: "select true", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100, Returns: "boolean", Language: "sql"}
	live.Functions["app.trg()"] = true
	live.FunctionDefs[trg] = &LiveFunction{Signature: "app.trg()", Kind: "f", Body: "begin return new; end", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100, Returns: "trigger", Language: "plpgsql"}
	live.Procedures[proc] = true
	live.ProcedureDefs[proc] = &LiveProcedure{Signature: "app.p(d date)", Args: "IN d date DEFAULT (now())::date", Body: "select 1", Security: "invoker", Language: "sql"}

	lt := newLiveTable()
	lt.Columns = map[string]*LiveColumn{
		"id":    {Type: "bigint"},
		"name":  {Type: "character varying(20)"},
		"email": {Type: "citext"},
		"at":    {Type: "date", Default: "(now())::date"},
		"up":    {Type: "text", Generated: "upper((name)::text)"},
		"r":     {Type: "int4range", Nullable: true},
	}
	lt.HasPK, lt.RLSEnabled = true, true
	for name, def := range map[string]string{
		"t_pkey": "PRIMARY KEY (id)",
		"t_name": "CHECK (((name)::text <> ''::text))",
		"t_excl": "EXCLUDE USING gist (r WITH &&) WHERE (((name)::text <> ''::text))",
	} {
		lt.Constraints[name], lt.ConstraintDefs[name] = true, def
	}
	lt.Indexes["t_email"] = true
	lt.IndexDefs["t_email"] = &LiveIndex{Method: "btree", Unique: true, Keys: []string{"(email::text)"}, Descending: []bool{false}, NullsFirst: []bool{false}, Options: map[string]string{}}
	lt.IndexOpclasses["t_email"] = []LiveIndexOpclass{{Name: "text_ops", Default: true, StatisticsTarget: -1}}
	lt.Indexes["t_named"] = true
	lt.IndexDefs["t_named"] = &LiveIndex{Method: "btree", Keys: []string{"id"}, Descending: []bool{false}, NullsFirst: []bool{false}, Predicate: "(name)::text <> ''::text", Options: map[string]string{}}
	lt.IndexOpclasses["t_named"] = []LiveIndexOpclass{{Name: "int8_ops", Default: true, StatisticsTarget: -1}}
	lt.Indexes["t_excl"] = true
	lt.IndexOpclasses["t_excl"] = []LiveIndexOpclass{{Name: "range_ops", Default: true, StatisticsTarget: -1}}
	lt.Policies["t_p"] = &LivePolicy{Cmd: "all", Using: "((name)::text = CURRENT_USER)", WithCheck: "((email)::text = 'x'::text)"}
	lt.Triggers["t_t"] = "CREATE TRIGGER t_t BEFORE UPDATE ON app.t FOR EACH ROW WHEN (((new.name)::text <> ''::text)) EXECUTE FUNCTION app.trg()"
	lt.TriggerEnabled["t_t"] = "origin"
	live.Tables["app.t"] = lt

	live.Views["app.v"] = true
	live.ViewColumnDefaults["app.v"] = map[string]string{"at": "(now())::date"}
	return live
}

// exprCastProbes returns the probes of the given cases as ProbeExpressions
// stores them.
func exprCastProbes(live *Live, desired *schema.Database, cases []exprCastCase) map[ExprKey]*ExprProbe {
	out := map[ExprKey]*ExprProbe{}
	for _, c := range cases {
		s := resolveExprSlot(live.scoped(), desired, c.key)
		if s == nil {
			panic("no slot for " + string(c.key.Kind) + " " + c.key.Object + " " + c.key.Name)
		}
		out[c.key] = &ExprProbe{Declared: s.declared(), Live: s.live(), Parts: c.parts}
	}
	return out
}

func TestExprProbeCastsMatchAsText(t *testing.T) {
	live, desired := exprCastLive(), exprCastDesired(t)
	if sql := unsafePlanSQL(live, desired); sql != "" {
		t.Errorf("every definition matches as text once casts are dropped, want an empty plan without probes, got:\n%s", sql)
	}
	plan := collectExprProbes(live, desired, false)
	got := map[ExprKey]bool{}
	for _, r := range plan.requests {
		got[r.key] = true
	}
	for _, c := range exprCastCases() {
		if !got[c.key] {
			t.Errorf("%s %s %s matches as text only by dropping a cast, so it is probed", c.key.Kind, c.key.Object, c.key.Name)
		}
	}
	if len(got) != len(exprCastCases()) || len(plan.pending) != 0 {
		t.Errorf("want %d requests and nothing pending, got %v and %d pending", len(exprCastCases()), got, len(plan.pending))
	}
}

func TestExprProbeDifferentOverridesEqual(t *testing.T) {
	cases := exprCastCases()
	for i, c := range cases {
		live, desired := exprCastLive(), exprCastDesired(t)
		live.ExprProbes = exprCastProbes(live, desired, cases[i:i+1])
		sql := unsafePlanSQL(live, desired)
		if !strings.Contains(sql, c.statement) {
			t.Errorf("%s %s %s: PostgreSQL reads the declared definition back as another one, want %q in:\n%s", c.key.Kind, c.key.Object, c.key.Name, c.statement, sql)
		}
		for j, other := range cases {
			if j != i && strings.Contains(sql, other.statement) {
				t.Errorf("%s %s %s: its probe must not change %q", c.key.Kind, c.key.Object, c.key.Name, other.statement)
			}
		}
	}
	live, desired := exprCastLive(), exprCastDesired(t)
	live.ExprProbes = exprCastProbes(live, desired, cases)
	if p := PlanReportingUnsafe(live, desired, Options{}); len(p.Warnings) == 0 {
		t.Error("without --unsafe the redefinitions are reported as left out")
	}
}

func TestExprProbeEqualKeepsCastsEqual(t *testing.T) {
	live, desired := exprCastLive(), exprCastDesired(t)
	cases := exprCastCases()
	live.ExprProbes = exprCastProbes(live, desired, cases)
	for _, c := range cases {
		s := resolveExprSlot(live.scoped(), desired, c.key)
		live.ExprProbes[c.key].Parts = s.live()
	}
	if sql := unsafePlanSQL(live, desired); sql != "" {
		t.Errorf("PostgreSQL stores every declared definition as the live one, want an empty plan, got:\n%s", sql)
	}
}

func TestExprProbeDifferentAppliesToItsLiveDefinition(t *testing.T) {
	for _, c := range exprCastCases() {
		live, desired := exprCastLive(), exprCastDesired(t)
		live.ExprProbes = exprCastProbes(live, desired, []exprCastCase{c})
		live.ExprProbes[c.key].Live = []string{"something else"}
		if sql := unsafePlanSQL(live, desired); sql != "" {
			t.Errorf("%s %s %s: a probe compared with another live definition must not apply, got:\n%s", c.key.Kind, c.key.Object, c.key.Name, sql)
		}
		live.ExprProbes = exprCastProbes(live, desired, []exprCastCase{c})
		live.ExprProbes[c.key].Err = "column does not exist"
		if sql := unsafePlanSQL(live, desired); sql != "" {
			t.Errorf("%s %s %s: a definition PostgreSQL rejected is compared as text, got:\n%s", c.key.Kind, c.key.Object, c.key.Name, sql)
		}
	}
}

func TestExprProbeDifferentKeepsLiveSnapshot(t *testing.T) {
	live, desired := exprCastLive(), exprCastDesired(t)
	live.ExprProbes = exprCastProbes(live, desired, exprCastCases())
	before := cloneLive(live)
	out := applyExprProbes(live.scoped(), desired)
	after := cloneLive(live)
	for name, pair := range map[string][2]any{
		"tables":        {before.Tables, after.Tables},
		"view defaults": {before.ViewColumnDefaults, after.ViewColumnDefaults},
		"domains":       {before.DomainDefs, after.DomainDefs},
		"functions":     {before.FunctionDefs, after.FunctionDefs},
		"procedures":    {before.ProcedureDefs, after.ProcedureDefs},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("marking definitions changed modified the snapshot's %s", name)
		}
	}
	lt := out.Tables["app.t"]
	if got := lt.IndexDefs["t_email"].Keys; !slices.Equal(got, []string{"(email::text)" + exprChanged}) {
		t.Errorf("the changed key is marked, got %q", got)
	}
	if got := lt.IndexDefs["t_named"].Predicate; got != "(name)::text <> ''::text"+exprChanged || lt.IndexDefs["t_named"].Keys[0] != "id" {
		t.Errorf("only the changed predicate is marked, got %q %q", lt.IndexDefs["t_named"].Keys, got)
	}
	if got := lt.Triggers["t_t"]; !strings.Contains(got, "/*≠*/ EXECUTE FUNCTION app.trg()") {
		t.Errorf("a trigger is marked before its function call, got %q", got)
	}
	if got := out.FunctionDefs[normalizeFunctionSignature("app.f(d date)")].Args; got != "d date DEFAULT (now())::date"+exprChanged {
		t.Errorf("an argument default is marked, got %q", got)
	}
}

func TestExprProbeDifferentKeepsNotValid(t *testing.T) {
	live, desired := exprCastLive(), exprCastDesired(t)
	live.Tables["app.t"].ConstraintDefs["t_name"] += " NOT VALID"
	live.DomainDefs["app.d"].Checks["d_check"] += " NOT VALID"
	cases := []exprCastCase{exprCastCases()[2], exprCastCases()[10]}
	live.ExprProbes = exprCastProbes(live, desired, cases)
	out := applyExprProbes(live.scoped(), desired)
	if got := out.Tables["app.t"].ConstraintDefs["t_name"]; !liveNotValid(got) || !strings.Contains(got, exprChanged) {
		t.Errorf("the mark goes before NOT VALID, got %q", got)
	}
	if got := out.DomainDefs["app.d"].Checks["d_check"]; !liveNotValid(got) || !strings.Contains(got, exprChanged) {
		t.Errorf("the mark goes before NOT VALID, got %q", got)
	}
	sql := unsafePlanSQL(live, desired)
	for _, c := range cases {
		if !strings.Contains(sql, c.statement) {
			t.Errorf("want %q in:\n%s", c.statement, sql)
		}
	}
}

func TestExprProbeDifferentNotPlannedInLaterStep(t *testing.T) {
	live, desired := exprCastLive(), exprCastDesired(t)
	live.ExprProbes = exprCastProbes(live, desired, exprCastCases())
	if Render(Plan(live, desired, true)) == "" {
		t.Fatal("want the definitions planned as changed")
	}
	again := Plan(Simulate(live, desired), desired, true)
	if sql := Render(again) + RenderConcurrent(again); sql != "" {
		t.Errorf("a later step of a split plan sees the definitions as applied, got:\n%s", sql)
	}
}

func TestExprProbeSchemaOnlyWaitsForCatalog(t *testing.T) {
	live, desired := exprCastLive(), exprCastDesired(t)
	live.Tables["app.t"].Columns["at"].Default = "gen_random_uuid()"
	desired.Tables["app.t"].Columns["at"].Default = "public.gen_random_uuid()"
	live.Tables["app.t"].Columns["name"].Default = "app.slug()"
	desired.Tables["app.t"].Columns["name"].Default = "app.slug()"
	key := ExprKey{ExprDefault, "app.t", "at"}
	plan := collectExprProbes(live, desired, false)
	for _, r := range plan.requests {
		if r.key == key {
			t.Fatal("a definition that only lost a schema is not probed before the catalog is known")
		}
	}
	if got := plan.pendingNames(); !slices.Equal(got, []string{"gen_random_uuid"}) {
		t.Fatalf("want gen_random_uuid pending, got %v", got)
	}
	requested := func(p *exprProbePlan) bool {
		return slices.ContainsFunc(p.requests, func(r *probeRequest) bool { return r.key == key })
	}
	plan.resolve(map[string]bool{"lower": true})
	if requested(plan) || len(plan.pending) != 0 {
		t.Error("a name pg_catalog doesn't share leaves the definition to the text comparison")
	}
	plan = collectExprProbes(live, desired, false)
	plan.resolve(map[string]bool{"gen_random_uuid": true})
	if !requested(plan) {
		t.Error("a name pg_catalog shares makes the definition a probe")
	}
}

func TestExprProbeEveryDefinition(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	live.ExprProbes = exprProbesFor(live, desired, exprProbeCases())
	spelled := applyExprProbes(live.scoped(), desired)
	spelled.ExprProbes = nil
	if plan := collectExprProbes(spelled, desired, false); len(plan.requests)+len(plan.pending) != 0 {
		t.Fatalf("a schema that matches as text has nothing to probe, got %d requests", len(plan.requests))
	}
	plan := collectExprProbes(spelled, desired, true)
	got := map[ExprKey]bool{}
	for _, r := range plan.requests {
		got[r.key] = true
	}
	for _, c := range exprProbeCases() {
		if !got[c.key] {
			t.Errorf("%s %s %s: every definition is probed, also one that matches as text", c.key.Kind, c.key.Object, c.key.Name)
		}
	}
}
