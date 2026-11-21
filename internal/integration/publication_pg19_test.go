package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationPublicationAllTablesExceptAndSequences(t *testing.T) {
	requireServerVersion(t, 190000)
	ctx := context.Background()
	pool := freshDatabase(t, connect(t), "pgytest_pub19")
	if _, err := pool.Exec(ctx, "create schema app"); err != nil {
		t.Fatal(err)
	}
	tables := map[string]*schema.Table{
		"app.a": {Name: "a", Columns: map[string]*schema.Column{"id": {Type: "int", PrimaryKey: true}}},
		"app.b": {Name: "b", Columns: map[string]*schema.Column{"id": {Type: "int", PrimaryKey: true}}},
		"app.c": {Name: "c", Columns: map[string]*schema.Column{"extra": {Type: "int", Nullable: true}}, Inherits: []string{"app.b"}},
	}
	desired := func(p *schema.Publication) *schema.Database {
		p.Name = "pub"
		return &schema.Database{Schemas: map[string]bool{"app": true}, Tables: tables, Publications: map[string]*schema.Publication{"pub": p}}
	}
	plan := func(d *schema.Database, opts diff.Options) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.PlanWith(live, d, opts)
	}
	publish := func() string {
		var ins, upd bool
		if err := pool.QueryRow(ctx, "select pubinsert, pubupdate from pg_publication where pubname = 'pub'").Scan(&ins, &upd); err != nil {
			t.Fatal(err)
		}
		return strings.Trim(strings.Join([]string{map[bool]string{true: "insert"}[ins], map[bool]string{true: "update"}[upd]}, ","), ",")
	}
	except := func(tables ...*schema.PublicationTable) []*schema.PublicationTable { return tables }
	steps := []struct {
		name string
		p    *schema.Publication
		want string
	}{
		{"tables", &schema.Publication{Tables: []*schema.PublicationTable{{Table: "app.a"}, {Table: "app.b", Only: true}}, Publish: []string{"insert"}}, "create publication"},
		{"all tables except", &schema.Publication{AllTables: true, Except: except(&schema.PublicationTable{Table: "app.b"})}, `set all tables except (table "app"."b")`},
		{"except only", &schema.Publication{AllTables: true, AllSequences: true, Except: except(&schema.PublicationTable{Table: "app.b", Only: true})}, `set all tables except (table only "app"."b"), all sequences`},
		{"all sequences", &schema.Publication{AllSequences: true}, "set all sequences"},
		{"all tables", &schema.Publication{AllTables: true}, "set all tables"},
	}
	for _, s := range steps {
		p := plan(desired(s.p), diff.Options{})
		sql := diff.Render(p)
		if !strings.Contains(sql, s.want) || (s.name != "tables" && strings.Contains(sql, "drop publication")) {
			t.Fatalf("%s: want %q in place, got:\n%s", s.name, s.want, sql)
		}
		applyPlan(t, pool, p)
		if sql := diff.Render(plan(desired(s.p), diff.Options{Unsafe: true})); sql != "" {
			t.Fatalf("%s: expected no changes after apply, got:\n%s", s.name, sql)
		}
		if got := publish(); got != "insert" {
			t.Errorf("%s: switching in place keeps publish, got %q", s.name, got)
		}
	}
	back := desired(&schema.Publication{Tables: []*schema.PublicationTable{{Table: "app.a"}}})
	if sql := diff.Render(plan(back, diff.Options{})); sql != "" {
		t.Fatalf("leaving FOR ALL TABLES needs --unsafe, got:\n%s", sql)
	}
	applyPlan(t, pool, plan(back, diff.Options{Unsafe: true}))
	if sql := diff.Render(plan(back, diff.Options{Unsafe: true})); sql != "" {
		t.Fatalf("expected no changes after apply, got:\n%s", sql)
	}
}

func TestIntegrationPublicationExceptedTableAttached(t *testing.T) {
	requireServerVersion(t, 190000)
	ctx := context.Background()
	pool := freshDatabase(t, connect(t), "pgytest_pub19_attach")
	if _, err := pool.Exec(ctx, `create schema app;
		create table app.p (id int) partition by range (id);
		create table app.p1 (id int);
		create table app.x (id int primary key);
		create publication pub for all tables except (table app.p1, table app.x)`); err != nil {
		t.Fatal(err)
	}
	d := &schema.Database{Schemas: map[string]bool{"app": true}, Tables: map[string]*schema.Table{
		"app.p":  {Name: "p", Columns: map[string]*schema.Column{"id": {Type: "int", Nullable: true}}, PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"id"}}},
		"app.p1": {Name: "p1", PartitionOf: "app.p", Partition: &schema.PartitionSpec{From: []string{"0"}, To: []string{"10"}, Modulus: -1, Remainder: -1}},
		"app.x":  {Name: "x", Columns: map[string]*schema.Column{"id": {Type: "int", PrimaryKey: true}}},
	}, Publications: map[string]*schema.Publication{"pub": {Name: "pub", AllTables: true, Except: []*schema.PublicationTable{{Table: "app.x"}}}}}
	plan := func() *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, d, false)
	}
	applyPlan(t, pool, plan())
	if sql := diff.Render(plan()); sql != "" {
		t.Fatalf("expected no changes after apply, got:\n%s", sql)
	}
}
