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

func notNullNameDB() *schema.Database {
	d := notNullDB()
	d.Tables["app.t"].Columns["a"].NotNullName = "a_must_exist"
	return d
}

func TestNotNullNameCreate(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	if sql := Render(Plan(live, notNullNameDB(), false)); !strings.Contains(sql, `("a" int constraint "a_must_exist" not null, "id" int not null)`) {
		t.Errorf("a new table names the NOT NULL constraint in the column definition:\n%s", sql)
	}
	live = notNullLive(180000)
	delete(live.Tables["app.t"].Columns, "a")
	if p := Plan(live, notNullNameDB(), false); len(p.Creates) != 1 || p.Creates[0] != `alter table "app"."t" add column "a" int constraint "a_must_exist" not null;` {
		t.Errorf("an added column names the NOT NULL constraint, got %v", p.Creates)
	}
	d := notNullNameDB()
	d.Tables["app.t"].Columns["a"].NotValid = true
	if sql := Render(Plan(emptyLive(), d, false)); !strings.Contains(sql, `alter table "app"."t" add constraint "a_must_exist" not null "a" not valid;`) {
		t.Errorf("a NOT VALID constraint takes the declared name:\n%s", sql)
	}
}

func TestNotNullNameSkippedIfExists(t *testing.T) {
	live := notNullLive(180000)
	lc := live.Tables["app.t"].Columns["a"]
	lc.Nullable, lc.NotNullName = false, "a_must_exist"
	if p := Plan(live, notNullNameDB(), false); Render(p) != "" || len(p.Concurrent) != 0 || len(p.Warnings) != 0 {
		t.Errorf("a constraint with the declared name needs nothing, got %q, %v, %+v", Render(p), p.Concurrent, p.Warnings)
	}
	lc.NotNullName = "t_a_not_null"
	if p := Plan(live, notNullDB(), false); Render(p) != "" {
		t.Errorf("without notNullName the live name is unmanaged, got %q", Render(p))
	}
}

func TestNotNullNameRename(t *testing.T) {
	live := notNullLive(180000)
	lc := live.Tables["app.t"].Columns["a"]
	lc.Nullable, lc.NotNullName = false, "t_a_not_null"
	p := Plan(live, notNullNameDB(), false)
	if want := `alter table "app"."t" rename constraint "t_a_not_null" to "a_must_exist";` + "\n"; Render(p) != want || len(p.Concurrent) != 0 {
		t.Errorf("expected only a rename, got %q and %v", Render(p), p.Concurrent)
	}
	if sql := Render(Plan(Simulate(live, notNullNameDB()), notNullNameDB(), false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
	lc.NotNullNotValid = "t_a_not_null"
	p = Plan(live, notNullNameDB(), false)
	if !findAlter(p, `alter table "app"."t" rename constraint "t_a_not_null" to "a_must_exist";`) || len(p.Concurrent) != 1 || p.Concurrent[0] != `alter table "app"."t" validate constraint "a_must_exist";` {
		t.Errorf("a renamed NOT VALID constraint is validated under its new name, got %q and %v", Render(p), p.Concurrent)
	}
	d := notNullNameDB()
	d.Tables["app.t"].Columns["a"].Nullable = true
	if sql := Render(Plan(live, d, false)); strings.Contains(sql, "rename constraint") {
		t.Errorf("a column that becomes nullable loses the constraint, it is not renamed:\n%s", sql)
	}
}

func TestNotNullNameAddedToExistingColumn(t *testing.T) {
	p := Plan(notNullLive(180000), notNullNameDB(), false)
	if !findAlter(p, `alter table "app"."t" add constraint "a_must_exist" not null "a" not valid;`) || len(p.Concurrent) != 1 || p.Concurrent[0] != `alter table "app"."t" validate constraint "a_must_exist";` {
		t.Errorf("expected the named constraint added NOT VALID and validated, got %q and %v", Render(p), p.Concurrent)
	}
	d := notNullNameDB()
	d.Tables["app.t"].PrimaryKey = []string{"a"}
	p = Plan(notNullLive(180000), d, false)
	if !findAlter(p, `alter table "app"."t" add constraint "a_must_exist" not null "a";`) || strings.Contains(Render(p), "set not null") {
		t.Errorf("a primary key column gets the named constraint in one step:\n%s", Render(p))
	}
	warned := false
	for _, w := range p.Warnings {
		warned = warned || strings.Contains(w.Message, "SET NOT NULL on app.t.a scans the whole table")
	}
	if !warned {
		t.Errorf("adding the validated constraint scans the table like SET NOT NULL, warnings: %+v", p.Warnings)
	}
}

func TestNotNullNameBeforePG18(t *testing.T) {
	live := notNullLive(170004)
	p := Plan(live, notNullNameDB(), false)
	if !findAlter(p, `alter table "app"."t" alter column "a" set not null;`) || strings.Contains(Render(p), "a_must_exist") {
		t.Errorf("before PostgreSQL 18 the name is left out, got:\n%s", Render(p))
	}
	warned := false
	for _, w := range p.Warnings {
		warned = warned || strings.Contains(w.Message, "notNullName on column app.t.a is ignored: PostgreSQL 17 does not record a name")
	}
	if !warned {
		t.Errorf("expected a warning that the name is ignored, got %+v", p.Warnings)
	}
	live.Tables["app.t"].Columns["a"].Nullable = false
	if p := Plan(live, notNullNameDB(), false); Render(p) != "" {
		t.Errorf("a server without constraint names has nothing to rename, got %q", Render(p))
	}
	live.ServerVersion = 180000
	for _, w := range Plan(live, notNullNameDB(), false).Warnings {
		if strings.Contains(w.Message, "notNullName") {
			t.Errorf("no warning on PostgreSQL 18, got %s", w.Message)
		}
	}
}

func TestDefaultNotNullName(t *testing.T) {
	for _, c := range []struct{ table, column, want string }{
		{"app.t", "a", "t_a_not_null"},
		{"app.long_table_name_that_goes_on_and_on_for_a_while", "another_quite_long_column_name_here_too", "long_table_name_that_goes_o_another_quite_long_column__not_null"},
		{"app.t", "a_column_name_that_is_exactly_sixty_three_bytes_long_0123456789ab", "t_a_column_name_that_is_exactly_sixty_three_bytes_long_not_null"},
	} {
		if got := DefaultNotNullName(c.table, c.column); got != c.want {
			t.Errorf("DefaultNotNullName(%s, %s) = %s, want %s", c.table, c.column, got, c.want)
		}
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
