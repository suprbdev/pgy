package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func foreignDB() *schema.Database {
	return &schema.Database{
		Servers: map[string]*schema.Server{"wh": {Name: "wh", Wrapper: "postgres_fdw", Options: map[string]string{"host": "h", "dbname": "d"}, Comment: "warehouse"}},
		Tables: map[string]*schema.Table{"app.orders": {
			Name:    "app.orders",
			Foreign: &schema.ForeignTable{Server: "wh", Options: map[string]string{"table_name": "orders"}},
			Comment: "remote",
			Columns: map[string]*schema.Column{
				"id":    {Type: "bigint"},
				"total": {Type: "numeric", Nullable: true, FDWOptions: map[string]string{"column_name": "amount"}},
			},
			ColumnOrder: []string{"id", "total"},
		}},
	}
}

func TestForeignTableCreate(t *testing.T) {
	p := Plan(emptyLive(), foreignDB(), false)
	sql := Render(p)
	for _, want := range []string{
		`create server "wh" foreign data wrapper "postgres_fdw" options ("dbname" 'd', "host" 'h');`,
		`create foreign table if not exists "app"."orders" ("id" bigint not null, "total" numeric) server "wh" options ("table_name" 'orders');`,
		`alter foreign table "app"."orders" alter column "total" options (add "column_name" 'amount');`,
		`comment on server "wh" is 'warehouse';`,
		`comment on foreign table "app"."orders" is 'remote';`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
	if strings.Index(sql, "create server") > strings.Index(sql, "create foreign table") {
		t.Errorf("the server must be created before its foreign tables:\n%s", sql)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "no user mapping") {
		t.Errorf("expected a user mapping warning for postgres_fdw, got %+v", p.Warnings)
	}
}

func foreignLive() *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	if live.Servers == nil {
		live.Servers = map[string]*LiveServer{}
	}
	if live.RelComments == nil {
		live.RelComments = map[string]string{}
	}
	live.Servers["wh"] = &LiveServer{Wrapper: "postgres_fdw", Options: map[string]string{"host": "h", "dbname": "d"}, Comment: "warehouse", UserMapping: true}
	lt := newLiveTable()
	lt.Foreign = &LiveForeignTable{Server: "wh", Options: map[string]string{"table_name": "orders"}}
	lt.Columns["id"] = &LiveColumn{Type: "bigint"}
	lt.Columns["total"] = &LiveColumn{Type: "numeric", Nullable: true, FDWOptions: map[string]string{"column_name": "amount"}}
	live.Tables["app.orders"] = lt
	live.RelComments["app.orders"] = "remote"
	return live
}

func TestForeignTableSkippedIfMatching(t *testing.T) {
	p := PlanWith(foreignLive(), foreignDB(), Options{Unsafe: true, Prune: true})
	if sql := Render(p); sql != "" {
		t.Errorf("matching server and foreign table must not diff, got:\n%s", sql)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %+v", p.Warnings)
	}
}

func TestForeignTableOptionsReconciled(t *testing.T) {
	d := foreignDB()
	d.Servers["wh"].Options = map[string]string{"host": "h2", "port": "5433"}
	d.Servers["wh"].Version = "2"
	d.Tables["app.orders"].Foreign = &schema.ForeignTable{Server: "other", Options: map[string]string{"table_name": "orders", "fetch_size": "500"}}
	d.Tables["app.orders"].Columns["total"].FDWOptions = map[string]string{}
	p := Plan(foreignLive(), d, false)
	sql := Render(p)
	for _, want := range []string{
		`alter server "wh" version '2';`,
		`alter server "wh" options (set "host" 'h2', add "port" '5433', drop "dbname");`,
		`alter foreign table "app"."orders" options (add "fetch_size" '500');`,
		`alter foreign table "app"."orders" alter column "total" options (drop "column_name");`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "server changed from wh to other") {
		t.Errorf("expected a server change warning, got %+v", p.Warnings)
	}
}

func TestForeignTablesAndServersPruned(t *testing.T) {
	d := &schema.Database{Servers: map[string]*schema.Server{}, Tables: map[string]*schema.Table{"app.keep": {Name: "app.keep", Columns: map[string]*schema.Column{"id": {Type: "int"}}}}}
	live := foreignLive()
	keep := newLiveTable()
	keep.Columns["id"] = &LiveColumn{Type: "integer"}
	live.Tables["app.keep"] = keep
	sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true}))
	ft, srv := strings.Index(sql, `drop foreign table "app"."orders";`), strings.Index(sql, `drop server "wh";`)
	if ft < 0 || srv < ft {
		t.Errorf("expected the foreign table, then its server, dropped:\n%s", sql)
	}
	if strings.Contains(sql, `drop table "app"."orders"`) {
		t.Errorf("foreign tables need DROP FOREIGN TABLE:\n%s", sql)
	}
	d.Servers = nil
	if sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true})); strings.Contains(sql, "drop server") {
		t.Errorf("without a servers block, servers are unmanaged:\n%s", sql)
	}
}

func TestForeignTableChangesRaiseNoLockWarnings(t *testing.T) {
	d := foreignDB()
	d.Tables["app.orders"].Columns["total"].Type = "numeric(14,2)"
	d.Tables["app.orders"].Columns["id"].Nullable = false
	d.Tables["app.orders"].Columns["note"] = &schema.Column{Type: "text", Default: "now()::text"}
	d.Tables["app.orders"].Constraints = []*schema.Constraint{{Name: "orders_total", Type: "check", Expression: "total >= 0"}}
	live := foreignLive()
	live.Tables["app.orders"].Columns["total"].Type = "integer"
	p := Plan(live, d, true)
	if !strings.Contains(Render(p), `alter column "total" type numeric(14,2)`) {
		t.Fatalf("expected a type change:\n%s", Render(p))
	}
	for _, w := range p.Warnings {
		t.Errorf("a foreign table holds no local rows, unexpected warning: %s", w.Message)
	}
}

func TestConcurrentIndexWithForeignPartitionBuiltPlain(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	parent := newLiveTable()
	parent.Partitioned = true
	parent.Columns["id"] = &LiveColumn{Type: "integer"}
	live.Tables["app.m"] = parent
	remote := newLiveTable()
	remote.IsPartition = true
	remote.Foreign = &LiveForeignTable{Server: "s"}
	live.Tables["app.m_us"] = remote
	live.Partitions = map[string][]string{"app.m": {"app.m_us"}}
	d := &schema.Database{Tables: map[string]*schema.Table{"app.m": {
		Name:        "app.m",
		Columns:     map[string]*schema.Column{"id": {Type: "int"}},
		PartitionBy: &schema.PartitionBy{Type: "list", Columns: []string{"id"}},
		Indexes:     []*schema.Index{{Name: "m_id", Columns: []string{"id"}, Concurrently: true}},
	}}}
	p := Plan(live, d, false)
	if len(p.Concurrent) != 0 || !findCreate(p, `create index if not exists "m_id" on "app"."m"("id");`) {
		t.Errorf("expected a plain index build, got creates %v, concurrent %v", p.Creates, p.Concurrent)
	}
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0].Message, "foreign partitions") {
		t.Errorf("expected a foreign partition warning, got %+v", p.Warnings)
	}
}
