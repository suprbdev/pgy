package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestIntegrationForeignTableInherits(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	srv := foreignServer(t, pool, "inherits")
	opts := diff.Options{Unsafe: true, Prune: true}
	y := func(s string) string { return strings.ReplaceAll(s, "SRV", srv) }
	sql := convergeYAMLWith(t, pool, sch, y(`
extensions: [postgres_fdw]
servers:
  SRV: {wrapper: postgres_fdw, options: {dbname: remote}}
schema SCH:
  table parent:
    columns:
      id: {type: integer}
      name: {type: text, nullable: true}
  foreign table child:
    server: SRV
    inherits: [parent]
    options: {table_name: child}
    columns:
      id: {storage: plain, options: {column_name: ident}}
      name: {default: "'x'", options: {}}
      extra: {type: integer, nullable: true, generated: id * 2, options: {}}
  foreign table loose:
    server: SRV
    options: {}
    columns:
      id: {type: integer, options: {}}
      name: {type: text, nullable: true, options: {}}
`), opts)
	for _, want := range []string{
		`("extra" integer generated always as (id * 2) stored) inherits ("` + sch + `"."parent") server "` + srv + `"`,
		`alter column "id" options (add "column_name" 'ident')`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	sql = convergeYAMLWith(t, pool, sch, y(`
extensions: [postgres_fdw]
servers:
  SRV: {wrapper: postgres_fdw, options: {dbname: remote}}
schema SCH:
  table parent:
    columns:
      id: {type: integer}
      name: {type: text, nullable: true}
  foreign table child:
    server: SRV
    inherits: []
    options: {table_name: child}
    columns:
      id: {type: integer, options: {}}
      name: {type: text, nullable: true, default: "'x'", options: {}}
      extra: {type: integer, nullable: true, generated: id * 2, options: {}}
  foreign table loose:
    server: SRV
    inherits: [parent]
    options: {}
    columns:
      id: {options: {}}
      name: {options: {column_name: label}}
`), opts)
	for _, want := range []string{
		`alter table "` + sch + `"."child" no inherit "` + sch + `"."parent";`,
		`alter table "` + sch + `"."loose" inherit "` + sch + `"."parent";`,
		`alter column "id" options (drop "column_name")`,
		`alter column "name" options (add "column_name" 'label')`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestIntegrationForeignPartitionColumnOptionsInOnePlan(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	srv := foreignServer(t, pool, "partopts")
	sql := convergeYAMLWith(t, pool, sch, strings.ReplaceAll(`
extensions: [postgres_fdw]
servers:
  SRV: {wrapper: postgres_fdw, options: {dbname: shards}}
schema SCH:
  table events:
    columns:
      id: {type: bigint}
      region: {type: text}
    partitionBy: {type: list, columns: [region]}
  foreign table events_us:
    server: SRV
    partitionOf: events
    forValues: {in: [us]}
    options: {table_name: events}
    columns:
      id: {options: {column_name: event_id}}
      region: {options: {}}
`, "SRV", srv), diff.Options{Unsafe: true, Prune: true})
	if !strings.Contains(sql, `alter column "id" options (add "column_name" 'event_id')`) {
		t.Errorf("the partition's column options belong in the plan that creates it:\n%s", sql)
	}
}

func TestIntegrationServerRename(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	old := foreignServer(t, pool, "rename_old")
	renamed := foreignServer(t, pool, "rename_new")
	opts := diff.Options{Unsafe: true, Prune: true}
	y := `
extensions: [postgres_fdw]
servers:
  SRV:
    wrapper: postgres_fdw
    options: {dbname: remote}
    RENAMED
schema SCH:
  foreign table orders:
    server: SRV
    options: {}
    columns:
      id: {type: integer, options: {}}
`
	convergeYAMLWith(t, pool, sch, strings.NewReplacer("SRV", old, "RENAMED", "comment: before").Replace(y), opts)
	if _, err := pool.Exec(context.Background(), `create user mapping for public server `+old); err != nil {
		t.Fatal(err)
	}
	sql := convergeYAMLWith(t, pool, sch, strings.NewReplacer("SRV", renamed, "RENAMED", "renamedFrom: "+old).Replace(y), opts)
	if want := `alter server "` + old + `" rename to "` + renamed + `";`; strings.TrimSpace(sql) != want {
		t.Errorf("expected only %q, got:\n%s", want, sql)
	}
	var server string
	if err := pool.QueryRow(context.Background(), `select s.srvname from pg_foreign_table f join pg_foreign_server s on s.oid = f.ftserver where f.ftrelid = ('"`+sch+`".orders')::regclass`).Scan(&server); err != nil || server != renamed {
		t.Errorf("the foreign table must follow the renamed server, got %q (%v)", server, err)
	}
}

func TestIntegrationPruneServerWithUserMappings(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	srv := foreignServer(t, pool, "prune_mapped")
	ctx := context.Background()
	for _, stmt := range []string{
		`create server ` + srv + ` foreign data wrapper postgres_fdw`,
		`create user mapping for public server ` + srv + ` options (user 'x', password 'y')`,
		`create user mapping for current_user server ` + srv,
		`create foreign table "` + sch + `".gone (id int) server ` + srv,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	sql := convergeYAMLWith(t, pool, sch, `
extensions: [postgres_fdw]
servers: {}
schema SCH:
  table keep:
    columns: {id: {type: int}}
`, diff.Options{Unsafe: true, Prune: true})
	for _, want := range []string{
		`drop foreign table "` + sch + `"."gone";`,
		`drop user mapping for public server "` + srv + `";`,
		`drop server "` + srv + `";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestIntegrationPruneKeepsServerWithForeignTablesElsewhere(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	srv := foreignServer(t, pool, "prune_kept")
	ctx := context.Background()
	other := "pgytest_unmanaged_foreign"
	dropOther := func() { pool.Exec(context.Background(), `drop schema if exists `+other+` cascade`) } //nolint
	dropOther()
	t.Cleanup(dropOther)
	for _, stmt := range []string{
		`create schema ` + other,
		`create server ` + srv + ` foreign data wrapper postgres_fdw`,
		`create foreign table "` + other + `".ft (id int) server ` + srv,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	d := loadYAML(t, sch, `
extensions: [postgres_fdw]
servers: {}
schema SCH:
  table keep:
    columns: {id: {type: int}}
`)
	plan := diff.PlanWith(introspect(t, pool, d), d, diff.Options{Unsafe: true, Prune: true})
	if sql := diff.Render(plan); strings.Contains(sql, `drop server "`+srv+`"`) {
		t.Errorf("a server a foreign table outside the pruned schemas uses must stay:\n%s", sql)
	}
	warned := false
	for _, w := range plan.Warnings {
		warned = warned || (strings.Contains(w.Message, "server "+srv+" is not in the servers block") && strings.Contains(w.Message, other+".ft"))
	}
	if !warned {
		t.Errorf("expected a warning naming the server and its foreign table, got %+v", plan.Warnings)
	}
	applyPlan(t, pool, plan)
}

func TestIntegrationTableThatIsForeignLiveIsLeftAlone(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	srv := foreignServer(t, pool, "kind")
	ctx := context.Background()
	for _, stmt := range []string{
		`create server ` + srv + ` foreign data wrapper postgres_fdw`,
		`create foreign table "` + sch + `".keyed (id int not null, v text) server ` + srv,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	d := loadYAML(t, sch, `
schema SCH:
  table keyed:
    columns:
      id: {type: int}
      v: {type: text, nullable: true, comment: a value}
    primaryKey: [id]
    comment: local
`)
	plan := diff.PlanWith(introspect(t, pool, d), d, diff.Options{Unsafe: true, Prune: true})
	if sql := diff.Render(plan); sql != "" {
		t.Errorf("a declared table that is a foreign table live is left alone, got:\n%s", sql)
	}
	warned := false
	for _, w := range plan.Warnings {
		warned = warned || strings.Contains(w.Message, sch+".keyed is declared as a table but is a foreign table in the database")
	}
	if !warned {
		t.Errorf("expected a kind warning, got %+v", plan.Warnings)
	}
}

func TestIntegrationChangedWrapperEmitsNothingForTheServer(t *testing.T) {
	pool := connect(t)
	srv := foreignServer(t, pool, "wrapper")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `create extension if not exists file_fdw`); err != nil {
		t.Skipf("file_fdw unavailable: %v", err)
	}
	if _, err := pool.Exec(ctx, `create server `+srv+` foreign data wrapper file_fdw`); err != nil {
		t.Fatal(err)
	}
	d := loadYAML(t, "public", strings.ReplaceAll(`
extensions: [postgres_fdw, file_fdw]
servers:
  SRV: {wrapper: postgres_fdw, version: "2", options: {dbname: z}}
`, "SRV", srv))
	plan := diff.PlanWith(introspect(t, pool, d), d, diff.Options{Unsafe: true})
	if sql := diff.Render(plan); strings.Contains(sql, "alter server") {
		t.Errorf("no ALTER SERVER for a server whose wrapper changed, got:\n%s", sql)
	}
	applyPlan(t, pool, plan)
}
