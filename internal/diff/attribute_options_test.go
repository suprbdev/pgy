package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func floatPtr(v float64) *float64 { return &v }

func nDistinctDB(cols map[string]*schema.Column) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: cols}}}
}

func TestColumnNDistinctCreate(t *testing.T) {
	d := nDistinctDB(map[string]*schema.Column{
		"a": {Type: "int", NDistinct: floatPtr(-0.5), NDistinctInherited: floatPtr(100)},
		"b": {Type: "int", NDistinct: floatPtr(0)},
	})
	sql := Render(Plan(emptyLive(), d, false))
	if want := `alter table "app"."t" alter column "a" set (n_distinct = -0.5, n_distinct_inherited = 100);`; !strings.Contains(sql, want) {
		t.Errorf("missing %q in\n%s", want, sql)
	}
	if strings.Contains(sql, `column "b" set (`) || strings.Contains(sql, `column "b" reset`) {
		t.Errorf("nDistinct 0 needs no statement on a new table:\n%s", sql)
	}
}

func TestColumnNDistinctReconciled(t *testing.T) {
	live := emptyLive()
	lt := newLiveTable()
	lt.Columns["a"] = &LiveColumn{Type: "integer", Options: map[string]string{"n_distinct": "-0.5"}}
	lt.Columns["b"] = &LiveColumn{Type: "integer", Options: map[string]string{"n_distinct": "10", "n_distinct_inherited": "5"}}
	lt.Columns["c"] = &LiveColumn{Type: "integer", Options: map[string]string{"n_distinct": "10"}}
	live.Tables["app.t"] = lt
	live.Schemas["app"] = true
	d := nDistinctDB(map[string]*schema.Column{
		"a": {Type: "int", NDistinct: floatPtr(-0.5)},
		"b": {Type: "int", NDistinct: floatPtr(20), NDistinctInherited: floatPtr(0)},
		"c": {Type: "int"},
	})
	p := Plan(live, d, false)
	for _, want := range []string{
		`alter table "app"."t" alter column "b" set (n_distinct = 20);`,
		`alter table "app"."t" alter column "b" reset (n_distinct_inherited);`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in\n%s", want, Render(p))
		}
	}
	if sql := Render(p); strings.Contains(sql, `column "a"`) || strings.Contains(sql, `column "c"`) {
		t.Errorf("matching or unmanaged columns must not diff:\n%s", sql)
	}
}

func TestAddedColumnGetsStatisticsAndOptions(t *testing.T) {
	live := emptyLive()
	lt := newLiveTable()
	lt.Columns["a"] = &LiveColumn{Type: "integer"}
	live.Tables["app.t"] = lt
	live.Schemas["app"] = true
	d := nDistinctDB(map[string]*schema.Column{
		"a": {Type: "int"},
		"b": {Type: "text", StatisticsTarget: intPtr(300), NDistinct: floatPtr(-1), Storage: "external"},
	})
	p := Plan(live, d, false)
	for _, want := range []string{
		`alter table "app"."t" alter column "b" set storage external;`,
		`alter table "app"."t" alter column "b" set statistics 300;`,
		`alter table "app"."t" alter column "b" set (n_distinct = -1);`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in\n%s", want, Render(p))
		}
	}
}

func TestDroppedIdentityColumnBecomesNullable(t *testing.T) {
	live := emptyLive()
	lt := newLiveTable()
	lt.Columns["b"] = &LiveColumn{Type: "bigint", Identity: "always"}
	live.Tables["app.t"] = lt
	live.Schemas["app"] = true
	d := nDistinctDB(map[string]*schema.Column{"b": {Type: "bigint", Nullable: true}})
	got := Plan(live, d, true).Alters
	want := []string{
		`alter table "app"."t" alter column "b" drop identity if exists;`,
		`alter table "app"."t" alter column "b" drop not null;`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("want %v, got %v", want, got)
	}
	if got := Plan(live, d, false).Alters; len(got) != 0 {
		t.Errorf("without --unsafe the identity and its NOT NULL stay, got %v", got)
	}
}
