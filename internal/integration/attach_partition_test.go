package integration

import (
	"context"

	"github.com/suprbdev/pgy/internal/diff"
	"testing"
)

func TestIntegrationDetachedPartitionIsReattached(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	y := `
schema SCH:
  table accounts:
    columns: {id: {type: int, primaryKey: true}}
  table m:
    columns: {id: {type: int}, at: {type: date}, account_id: {type: int, nullable: true}}
    primaryKey: [id, at]
    partitionBy: {range: [at]}
    indexes: {m_account: {columns: [account_id]}}
    foreignKeys: {m_account_fk: {columns: [account_id], references: {table: SCH.accounts, columns: [id]}}}
  table m_2025:
    partitionOf: SCH.m
    forValues: {from: ["2025-01-01"], to: ["2026-01-01"]}
  table m_2026:
    partitionOf: SCH.m
    forValues: {from: ["2026-01-01"], to: ["2027-01-01"]}
`
	convergeYAML(t, pool, sch, y)
	if _, err := pool.Exec(ctx, `alter table `+sch+`.m detach partition `+sch+`.m_2026`); err != nil {
		t.Fatal(err)
	}
	if sql := convergeYAMLWith(t, pool, sch, y, diff.Options{Unsafe: true, Prune: true}); !contains(sql, "attach partition") || contains(sql, "drop ") {
		t.Errorf("want a re-attach and no drops, got:\n%s", sql)
	}
	var parent string
	if err := pool.QueryRow(ctx, `select inhparent::regclass::text from pg_inherits where inhrelid = $1::regclass`, sch+".m_2026").Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != sch+".m" {
		t.Errorf("m_2026 is attached to %q", parent)
	}
}
