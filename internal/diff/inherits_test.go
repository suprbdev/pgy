package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func inheritsDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{
		"app.base":  {Name: "base", Columns: map[string]*schema.Column{"id": {Type: "int"}}},
		"app.child": {Name: "child", Inherits: []string{"app.base"}, Columns: map[string]*schema.Column{"extra": {Type: "int", Nullable: true}}},
	}}
}

func TestInheritsCreate(t *testing.T) {
	sql := Render(Plan(emptyLive(), inheritsDB(), false))
	want := `create table if not exists "app"."child" ("extra" int) inherits ("app"."base");`
	if !strings.Contains(sql, want) || strings.Index(sql, `"app"."base" (`) > strings.Index(sql, want) {
		t.Errorf("missing or misordered %q in\n%s", want, sql)
	}
}

func TestInheritsAlter(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	base, child := newLiveTable(), newLiveTable()
	base.Columns["id"] = &LiveColumn{Type: "integer"}
	child.Columns["id"] = &LiveColumn{Type: "integer"}
	child.Columns["extra"] = &LiveColumn{Type: "integer", Nullable: true}
	child.Inherits = []string{"app.old"}
	live.Tables["app.base"], live.Tables["app.child"], live.Tables["app.old"] = base, child, newLiveTable()
	d := inheritsDB()
	d.Tables["app.old"] = &schema.Table{Name: "old", Columns: map[string]*schema.Column{}}
	p := Plan(live, d, false)
	if !findAlter(p, `alter table "app"."child" inherit "app"."base";`) || strings.Contains(Render(p), "no inherit") {
		t.Errorf("expected INHERIT only without --unsafe:\n%s", Render(p))
	}
	p = Plan(live, d, true)
	if !findAlter(p, `alter table "app"."child" no inherit "app"."old";`) || strings.Contains(Render(p), `drop column "id"`) {
		t.Errorf("expected NO INHERIT and no drop of the column base declares:\n%s", Render(p))
	}
	child.Columns["id"].Inherited = true
	child.Inherits = []string{"app.base"}
	d.Tables["app.child"].Inherits = []string{"app.base"}
	if sql := Render(Plan(live, d, true)); sql != "" {
		t.Errorf("inherited columns are the parent's business, got:\n%s", sql)
	}
	d.Tables["app.child"].Inherits = nil
	if sql := Render(Plan(live, d, true)); sql != "" {
		t.Errorf("without an inherits key the parents are unmanaged, got:\n%s", sql)
	}
}
