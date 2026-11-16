package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func viewLive() *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Views = map[string]bool{"app.v": true, "app.w": true}
	live.MatViews = map[string]bool{"app.mv": true}
	live.RelComments = map[string]string{"app.v": "vee"}
	live.ViewDependents = map[string][]string{"app.v": {"app.w"}}
	live.ViewProbes = map[string]*ViewProbe{
		"app.v":  {DesiredDef: " SELECT 1", LiveDef: " SELECT 1", DesiredCols: []string{"id integer"}, LiveCols: []string{"id integer"}},
		"app.w":  {DesiredDef: " SELECT 2", LiveDef: " SELECT 2"},
		"app.mv": {DesiredDef: " SELECT 3", LiveDef: " SELECT 3"},
	}
	return live
}

func probedViews() *schema.Database {
	return &schema.Database{Views: map[string]*schema.View{
		"app.v":  {Name: "v", Query: "select id from app.t", Comment: "vee"},
		"app.w":  {Name: "w", Query: "select id from app.v"},
		"app.mv": {Name: "mv", Materialized: true, Query: "select 3"},
	}}
}

func TestViewProbeUnchangedIsLeftAlone(t *testing.T) {
	if sql := Render(Plan(viewLive(), probedViews(), true)); sql != "" {
		t.Errorf("matching views must be left alone, got:\n%s", sql)
	}
}

func TestViewProbeCompatibleChangeReplaces(t *testing.T) {
	live := viewLive()
	live.ViewProbes["app.v"] = &ViewProbe{DesiredDef: " SELECT id FROM app.t WHERE true", LiveDef: " SELECT 1", DesiredCols: []string{"id integer", "a text"}, LiveCols: []string{"id integer"}}
	sql := Render(Plan(live, probedViews(), false))
	if !strings.Contains(sql, `create or replace view "app"."v" as select id from app.t;`) || strings.Contains(sql, "drop view") {
		t.Errorf("a query change that keeps the columns replaces the view, got:\n%s", sql)
	}
}

func TestViewProbeIncompatibleChangeRecreates(t *testing.T) {
	live := viewLive()
	live.ViewProbes["app.v"] = &ViewProbe{DesiredDef: " SELECT a", LiveDef: " SELECT id", DesiredCols: []string{"a text"}, LiveCols: []string{"id integer"}}
	safe := Plan(live, probedViews(), false)
	if sql := Render(safe); sql != "" || !strings.Contains(safe.Warnings[0].Message, "its columns changed from (id integer) to (a text)") {
		t.Errorf("without --unsafe: only a warning, got:\n%s\n%+v", sql, safe.Warnings)
	}
	sql := Render(Plan(live, probedViews(), true))
	dropW, dropV := strings.Index(sql, `drop view if exists "app"."w";`), strings.Index(sql, `drop view if exists "app"."v";`)
	createV, createW := strings.Index(sql, `create or replace view "app"."v"`), strings.Index(sql, `create or replace view "app"."w"`)
	if dropW < 0 || dropV < dropW || createV < dropV || createW < createV {
		t.Errorf("want the dependent dropped first and both recreated in order, got:\n%s", sql)
	}
	if !strings.Contains(sql, `comment on view "app"."v" is 'vee';`) {
		t.Errorf("the comment the drop discards must be restored, got:\n%s", sql)
	}
}

func TestViewProbeUndeclaredDependentBlocks(t *testing.T) {
	live := viewLive()
	live.ViewDependents["app.v"] = []string{"app.w", "app.other"}
	live.ViewProbes["app.v"] = &ViewProbe{DesiredDef: " SELECT a", LiveDef: " SELECT id", DesiredCols: []string{"a text"}, LiveCols: []string{"id integer"}}
	p := Plan(live, probedViews(), true)
	if sql := Render(p); strings.Contains(sql, "drop view") {
		t.Errorf("an undeclared dependent must block the recreate, got:\n%s", sql)
	}
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0].Message, "app.other, which the YAML doesn't declare") {
		t.Errorf("want a warning naming the dependent: %+v", p.Warnings)
	}
}

func TestViewProbeMaterializedViewRecreates(t *testing.T) {
	live := viewLive()
	live.ViewProbes["app.mv"] = &ViewProbe{DesiredDef: " SELECT 4", LiveDef: " SELECT 3"}
	p := Plan(live, probedViews(), true)
	sql := Render(p)
	if !strings.Contains(sql, `drop materialized view if exists "app"."mv";`) || !strings.Contains(sql, `create materialized view if not exists "app"."mv"`) {
		t.Errorf("a changed materialized view is recreated under --unsafe, got:\n%s", sql)
	}
	lock := false
	for _, w := range p.Warnings {
		lock = lock || w.Kind == "lock" && strings.Contains(w.Message, "recreating materialized view app.mv")
	}
	if !lock {
		t.Errorf("want a lock warning: %+v", p.Warnings)
	}
	live.ViewProbes["app.mv"].Err = "relation does not exist"
	if sql := Render(Plan(live, probedViews(), true)); strings.Contains(sql, "materialized view") {
		t.Errorf("a materialized view that couldn't be compared is left alone, got:\n%s", sql)
	}
}

func TestViewProbeErrorReplacesPlainView(t *testing.T) {
	live := viewLive()
	live.ViewProbes["app.v"] = &ViewProbe{Err: `column "a" does not exist`, LiveDef: " SELECT id"}
	p := Plan(live, probedViews(), false)
	if !strings.Contains(Render(p), `create or replace view "app"."v"`) || !strings.Contains(p.Warnings[0].Message, "couldn't be compared") {
		t.Errorf("a view whose query uses something the plan adds is replaced, got:\n%s\n%+v", Render(p), p.Warnings)
	}
}

func TestViewProbeSimulateConverges(t *testing.T) {
	live := viewLive()
	live.ViewProbes["app.v"] = &ViewProbe{DesiredDef: " SELECT 9", LiveDef: " SELECT 1", DesiredCols: []string{"id integer"}, LiveCols: []string{"id integer"}}
	d := probedViews()
	if sql := Render(Plan(Simulate(live, d), d, false)); strings.Contains(sql, "view") {
		t.Errorf("expected no view changes after simulate, got:\n%s", sql)
	}
}
