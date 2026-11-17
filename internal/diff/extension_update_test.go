package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

const updateToOneTen = `alter extension "pg_stat_statements" update to '1.10';`

// extensionUpdateLive has pg_stat_statements 1.6 installed, with deps as the
// objects that use it and each view and materialized view among them live.
func extensionUpdateLive(deps ...UpdateDependent) *Live {
	l := emptyLive()
	l.Schemas["app"] = true
	l.CurrentUser = "owner"
	l.Extensions["pg_stat_statements"] = true
	l.ExtensionDefs = map[string]*LiveExtension{"pg_stat_statements": {Schema: "public", Version: "1.6", UpdateDependents: deps}}
	for _, d := range deps {
		switch d.Kind {
		case "view":
			l.Views[d.Name] = true
		case "matview":
			l.MatViews[d.Name] = true
		}
	}
	return l
}

// extensionUpdateDesired raises pg_stat_statements to 1.10 and declares the
// plain views named by queries, each with its query.
func extensionUpdateDesired(queries map[string]string) *schema.Database {
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "pg_stat_statements", Version: "1.10"}},
		Views:      map[string]*schema.View{},
	}
	for k, q := range queries {
		d.Views[k] = &schema.View{Name: k[strings.IndexByte(k, '.')+1:], Query: q}
	}
	return d
}

func dependentsWarnings(t *testing.T, p *PlanDiff) []string {
	t.Helper()
	out := []string{}
	for _, w := range p.Warnings {
		if w.Kind != "dependents" {
			t.Errorf("unexpected warning %+v", w)
		}
		out = append(out, w.Message)
	}
	return out
}

func TestExtensionUpdateWithoutDependents(t *testing.T) {
	d := extensionUpdateDesired(map[string]string{"app.other": "select 1 as one"})
	live := extensionUpdateLive()
	live.Views["app.other"] = true
	for _, opts := range []Options{{}, {Unsafe: true}, {Unsafe: true, Prune: true}} {
		p := PlanWith(live, d, opts)
		if sql := Render(p); sql != updateToOneTen+"\n" || len(p.Warnings) != 0 {
			t.Errorf("%+v: want only the update, got:\n%s%v", opts, sql, p.Warnings)
		}
	}
	live.ExtensionDefs["pg_stat_statements"].UpdateDependents = nil
	if sql := Render(Plan(live, d, true)); sql != updateToOneTen+"\n" {
		t.Errorf("dependents that were not loaded: want only the update, got:\n%s", sql)
	}
}

func TestExtensionUpdateRecreatesDependentView(t *testing.T) {
	live := extensionUpdateLive(UpdateDependent{Kind: "view", Name: "app.slow"})
	live.RelComments = map[string]string{"app.slow": "slow statements"}
	live.TableGrants["app.slow"] = map[string]map[string]bool{"reader": {"select": true}}
	live.Owners = map[string]string{"rel:app.slow": "dba"}
	live.ViewColumnComments = map[string]map[string]string{"app.slow": {"calls": "how often"}}
	live.ViewColumnDefaults = map[string]map[string]string{"app.slow": {"calls": "0"}}
	d := extensionUpdateDesired(map[string]string{"app.slow": "select query, calls from pg_stat_statements"})
	p := Plan(live, d, true)
	wantStatements(t, p.Creates, []string{
		`drop view "app"."slow";`,
		updateToOneTen,
		`create or replace view "app"."slow" as select query, calls from pg_stat_statements;`,
	})
	wantStatements(t, p.Alters, []string{
		`grant select on table "app"."slow" to "reader";`,
		`comment on view "app"."slow" is 'slow statements';`,
		`comment on column "app"."slow"."calls" is 'how often';`,
		`alter view "app"."slow" alter column "calls" set default 0;`,
		`alter view "app"."slow" owner to "dba";`,
	})
	if len(p.Drops) != 0 || len(p.Warnings) != 0 {
		t.Errorf("drops %v, warnings %v", p.Drops, p.Warnings)
	}
	if !live.Views["app.slow"] || live.RelComments["app.slow"] == "" {
		t.Error("the live snapshot must not be modified")
	}
}

func TestExtensionUpdateWithoutUnsafeWarns(t *testing.T) {
	live := extensionUpdateLive(
		UpdateDependent{Kind: "view", Name: "app.slow"},
		UpdateDependent{Kind: "view", Name: "app.top", On: "app.slow"},
	)
	d := extensionUpdateDesired(map[string]string{
		"app.slow": "select query, calls from pg_stat_statements",
		"app.top":  "select query from app.slow",
	})
	p := Plan(live, d, false)
	if sql := Render(p); sql != updateToOneTen+"\n" {
		t.Errorf("without --unsafe the plan is the update alone, got:\n%s", sql)
	}
	ws := dependentsWarnings(t, p)
	if len(ws) != 1 || !strings.Contains(ws[0], "extension pg_stat_statements: views depend on it, directly or through other views (app.slow, app.top)") ||
		!strings.Contains(ws[0], "rejects the update to 1.10") || !strings.Contains(ws[0], "--unsafe drops them before the update and recreates them after it") {
		t.Errorf("want one warning naming the views and --unsafe: %v", ws)
	}
	left := []string{}
	for _, w := range PlanReportingUnsafe(live, d, Options{}).Warnings {
		if w.Kind == "unsafe" {
			left = append(left, w.Statement)
		}
	}
	wantStatements(t, left, []string{
		`drop view "app"."top";`,
		`drop view "app"."slow";`,
		`create or replace view "app"."slow" as select query, calls from pg_stat_statements;`,
		`create or replace view "app"."top" as select query from app.slow;`,
	})
}

func TestExtensionUpdateListsFewViewsWithoutUnsafe(t *testing.T) {
	deps, queries := []UpdateDependent{}, map[string]string{}
	for _, name := range []string{"app.a", "app.b", "app.c", "app.d", "app.e"} {
		deps = append(deps, UpdateDependent{Kind: "view", Name: name})
		queries[name] = "select calls from pg_stat_statements"
	}
	ws := dependentsWarnings(t, Plan(extensionUpdateLive(deps...), extensionUpdateDesired(queries), false))
	if len(ws) != 1 || !strings.Contains(ws[0], "(app.a, app.b, app.c and 2 more)") {
		t.Errorf("want the first views listed and the rest counted: %v", ws)
	}
}

func TestExtensionUpdateChainedViews(t *testing.T) {
	live := extensionUpdateLive(
		UpdateDependent{Kind: "view", Name: "app.slow"},
		UpdateDependent{Kind: "view", Name: "app.count", On: "app.slow"},
		UpdateDependent{Kind: "view", Name: "app.top", On: "app.slow"},
		UpdateDependent{Kind: "view", Name: "app.best", On: "app.top"},
	)
	d := extensionUpdateDesired(map[string]string{
		"app.slow":  "select query, calls from pg_stat_statements",
		"app.top":   "select query from app.slow",
		"app.count": "select count(*) as n from app.slow",
		"app.best":  "select query from app.top limit 1",
	})
	p := Plan(live, d, true)
	wantStatements(t, p.Creates, []string{
		`drop view "app"."count";`,
		`drop view "app"."best";`,
		`drop view "app"."top";`,
		`drop view "app"."slow";`,
		updateToOneTen,
		`create or replace view "app"."slow" as select query, calls from pg_stat_statements;`,
		`create or replace view "app"."count" as select count(*) as n from app.slow;`,
		`create or replace view "app"."top" as select query from app.slow;`,
		`create or replace view "app"."best" as select query from app.top limit 1;`,
	})
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.Warnings)
	}
}

func TestExtensionUpdateKeepsMaterializedView(t *testing.T) {
	live := extensionUpdateLive(
		UpdateDependent{Kind: "matview", Name: "app.snap"},
		UpdateDependent{Kind: "view", Name: "app.slow"},
		UpdateDependent{Kind: "matview", Name: "app.daily", On: "app.slow"},
		UpdateDependent{Kind: "view", Name: "app.onsnap", On: "app.snap"},
		UpdateDependent{Kind: "view", Name: "app.free"},
	)
	d := extensionUpdateDesired(map[string]string{
		"app.slow":   "select query, calls from pg_stat_statements",
		"app.onsnap": "select calls from app.snap",
		"app.free":   "select count(*) as n from pg_stat_statements",
	})
	d.Views["app.snap"] = &schema.View{Name: "snap", Materialized: true, Query: "select calls from pg_stat_statements"}
	d.Views["app.daily"] = &schema.View{Name: "daily", Materialized: true, Query: "select calls from app.slow"}
	p := Plan(live, d, true)
	wantStatements(t, p.Creates, []string{
		`drop view "app"."free";`,
		updateToOneTen,
		`create or replace view "app"."free" as select count(*) as n from pg_stat_statements;`,
	})
	ws := dependentsWarnings(t, p)
	if len(ws) != 1 || !strings.Contains(ws[0], "extension pg_stat_statements: objects that pgy doesn't drop for an update depend on it, directly or through views (materialized view app.snap, materialized view app.daily)") ||
		!strings.Contains(ws[0], "rejects the update to 1.10") || !strings.Contains(ws[0], "drop them in a hand-written migration first") {
		t.Errorf("want one warning naming the materialized views: %v", ws)
	}
	safe := dependentsWarnings(t, Plan(live, d, false))
	if len(safe) != 2 || !strings.Contains(safe[0], "views depend on it, directly or through other views (app.free)") || safe[1] != ws[0] {
		t.Errorf("without --unsafe: want the view --unsafe recreates, then the materialized views: %v", safe)
	}
}

func TestExtensionUpdateReportsSQLBodyRoutines(t *testing.T) {
	live := extensionUpdateLive(
		UpdateDependent{Kind: "function", Name: "app.calls()"},
		UpdateDependent{Kind: "view", Name: "app.slow"},
		UpdateDependent{Kind: "procedure", Name: "app.report(integer)", On: "app.slow"},
		UpdateDependent{Kind: "view", Name: "app.free"},
	)
	d := extensionUpdateDesired(map[string]string{
		"app.slow": "select query, calls from pg_stat_statements",
		"app.free": "select count(*) as n from pg_stat_statements",
	})
	p := Plan(live, d, true)
	sql := Render(p)
	if strings.Contains(sql, "drop function") || strings.Contains(sql, "drop procedure") || strings.Contains(sql, `"app"."slow"`) || !strings.Contains(sql, `drop view "app"."free";`) {
		t.Errorf("routines and the views they use stay, other views are recreated; got:\n%s", sql)
	}
	ws := dependentsWarnings(t, p)
	if len(ws) != 1 || !strings.Contains(ws[0], "(function app.calls(), procedure app.report(integer))") {
		t.Errorf("want one warning naming the routines: %v", ws)
	}
}

func TestExtensionUpdateKeepsViewWithOtherDependent(t *testing.T) {
	live := extensionUpdateLive(
		UpdateDependent{Kind: "view", Name: "app.slow"},
		UpdateDependent{Kind: "other", Name: "policy p on table app.t", On: "app.slow"},
	)
	d := extensionUpdateDesired(map[string]string{"app.slow": "select query, calls from pg_stat_statements"})
	p := Plan(live, d, true)
	if sql := Render(p); sql != updateToOneTen+"\n" {
		t.Errorf("a view with a dependent pgy can't recreate stays, got:\n%s", sql)
	}
	if ws := dependentsWarnings(t, p); len(ws) != 1 || !strings.Contains(ws[0], "(policy p on table app.t)") {
		t.Errorf("want one warning naming the policy: %v", ws)
	}
}

func TestExtensionUpdateUndeclaredView(t *testing.T) {
	live := extensionUpdateLive(
		UpdateDependent{Kind: "view", Name: "app.slow"},
		UpdateDependent{Kind: "view", Name: "app.legacy", On: "app.slow"},
		UpdateDependent{Kind: "matview", Name: "app.oldsnap", On: "app.legacy"},
	)
	d := extensionUpdateDesired(map[string]string{"app.slow": "select query, calls from pg_stat_statements"})
	p := Plan(live, d, true)
	if sql := Render(p); sql != updateToOneTen+"\n" {
		t.Errorf("an undeclared view is not dropped, nor the views it uses; got:\n%s", sql)
	}
	if ws := dependentsWarnings(t, p); len(ws) != 1 || !strings.Contains(ws[0], "(view app.legacy (not declared))") {
		t.Errorf("want one warning naming the undeclared view: %v", ws)
	}

	p = PlanWith(live, d, Options{Unsafe: true, Prune: true})
	wantStatements(t, p.Creates, []string{
		`drop materialized view "app"."oldsnap";`,
		`drop view "app"."legacy";`,
		`drop view "app"."slow";`,
		updateToOneTen,
		`create or replace view "app"."slow" as select query, calls from pg_stat_statements;`,
	})
	if len(p.Drops) != 0 || len(p.Warnings) != 0 {
		t.Errorf("--prune drops undeclared views before the update, once: drops %v, warnings %v", p.Drops, p.Warnings)
	}

	live.Views["other.legacy"] = true
	live.ExtensionDefs["pg_stat_statements"].UpdateDependents = []UpdateDependent{{Kind: "view", Name: "other.legacy"}}
	p = PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if strings.Contains(Render(p), `"other"."legacy"`) {
		t.Errorf("a view in a schema the YAML doesn't manage is never dropped, got:\n%s", Render(p))
	}
	if ws := dependentsWarnings(t, p); len(ws) != 1 || !strings.Contains(ws[0], "(view other.legacy (not declared))") {
		t.Errorf("want one warning naming the unmanaged view: %v", ws)
	}
}

func TestExtensionUpdateAttributeOnlyViewStays(t *testing.T) {
	live := extensionUpdateLive(UpdateDependent{Kind: "view", Name: "app.slow"})
	d := extensionUpdateDesired(nil)
	d.Views["app.slow"] = &schema.View{Name: "slow", Comment: "managed elsewhere"}
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if strings.Contains(Render(p), "drop view") {
		t.Errorf("a view declared without a query can't be recreated, got:\n%s", Render(p))
	}
	if ws := dependentsWarnings(t, p); len(ws) != 1 || !strings.Contains(ws[0], "(view app.slow (not declared))") {
		t.Errorf("want one warning naming the view: %v", ws)
	}
}

func TestExtensionUpdateRenamedView(t *testing.T) {
	live := extensionUpdateLive(UpdateDependent{Kind: "view", Name: "app.slow"}, UpdateDependent{Kind: "view", Name: "app.top", On: "app.slow"})
	d := extensionUpdateDesired(map[string]string{
		"app.sluggish": "select query, calls from pg_stat_statements",
		"app.top":      "select query from app.sluggish",
	})
	d.Views["app.sluggish"].RenamedFrom = "slow"
	p := Plan(live, d, true)
	wantStatements(t, p.Creates, []string{
		`alter view "app"."slow" rename to "sluggish";`,
		`drop view "app"."top";`,
		`drop view "app"."sluggish";`,
		updateToOneTen,
		`create or replace view "app"."sluggish" as select query, calls from pg_stat_statements;`,
		`create or replace view "app"."top" as select query from app.sluggish;`,
	})
}

func TestExtensionUpdateDropsSharedViewOnce(t *testing.T) {
	live := extensionUpdateLive(UpdateDependent{Kind: "view", Name: "app.both"}, UpdateDependent{Kind: "view", Name: "app.slow"})
	live.Extensions["hstore"] = true
	live.ExtensionDefs["hstore"] = &LiveExtension{Schema: "public", Version: "1.4", UpdateDependents: []UpdateDependent{{Kind: "view", Name: "app.both"}}}
	d := extensionUpdateDesired(map[string]string{
		"app.both": "select akeys(''::hstore) as k, (select count(*) from pg_stat_statements) as n",
		"app.slow": "select query, calls from pg_stat_statements",
	})
	d.Extensions = append(d.Extensions, &schema.Extension{Name: "hstore", Version: "1.8"})
	wantStatements(t, Plan(live, d, true).Creates, []string{
		`drop view "app"."both";`,
		`alter extension "hstore" update to '1.8';`,
		`drop view "app"."slow";`,
		updateToOneTen,
		`create or replace view "app"."both" as select akeys(''::hstore) as k, (select count(*) from pg_stat_statements) as n;`,
		`create or replace view "app"."slow" as select query, calls from pg_stat_statements;`,
	})
}

func TestExtensionUpdateWithoutPathLeavesViews(t *testing.T) {
	live := extensionUpdateLive(UpdateDependent{Kind: "view", Name: "app.slow"})
	live.ExtensionDefs["pg_stat_statements"].UpdateTargets = map[string]bool{"1.7": true}
	d := extensionUpdateDesired(map[string]string{"app.slow": "select query, calls from pg_stat_statements"})
	p := Plan(live, d, true)
	if sql := Render(p); sql != "" {
		t.Errorf("no update is planned, so nothing is dropped; got:\n%s", sql)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "no update path") {
		t.Errorf("warnings: %v", p.Warnings)
	}
}

func TestExtensionUpdateViewRecreatedForItsColumns(t *testing.T) {
	live := extensionUpdateLive(UpdateDependent{Kind: "view", Name: "app.slow"}, UpdateDependent{Kind: "view", Name: "app.top", On: "app.slow"})
	live.ViewDependents = map[string][]string{"app.slow": {"app.top"}}
	live.ViewProbes = map[string]*ViewProbe{
		"app.slow": {DesiredDef: " SELECT query", LiveDef: " SELECT query, calls", DesiredCols: []string{"query text"}, LiveCols: []string{"query text", "calls bigint"}},
		"app.top":  {DesiredDef: " SELECT query", LiveDef: " SELECT query"},
	}
	d := extensionUpdateDesired(map[string]string{
		"app.slow": "select query from pg_stat_statements",
		"app.top":  "select query from app.slow",
	})
	wantStatements(t, Plan(live, d, true).Creates, []string{
		`drop view if exists "app"."top";`,
		`drop view if exists "app"."slow";`,
		updateToOneTen,
		`create or replace view "app"."slow" as select query from pg_stat_statements;`,
		`create or replace view "app"."top" as select query from app.slow;`,
	})
}

func TestExtensionUpdateViewOnRetypedColumn(t *testing.T) {
	live := extensionUpdateLive(UpdateDependent{Kind: "view", Name: "app.v"})
	live.Tables["app.t"] = newLiveTable()
	live.Tables["app.t"].Columns["b"] = &LiveColumn{Type: "integer"}
	live.ColumnDependents = map[string][]ColumnDependent{"app.t.b": {{Kind: "view", Name: "app.v", Table: "app.v"}}}
	live.RelComments = map[string]string{"app.v": "kept"}
	d := extensionUpdateDesired(map[string]string{"app.v": "select b, (select count(*) from pg_stat_statements) as n from app.t"})
	d.Tables = map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"b": {Type: "bigint"}}}}
	p := Plan(live, d, true)
	wantStatements(t, p.Creates, []string{
		`drop view "app"."v";`,
		updateToOneTen,
		`drop view if exists "app"."v";`,
		`alter table "app"."t" alter column "b" type bigint;`,
		`create or replace view "app"."v" as select b, (select count(*) from pg_stat_statements) as n from app.t;`,
	})
	wantStatements(t, p.Alters, []string{`comment on view "app"."v" is 'kept';`})
}

func TestColumnTypeChangeViewRecreatedForItsColumns(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"b": {Type: "integer"}})
	live.Schemas["app"] = true
	live.Views["app.v"] = true
	live.ColumnDependents = map[string][]ColumnDependent{"app.t.b": {{Kind: "view", Name: "app.v", Table: "app.v"}}}
	live.ViewDependents = map[string][]string{}
	live.ViewProbes = map[string]*ViewProbe{"app.v": {DesiredDef: " SELECT b", LiveDef: " SELECT b, 1", DesiredCols: []string{"b integer"}, LiveCols: []string{"b integer", "x integer"}}}
	d := &schema.Database{
		Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"b": {Type: "bigint"}}}},
		Views:  map[string]*schema.View{"app.v": {Name: "v", Query: "select b from app.t"}},
	}
	wantStatements(t, Plan(live, d, true).Creates, []string{
		`drop view if exists "app"."v";`,
		`drop view if exists "app"."v";`,
		`alter table "app"."t" alter column "b" type bigint;`,
		`create or replace view "app"."v" as select b from app.t;`,
	})
}

func TestExtensionUpdateRunsInASessionOfItsOwn(t *testing.T) {
	live := extensionUpdateLive(UpdateDependent{Kind: "matview", Name: "app.agg"}, UpdateDependent{Kind: "view", Name: "app.kept"})
	d := extensionUpdateDesired(nil)
	d.Tables = map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}}}
	p := Plan(live, d, true)
	wantStatements(t, p.ExtensionUpdates, []string{updateToOneTen})
	for _, s := range p.Creates {
		if strings.Contains(s, "alter extension") {
			t.Errorf("the update runs before the rest of the plan, not in it: %v", p.Creates)
		}
	}
	if sql := Render(p); !strings.HasPrefix(sql, updateToOneTen+"\n") {
		t.Errorf("Render starts with the update:\n%s", sql)
	}
	if got := RenderExtensionUpdates(p); got != "-- pgy:new-session\n"+updateToOneTen+"\n" {
		t.Errorf("RenderExtensionUpdates: %q", got)
	}
}

func TestExtensionUpdateWithDroppedViewsStaysInTheMigration(t *testing.T) {
	live := extensionUpdateLive(UpdateDependent{Kind: "view", Name: "app.slow"})
	d := extensionUpdateDesired(map[string]string{"app.slow": "select query, calls from pg_stat_statements"})
	if p := Plan(live, d, true); len(p.ExtensionUpdates) != 0 {
		t.Errorf("an update pgy drops views for shares their transaction: %v", p.ExtensionUpdates)
	}
	if p := Plan(live, d, false); len(p.ExtensionUpdates) != 1 {
		t.Errorf("without --unsafe nothing is dropped, so the update runs on its own: %v", p.Creates)
	}
}

func TestExtensionUpdateNeedingANewExtensionStaysInTheMigration(t *testing.T) {
	live := extensionUpdateLive()
	live.AvailableExtensions = map[string]*AvailableExtension{
		"pg_stat_statements": {DefaultVersion: "1.10", Requires: map[string][]string{"1.6": nil, "1.10": {"helper"}}},
		"helper":             {DefaultVersion: "1.0", Requires: map[string][]string{"1.0": nil}},
	}
	d := extensionUpdateDesired(nil)
	d.Extensions = append(d.Extensions, &schema.Extension{Name: "helper"})
	p := Plan(live, d, false)
	if len(p.ExtensionUpdates) != 0 {
		t.Errorf("the update needs helper, which the migration creates: %v", p.ExtensionUpdates)
	}
	wantStatements(t, p.Creates, []string{`create extension "helper";`, updateToOneTen})
}
