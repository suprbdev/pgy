package diff

import (
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestSQLValue(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{
		{nil, "null"}, {true, "true"}, {42, "42"}, {9.5, "9.5"}, {"it's", "'it''s'"},
		{time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "'2026-01-02'"},
		{[]any{"a", "b c", nil, 1}, `'{"a","b c",NULL,1}'`},
		{[]any{[]any{1, 2}, []any{3, 4}}, `'{{1,2},{3,4}}'`},
		{map[string]any{"x": 1}, `'{"x":1}'`},
	} {
		if got := sqlValue(c.in); got != c.want {
			t.Errorf("sqlValue(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func dataDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{
		"app.feature": {Name: "feature", ColumnOrder: []string{"id", "plan", "label"},
			Columns:     map[string]*schema.Column{"id": {Type: "int", Identity: "always"}, "plan": {Type: "text"}, "label": {Type: "text"}},
			PrimaryKey:  []string{"id"},
			ForeignKeys: []*schema.ForeignKey{{Name: "f_plan", Columns: []string{"plan"}, RefTable: "app.plan", RefColumns: []string{"code"}}},
			Data:        &schema.TableData{Rows: []map[string]any{{"id": 1, "plan": "free", "label": "basics"}}}},
		"app.plan": {Name: "plan", ColumnOrder: []string{"code", "name"},
			Columns:    map[string]*schema.Column{"code": {Type: "text"}, "name": {Type: "text"}},
			PrimaryKey: []string{"code"},
			Data:       &schema.TableData{Rows: []map[string]any{{"code": "free", "name": "Free"}, {"code": "pro", "name": "Pro"}}}},
	}}
}

func TestDataNewTablesInsertInForeignKeyOrder(t *testing.T) {
	p := Plan(emptyLive(), dataDB(), false)
	sql := Render(p)
	plan := strings.Index(sql, `insert into "app"."plan" ("code", "name") values ('free', 'Free');`)
	feature := strings.Index(sql, `insert into "app"."feature" ("id", "plan", "label") overriding system value values (1, 'free', 'basics');`)
	fk := strings.Index(sql, `add constraint "f_plan"`)
	if plan < 0 || feature < 0 || plan > feature || fk < feature {
		t.Errorf("expected plan rows, then feature rows, then the foreign key:\n%s", sql)
	}
}

func TestDataFromSnapshot(t *testing.T) {
	live := Simulate(emptyLive(), dataDB())
	if sql := Render(PlanWith(live, dataDB(), Options{Unsafe: true, Prune: true})); sql != "" {
		t.Fatalf("simulated data must be in sync, got:\n%s", sql)
	}
	live.TableData["app.plan"] = &LiveTableData{Missing: []int{1}, Changed: map[int][]string{0: {"name"}}, Extra: []map[string]string{{"code": "old"}}}
	p := PlanWith(live, dataDB(), Options{Unsafe: true})
	for _, want := range []string{
		`insert into "app"."plan" ("code", "name") values ('pro', 'Pro');`,
		`update "app"."plan" set "name" = 'Free' where "code" = 'free';`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in\n%s", want, Render(p))
		}
	}
	if strings.Contains(Render(p), "delete") {
		t.Error("rows are only deleted with --prune")
	}
	if p := PlanWith(live, dataDB(), Options{Unsafe: true, Prune: true}); len(p.Drops) == 0 || p.Drops[0] != `delete from "app"."plan" where "code" = 'old';` {
		t.Errorf("expected a delete, got %v", p.Drops)
	}
	delete(live.TableData, "app.plan")
	if !findAlter(PlanWith(live, dataDB(), Options{}), `insert into "app"."plan" ("code", "name") values ('free', 'Free') on conflict ("code") do update set "name" = excluded."name";`) {
		t.Error("without introspected data an existing table gets upserts")
	}
}

func TestDataSQLRoutesMapsThroughPopulateRecord(t *testing.T) {
	if got := dataSQL("app.t", "p", map[string]any{"a": 1}); got != `(jsonb_populate_record(null::"app"."t", '{"p":{"a":1}}'::jsonb))."p"` {
		t.Errorf("got %s", got)
	}
	if got := dataSQL("app.t", "n", 5); got != "5" {
		t.Errorf("scalars stay literals, got %s", got)
	}
}
