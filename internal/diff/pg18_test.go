package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestVirtualGeneratedColumnRendered(t *testing.T) {
	c := &schema.Column{Type: "int", Nullable: true, Generated: "a * 2", Virtual: true}
	if got, want := renderColumn("v", c), `"v" int generated always as (a * 2) virtual`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNotEnforcedConstraintsRendered(t *testing.T) {
	fk := &schema.ForeignKey{Columns: []string{"pid"}, RefTable: "p", RefColumns: []string{"id"}, OnDelete: "cascade", InitiallyDeferred: true, NotValid: true, NotEnforced: true}
	if got, want := renderFKDef(fk), `foreign key ("pid") references "p"("id") on delete cascade deferrable initially deferred not enforced`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	ct := &schema.Constraint{Type: "check", Expression: "a > 0", NoInherit: true, NotEnforced: true}
	if got, want := renderConstraintDef(ct), `check (a > 0) no inherit not enforced`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNotEnforcedEquivalentToLiveAndChangeDetected(t *testing.T) {
	live := constraintAlterLive(map[string]string{
		"f": "FOREIGN KEY (id) REFERENCES orgs(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED NOT ENFORCED",
		"c": "CHECK ((id > 0)) NO INHERIT NOT ENFORCED",
	})
	desired := constraintAlterDesired(
		[]*schema.Constraint{{Name: "c", Type: "check", Expression: "id > 0", NoInherit: true, NotEnforced: true}},
		[]*schema.ForeignKey{{Name: "f", Columns: []string{"id"}, RefTable: "orgs", RefColumns: []string{"id"}, OnDelete: "cascade", InitiallyDeferred: true, NotValid: true, NotEnforced: true}},
	)
	if p := Plan(live, desired, true); len(p.Alters) != 0 {
		t.Errorf("equivalent NOT ENFORCED constraints must not diff, got %+v", p.Alters)
	}
	desired.Tables["public.users"].Constraints[0].NotEnforced = false
	desired.Tables["public.users"].ForeignKeys[0].NotEnforced = false
	if p := Plan(live, desired, true); len(p.Alters) != 4 {
		t.Errorf("enforcing a NOT ENFORCED constraint is a definition change, got %+v", p.Alters)
	}
}

func TestTemporalConstraintColumns(t *testing.T) {
	if got, want := constraintColumns([]string{"id", "valid WITHOUT OVERLAPS"}), `"id", "valid" without overlaps`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := constraintColumns([]string{"pid", "PERIOD valid"}), `"pid", period "valid"`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	live := constraintAlterLive(map[string]string{
		"u": "UNIQUE (id, valid WITHOUT OVERLAPS)",
		"f": "FOREIGN KEY (id, PERIOD valid) REFERENCES orgs(id, PERIOD valid)",
	})
	desired := constraintAlterDesired(
		[]*schema.Constraint{{Name: "u", Type: "unique", Columns: []string{"id", "valid without overlaps"}}},
		[]*schema.ForeignKey{{Name: "f", Columns: []string{"id", "period valid"}, RefTable: "orgs", RefColumns: []string{"id", "period valid"}}},
	)
	if p := Plan(live, desired, true); len(p.Alters) != 0 {
		t.Errorf("equivalent temporal constraints must not diff, got %+v", p.Alters)
	}
}

func TestNotNullNotValidColumn(t *testing.T) {
	col := func() *schema.Column { return &schema.Column{Type: "int", NotValid: true} }
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}, "a": col()}}}}
	live := emptyLive()
	live.ServerVersion = 180001
	p := Plan(live, d, false)
	sql := Render(p)
	if !strings.Contains(sql, `("a" int, "id" int not null)`) || !strings.Contains(sql, `alter table "app"."t" add constraint "t_a_not_null" not null "a" not valid;`) || len(p.Concurrent) != 0 {
		t.Errorf("a new table gets the NOT NULL as NOT VALID and never validates it:\n%s", sql)
	}
	live = liveWithTable("app.t", map[string]*LiveColumn{"id": {Type: "integer"}, "a": {Type: "integer", Nullable: true}})
	live.ServerVersion = 180001
	live.Schemas["app"] = true
	if p := Plan(live, d, false); !strings.Contains(Render(p), `add constraint "t_a_not_null" not null "a" not valid;`) || len(p.Concurrent) != 0 {
		t.Errorf("an existing nullable column gets a NOT VALID NOT NULL, not validated:\n%s", sql)
	}
	live.Tables["app.t"].Columns["a"] = &LiveColumn{Type: "integer", NotNullNotValid: "t_a_nn"}
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("a live NOT VALID NOT NULL is left alone, got:\n%s", sql)
	}
	if p := Plan(live, d, false); len(p.Concurrent) != 0 {
		t.Errorf("notValid must not validate, got %v", p.Concurrent)
	}
	d.Tables["app.t"].Columns["a"].NotValid = false
	if p := Plan(live, d, false); !strings.Contains(strings.Join(p.Concurrent, "\n"), `validate constraint "t_a_nn"`) {
		t.Errorf("without notValid it is validated, got %v", p.Concurrent)
	}
	d.Tables["app.t"].Columns["a"].NotValid = true
	live = emptyLive()
	live.ServerVersion = 170004
	found := false
	for _, w := range Plan(live, d, false).Warnings {
		found = found || strings.Contains(w.Message, "notValid on column app.t.a needs PostgreSQL 18")
	}
	if !found {
		t.Error("notValid needs PostgreSQL 18")
	}
	if sql := Render(Plan(Simulate(emptyLive(), d), d, false)); strings.Contains(sql, "constraint") {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}
