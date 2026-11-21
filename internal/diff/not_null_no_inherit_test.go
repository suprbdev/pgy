package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func notNullNoInheritDB() *schema.Database {
	d := notNullDB()
	d.Tables["app.t"].Columns["a"].NotNullNoInherit = true
	return d
}

func TestNotNullNoInheritCreate(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	if sql := Render(Plan(live, notNullNoInheritDB(), false)); !strings.Contains(sql, `("a" int not null no inherit, "id" int not null)`) {
		t.Errorf("a new table makes the NOT NULL constraint NO INHERIT in the column definition:\n%s", sql)
	}
	d := notNullNoInheritDB()
	d.Tables["app.t"].Columns["a"].NotNullName = "a_here"
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, `"a" int constraint "a_here" not null no inherit`) {
		t.Errorf("a named constraint is NO INHERIT too:\n%s", sql)
	}
	d.Tables["app.t"].Columns["a"].NotValid = true
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, `alter table "app"."t" add constraint "a_here" not null "a" no inherit not valid;`) {
		t.Errorf("a NOT VALID constraint is NO INHERIT too:\n%s", sql)
	}
}

func TestNotNullNoInheritSkippedIfExists(t *testing.T) {
	live := notNullLive(180000)
	lc := live.Tables["app.t"].Columns["a"]
	lc.Nullable, lc.NotNullName, lc.NotNullNoInherit = false, "t_a_not_null", true
	if p := Plan(live, notNullNoInheritDB(), false); Render(p) != "" || len(p.Concurrent) != 0 || len(p.Warnings) != 0 {
		t.Errorf("a NO INHERIT constraint that exists needs nothing, got %q, %v, %+v", Render(p), p.Concurrent, p.Warnings)
	}
}

func TestNotNullNoInheritAlter(t *testing.T) {
	live := notNullLive(180000)
	lc := live.Tables["app.t"].Columns["a"]
	lc.Nullable, lc.NotNullName = false, "t_a_not_null"
	if sql := Render(Plan(live, notNullNoInheritDB(), false)); sql != `alter table "app"."t" alter constraint "t_a_not_null" no inherit;`+"\n" {
		t.Errorf("an inheritable constraint becomes NO INHERIT in place, got %q", sql)
	}
	if sql := Render(Plan(Simulate(live, notNullNoInheritDB()), notNullNoInheritDB(), false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
	lc.NotNullNoInherit = true
	if sql := Render(Plan(live, notNullDB(), false)); sql != `alter table "app"."t" alter constraint "t_a_not_null" inherit;`+"\n" {
		t.Errorf("a NO INHERIT constraint becomes inheritable in place, got %q", sql)
	}
	d := notNullNoInheritDB()
	d.Tables["app.t"].Columns["a"].NotNullName = "a_here"
	lc.NotNullNoInherit = false
	p := Plan(live, d, false)
	if !findAlter(p, `alter table "app"."t" rename constraint "t_a_not_null" to "a_here";`) || !findAlter(p, `alter table "app"."t" alter constraint "a_here" no inherit;`) {
		t.Errorf("a renamed constraint is changed under its new name, got:\n%s", Render(p))
	}
}

func TestNotNullNoInheritAddedToExistingColumn(t *testing.T) {
	p := Plan(notNullLive(180000), notNullNoInheritDB(), false)
	if !findAlter(p, `alter table "app"."t" add constraint "t_a_not_null" not null "a" no inherit not valid;`) || len(p.Concurrent) != 1 || p.Concurrent[0] != `alter table "app"."t" validate constraint "t_a_not_null";` {
		t.Errorf("expected the NO INHERIT constraint added NOT VALID and validated, got %q and %v", Render(p), p.Concurrent)
	}
	if strings.Contains(Render(p), "alter constraint") {
		t.Errorf("a constraint added NO INHERIT needs no ALTER CONSTRAINT:\n%s", Render(p))
	}
	d := notNullNoInheritDB()
	d.Tables["app.t"].Columns["a"].Validate = schema.ValidateImmediate
	if p := Plan(notNullLive(180000), d, false); !findAlter(p, `alter table "app"."t" add constraint "t_a_not_null" not null "a" no inherit;`) {
		t.Errorf("validate: immediate adds the NO INHERIT constraint in one step:\n%s", Render(p))
	}
}

func TestNotNullNoInheritBeforePG18(t *testing.T) {
	p := Plan(notNullLive(170004), notNullNoInheritDB(), false)
	warned := false
	for _, w := range p.Warnings {
		warned = warned || strings.Contains(w.Message, "notNullNoInherit on column app.t.a needs PostgreSQL 18")
	}
	if !warned {
		t.Errorf("expected a version warning, got %+v", p.Warnings)
	}
}

func TestNotNullNoInheritOnInheritedColumn(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{
		"app.base": {Name: "base", Columns: map[string]*schema.Column{"a": {Type: "int", Nullable: true}}},
		"app.kid":  {Name: "kid", Inherits: []string{"app.base"}, Columns: map[string]*schema.Column{"a": {NullableSet: true, NotNullNoInherit: true}}},
	}}
	live := emptyLive()
	live.ServerVersion = 180000
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, `alter table "app"."kid" add constraint "kid_a_not_null" not null "a" no inherit;`) {
		t.Errorf("an inheritance child adds its own NO INHERIT NOT NULL:\n%s", sql)
	}
}
