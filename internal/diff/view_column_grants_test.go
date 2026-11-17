package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func viewColumnGrantsYAML(grants map[string]map[string][]string) *schema.Database {
	return &schema.Database{
		Views: map[string]*schema.View{
			"app.v":  {Name: "v", Query: "select 1 as a, 2 as b", ColumnGrants: grants},
			"app.mv": {Name: "mv", Materialized: true, Query: "select 1 as id", ColumnGrants: map[string]map[string][]string{"id": {"reader": {"select"}}}},
		},
	}
}

func TestViewColumnGrantsCreate(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	p := Plan(live, viewColumnGrantsYAML(map[string]map[string][]string{"a": {"reader": {"select"}, "editor": {"update*"}}}), false)
	for _, want := range []string{
		`grant update ("a") on table "app"."v" to "editor" with grant option;`,
		`grant select ("a") on table "app"."v" to "reader";`,
		`grant select ("id") on table "app"."mv" to "reader";`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %s in:\n%s", want, Render(p))
		}
	}
}

func TestViewColumnGrantsSkippedIfPresent(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Views["app.v"], live.MatViews["app.mv"] = true, true
	live.ColumnGrants["app.v"] = map[string]map[string]map[string]bool{"a": {"reader": {"select": true}}, "b": {"other": {"select": true}}}
	live.ColumnGrants["app.mv"] = map[string]map[string]map[string]bool{"id": {"reader": {"select": true}}}
	p := Plan(live, viewColumnGrantsYAML(map[string]map[string][]string{"a": {"reader": {"select"}}}), false)
	if sql := Render(p); strings.Contains(sql, "grant") || strings.Contains(sql, "revoke") {
		t.Errorf("matching column grants, and those of a column the YAML doesn't list, emit nothing:\n%s", sql)
	}
}

func TestViewColumnGrantsRevoke(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Views["app.v"], live.MatViews["app.mv"] = true, true
	live.ColumnGrants["app.v"] = map[string]map[string]map[string]bool{"a": {"reader": {"select": true, "update": true}, "public": {"select": true}}, "b": {"other": {"select": true}}}
	p := Plan(live, viewColumnGrantsYAML(map[string]map[string][]string{"a": {"reader": {"select"}}, "b": {}}), false)
	for _, want := range []string{
		`revoke select ("a") on table "app"."v" from public;`,
		`revoke update ("a") on table "app"."v" from "reader";`,
		`revoke select ("b") on table "app"."v" from "other";`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %s in:\n%s", want, Render(p))
		}
	}
	warned := false
	for _, w := range p.Warnings {
		warned = warned || strings.Contains(w.Message, `from public`)
	}
	if !warned {
		t.Errorf("a PUBLIC column revoke is warned about: %+v", p.Warnings)
	}
}

func TestViewColumnGrantsAfterViewRevoke(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Views["app.v"] = true
	live.TableGrants["app.v"] = map[string]map[string]bool{"reader": {"select": true}}
	live.ColumnGrants["app.v"] = map[string]map[string]map[string]bool{"a": {"reader": {"select": true}}}
	d := viewColumnGrantsYAML(map[string]map[string][]string{"a": {"reader": {"select"}}})
	delete(d.Views, "app.mv")
	d.Views["app.v"].Grants = map[string][]string{"reader": {}}
	sql := Render(Plan(live, d, false))
	statementOrder(t, sql, `revoke select on table "app"."v" from "reader";`, `grant select ("a") on table "app"."v" to "reader";`)
}

func TestViewColumnGrantsExpandAll(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 170000
	live.Schemas["app"] = true
	live.Views["app.v"] = true
	live.ColumnGrants["app.v"] = map[string]map[string]map[string]bool{"a": {"reader": {"insert": true, "references": true, "select": true, "update": true}}}
	d := viewColumnGrantsYAML(map[string]map[string][]string{"a": {"reader": {"all"}}})
	delete(d.Views, "app.mv")
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("all on a column is every column privilege:\n%s", sql)
	}
}

func TestViewColumnGrantsSimulated(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	d := viewColumnGrantsYAML(map[string]map[string][]string{"a": {"reader": {"select"}}})
	if p := Plan(Simulate(live, d), d, false); planSize(p) != 0 {
		t.Errorf("a plan after the simulated one must be empty, got:\n%s", Render(p))
	}
}

func TestViewRecreatedKeepsDeclaredColumnGrants(t *testing.T) {
	live, d := recreatedViewLive(), recreatedViewYAML()
	live.ColumnDependents = map[string][]ColumnDependent{"app.t.b": {{Kind: "view", Name: "app.v", Table: "app.v"}}}
	d.Tables["app.t"].Columns["b"].Type = "bigint"
	d.Views["app.v"].ColumnGrants = map[string]map[string][]string{"b": {"reader": {"select"}}}
	sql := Render(Plan(live, d, true))
	if strings.Count(sql, `grant select ("b") on table "app"."v" to "reader";`) != 1 {
		t.Errorf("the declared column grant is made once on the recreated view:\n%s", sql)
	}
	for _, unwanted := range []string{`"editor"`, `update ("b")`} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("live grants on a column the YAML manages are not restored (%s):\n%s", unwanted, sql)
		}
	}
}
