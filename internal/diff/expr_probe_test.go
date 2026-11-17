package diff

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

const exprProbeYAML = `
schema app:
  domain props:
    type: hstore
    default: "'v=>1'"
    check: "value @> 'v=>1'"
  function f(h hstore default 'a=>1'):
    returns: boolean
    language: sql
    body: select true
  procedure p(i interval default '1 hour'):
    language: sql
    body: select 1
  function trg():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table t:
    columns:
      id: {type: bigint}
      attrs: {type: hstore, nullable: true, default: "'a=>1'"}
      old: {type: isbn, nullable: true}
      e: {type: ean13, nullable: true, generated: "ean13(old)"}
      c: {type: cube, nullable: true}
      s: {type: seg, nullable: true}
    primaryKey: [id]
    constraints:
      t_box: {type: check, expression: "c <@ cube '(0, 0),(1, 1)'"}
      t_excl: {type: exclude, expression: "using gist (c with &&) where (s <@ '0 .. 10')"}
    indexes:
      t_part: {columns: [id, "lower(attrs::text)"], where: "attrs @> 'kind=>widget'"}
    rowLevelSecurity: true
    policies:
      t_p: {using: "s && '0 .. 10'::seg", withCheck: "s && '0 .. 10'::seg"}
    triggers:
      t_t:
        timing: before
        events: [update]
        level: row
        when: "new.attrs @> 'locked=>true'"
        procedure: app.trg()
  view v:
    query: select id, attrs from app.t
    columnDefaults:
      attrs: "'a=>1'::hstore"
  materialized view mv:
    query: select id, attrs from app.t
    indexes:
      mv_part: {columns: [id], where: "attrs @> 'kind=>widget'"}
`

// exprProbeCase is one definition of exprProbeYAML that the live fixture
// stores in PostgreSQL's spelling: its probe key, what the probe reads
// back when PostgreSQL stores the declared definition as the live one, and
// a fragment of the statement the plan emits when the two are taken for
// different.
type exprProbeCase struct {
	key       ExprKey
	parts     []string
	statement string
}

func exprProbeCases() []exprProbeCase {
	return []exprProbeCase{
		{ExprKey{ExprDefault, "app.t", "attrs"}, []string{`'"a"=>"1"'::hstore`}, `alter table "app"."t" alter column "attrs" set default 'a=>1';`},
		{ExprKey{ExprGenerated, "app.t", "e"}, []string{`(old)::ean13`}, `alter column "e" set expression as (ean13(old))`},
		{ExprKey{ExprConstraint, "app.t", "t_box"}, []string{`CHECK ((c <@ '(0, 0),(1, 1)'::cube))`}, `add constraint "t_box" check`},
		{ExprKey{ExprConstraint, "app.t", "t_excl"}, []string{`EXCLUDE USING gist (c WITH &&) WHERE ((s <@ '0 .. 1.0e1'::seg))`}, `add constraint "t_excl" exclude`},
		{ExprKey{ExprIndex, "app.t", "t_part"}, []string{"id", "lower(attrs::text)", `attrs @> '"kind"=>"widget"'::hstore`}, `create index if not exists "t_part"`},
		{ExprKey{ExprPolicy, "app.t", "t_p"}, []string{`(s && '0 .. 1.0e1'::seg)`, `(s && '0 .. 1.0e1'::seg)`}, `create policy "t_p"`},
		{ExprKey{ExprTrigger, "app.t", "t_t"}, []string{`CREATE TRIGGER t_t BEFORE UPDATE ON app.t FOR EACH ROW WHEN ((new.attrs @> '"locked"=>"true"'::hstore)) EXECUTE FUNCTION app.trg()`}, `create trigger "t_t"`},
		{ExprKey{ExprViewDefault, "app.v", "attrs"}, []string{`'"a"=>"1"'::hstore`}, `alter view "app"."v" alter column "attrs" set default`},
		{ExprKey{ExprIndex, "app.mv", "mv_part"}, []string{"id", `attrs @> '"kind"=>"widget"'::hstore`}, `create index if not exists "mv_part"`},
		{ExprKey{ExprDomainDefault, "app.props", ""}, []string{`'"v"=>"1"'::hstore`}, `alter domain "app"."props" set default`},
		{ExprKey{ExprDomainCheck, "app.props", "props_check"}, []string{`CHECK ((VALUE @> '"v"=>"1"'::hstore))`}, `alter domain "app"."props" add constraint "props_check"`},
		{ExprKey{ExprFunctionArgs, "app.f(h hstore default 'a=>1')", ""}, []string{`h hstore DEFAULT '"a"=>"1"'::hstore`}, `create or replace function "app"."f"`},
		{ExprKey{ExprProcedureArgs, "app.p(i interval default '1 hour')", ""}, []string{`IN i interval DEFAULT '01:00:00'::interval`}, `create or replace procedure "app"."p"`},
	}
}

func exprProbeDesired(t *testing.T) *schema.Database {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.yml")
	if err := os.WriteFile(path, []byte(exprProbeYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// exprProbeLive is the database exprProbeYAML was applied to, as
// PostgreSQL 17 prints it.
func exprProbeLive() *Live {
	live := emptyLive()
	live.ServerVersion = 170000
	live.Schemas["app"] = true
	live.DomainDefs = map[string]*LiveDomain{}
	live.ViewColumnDefaults = map[string]map[string]string{}
	live.MatViewTables = map[string]*LiveTable{}

	live.Domains["app.props"] = true
	live.DomainDefs["app.props"] = &LiveDomain{Type: "hstore", Default: `'"v"=>"1"'::hstore`, Checks: map[string]string{"props_check": `CHECK ((VALUE @> '"v"=>"1"'::hstore))`}}

	f, trg, proc := normalizeFunctionSignature("app.f(h hstore)"), normalizeFunctionSignature("app.trg()"), normalizeFunctionSignature("app.p(i interval)")
	live.Functions["app.f(h hstore)"] = true
	live.FunctionDefs[f] = &LiveFunction{Signature: "app.f(h hstore)", Args: `h hstore DEFAULT '"a"=>"1"'::hstore`, Kind: "f", Body: "select true", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100, Returns: "boolean", Language: "sql"}
	live.Functions["app.trg()"] = true
	live.FunctionDefs[trg] = &LiveFunction{Signature: "app.trg()", Kind: "f", Body: "begin return new; end", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100, Returns: "trigger", Language: "plpgsql"}
	live.Procedures[proc] = true
	live.ProcedureDefs[proc] = &LiveProcedure{Signature: "app.p(i interval)", Args: `IN i interval DEFAULT '01:00:00'::interval`, Body: "select 1", Security: "invoker", Language: "sql"}

	lt := newLiveTable()
	lt.Columns = map[string]*LiveColumn{
		"id":    {Type: "bigint"},
		"attrs": {Type: "hstore", Nullable: true, Default: `'"a"=>"1"'::hstore`},
		"old":   {Type: "isbn", Nullable: true},
		"e":     {Type: "ean13", Nullable: true, Generated: "(old)::ean13"},
		"c":     {Type: "cube", Nullable: true},
		"s":     {Type: "seg", Nullable: true},
	}
	lt.HasPK, lt.RLSEnabled = true, true
	for name, def := range map[string]string{
		"t_pkey": "PRIMARY KEY (id)",
		"t_box":  "CHECK ((c <@ '(0, 0),(1, 1)'::cube))",
		"t_excl": "EXCLUDE USING gist (c WITH &&) WHERE ((s <@ '0 .. 1.0e1'::seg))",
	} {
		lt.Constraints[name], lt.ConstraintDefs[name] = true, def
	}
	lt.Indexes["t_part"] = true
	lt.IndexDefs["t_part"] = &LiveIndex{Method: "btree", Keys: []string{"id", "lower(attrs::text)"}, Descending: []bool{false, false}, NullsFirst: []bool{false, false}, Predicate: `attrs @> '"kind"=>"widget"'::hstore`, Options: map[string]string{}}
	lt.IndexOpclasses["t_part"] = []LiveIndexOpclass{{Name: "int8_ops", Default: true, StatisticsTarget: -1}, {Name: "text_ops", Default: true, StatisticsTarget: -1}}
	lt.Indexes["t_excl"] = true
	lt.IndexOpclasses["t_excl"] = []LiveIndexOpclass{{Name: "gist_cube_ops", Default: true, StatisticsTarget: -1}}
	lt.Policies["t_p"] = &LivePolicy{Cmd: "all", Using: `(s && '0 .. 1.0e1'::seg)`, WithCheck: `(s && '0 .. 1.0e1'::seg)`}
	lt.Triggers["t_t"] = `CREATE TRIGGER t_t BEFORE UPDATE ON app.t FOR EACH ROW WHEN ((new.attrs @> '"locked"=>"true"'::hstore)) EXECUTE FUNCTION app.trg()`
	lt.TriggerEnabled["t_t"] = "origin"
	live.Tables["app.t"] = lt

	live.Views["app.v"] = true
	live.ViewColumnDefaults["app.v"] = map[string]string{"attrs": `'"a"=>"1"'::hstore`}

	live.MatViews["app.mv"] = true
	mv := newLiveTable()
	mv.Indexes["mv_part"] = true
	mv.IndexDefs["mv_part"] = &LiveIndex{Method: "btree", Keys: []string{"id"}, Descending: []bool{false}, NullsFirst: []bool{false}, Predicate: `attrs @> '"kind"=>"widget"'::hstore`, Options: map[string]string{}}
	mv.IndexOpclasses["mv_part"] = []LiveIndexOpclass{{Name: "int8_ops", Default: true, StatisticsTarget: -1}}
	live.MatViewTables["app.mv"] = mv
	return live
}

// exprProbesFor returns the probes of the given cases as ProbeExpressions
// stores them: each declared definition with what PostgreSQL made of it.
func exprProbesFor(live *Live, desired *schema.Database, cases []exprProbeCase) map[ExprKey]*ExprProbe {
	out := map[ExprKey]*ExprProbe{}
	for _, c := range cases {
		s := resolveExprSlot(live.scoped(), desired, c.key)
		if s == nil {
			panic("no slot for " + string(c.key.Kind) + " " + c.key.Object + " " + c.key.Name)
		}
		out[c.key] = &ExprProbe{Declared: s.declared(), Parts: c.parts}
	}
	return out
}

func unsafePlanSQL(live *Live, desired *schema.Database) string {
	p := Plan(live, desired, true)
	return Render(p) + RenderConcurrent(p)
}

func TestExprProbeWithoutResultComparesAsText(t *testing.T) {
	sql := unsafePlanSQL(exprProbeLive(), exprProbeDesired(t))
	for _, c := range exprProbeCases() {
		if !strings.Contains(sql, c.statement) {
			t.Errorf("%s %s %s: without a probe the text comparison reports a change, want %q in:\n%s", c.key.Kind, c.key.Object, c.key.Name, c.statement, sql)
		}
	}
}

func TestExprProbeEqualEmitsNothing(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	live.ExprProbes = exprProbesFor(live, desired, exprProbeCases())
	if sql := unsafePlanSQL(live, desired); sql != "" {
		t.Errorf("every definition is stored as declared, want an empty plan, got:\n%s", sql)
	}
	if p := PlanReportingUnsafe(live, desired, Options{}); len(p.Warnings) > 0 || Render(p) != "" {
		t.Errorf("want nothing planned or reported without --unsafe, got:\n%s%+v", Render(p), p.Warnings)
	}
}

func TestExprProbeEqualOnlyForItsDefinition(t *testing.T) {
	cases := exprProbeCases()
	for i, c := range cases {
		live, desired := exprProbeLive(), exprProbeDesired(t)
		live.ExprProbes = exprProbesFor(live, desired, cases[i:i+1])
		sql := unsafePlanSQL(live, desired)
		if strings.Contains(sql, c.statement) {
			t.Errorf("%s %s %s: stored as declared, so %q must not be planned:\n%s", c.key.Kind, c.key.Object, c.key.Name, c.statement, sql)
		}
		for j, other := range cases {
			if j != i && !strings.Contains(sql, other.statement) {
				t.Errorf("%s %s %s: its probe must not hide %q", c.key.Kind, c.key.Object, c.key.Name, other.statement)
			}
		}
	}
}

func TestExprProbeDifferentKeepsStatement(t *testing.T) {
	for _, c := range exprProbeCases() {
		live, desired := exprProbeLive(), exprProbeDesired(t)
		live.ExprProbes = exprProbesFor(live, desired, []exprProbeCase{c})
		p := live.ExprProbes[c.key]
		p.Parts = slices.Clone(p.Parts)
		p.Parts[len(p.Parts)-1] += " IS NOT NULL"
		if sql := unsafePlanSQL(live, desired); !strings.Contains(sql, c.statement) {
			t.Errorf("%s %s %s: PostgreSQL stores the declared definition another way than the live one, want %q in:\n%s", c.key.Kind, c.key.Object, c.key.Name, c.statement, sql)
		}
	}
}

func TestExprProbeErrorComparesAsText(t *testing.T) {
	for _, c := range exprProbeCases() {
		live, desired := exprProbeLive(), exprProbeDesired(t)
		live.ExprProbes = exprProbesFor(live, desired, []exprProbeCase{c})
		live.ExprProbes[c.key].Err = `function nope() does not exist`
		if sql := unsafePlanSQL(live, desired); !strings.Contains(sql, c.statement) {
			t.Errorf("%s %s %s: a definition PostgreSQL rejected is compared as text, want %q in:\n%s", c.key.Kind, c.key.Object, c.key.Name, c.statement, sql)
		}
	}
}

func TestExprProbeOfAnotherDefinitionIsIgnored(t *testing.T) {
	for _, c := range exprProbeCases() {
		live, desired := exprProbeLive(), exprProbeDesired(t)
		live.ExprProbes = exprProbesFor(live, desired, []exprProbeCase{c})
		live.ExprProbes[c.key].Declared += " -- edited since"
		if sql := unsafePlanSQL(live, desired); !strings.Contains(sql, c.statement) {
			t.Errorf("%s %s %s: a probe made from another declaration must not apply, want %q in:\n%s", c.key.Kind, c.key.Object, c.key.Name, c.statement, sql)
		}
	}
}

func TestExprProbeKeepsLiveSnapshot(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	live.ExprProbes = exprProbesFor(live, desired, exprProbeCases())
	before := cloneLive(live)
	out := applyExprProbes(live, desired)
	if out == live {
		t.Fatal("want an edited copy")
	}
	after := cloneLive(live)
	for name, pair := range map[string][2]any{
		"tables":        {before.Tables, after.Tables},
		"matviews":      {before.MatViewTables, after.MatViewTables},
		"view defaults": {before.ViewColumnDefaults, after.ViewColumnDefaults},
		"domains":       {before.DomainDefs, after.DomainDefs},
		"functions":     {before.FunctionDefs, after.FunctionDefs},
		"procedures":    {before.ProcedureDefs, after.ProcedureDefs},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("applying probes changed the snapshot's %s", name)
		}
	}
	if got := out.Tables["app.t"].Columns["attrs"].Default; got != "'a=>1'" {
		t.Errorf("the copy takes the declared spelling, got %q", got)
	}
	if out.Tables["app.t"].Columns["id"] == live.Tables["app.t"].Columns["id"] {
		t.Error("an edited table must not share its columns with the snapshot")
	}
	if len(out.Tables) != len(live.Tables) || len(out.MatViewTables) != len(live.MatViewTables) {
		t.Errorf("the copy has the snapshot's relations and no others: tables %v, materialized views %v", sortedKeys(out.Tables), sortedKeys(out.MatViewTables))
	}
	if got := out.MatViewTables["app.mv"].IndexDefs["mv_part"].Predicate; got != "attrs @> 'kind=>widget'" {
		t.Errorf("a materialized view's index takes the declared spelling, got %q", got)
	}
	if applyExprProbes(exprProbeLive(), desired) == nil || applyExprProbes(nil, desired) != nil {
		t.Error("a snapshot without probes is returned as it is")
	}
}

func TestExprProbeKeepsNotValid(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	lt := live.Tables["app.t"]
	lt.ConstraintDefs["t_box"] += " NOT VALID"
	live.ExprProbes = exprProbesFor(live, desired, exprProbeCases())
	p := Plan(live, desired, true)
	if sql := Render(p); sql != "" {
		t.Errorf("a constraint that only awaits validation is not redefined, got:\n%s", sql)
	}
	if want := []string{`alter table "app"."t" validate constraint "t_box";`}; !slices.Equal(p.Concurrent, want) {
		t.Errorf("want %v, got %v", want, p.Concurrent)
	}
}

func TestExprProbeIgnoresExclusionIndexParameters(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	lt := live.Tables["app.t"]
	lt.ConstraintDefs["t_excl"] = "EXCLUDE USING gist (c WITH &&) WITH (fillfactor='70') USING INDEX TABLESPACE fast WHERE ((s <@ '0 .. 1.0e1'::seg))"
	live.ExprProbes = exprProbesFor(live, desired, exprProbeCases())
	if sql := unsafePlanSQL(live, desired); sql != "" {
		t.Errorf("storage parameters and the tablespace are not part of the comparison, got:\n%s", sql)
	}
}

func TestExprProbeCollectsTextMismatchesOnly(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	plan := collectExprProbes(live, desired, false)
	got := map[ExprKey]bool{}
	for _, r := range plan.requests {
		got[r.key] = true
	}
	for _, c := range exprProbeCases() {
		if !got[c.key] {
			t.Errorf("%s %s %s was not collected", c.key.Kind, c.key.Object, c.key.Name)
		}
	}
	if len(got) != len(exprProbeCases()) {
		t.Errorf("want %d requests, got %v", len(exprProbeCases()), got)
	}
	if len(plan.spec.Relations) != 3 {
		t.Errorf("want one temporary table per affected relation (t, v, mv), got %d", len(plan.spec.Relations))
	}

	live.ExprProbes = exprProbesFor(live, desired, exprProbeCases())
	spelled := applyExprProbes(live.scoped(), desired)
	spelled.ExprProbes = nil
	if again := collectExprProbes(spelled, desired, false); len(again.requests) != 0 {
		t.Errorf("a schema that matches as text has nothing to probe, got %d requests", len(again.requests))
	}
}

func TestExprProbeSkipsChangesNoSpellingExplains(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	lt := live.Tables["app.t"]
	lt.Columns["attrs"].Default = ""
	lt.Columns["e"].Generated = ""
	delete(lt.Constraints, "t_box")
	delete(lt.ConstraintDefs, "t_box")
	lt.IndexDefs["t_part"].Predicate = ""
	lt.Policies["t_p"].WithCheck = ""
	delete(lt.Triggers, "t_t")
	delete(live.ViewColumnDefaults["app.v"], "attrs")
	live.DomainDefs["app.props"].Default = ""
	delete(live.DomainDefs["app.props"].Checks, "props_check")
	live.FunctionDefs[normalizeFunctionSignature("app.f(h hstore)")].Args = "h hstore"
	delete(live.Tables, "app.missing")
	want := map[ExprKey]bool{
		{ExprConstraint, "app.t", "t_excl"}:                           true,
		{ExprIndex, "app.mv", "mv_part"}:                              true,
		{ExprProcedureArgs, "app.p(i interval default '1 hour')", ""}: true,
	}
	got := map[ExprKey]bool{}
	for _, r := range collectExprProbes(live, desired, false).requests {
		got[r.key] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a definition that is missing on one side is a change, not a spelling; want %v, got %v", want, got)
	}
}

func TestExprProbeStatements(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	plan := collectExprProbes(live, desired, false)
	stmts := map[ExprKey]probeStmt{}
	for _, r := range plan.requests {
		stmts[r.key] = r.attach.stmt
	}
	for key, want := range map[ExprKey]string{
		{ExprDefault, "app.t", "attrs"}:                               `alter table "pg_temp"."t" alter column "attrs" set default 'a=>1'`,
		{ExprConstraint, "app.t", "t_box"}:                            `check (c <@ cube '(0, 0),(1, 1)')`,
		{ExprConstraint, "app.t", "t_excl"}:                           `exclude using gist (c with &&) where (s <@ '0 .. 10')`,
		{ExprIndex, "app.t", "t_part"}:                                `on "pg_temp"."t" using btree ("id", (lower(attrs::text))) where (attrs @> 'kind=>widget')`,
		{ExprPolicy, "app.t", "t_p"}:                                  `on "pg_temp"."t" using (s && '0 .. 10'::seg) with check (s && '0 .. 10'::seg)`,
		{ExprTrigger, "app.t", "t_t"}:                                 `create trigger "t_t" BEFORE UPDATE on "pg_temp"."t" for each row when (new.attrs @> 'locked=>true') execute procedure app.trg();`,
		{ExprViewDefault, "app.v", "attrs"}:                           `alter table "pg_temp"."v" alter column "attrs" set default 'a=>1'::hstore`,
		{ExprIndex, "app.mv", "mv_part"}:                              `on "pg_temp"."mv" using btree ("id") where (attrs @> 'kind=>widget')`,
		{ExprDomainDefault, "app.props", ""}:                          `alter domain "pg_temp"."pgy_probe_d0" set default 'v=>1'`,
		{ExprDomainCheck, "app.props", "props_check"}:                 `alter domain "pg_temp"."pgy_probe_d0" add constraint`,
		{ExprFunctionArgs, "app.f(h hstore default 'a=>1')", ""}:      `(h hstore default 'a=>1') returns boolean language plpgsql as ''`,
		{ExprProcedureArgs, "app.p(i interval default '1 hour')", ""}: `(i interval default '1 hour') language plpgsql as ''`,
	} {
		if !strings.Contains(stmts[key].SQL, want) {
			t.Errorf("%s %s %s: want %q in %q", key.Kind, key.Object, key.Name, want, stmts[key].SQL)
		}
	}
	gen := stmts[ExprKey{ExprGenerated, "app.t", "e"}]
	if !strings.Contains(gen.SQL, "%s generated always as (ean13(old)) stored") || !strings.Contains(gen.Type, `'"pg_temp"."t"'::regclass and a.attname = 'e'`) {
		t.Errorf("a generated column is added with the live column's type, got %+v", gen)
	}
	for _, r := range plan.spec.Relations {
		if r.Schema != "app" || r.Like != `"app"."`+r.Name+`"` {
			t.Errorf("want a temporary table LIKE the live relation under its own name, got %+v", r)
		}
	}
	if temps := []string{plan.spec.Relations[0].Temp, plan.spec.Relations[1].Temp, plan.spec.Relations[2].Temp}; !slices.Equal(temps, []string{"pgy_probe_t0", "pgy_probe_t1", "pgy_probe_t2"}) {
		t.Errorf("each copy is renamed to a name of its own, got %v", temps)
	}
	for _, s := range append(plan.spec.Run, plan.spec.Relations[0].Run...) {
		for _, forbidden := range []string{`"app"."t"`, `"app"."v"`, `"app"."mv"`, `"app"."props"`, `"app"."f"`, `"app"."p"`} {
			if strings.Contains(s.SQL, forbidden) {
				t.Errorf("nothing may be attached to a real object: %s", s.SQL)
			}
		}
	}
}

func TestExprProbeGeneratedEscapesPercent(t *testing.T) {
	c, lc := &schema.Column{Generated: "a % 2"}, &LiveColumn{Generated: "(a % 2)"}
	a := newGeneratedSlot("app.t", "g", c, lc).attach("pg_temp.t", 7)
	if !strings.Contains(a.stmt.SQL, `"pgy_probe_g7" %s generated always as (a %% 2) stored`) || a.name != "pgy_probe_g7" {
		t.Errorf("the statement goes through format(), got %+v", a)
	}
}

func TestExprProbeRenamedTableCopiesTheLiveOne(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	desired.Tables["app.t2"] = desired.Tables["app.t"]
	delete(desired.Tables, "app.t")
	renamed := *desired.Tables["app.t2"]
	renamed.Name, renamed.RenamedFrom = "t2", "t"
	renamed.Columns = map[string]*schema.Column{}
	for cn, c := range desired.Tables["app.t2"].Columns {
		renamed.Columns[cn] = c
	}
	shape := *renamed.Columns["c"]
	shape.RenamedFrom = "c"
	delete(renamed.Columns, "c")
	renamed.Columns["shape"] = &shape
	renamed.Constraints = []*schema.Constraint{{Name: "t_box", Type: "check", Expression: "shape <@ cube '(0, 0),(1, 1)'"}}
	desired.Tables["app.t2"] = &renamed
	plan := collectExprProbes(live, desired, false)
	var rel *probeRelation
	for _, r := range plan.spec.Relations {
		if r.Name == "t2" {
			rel = r
		}
	}
	if rel == nil || rel.Like != `"app"."t"` || rel.Schema != "app" {
		t.Fatalf("a table the plan renames is copied from its current name under its new one, got %+v", plan.spec.Relations)
	}
	if first := rel.Run[0]; first.ID != -1 || first.SQL != `alter table "pg_temp"."t2" rename column "c" to "shape"` {
		t.Errorf("the copy takes the plan's column renames before anything is attached, got %+v", first)
	}
	found := false
	for _, r := range plan.requests {
		found = found || r.key == ExprKey{ExprDefault, "app.t2", "attrs"}
	}
	if !found {
		t.Error("the renamed table's definitions are keyed by its declared name")
	}
}

func TestExprProbeResults(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	plan := collectExprProbes(live, desired, false)
	reply := &probeReply{}
	reply.Relations = make([]probeRelationReply, len(plan.spec.Relations))
	rels := map[string]int{}
	for i, r := range plan.spec.Relations {
		rels[r.Name] = i
	}
	reply.Relations[rels["mv"]].Err, reply.Relations[rels["mv"]].Code = `permission denied for materialized view mv`, insufficientPrivilege
	failedID := -1
	for id, r := range plan.requests {
		switch {
		case r.key == ExprKey{ExprConstraint, "app.t", "t_box"}:
			failedID = id
		case r.key == ExprKey{ExprPolicy, "app.t", "t_p"}:
			self := "(EXISTS ( SELECT 1 FROM " + plan.spec.Relations[r.relation].Temp + " o WHERE (o.id = t.id)))"
			reply.Relations[r.relation].Defs = append(reply.Relations[r.relation].Defs, probeDef{Kind: r.attach.kind, Name: r.attach.name, Parts: []string{self, ""}})
		case r.relation == rels["t"] || r.relation == rels["v"]:
			reply.Relations[r.relation].Defs = append(reply.Relations[r.relation].Defs, probeDef{Kind: r.attach.kind, Name: r.attach.name, Parts: []string{"read " + r.attach.name}})
		case r.key.Kind == ExprDomainDefault:
			reply.Defs = append(reply.Defs, probeDef{Kind: r.attach.kind, Name: r.attach.name, Parts: []string{"read " + r.attach.name}})
		}
	}
	reply.Errors = append(reply.Errors, probeFailure{ID: failedID, Err: `function cube(unknown) does not exist`, Code: "42883"}, probeFailure{ID: -1, Err: `ignored`})

	probes := plan.results(reply)
	if len(probes) != len(plan.requests) {
		t.Fatalf("want a probe per request, got %d for %d", len(probes), len(plan.requests))
	}
	if p := probes[ExprKey{ExprDefault, "app.t", "attrs"}]; p.Err != "" || !slices.Equal(p.Parts, []string{"read attrs"}) || p.Declared != "'a=>1'" {
		t.Errorf("a default is read back under its column, got %+v", p)
	}
	if p := probes[ExprKey{ExprConstraint, "app.t", "t_box"}]; p.Err != `function cube(unknown) does not exist` || p.Parts != nil {
		t.Errorf("a rejected statement fails its own probe only, got %+v", p)
	}
	if p := probes[ExprKey{ExprConstraint, "app.t", "t_excl"}]; p.Err != "" {
		t.Errorf("the other definitions of the table are unaffected, got %+v", p)
	}
	if p := probes[ExprKey{ExprPolicy, "app.t", "t_p"}]; p.Err != selfReference || p.Parts != nil {
		t.Errorf("a definition that reads the probe's copy is not compared, got %+v", p)
	}
	if p := probes[ExprKey{ExprIndex, "app.mv", "mv_part"}]; p.Err != `permission denied for materialized view mv` || !p.Denied {
		t.Errorf("a relation that can't be copied fails its definitions, for a missing privilege, got %+v", p)
	}
	if p := probes[ExprKey{ExprConstraint, "app.t", "t_box"}]; p.Denied {
		t.Errorf("an error other than a missing privilege is not a denial, got %+v", p)
	}
	if p := probes[ExprKey{ExprDomainDefault, "app.props", ""}]; p.Err != "" || !slices.Equal(p.Parts, []string{"read pgy_probe_d0"}) {
		t.Errorf("a domain default is read back under the probe's domain, got %+v", p)
	}
	if p := probes[ExprKey{ExprFunctionArgs, "app.f(h hstore default 'a=>1')", ""}]; p.Err != "no result" {
		t.Errorf("a definition the server didn't return has no result, got %+v", p)
	}
}

func TestExprProbeSimulatedStepIsNotPlannedAgain(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	live.ExprProbes = exprProbesFor(live, desired, exprProbeCases())
	for _, p := range live.ExprProbes {
		p.Parts = []string{"something else"}
	}
	delete(desired.Functions, "app.f(h hstore default 'a=>1')")
	first := Plan(live, desired, true)
	if Render(first) == "" {
		t.Fatal("want the definitions planned as changed")
	}
	again := Plan(Simulate(live, desired), desired, true)
	if sql := Render(again) + RenderConcurrent(again); sql != "" {
		t.Errorf("a later step of a split plan sees the definitions as applied, got:\n%s", sql)
	}
}

func TestExprProbeNeedsAConnection(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	if err := ProbeExpressions(context.Background(), nil, live, desired); err != nil || live.ExprProbes != nil {
		t.Errorf("without a connection nothing is probed, got %v, %v", err, live.ExprProbes)
	}
	live.Offline = true
	if plan := collectExprProbes(live, desired, false); len(plan.requests) == 0 {
		t.Fatal("the fixture has definitions to probe")
	}
	if err := ProbeExpressions(context.Background(), nil, live, desired); err != nil || live.ExprProbes != nil {
		t.Errorf("--from-empty has no database to probe, got %v, %v", err, live.ExprProbes)
	}
}

func TestExprProbeSkipsViewTriggers(t *testing.T) {
	live, desired := exprProbeLive(), exprProbeDesired(t)
	desired.Views["app.v"].Triggers = []*schema.Trigger{{Name: "v_t", Timing: "instead of", Events: []string{"insert"}, Level: "row", Procedure: "app.trg('1e1')"}}
	vt := newLiveTable()
	vt.Triggers["v_t"] = `CREATE TRIGGER v_t INSTEAD OF INSERT ON app.v FOR EACH ROW EXECUTE FUNCTION app.trg('10')`
	live.ViewTables = map[string]*LiveTable{"app.v": vt}
	for _, r := range collectExprProbes(live, desired, false).requests {
		if r.key.Kind == ExprTrigger && r.key.Object == "app.v" {
			t.Errorf("a view's trigger can't be attached to a table, so it is not probed: %+v", r.key)
		}
	}
}

func TestExcludeCore(t *testing.T) {
	for in, want := range map[string]string{
		`USING gist (c WITH &&)`: `USING gist (c WITH &&)`,
		`USING gist (c WITH &&) WITH (fillfactor='70') USING INDEX TABLESPACE fast WHERE ((id > 0))`: `USING gist (c WITH &&) WHERE ((id > 0))`,
		`using gist (c with &&) include (id) with (fillfactor = 70) deferrable`:                      `using gist (c with &&) include (id) deferrable`,
		`(note WITH =) WHERE ((note <> ' USING INDEX TABLESPACE x'::text))`:                          `(note WITH =) WHERE ((note <> ' USING INDEX TABLESPACE x'::text))`,
	} {
		if got, ok := excludeCore(in); !ok || got != want {
			t.Errorf("excludeCore(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := excludeCore("not an exclusion"); ok {
		t.Error("want no core for a definition that doesn't parse")
	}
}
