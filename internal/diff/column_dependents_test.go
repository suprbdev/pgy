package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestRecreateColumnDependents(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"b": {Type: "integer"}, "c": {Type: "integer"}})
	live.Schemas["app"] = true
	live.Views = map[string]bool{"app.v": true, "app.vv": true}
	live.CurrentUser = "owner"
	live.RelComments = map[string]string{"app.v": "kept"}
	live.TableGrants = map[string]map[string]map[string]bool{"app.vv": {"reader": {"select": true}}}
	live.Owners = map[string]string{"rel:app.v": "other"}
	live.Tables["app.t"].Triggers = map[string]string{"t_b": "CREATE TRIGGER t_b ..."}
	live.ColumnDependents = map[string][]ColumnDependent{
		"app.t.b":   {{Kind: "view", Name: "app.v", Table: "app.v"}, {Kind: "trigger", Name: "t_b", Table: "app.t"}},
		"app.t.c":   {{Kind: "view", Name: "app.v", Table: "app.v"}},
		"app.v.b":   {{Kind: "view", Name: "app.vv", Table: "app.vv"}},
	}
	d := &schema.Database{
		Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"b": {Type: "bigint"}, "c": {Type: "bigint"}},
			Triggers: []*schema.Trigger{{Name: "t_b", Timing: "before", Events: []string{"update of b"}, Level: "row", Procedure: "app.touch()"}}}},
		Views: map[string]*schema.View{"app.v": {Name: "v", Query: "select b, c from app.t"}, "app.vv": {Name: "vv", Query: "select b from app.v"}},
	}
	out, _, pre, post, _, warns := recreateColumnDependents(live, d, Options{Unsafe: true})
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	want := `drop trigger "t_b" on "app"."t";|drop view "app"."vv";|drop view "app"."v";`
	if got := strings.Join(pre["app.t"], "|"); got != want {
		t.Errorf("pre:\n got %s\nwant %s", got, want)
	}
	if out.Views["app.v"] || out.Tables["app.t"].Triggers["t_b"] != "" || !live.Views["app.v"] {
		t.Error("dependents must be removed from the returned copy only")
	}
	joined := strings.Join(post, "\n")
	for _, w := range []string{`grant select on table "app"."vv" to "reader";`, `comment on view "app"."v" is 'kept';`, `alter view "app"."v" owner to "other";`} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing %q in post:\n%s", w, joined)
		}
	}
	if _, _, pre, _, _, _ := recreateColumnDependents(live, d, Options{}); len(pre) != 0 {
		t.Error("without --unsafe types don't change, so nothing is dropped")
	}
	delete(d.Views, "app.vv")
	_, _, pre, _, _, warns = recreateColumnDependents(live, d, Options{Unsafe: true})
	if len(pre) != 0 || len(warns) != 2 || !strings.Contains(warns[0].Message, "view app.vv depends on it and isn't declared") {
		t.Errorf("undeclared view: pre %v, warnings %v", pre, warns)
	}
}

func wholeRelationDependents() (*Live, *schema.Database) {
	live := liveWithTablePK("app.t", map[string]*LiveColumn{"id": {Type: "integer"}, "b": {Type: "integer", Nullable: true}})
	live.Schemas["app"] = true
	live.Tables["app.r"] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer"}}, Policies: map[string]*LivePolicy{"r_p": {Cmd: "all", Using: "(EXISTS ( SELECT 1 FROM app.w WHERE (w.n > 0)))"}}, HasPK: true}
	live.Views = map[string]bool{"app.v": true, "app.w": true}
	live.ColumnDependents = map[string][]ColumnDependent{
		"app.t.b": {{Kind: "view", Name: "app.v", Table: "app.v"}},
		"app.w.n": {{Kind: "policy", Name: "r_p", Table: "app.r"}},
	}
	live.ViewDependents = map[string][]string{"app.v": {"app.w"}}
	d := &schema.Database{
		Tables: map[string]*schema.Table{
			"app.t": {Name: "t", PrimaryKey: []string{"id"}, Columns: map[string]*schema.Column{"id": {Type: "integer"}, "b": {Type: "bigint", Nullable: true}}},
			"app.r": {Name: "r", PrimaryKey: []string{"id"}, Columns: map[string]*schema.Column{"id": {Type: "integer"}},
				Policies: []*schema.Policy{{Name: "r_p", Using: "exists (select 1 from app.w where w.n > 0)"}}},
		},
		Views: map[string]*schema.View{
			"app.v": {Name: "v", Query: "select id, b from app.t"},
			"app.w": {Name: "w", Query: "select count(*) as n from app.v", DependsOn: []string{"view app.v"}},
		},
	}
	return live, d
}

func TestTypeChangeFollowsWholeRelationViewDependents(t *testing.T) {
	live, d := wholeRelationDependents()
	p := Plan(live, d, true)
	sql := Render(p)
	statementOrder(t, sql,
		`drop policy "r_p" on "app"."r";`,
		`drop view "app"."w";`,
		`drop view "app"."v";`,
		`alter table "app"."t" alter column "b" type bigint;`,
		`create or replace view "app"."v" as select id, b from app.t;`,
		`create or replace view "app"."w" as select count(*) as n from app.v;`,
		`create policy "r_p" on "app"."r" using (exists (select 1 from app.w where w.n > 0));`)
	for _, w := range p.Warnings {
		if w.Kind == "dependents" {
			t.Errorf("everything is declared, so nothing is reported: %v", w)
		}
	}

	delete(d.Views, "app.w")
	p = Plan(live, d, true)
	found := false
	for _, w := range p.Warnings {
		found = found || w.Kind == "dependents" && strings.Contains(w.Message, "view app.w depends on it and isn't declared")
	}
	if !found || strings.Contains(Render(p), "drop view") {
		t.Errorf("a view using a dependent view as a whole must be declared to be recreated, got %v\n%s", p.Warnings, Render(p))
	}
}

func TestTypeChangeFollowsRenamedDependents(t *testing.T) {
	live, d := wholeRelationDependents()
	delete(live.Tables, "app.r")
	delete(d.Tables, "app.r")
	live.Views = map[string]bool{"app.v0": true, "app.w": true}
	live.Tables["app.t0"] = live.Tables["app.t"]
	delete(live.Tables, "app.t")
	live.Tables["app.t0"].Columns["b0"] = live.Tables["app.t0"].Columns["b"]
	delete(live.Tables["app.t0"].Columns, "b")
	live.ColumnDependents = map[string][]ColumnDependent{"app.t0.b0": {{Kind: "view", Name: "app.v0", Table: "app.v0"}}}
	live.ViewDependents = map[string][]string{"app.v0": {"app.w"}}
	d.Tables["app.t"].RenamedFrom = "t0"
	d.Tables["app.t"].Columns["b"].RenamedFrom = "b0"
	d.Views["app.v"].RenamedFrom = "v0"
	d.Views["app.w"].Query = "select count(*) as n from app.v"

	p := Plan(live, d, true)
	statementOrder(t, Render(p),
		`alter table "app"."t0" rename to "t";`,
		`alter view "app"."v0" rename to "v";`,
		`alter table "app"."t" rename column "b0" to "b";`,
		`drop view "app"."w";`,
		`drop view "app"."v";`,
		`alter table "app"."t" alter column "b" type bigint;`,
		`create or replace view "app"."v" as select id, b from app.t;`,
		`create or replace view "app"."w" as select count(*) as n from app.v;`)
	for _, w := range p.Warnings {
		if w.Kind == "dependents" {
			t.Errorf("a renamed view is declared under its new name: %v", w)
		}
	}
	if live.ColumnDependents["app.t0.b0"][0].Name != "app.v0" || live.ViewDependents["app.v0"][0] != "app.w" {
		t.Error("the live snapshot passed in must not be changed")
	}
}
