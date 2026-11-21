package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationPublications(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	name := sch + "_pub"
	t.Cleanup(func() { pool.Exec(context.Background(), fmt.Sprintf("drop publication if exists %q", name)) }) //nolint
	tables := map[string]*schema.Table{
		sch + ".a": {Name: "a", Columns: map[string]*schema.Column{"id": {Type: "int", PrimaryKey: true}, "x": {Type: "int"}, "y": {Type: "text"}}},
		sch + ".b": {Name: "b", Columns: map[string]*schema.Column{"id": {Type: "int", PrimaryKey: true}}},
	}
	desired := func(p *schema.Publication) *schema.Database {
		p.Name = name
		return &schema.Database{Tables: tables, Publications: map[string]*schema.Publication{name: p}}
	}
	plan := func(d *schema.Database) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, d, false)
	}
	steps := []*schema.Database{
		desired(&schema.Publication{Tables: []*schema.PublicationTable{{Table: sch + ".a", Columns: []string{"x", "id"}, Where: "x > 0"}, {Table: sch + ".b"}},
			Publish: []string{"insert", "update"}, ViaPartitionRoot: schema.BoolPtr(true), Comment: "cdc"}),
		desired(&schema.Publication{Tables: []*schema.PublicationTable{{Table: sch + ".b"}}, Publish: []string{"insert", "update", "delete"}, Comment: "cdc v2"}),
		desired(&schema.Publication{Schemas: []string{sch}}),
		desired(&schema.Publication{}),
	}
	for i, d := range steps {
		applyPlan(t, pool, plan(d))
		if sql := diff.Render(plan(d)); sql != "" {
			t.Fatalf("step %d: expected no changes after apply, got:\n%s", i, sql)
		}
	}
}

func TestIntegrationPublicationGeneratedColumns(t *testing.T) {
	pool := connect(t)
	var version int
	if err := pool.QueryRow(context.Background(), "select current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	y := `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      doubled: {type: int, nullable: true, generated: id * 2}
publications:
  SCH_gc:
    tables: [SCH.t]
    generatedColumns: stored
`
	if version < 180000 {
		sch := freshSchema(t, pool)
		pub := sch + "_gc"
		t.Cleanup(func() { pool.Exec(context.Background(), fmt.Sprintf("drop publication if exists %q", pub)) }) //nolint
		sql := convergeYAMLWith(t, pool, sch, y, diff.Options{})
		if strings.Contains(sql, "publish_generated_columns") {
			t.Errorf("before PostgreSQL 18 the option must not be emitted:\n%s", sql)
		}
		return
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `do $$ declare r record; begin
			for r in select pubname from pg_publication where pubname like 'pgytest_testintegrationpublicationgenerated%' loop
				execute format('drop publication %I', r.pubname);
			end loop; end $$`)
	})
	assertYAMLIdempotent(t, map[string]string{"stored": y})
}

func TestIntegrationPublicationEmptiedWithOnlyTable(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	name := sch + "_only"
	t.Cleanup(func() { pool.Exec(context.Background(), `drop publication if exists "`+name+`"`) }) //nolint
	tables := map[string]*schema.Table{
		sch + ".base": {Name: "base", Columns: map[string]*schema.Column{"id": {Type: "int", PrimaryKey: true}}},
		sch + ".ext":  {Name: "ext", Columns: map[string]*schema.Column{"extra": {Type: "int", Nullable: true}}, Inherits: []string{sch + ".base"}},
	}
	for i, p := range []*schema.Publication{{Tables: []*schema.PublicationTable{{Table: sch + ".base", Only: true}}}, {}} {
		p.Name = name
		d := &schema.Database{Tables: tables, Publications: map[string]*schema.Publication{name: p}}
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		applyPlan(t, pool, diff.Plan(live, d, false))
		live, err = diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		if sql := diff.Render(diff.Plan(live, d, false)); sql != "" {
			t.Fatalf("step %d: expected no changes after apply, got:\n%s", i, sql)
		}
	}
}
