package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func recreatedViewLive() *Live {
	live := liveWithTablePK("app.t", map[string]*LiveColumn{"id": {Type: "integer"}, "b": {Type: "integer", Nullable: true}})
	live.Schemas["app"] = true
	live.CurrentUser = "owner"
	live.Views = map[string]bool{"app.v": true}
	live.RelComments = map[string]string{"app.v": "unmanaged comment"}
	live.TableGrants = map[string]map[string]map[string]bool{"app.v": {"reader": {"select": true}}}
	live.ColumnGrants = map[string]map[string]map[string]map[string]bool{"app.v": {"b": {"reader": {"update": true}, "editor": {"select*": true}}}}
	live.ViewColumnComments = map[string]map[string]string{"app.v": {"b": "bee"}}
	live.ViewColumnDefaults = map[string]map[string]string{"app.v": {"b": "0"}}
	live.ViewOptions = map[string]map[string]string{"app.v": {"security_barrier": "true"}}
	live.Owners = map[string]string{"rel:app.v": "other"}
	vt := newLiveTable()
	vt.Triggers["v_ins"] = "CREATE TRIGGER v_ins INSTEAD OF INSERT ON app.v FOR EACH ROW EXECUTE FUNCTION app.ins()"
	vt.TriggerComments["v_ins"] = "declared, comment unmanaged"
	vt.Triggers["v_upd"] = "CREATE TRIGGER v_upd INSTEAD OF UPDATE ON app.v FOR EACH ROW EXECUTE FUNCTION app.upd()"
	vt.TriggerComments["v_upd"] = "undeclared"
	vt.Rules["v_del"] = &LiveRule{Event: "delete", Instead: true, Def: "CREATE RULE v_del AS\n    ON DELETE TO app.v DO INSTEAD NOTHING;"}
	vt.RuleComments["v_del"] = "undeclared rule"
	live.ViewTables = map[string]*LiveTable{"app.v": vt}
	return live
}

func recreatedViewYAML() *schema.Database {
	return &schema.Database{
		Tables: map[string]*schema.Table{"app.t": {Name: "t", PrimaryKey: []string{"id"}, Columns: map[string]*schema.Column{"id": {Type: "integer"}, "b": {Type: "integer", Nullable: true}}}},
		Views: map[string]*schema.View{"app.v": {Name: "v", Query: "select id, b from app.t",
			Triggers: []*schema.Trigger{{Name: "v_ins", Timing: "instead of", Events: []string{"insert"}, Level: "row", Procedure: "app.ins()"}}}},
	}
}

var restoredView = []string{
	`grant select on table "app"."v" to "reader";`,
	`grant select ("b") on table "app"."v" to "editor" with grant option;`,
	`grant update ("b") on table "app"."v" to "reader";`,
	`comment on view "app"."v" is 'unmanaged comment';`,
	`comment on column "app"."v"."b" is 'bee';`,
	`alter view "app"."v" alter column "b" set default 0;`,
	`comment on trigger "v_ins" on "app"."v" is 'declared, comment unmanaged';`,
	`CREATE TRIGGER v_upd INSTEAD OF UPDATE ON app.v FOR EACH ROW EXECUTE FUNCTION app.upd();`,
	`comment on trigger "v_upd" on "app"."v" is 'undeclared';`,
	"CREATE RULE v_del AS\n    ON DELETE TO app.v DO INSTEAD NOTHING;",
	`comment on rule "v_del" on "app"."v" is 'undeclared rule';`,
	`alter view "app"."v" owner to "other";`,
}

func assertRestoredView(t *testing.T, path string, p *PlanDiff, prune bool) {
	t.Helper()
	sql := Render(p)
	for _, want := range restoredView {
		pruned := prune && (strings.Contains(want, "v_upd") || strings.Contains(want, "v_del"))
		want1 := 1
		if pruned {
			want1 = 0
		}
		if got := strings.Count(sql, want); got != want1 {
			t.Errorf("%s (prune %v): %q appears %d times in:\n%s", path, prune, want, got, sql)
		}
	}
	if !strings.Contains(sql, `create or replace view "app"."v" with (security_barrier = true) as select id, b from app.t;`) {
		t.Errorf("%s: the options the YAML leaves unmanaged are kept, got:\n%s", path, sql)
	}
	if !strings.Contains(sql, `create trigger "v_ins" INSTEAD OF INSERT on "app"."v"`) {
		t.Errorf("%s: the declared trigger is created again, got:\n%s", path, sql)
	}
}

func TestViewRecreatedForTypeChangeRestoresUnmanaged(t *testing.T) {
	for _, prune := range []bool{false, true} {
		live, d := recreatedViewLive(), recreatedViewYAML()
		live.ColumnDependents = map[string][]ColumnDependent{"app.t.b": {{Kind: "view", Name: "app.v", Table: "app.v"}}}
		d.Tables["app.t"].Columns["b"].Type = "bigint"
		assertRestoredView(t, "type change", PlanWith(live, d, Options{Unsafe: true, Prune: prune}), prune)
	}
}

func TestViewRecreatedForChangedColumnsRestoresUnmanaged(t *testing.T) {
	for _, prune := range []bool{false, true} {
		live, d := recreatedViewLive(), recreatedViewYAML()
		live.ViewProbes = map[string]*ViewProbe{"app.v": {DesiredDef: " SELECT t.id, t.b", LiveDef: " SELECT t.b", DesiredCols: []string{"id integer", "b integer"}, LiveCols: []string{"b integer"}}}
		p := PlanWith(live, d, Options{Unsafe: true, Prune: prune})
		statementOrder(t, Render(p), `drop view if exists "app"."v";`, `create or replace view "app"."v"`, `comment on view "app"."v" is 'unmanaged comment';`)
		assertRestoredView(t, "changed columns", p, prune)
	}
}

func TestViewRecreatedForExtensionUpdateRestoresUnmanaged(t *testing.T) {
	for _, prune := range []bool{false, true} {
		live, d := recreatedViewLive(), recreatedViewYAML()
		live.Extensions = map[string]bool{"pg_stat_statements": true}
		live.ExtensionDefs = map[string]*LiveExtension{"pg_stat_statements": {Schema: "public", Version: "1.6", UpdateDependents: []UpdateDependent{{Kind: "view", Name: "app.v"}}}}
		d.Extensions = []*schema.Extension{{Name: "pg_stat_statements", Version: "1.10"}}
		assertRestoredView(t, "extension update", PlanWith(live, d, Options{Unsafe: true, Prune: prune}), prune)
	}
}

func TestViewRecreatedKeepsManagedAttributesToTheYAML(t *testing.T) {
	live, d := recreatedViewLive(), recreatedViewYAML()
	live.ColumnDependents = map[string][]ColumnDependent{"app.t.b": {{Kind: "view", Name: "app.v", Table: "app.v"}}}
	d.Tables["app.t"].Columns["b"].Type = "bigint"
	vw := d.Views["app.v"]
	vw.Comment, vw.Owner, vw.Grants = "declared", "owner", map[string][]string{"reader": {"select"}}
	vw.ColumnComments, vw.ColumnDefaults = map[string]string{"b": "declared bee"}, map[string]string{"b": "1"}
	vw.Triggers[0].Comment = "declared trigger"
	sql := Render(Plan(live, d, true))
	for _, want := range []string{`comment on view "app"."v" is 'declared';`, `comment on column "app"."v"."b" is 'declared bee';`, `alter view "app"."v" alter column "b" set default 1;`, `comment on trigger "v_ins" on "app"."v" is 'declared trigger';`, `grant select on table "app"."v" to "reader";`} {
		if strings.Count(sql, want) != 1 {
			t.Errorf("want %q once in:\n%s", want, sql)
		}
	}
	for _, unwanted := range []string{"unmanaged comment", "'bee'", "set default 0", "declared, comment unmanaged", `owner to "other"`} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("%q is managed by the YAML and must not be restored, got:\n%s", unwanted, sql)
		}
	}
}

func TestViewChangeLeavesViewWithQuerylessDependentAlone(t *testing.T) {
	live := viewLive()
	live.ViewProbes["app.v"] = &ViewProbe{DesiredDef: " SELECT a", LiveDef: " SELECT id", DesiredCols: []string{"a text"}, LiveCols: []string{"id integer"}}
	d := probedViews()
	d.Views["app.w"].Query = ""
	p := Plan(live, d, true)
	if sql := Render(p); strings.Contains(sql, "drop view") || len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0].Message, "app.w, which the YAML doesn't declare") {
		t.Errorf("a dependent declared without a query can't be recreated, got %v\n%s", p.Warnings, sql)
	}
}

func TestRenamedViewKeepsColumnDefaults(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Views = map[string]bool{"app.v0": true}
	live.ViewColumnDefaults = map[string]map[string]string{"app.v0": {"a": "0"}}
	d := &schema.Database{Views: map[string]*schema.View{"app.v": {Name: "v", RenamedFrom: "v0", Query: "select 1 as a", ColumnDefaults: map[string]string{"a": "0"}}}}
	if sql := Render(Plan(live, d, false)); sql != `alter view "app"."v0" rename to "v";`+"\n" {
		t.Errorf("a renamed view keeps its column defaults, got:\n%s", sql)
	}
}
