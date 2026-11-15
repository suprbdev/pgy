package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func notNullLive(version int) *Live {
	live := emptyLive()
	live.ServerVersion = version
	live.Schemas["app"] = true
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	lt.Columns["a"] = &LiveColumn{Type: "integer", Nullable: true}
	live.Tables["app.t"] = lt
	return live
}

func notNullDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name:    "app.t",
		Columns: map[string]*schema.Column{"id": {Type: "int"}, "a": {Type: "int"}},
	}}}
}

func TestSetNotNullUsesNotValidConstraintOnPG18(t *testing.T) {
	p := Plan(notNullLive(180000), notNullDB(), false)
	if !findAlter(p, `alter table "app"."t" add constraint "t_a_not_null" not null "a" not valid;`) {
		t.Errorf("expected a NOT VALID not-null constraint, got alters %v", p.Alters)
	}
	if len(p.Concurrent) != 1 || p.Concurrent[0] != `alter table "app"."t" validate constraint "t_a_not_null";` {
		t.Errorf("validation belongs in the no-transaction migration, got %v", p.Concurrent)
	}
	if strings.Contains(Render(p), "set not null") {
		t.Errorf("SET NOT NULL must not be emitted on PostgreSQL 18:\n%s", Render(p))
	}
	for _, w := range p.Warnings {
		if strings.Contains(w.Message, "NOT NULL") {
			t.Errorf("no lock warning expected, got %s", w.Message)
		}
	}
}

func TestSetNotNullBeforePG18(t *testing.T) {
	p := Plan(notNullLive(170000), notNullDB(), false)
	if !findAlter(p, `alter table "app"."t" alter column "a" set not null;`) || len(p.Concurrent) != 0 {
		t.Errorf("expected SET NOT NULL before PostgreSQL 18, got alters %v, concurrent %v", p.Alters, p.Concurrent)
	}
}

func TestPendingNotNullValidationIsRetried(t *testing.T) {
	live := notNullLive(180000)
	lc := live.Tables["app.t"].Columns["a"]
	lc.Nullable, lc.NotNullNotValid = false, "t_a_not_null"
	p := Plan(live, notNullDB(), false)
	if len(p.Concurrent) != 1 || p.Concurrent[0] != `alter table "app"."t" validate constraint "t_a_not_null";` || Render(p) != "" {
		t.Errorf("expected only the pending VALIDATE, got %q and %v", Render(p), p.Concurrent)
	}
}

func TestSetNotNullOnPrimaryKeyColumnStaysPlain(t *testing.T) {
	d := notNullDB()
	d.Tables["app.t"].Columns["a"].PrimaryKey = true
	p := Plan(notNullLive(180000), d, false)
	if len(p.Concurrent) != 0 || strings.Contains(Render(p), "not valid") {
		t.Errorf("a primary key column keeps SET NOT NULL, got %s / %v", Render(p), p.Concurrent)
	}
}
