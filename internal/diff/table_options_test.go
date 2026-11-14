package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func intPtr(v int) *int { return &v }

func optionsDB() *schema.Database {
	return &schema.Database{
		Tables: map[string]*schema.Table{"app.t": {
			Name: "t", ClusterOn: "t_a_idx",
			Columns: map[string]*schema.Column{
				"a": {Type: "int", StatisticsTarget: intPtr(500)},
				"b": {Type: "int", StatisticsTarget: intPtr(-1)},
			},
			Indexes:  []*schema.Index{{Name: "t_a_idx", Columns: []string{"a"}}},
			Triggers: []*schema.Trigger{{Name: "t_trg", Timing: "before", Events: []string{"insert"}, Level: "row", Procedure: "app.f()", Enabled: "disabled"}},
		}},
		Views: map[string]*schema.View{"app.v": {Name: "v", Query: "select 1 as one", ColumnComments: map[string]string{"one": "the number one"}}},
	}
}

func TestTableOptionsCreate(t *testing.T) {
	p := Plan(emptyLive(), optionsDB(), false)
	for _, want := range []string{
		`alter table "app"."t" alter column "a" set statistics 500;`,
		`alter table "app"."t" cluster on "t_a_idx";`,
		`alter table "app"."t" disable trigger "t_trg";`,
		`comment on column "app"."v"."one" is 'the number one';`,
	} {
		if !findCreate(p, want) && !findAlter(p, want) {
			t.Errorf("missing %q in\n%s", want, Render(p))
		}
	}
	if findAlter(p, `alter table "app"."t" alter column "b" set statistics -1;`) || findCreate(p, `alter table "app"."t" alter column "b" set statistics -1;`) {
		t.Error("a default (-1) target needs no statement on a new table")
	}
}

func TestTableOptionsSkippedAndAltered(t *testing.T) {
	live := Simulate(emptyLive(), optionsDB())
	if sql := Render(Plan(live, optionsDB(), false)); sql != "" {
		t.Fatalf("simulated state must match, got:\n%s", sql)
	}
	d := optionsDB()
	tbl := d.Tables["app.t"]
	tbl.Columns["a"].StatisticsTarget = intPtr(-1)
	tbl.ClusterOn = "none"
	tbl.Triggers[0].Enabled = "always"
	d.Views["app.v"].ColumnComments["one"] = "1"
	p := Plan(live, d, false)
	for _, want := range []string{
		`alter table "app"."t" alter column "a" set statistics -1;`,
		`alter table "app"."t" set without cluster;`,
		`alter table "app"."t" enable always trigger "t_trg";`,
		`comment on column "app"."v"."one" is '1';`,
	} {
		if !findCreate(p, want) && !findAlter(p, want) {
			t.Errorf("missing %q in\n%s", want, Render(p))
		}
	}
	tbl.Triggers[0].Enabled = ""
	tbl.ClusterOn = ""
	tbl.Columns["a"].StatisticsTarget = nil
	d.Views["app.v"].ColumnComments = nil
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("unmanaged options must not diff, got:\n%s", sql)
	}
}
