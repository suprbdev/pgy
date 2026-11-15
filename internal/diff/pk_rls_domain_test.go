package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func pkTable(name string) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name: "app.t", Columns: map[string]*schema.Column{"id": {Type: "int"}}, PrimaryKey: []string{"id"}, PrimaryKeyName: name,
	}}}
}

func TestNamedPrimaryKey(t *testing.T) {
	if sql := Render(Plan(emptyLive(), pkTable("t_pk"), false)); !strings.Contains(sql, `alter table "app"."t" add constraint "t_pk" primary key ("id");`) {
		t.Errorf("expected a named primary key:\n%s", sql)
	}
	live := emptyLive()
	live.Schemas["app"] = true
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	lt.HasPK = true
	lt.Constraints["t_pkey"] = true
	lt.ConstraintDefs["t_pkey"] = "PRIMARY KEY (id)"
	live.Tables["app.t"] = lt
	if sql := Render(Plan(live, pkTable("t_pk"), false)); !strings.Contains(sql, `alter table "app"."t" rename constraint "t_pkey" to "t_pk";`) {
		t.Errorf("expected the live primary key renamed:\n%s", sql)
	}
	if sql := Render(Plan(live, pkTable(""), false)); sql != "" {
		t.Errorf("without a name the primary key's name is unmanaged:\n%s", sql)
	}
	if DefaultPrimaryKeyName("app.t") != "t_pkey" {
		t.Errorf("DefaultPrimaryKeyName = %q", DefaultPrimaryKeyName("app.t"))
	}
}

func TestForceRowLevelSecurity(t *testing.T) {
	on, off := true, false
	live := emptyLive()
	live.Schemas["app"] = true
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	lt.RLSEnabled = true
	live.Tables["app.t"] = lt
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "app.t", Columns: map[string]*schema.Column{"id": {Type: "int"}}, ForceRowLevelSecurity: &on}}}
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, `alter table "app"."t" force row level security;`) {
		t.Errorf("expected FORCE ROW LEVEL SECURITY:\n%s", sql)
	}
	lt.RLSForced = true
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("already forced:\n%s", sql)
	}
	d.Tables["app.t"].ForceRowLevelSecurity = &off
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("NO FORCE weakens access and needs --unsafe:\n%s", sql)
	}
	if sql := Render(Plan(live, d, true)); !strings.Contains(sql, `alter table "app"."t" no force row level security;`) {
		t.Errorf("expected NO FORCE under --unsafe:\n%s", sql)
	}
}

func TestDomainCheckComments(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{}, Domains: map[string]*schema.Domain{"app.pct": {
		Name: "pct", Type: "int", Checks: map[string]string{"pct_min": "VALUE >= 0"}, CheckComments: map[string]string{"pct_min": "not negative"},
	}}}
	if sql := Render(Plan(emptyLive(), d, false)); !strings.Contains(sql, `comment on constraint "pct_min" on domain "app"."pct" is 'not negative';`) {
		t.Errorf("expected the domain constraint comment:\n%s", sql)
	}
}
