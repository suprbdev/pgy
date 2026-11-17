package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func warningsContain(p *PlanDiff, substr string) bool {
	for _, w := range p.Warnings {
		if strings.Contains(w.Message, substr) {
			return true
		}
	}
	return false
}

func TestForeignTableInheritsCreate(t *testing.T) {
	d := foreignDB()
	d.Tables["app.parent"] = &schema.Table{Name: "app.parent", Columns: map[string]*schema.Column{"id": {Type: "bigint"}}}
	ft := d.Tables["app.orders"]
	ft.Inherits = []string{"app.parent"}
	ft.Columns["id"] = &schema.Column{Storage: "plain", FDWOptions: map[string]string{"column_name": "ident"}}
	sql := Render(Plan(emptyLive(), d, false))
	for _, want := range []string{
		`create foreign table if not exists "app"."orders" ("total" numeric) inherits ("app"."parent") server "wh" options ("table_name" 'orders');`,
		`alter foreign table "app"."orders" alter column "id" options (add "column_name" 'ident');`,
		`alter foreign table "app"."orders" alter column "total" options (add "column_name" 'amount');`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
	if n := strings.Count(sql, `alter column "id" set storage plain;`); n != 1 {
		t.Errorf("the inherited column's storage must be set once, got %d times in\n%s", n, sql)
	}
	if strings.Index(sql, `create table if not exists "app"."parent"`) > strings.Index(sql, "create foreign table") {
		t.Errorf("the parent must be created before the foreign child:\n%s", sql)
	}
}

func TestForeignTableInheritAddedToExisting(t *testing.T) {
	d := foreignDB()
	d.Tables["app.parent"] = &schema.Table{Name: "app.parent", Columns: map[string]*schema.Column{"id": {Type: "bigint"}}}
	d.Tables["app.orders"].Inherits = []string{"app.parent"}
	live := foreignLive()
	parent := newLiveTable()
	parent.Columns["id"] = &LiveColumn{Type: "bigint"}
	live.Tables["app.parent"] = parent
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, `alter table "app"."orders" inherit "app"."parent";`) {
		t.Errorf("expected INHERIT for the existing foreign table:\n%s", sql)
	}
	live.Tables["app.orders"].Inherits = []string{"app.parent"}
	live.Tables["app.orders"].Columns["id"].Inherited = true
	if sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("an inheriting foreign table that matches must not diff, got:\n%s", sql)
	}
}

func TestForeignPartitionColumnOptionsOnCreate(t *testing.T) {
	d := &schema.Database{
		Servers: map[string]*schema.Server{"wh": {Name: "wh", Wrapper: "file_fdw"}},
		Tables: map[string]*schema.Table{
			"app.events": {Name: "app.events", Columns: map[string]*schema.Column{"id": {Type: "bigint"}, "region": {Type: "text"}}, PartitionBy: &schema.PartitionBy{Type: "list", Columns: []string{"region"}}},
			"app.events_us": {
				Name:        "app.events_us",
				PartitionOf: "app.events",
				Partition:   &schema.PartitionSpec{In: []string{"us"}, Modulus: -1, Remainder: -1},
				Foreign:     &schema.ForeignTable{Server: "wh"},
				Columns:     map[string]*schema.Column{"id": {FDWOptions: map[string]string{"column_name": "event_id"}}, "region": {FDWOptions: map[string]string{}}},
			},
		},
	}
	sql := Render(Plan(emptyLive(), d, false))
	if !strings.Contains(sql, `alter foreign table "app"."events_us" alter column "id" options (add "column_name" 'event_id');`) {
		t.Errorf("a new foreign partition's column options belong in the plan that creates it:\n%s", sql)
	}
	if strings.Contains(sql, `alter column "region" options`) {
		t.Errorf("an empty options block adds nothing:\n%s", sql)
	}
}

func TestServerWrapperChangeEmitsNoAlters(t *testing.T) {
	d := foreignDB()
	d.Servers["wh"].Wrapper = "file_fdw"
	d.Servers["wh"].Options = map[string]string{"dbname": "z"}
	d.Servers["wh"].Version = "3"
	d.Servers["wh"].Owner = "admin"
	d.Tables = map[string]*schema.Table{}
	p := Plan(foreignLive(), d, true)
	if !warningsContain(p, "foreign-data wrapper or type changed") {
		t.Errorf("expected a wrapper change warning, got %+v", p.Warnings)
	}
	if sql := Render(p); strings.Contains(sql, "alter server") {
		t.Errorf("a server that must be recreated by hand gets no ALTER SERVER:\n%s", sql)
	}
}

func TestTableKindMismatchIsLeftAlone(t *testing.T) {
	d := foreignDB()
	d.Tables["app.orders"].Foreign = nil
	d.Tables["app.orders"].PrimaryKey = []string{"id"}
	d.Tables["app.orders"].Columns["note"] = &schema.Column{Type: "text", Comment: "a note"}
	p := PlanWith(foreignLive(), d, Options{Unsafe: true, Prune: true})
	if !warningsContain(p, "app.orders is declared as a table but is a foreign table in the database") {
		t.Errorf("expected a kind warning, got %+v", p.Warnings)
	}
	if sql := Render(p); sql != "" {
		t.Errorf("a table that is a foreign table live is left alone, got:\n%s", sql)
	}

	d = foreignDB()
	d.Tables["app.orders"].Columns["note"] = &schema.Column{Type: "text"}
	live := foreignLive()
	live.Tables["app.orders"].Foreign = nil
	p = PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if !warningsContain(p, "app.orders is declared as a foreign table but is a table in the database") {
		t.Errorf("expected a kind warning, got %+v", p.Warnings)
	}
	if sql := Render(p); sql != "" {
		t.Errorf("a foreign table that is a table live is left alone, got:\n%s", sql)
	}
}

func TestServerRename(t *testing.T) {
	d := foreignDB()
	d.Servers["warehouse"] = d.Servers["wh"]
	delete(d.Servers, "wh")
	d.Servers["warehouse"].Name, d.Servers["warehouse"].RenamedFrom = "warehouse", "wh"
	d.Tables["app.orders"].Foreign.Server = "warehouse"
	p := PlanWith(foreignLive(), d, Options{Unsafe: true, Prune: true})
	if sql := Render(p); sql != "alter server \"wh\" rename to \"warehouse\";\n" {
		t.Errorf("expected only the rename, got:\n%s", sql)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %+v", p.Warnings)
	}
	live := foreignLive()
	live.Servers["warehouse"] = live.Servers["wh"]
	delete(live.Servers, "wh")
	live.Tables["app.orders"].Foreign.Server = "warehouse"
	if sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("an applied rename is a no-op, got:\n%s", sql)
	}
}

func TestServerRenameWithoutRenamedFromKeepsTheOldServer(t *testing.T) {
	d := foreignDB()
	d.Servers["warehouse"] = d.Servers["wh"]
	delete(d.Servers, "wh")
	d.Tables["app.orders"].Foreign.Server = "warehouse"
	p := PlanWith(foreignLive(), d, Options{Unsafe: true, Prune: true})
	sql := Render(p)
	if !strings.Contains(sql, `create server "warehouse"`) || strings.Contains(sql, "drop server") {
		t.Errorf("the server a foreign table stays on must not be dropped:\n%s", sql)
	}
	if !warningsContain(p, "server wh is not in the servers block but still has foreign tables this plan keeps (app.orders)") {
		t.Errorf("expected a kept-server warning, got %+v", p.Warnings)
	}
}

func TestPruneServerDropsItsUserMappings(t *testing.T) {
	d := &schema.Database{Servers: map[string]*schema.Server{}, Tables: map[string]*schema.Table{"app.keep": {Name: "app.keep", Columns: map[string]*schema.Column{"id": {Type: "int"}}}}}
	live := foreignLive()
	live.Servers["wh"].UserMappings = []string{"App User", "public"}
	keep := newLiveTable()
	keep.Columns["id"] = &LiveColumn{Type: "integer"}
	live.Tables["app.keep"] = keep
	sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true}))
	order := []string{
		`drop foreign table "app"."orders";`,
		`drop user mapping for "App User" server "wh";`,
		`drop user mapping for public server "wh";`,
		`drop server "wh";`,
	}
	at := -1
	for _, stmt := range order {
		i := strings.Index(sql, stmt)
		if i <= at {
			t.Fatalf("expected %q after the previous statement in:\n%s", stmt, sql)
		}
		at = i
	}
}

func TestPruneKeepsServerUsedOutsideManagedSchemas(t *testing.T) {
	d := &schema.Database{Servers: map[string]*schema.Server{}, Tables: map[string]*schema.Table{"app.keep": {Name: "app.keep", Columns: map[string]*schema.Column{"id": {Type: "int"}}}}}
	live := foreignLive()
	keep := newLiveTable()
	keep.Columns["id"] = &LiveColumn{Type: "integer"}
	live.Tables["app.keep"] = keep
	other := newLiveTable()
	other.Foreign = &LiveForeignTable{Server: "wh"}
	live.Tables["other.ft"] = other
	parent := newLiveTable()
	parent.Partitioned = true
	live.Tables["other.events"] = parent
	part := newLiveTable()
	part.IsPartition = true
	part.Foreign = &LiveForeignTable{Server: "wh"}
	live.Tables["app.events_us"] = part
	live.Partitions = map[string][]string{"other.events": {"app.events_us"}}
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true})
	sql := Render(p)
	if !strings.Contains(sql, `drop foreign table "app"."orders";`) || strings.Contains(sql, "drop server") {
		t.Errorf("a server with foreign tables the plan keeps must stay:\n%s", sql)
	}
	if !warningsContain(p, "(app.events_us, other.ft)") {
		t.Errorf("expected the kept foreign tables to be named, got %+v", p.Warnings)
	}
}

func TestOfflinePlanHasNoUserMappingWarning(t *testing.T) {
	live := emptyLive()
	live.Offline = true
	p := Plan(live, foreignDB(), false)
	if warningsContain(p, "no user mapping for the connected role") {
		t.Errorf("nothing is connected under --from-empty, got %+v", p.Warnings)
	}
}

func TestMaterializedViewOnUnmappedServerWarns(t *testing.T) {
	d := foreignDB()
	d.Views = map[string]*schema.View{
		"app.cache":   {Schema: "app", Name: "cache", Materialized: true, Query: `select id from "app"."orders"`},
		"app.empty":   {Schema: "app", Name: "empty", Materialized: true, Query: "select id from app.orders", WithData: new(bool)},
		"app.other":   {Schema: "app", Name: "other", Materialized: true, Query: "select 1 from app.orders_archive"},
		"app.regular": {Schema: "app", Name: "regular", Query: "select id from app.orders"},
	}
	offline := emptyLive()
	offline.Offline = true
	for name, live := range map[string]*Live{"connected": emptyLive(), "offline": offline} {
		p := Plan(live, d, false)
		n := 0
		for _, w := range p.Warnings {
			if strings.Contains(w.Message, "creating it with data fails") {
				n++
				if !strings.Contains(w.Message, "materialized view app.cache reads foreign table app.orders, whose server wh this plan creates without a user mapping") {
					t.Errorf("%s: unexpected warning: %s", name, w.Message)
				}
			}
		}
		if n != 1 {
			t.Errorf("%s: expected one warning, for app.cache, got %+v", name, p.Warnings)
		}
	}
	p := Plan(foreignLive(), d, false)
	if warningsContain(p, "creating it with data fails") {
		t.Errorf("a server with a user mapping needs no warning, got %+v", p.Warnings)
	}
	live := foreignLive()
	live.Servers["wh"].UserMapping = false
	if p := Plan(live, d, false); !warningsContain(p, "whose server wh has no user mapping for the connected role or PUBLIC") {
		t.Errorf("expected a warning for the unmapped live server, got %+v", p.Warnings)
	}
}
