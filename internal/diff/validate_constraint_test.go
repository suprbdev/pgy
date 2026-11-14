package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestValidateConstraintWhenNotValidRemoved(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	lt.Columns["ref"] = &LiveColumn{Type: "integer"}
	lt.Constraints["t_id_check"] = true
	lt.ConstraintDefs["t_id_check"] = "CHECK ((id > 0)) NOT VALID"
	lt.Constraints["t_ref_fkey"] = true
	lt.ConstraintDefs["t_ref_fkey"] = "FOREIGN KEY (ref) REFERENCES app.t(id) NOT VALID"
	live.Tables["app.t"] = lt
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name:        "t",
		Columns:     map[string]*schema.Column{"id": {Type: "int"}, "ref": {Type: "int"}},
		Constraints: []*schema.Constraint{{Name: "t_id_check", Type: "check", Expression: "id > 0"}},
		ForeignKeys: []*schema.ForeignKey{{Name: "t_ref_fkey", Columns: []string{"ref"}, RefTable: "app.t", RefColumns: []string{"id"}}},
	}}}
	p := Plan(live, d, false)
	for _, want := range []string{`alter table "app"."t" validate constraint "t_id_check";`, `alter table "app"."t" validate constraint "t_ref_fkey";`} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in\n%s", want, Render(p))
		}
	}
	d.Tables["app.t"].Constraints[0].NotValid = true
	d.Tables["app.t"].ForeignKeys[0].NotValid = true
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("constraints still declared notValid must be left alone, got:\n%s", sql)
	}
}
