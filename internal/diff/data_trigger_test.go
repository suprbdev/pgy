package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func triggerDataDB() *schema.Database {
	on := true
	return &schema.Database{Tables: map[string]*schema.Table{
		"app.audit": {Name: "audit", ColumnOrder: []string{"id", "tbl"},
			Columns:    map[string]*schema.Column{"id": {Type: "bigint", Identity: "always"}, "tbl": {Type: "text"}},
			PrimaryKey: []string{"id"}},
		"app.account": {Name: "account", ColumnOrder: []string{"id", "seats"},
			Columns:    map[string]*schema.Column{"id": {Type: "int"}, "seats": {Type: "int"}},
			PrimaryKey: []string{"id"},
			Triggers: []*schema.Trigger{
				{Name: "account_seats", Constraint: true, Timing: "after", Events: []string{"insert", "update"}, Level: "row", Deferrable: true, InitiallyDeferred: true, Procedure: "app.check_seats()"},
				{Name: "account_audit", Timing: "after", Events: []string{"insert"}, Level: "row", Procedure: "app.audit_ins()", Enabled: "always"},
			},
			Rules:            []*schema.Rule{{Name: "account_log", Event: "insert", Do: "insert into app.audit (tbl) values ('account')"}},
			RowLevelSecurity: &on,
			Data:             &schema.TableData{Rows: []map[string]any{{"id": 1, "seats": 3}, {"id": 2, "seats": 5}}}},
		"app.event": {Name: "event", ColumnOrder: []string{"id", "kind"},
			Columns:     map[string]*schema.Column{"id": {Type: "int"}, "kind": {Type: "text"}},
			PrimaryKey:  []string{"id", "kind"},
			PartitionBy: &schema.PartitionBy{Type: "list", Columns: []string{"kind"}},
			Data:        &schema.TableData{Rows: []map[string]any{{"id": 1, "kind": "a"}}}},
		"app.event_a": {Name: "event_a", PartitionOf: "app.event", Partition: &schema.PartitionSpec{In: []string{"'a'"}, Modulus: -1, Remainder: -1},
			Columns:  map[string]*schema.Column{},
			Triggers: []*schema.Trigger{{Name: "event_audit", Timing: "after", Events: []string{"insert"}, Level: "row", Procedure: "app.audit_ins()"}}},
	}}
}

func TestDataNewTableRowsBeforeTriggers(t *testing.T) {
	sql := Render(Plan(emptyLive(), triggerDataDB(), false))
	at := func(s string) int {
		t.Helper()
		i := strings.Index(sql, s)
		if i < 0 {
			t.Fatalf("missing %q in\n%s", s, sql)
		}
		return i
	}
	rows := at(`insert into "app"."account" ("id", "seats") values (2, 5);`)
	for _, after := range []string{
		`create constraint trigger "account_seats"`,
		`create trigger "account_audit"`,
		`alter table "app"."account" enable always trigger "account_audit";`,
		`create or replace rule "account_log"`,
		`alter table "app"."account" enable row level security;`,
	} {
		if at(after) < rows {
			t.Errorf("%q runs before the rows are inserted:\n%s", after, sql)
		}
	}
	if at(`create trigger "event_audit"`) < at(`insert into "app"."event" ("id", "kind") values (1, 'a');`) {
		t.Errorf("a trigger on a new partition runs before the rows of its parent:\n%s", sql)
	}
	if at(`alter table "app"."account" add primary key ("id");`) > rows {
		t.Errorf("the primary key comes before the rows:\n%s", sql)
	}
	if strings.Contains(sql, "set constraints") {
		t.Errorf("rows of new tables leave no deferred trigger events:\n%s", sql)
	}
}

func TestDataNewTableWithoutRowsKeepsTriggerOrder(t *testing.T) {
	d := triggerDataDB()
	d.Tables["app.account"].Data = nil
	d.Tables["app.event"].Data = nil
	p := Plan(emptyLive(), d, false)
	for _, want := range []string{`create constraint trigger "account_seats"`, `create trigger "event_audit"`, `create or replace rule "account_log"`} {
		found := false
		for _, s := range p.Creates {
			found = found || strings.Contains(s, want)
		}
		if !found {
			t.Errorf("%q stays with the creates of a table without rows:\n%s", want, Render(p))
		}
	}
}

func TestDataExistingTableSetsConstraintsImmediate(t *testing.T) {
	d := triggerDataDB()
	live := Simulate(emptyLive(), d)
	live.TableData["app.account"] = &LiveTableData{Missing: []int{1}, Changed: map[int][]string{}}
	sql := Render(PlanWith(live, d, Options{}))
	rows := strings.Index(sql, `insert into "app"."account" ("id", "seats") values (2, 5);`)
	set := strings.Index(sql, "set constraints all immediate;")
	if rows < 0 || set < rows {
		t.Errorf("expected the deferred trigger events fired after the rows:\n%s", sql)
	}

	live.TableData["app.account"] = &LiveTableData{Changed: map[int][]string{}, Extra: []map[string]string{{"id": "9"}}}
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if len(p.Drops) < 2 || !strings.HasPrefix(p.Drops[0], `delete from only "app"."account"`) || p.Drops[1] != "set constraints all immediate;" {
		t.Errorf("expected the deferred trigger events fired after the deletes, got %v", p.Drops)
	}
}

func TestDataExistingTableWithoutDeferredConstraints(t *testing.T) {
	d := triggerDataDB()
	d.Tables["app.account"].Triggers = d.Tables["app.account"].Triggers[1:]
	live := Simulate(emptyLive(), d)
	live.TableData["app.account"] = &LiveTableData{Missing: []int{1}, Changed: map[int][]string{}}
	if sql := Render(PlanWith(live, d, Options{})); !strings.Contains(sql, "insert into") || strings.Contains(sql, "set constraints") {
		t.Errorf("expected the rows without SET CONSTRAINTS:\n%s", sql)
	}

	live.Tables["app.audit"].ConstraintDefs = map[string]string{"audit_fk": "FOREIGN KEY (tbl) REFERENCES app.t(name) DEFERRABLE INITIALLY DEFERRED"}
	if sql := Render(PlanWith(live, d, Options{})); !strings.Contains(sql, "set constraints all immediate;") {
		t.Errorf("an initially deferred foreign key of another table can pend trigger events too:\n%s", sql)
	}
}
