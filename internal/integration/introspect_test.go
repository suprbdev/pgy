package integration

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationIntrospectNoPrivileges(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	owner := freshRole(t, pool, "owner")
	reader := freshRole(t, pool, "reader")
	for _, stmt := range []string{
		fmt.Sprintf(`alter role %q login password 'pgy'`, reader),
		fmt.Sprintf(`create schema %q authorization %q`, sch+"_hidden", owner),
		fmt.Sprintf(`create table %q.t (id int)`, sch+"_hidden"),
		fmt.Sprintf(`create view %q.v as select 1 as x`, sch+"_hidden"),
		fmt.Sprintf(`alter table %q.t owner to %q`, sch+"_hidden", owner),
		fmt.Sprintf(`alter view %q.v owner to %q`, sch+"_hidden", owner),
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), fmt.Sprintf(`drop schema if exists %q cascade`, sch+"_hidden")) //nolint
	})

	u, err := url.Parse(dsn(t))
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(reader, "pgy")
	readerPool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer readerPool.Close()

	live, err := diff.Introspect(ctx, readerPool)
	if err != nil {
		t.Fatal(err)
	}
	if !live.Schemas[sch+"_hidden"] {
		t.Errorf("schema without privileges not introspected")
	}
	if live.Tables[sch+"_hidden.t"] == nil || live.Tables[sch+"_hidden.t"].Columns["id"] == nil {
		t.Errorf("table without privileges not introspected")
	}
	if !live.Views[sch+"_hidden.v"] {
		t.Errorf("view without privileges not introspected")
	}
}

func TestIntegrationExtensionSchemaAndVersion(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "drop extension if exists ltree"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "drop extension if exists ltree cascade") }) //nolint

	plan := func(ext *schema.Extension) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, &schema.Database{Tables: map[string]*schema.Table{}, Extensions: []*schema.Extension{ext}}, false)
	}
	first := &schema.Extension{Name: "ltree", Schema: sch + "_a", Version: "1.1"}
	applyPlan(t, pool, plan(first))
	t.Cleanup(func() { pool.Exec(context.Background(), fmt.Sprintf("drop schema if exists %q, %q cascade", sch+"_a", sch+"_b")) }) //nolint
	if sql := diff.Render(plan(first)); sql != "" {
		t.Fatalf("expected no changes after create, got:\n%s", sql)
	}

	second := &schema.Extension{Name: "ltree", Schema: sch + "_b", Version: "1.2"}
	p := plan(second)
	if len(p.Alters) != 2 {
		t.Fatalf("expected set schema + update to, got creates=%v alters=%v", p.Creates, p.Alters)
	}
	applyPlan(t, pool, p)
	if sql := diff.Render(plan(second)); sql != "" {
		t.Fatalf("expected no changes after alter, got:\n%s", sql)
	}
	var nsp, ver string
	if err := pool.QueryRow(ctx, `select n.nspname, e.extversion from pg_extension e join pg_namespace n on n.oid = e.extnamespace where e.extname = 'ltree'`).Scan(&nsp, &ver); err != nil {
		t.Fatal(err)
	}
	if nsp != sch+"_b" || ver != "1.2" {
		t.Errorf("ltree in %s version %s", nsp, ver)
	}
}

func TestIntegrationGeneratedColumnAlter(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	desired := func(generated string) *schema.Database {
		return &schema.Database{Tables: map[string]*schema.Table{
			sch + ".t": {Name: "t", ColumnOrder: []string{"a", "b"}, Columns: map[string]*schema.Column{
				"a": {Type: "int"},
				"b": {Type: "int", Nullable: true, Generated: generated},
			}},
		}}
	}
	plan := func(d *schema.Database) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, d, true)
	}
	applyPlan(t, pool, plan(desired("a * 2")))
	if sql := diff.Render(plan(desired("a * 2"))); sql != "" {
		t.Fatalf("expected no changes after create, got:\n%s", sql)
	}

	var version int
	if err := pool.QueryRow(ctx, "select current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version >= 170000 {
		applyPlan(t, pool, plan(desired("a * 3")))
		if sql := diff.Render(plan(desired("a * 3"))); sql != "" {
			t.Fatalf("expected no changes after set expression, got:\n%s", sql)
		}
	}

	applyPlan(t, pool, plan(desired("")))
	if sql := diff.Render(plan(desired(""))); sql != "" {
		t.Fatalf("expected no changes after drop expression, got:\n%s", sql)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`insert into %q.t (a, b) values (1, 42)`, sch)); err != nil {
		t.Errorf("column should accept writes once no longer generated: %v", err)
	}
}
