package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func typedTableDB() *schema.Database {
	return &schema.Database{
		Types: map[string]*schema.TypeDef{"app.addr": {Name: "addr", Kind: "composite", Attributes: map[string]string{"street": "text", "city": "text"}, AttributeOrder: []string{"street", "city"}}},
		Tables: map[string]*schema.Table{"app.addresses": {Name: "addresses", Of: "app.addr", PrimaryKey: []string{"street"},
			Columns: map[string]*schema.Column{"city": {Default: "'Paris'", Nullable: false, NullableSet: true, Comment: "city name"}}}},
	}
}

func TestTypedTableCreate(t *testing.T) {
	sql := Render(Plan(emptyLive(), typedTableDB(), false))
	for _, want := range []string{
		`create table if not exists "app"."addresses" of "app"."addr";`,
		`alter table "app"."addresses" alter column "city" set default 'Paris';`,
		`alter table "app"."addresses" alter column "city" set not null;`,
		`comment on column "app"."addresses"."city" is 'city name';`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if sql := Render(Plan(Simulate(emptyLive(), typedTableDB()), typedTableDB(), false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}

func TestTypedTableLinkAndColumns(t *testing.T) {
	live := liveWithTablePK("app.addresses", map[string]*LiveColumn{"street": {Type: "text"}, "city": {Type: "text", Default: "'Paris'::text", Comment: "city name"}})
	live.Schemas["app"] = true
	live.Types = map[string]bool{"app.addr": true}
	d := typedTableDB()
	sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true}))
	if !strings.Contains(sql, `alter table "app"."addresses" of "app"."addr";`) || strings.Contains(sql, "drop column") {
		t.Errorf("want the table linked to its type and no column drops, got:\n%s", sql)
	}
	live.Tables["app.addresses"].OfType = "app.addr"
	if sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("a typed table matching its YAML must not diff:\n%s", sql)
	}
	d.Tables["app.addresses"].Of = ""
	d.Tables["app.addresses"].Columns = map[string]*schema.Column{"street": {Type: "text"}, "city": {Type: "text", Default: "'Paris'", Comment: "city name"}}
	p := Plan(live, d, false)
	if Render(p) != "" || len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0].Message, "NOT OF needs --unsafe") {
		t.Errorf("without --unsafe a typed table is only reported: %v\n%s", p.Warnings, Render(p))
	}
	if sql := Render(Plan(live, d, true)); !strings.Contains(sql, `alter table "app"."addresses" not of;`) {
		t.Errorf("want NOT OF under --unsafe, got:\n%s", sql)
	}
}

func TestCompositeAlterCascadesToTypedTables(t *testing.T) {
	live := liveWithTable("app.addresses", map[string]*LiveColumn{"street": {Type: "text"}})
	live.Tables["app.addresses"].OfType = "app.addr"
	live.Types = map[string]bool{"app.addr": true}
	live.CompositeAttributes = map[string]map[string]string{"app.addr": {"street": "text"}}
	d := typedTableDB()
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, `alter type "app"."addr" add attribute "city" text cascade;`) {
		t.Errorf("want CASCADE for a type used by a typed table, got:\n%s", sql)
	}
}
