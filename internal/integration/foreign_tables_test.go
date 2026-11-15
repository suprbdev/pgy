package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
)

// foreignServer creates the postgres_fdw extension and returns a server
// name unique to the test, dropped (with its foreign tables) afterwards.
func foreignServer(t *testing.T, pool *pgxpool.Pool, suffix string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `create extension if not exists postgres_fdw`); err != nil {
		t.Skipf("postgres_fdw unavailable: %v", err)
	}
	name := "pgytest_srv_" + suffix
	drop := func() { pool.Exec(context.Background(), `drop server if exists `+name+` cascade`) } //nolint
	drop()
	t.Cleanup(drop)
	return name
}

func TestIntegrationForeignTablesConverge(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	srv := foreignServer(t, pool, "converge")
	opts := diff.Options{Unsafe: true, Prune: true}
	y := func(s string) string { return strings.ReplaceAll(s, "SRV", srv) }
	sql := convergeYAMLWith(t, pool, sch, y(`
extensions: [postgres_fdw]
servers:
  SRV:
    wrapper: postgres_fdw
    options: {host: localhost, dbname: remote}
    comment: the warehouse
schema SCH:
  foreign table orders:
    server: SRV
    options: {schema_name: public, table_name: orders}
    comment: remote orders
    grants: {public: [select]}
    columns:
      id: {type: bigint}
      total: {type: "numeric(12,2)", nullable: true, options: {column_name: amount}}
`), opts)
	for _, want := range []string{"create server", "create foreign table if not exists", `comment on foreign table`, `alter column "total" options (add "column_name" 'amount')`} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	sql = convergeYAMLWith(t, pool, sch, y(`
extensions: [postgres_fdw]
servers:
  SRV:
    wrapper: postgres_fdw
    version: "2"
    options: {host: db.internal, port: "5433"}
schema SCH:
  foreign table orders:
    server: SRV
    options: {table_name: orders, fetch_size: "500"}
    columns:
      id: {type: bigint}
      total: {type: "numeric(14,2)", nullable: true, options: {column_name: total_amount}}
      note: {type: text, nullable: true, options: {column_name: remark}}
`), opts)
	for _, want := range []string{
		`alter server "` + srv + `" version '2';`,
		`options (set "host" 'db.internal', add "port" '5433', drop "dbname")`,
		`options (add "fetch_size" '500', drop "schema_name")`,
		`add column "note" text`,
		`alter column "note" options (add "column_name" 'remark')`,
		`alter column "total" type numeric(14,2)`,
		`alter column "total" options (set "column_name" 'total_amount')`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	sql = convergeYAMLWith(t, pool, sch, `
extensions: [postgres_fdw]
servers: {}
schema SCH:
  table keep:
    columns: {id: {type: int}}
`, opts)
	for _, want := range []string{`drop foreign table "` + sch + `"."orders";`, `drop server "` + srv + `";`} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestIntegrationForeignTablesIdempotent(t *testing.T) {
	pool := connect(t)
	srv := foreignServer(t, pool, "idem")
	assertYAMLIdempotent(t, map[string]string{
		"foreign": strings.ReplaceAll(`
extensions: [postgres_fdw]
servers:
  SRV:
    wrapper: postgres_fdw
    version: "1.1"
    options: {host: localhost, dbname: remote, use_remote_estimate: "true"}
    comment: remote db
schema SCH:
  type status: {type: enum, labels: [open, closed]}
  foreign table orders:
    server: SRV
    options: {schema_name: public, table_name: orders}
    comment: remote orders
    grants: {public: [select]}
    columns:
      id: {type: bigint}
      status: {type: SCH.status, nullable: true}
      total: {type: "numeric(12,2)", nullable: true, default: 0, options: {column_name: amount}, comment: order total}
    dependsOn: [type SCH.status]
  view open_orders:
    query: select id, total from SCH.orders where status = 'open'
    dependsOn: [table SCH.orders]
`, "SRV", srv),
	})
}

func TestIntegrationForeignPartitions(t *testing.T) {
	pool := connect(t)
	srv := foreignServer(t, pool, "parts")
	assertYAMLIdempotent(t, map[string]string{
		"foreign_partitions": strings.ReplaceAll(`
extensions: [postgres_fdw]
servers:
  SRV: {wrapper: postgres_fdw, options: {dbname: shards}}
schema SCH:
  function touch():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table m:
    columns:
      id: {type: int}
      region: {type: text}
    partitionBy: {list: [region]}
    indexes: {m_id: {columns: [id]}}
  table m_eu:
    partitionOf: SCH.m
    forValues: {in: [eu]}
  foreign table m_us:
    server: SRV
    partitionOf: SCH.m
    forValues: {in: [us]}
    options: {table_name: m_us}
  foreign table audit:
    server: SRV
    columns: {id: {type: int}}
    triggers:
      audit_touch: {timing: before, events: [insert], level: row, procedure: SCH.touch()}
`, "SRV", srv),
	})
}

func TestIntegrationConcurrentIndexWithForeignPartition(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	srv := foreignServer(t, pool, "conc")
	opts := diff.Options{Unsafe: true, Prune: true}
	base := strings.ReplaceAll(`
extensions: [postgres_fdw]
servers:
  SRV: {wrapper: postgres_fdw}
schema SCH:
  table m:
    columns: {id: {type: int}, region: {type: text}}
    partitionBy: {list: [region]}
INDEX
  table m_eu:
    partitionOf: SCH.m
    forValues: {in: [eu]}
  foreign table m_us:
    server: SRV
    partitionOf: SCH.m
    forValues: {in: [us]}
`, "SRV", srv)
	convergeYAMLWith(t, pool, sch, strings.Replace(base, "INDEX\n", "", 1), opts)
	sql := convergeYAMLWith(t, pool, sch, strings.Replace(base, "INDEX", "    indexes: {m_region: {columns: [region], concurrently: true}}", 1), opts)
	if strings.Contains(sql, "concurrently") || !strings.Contains(sql, `create index if not exists "m_region" on "`+sch+`"."m"`) {
		t.Errorf("with a foreign partition the index must be built without CONCURRENTLY:\n%s", sql)
	}
}

func TestIntegrationForeignTableRename(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	srv := foreignServer(t, pool, "rename")
	opts := diff.Options{Unsafe: true, Prune: true}
	base := "extensions: [postgres_fdw]\nservers:\n  " + srv + ": {wrapper: postgres_fdw}\nschema SCH:\n"
	convergeYAMLWith(t, pool, sch, base+"  foreign table a:\n    server: "+srv+"\n    columns: {id: {type: int}}\n", opts)
	sql := convergeYAMLWith(t, pool, sch, base+"  foreign table b:\n    server: "+srv+"\n    renamedFrom: a\n    columns: {id: {type: int}}\n", opts)
	if !strings.Contains(sql, `rename to "b"`) || strings.Contains(sql, "drop foreign table") {
		t.Errorf("expected a rename, not a drop and create:\n%s", sql)
	}
}
