package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationConcurrentIndexOnPartitionedTable(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	q := func(s string) string { return strings.ReplaceAll(s, "SCH", `"`+sch+`"`) }
	if _, err := pool.Exec(ctx, q(`
		create table SCH.ev (id int, region text, yr int) partition by list (region);
		create table SCH.ev_eu partition of SCH.ev for values in ('eu') partition by range (yr);
		create table SCH.ev_eu_2024 partition of SCH.ev_eu for values from (2024) to (2025);
		create table SCH.ev_us partition of SCH.ev for values in ('us');
		insert into SCH.ev values (1, 'eu', 2024), (2, 'us', 2023);
		create index ev_region_idx on only SCH.ev (region)`)); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(`
schema SCH:
  table ev:
    columns:
      id: {type: int, nullable: true}
      region: {type: text, nullable: true}
      yr: {type: int, nullable: true}
    partitionBy: {type: list, columns: [region]}
    indexes:
      ev_id_idx: {columns: [id], concurrently: true}
      ev_region_idx: {columns: [region], concurrently: true}
  table ev_eu:
    partitionOf: SCH.ev
    forValues: {in: [eu]}
    partitionBy: {type: range, columns: [yr]}
  table ev_eu_2024:
    partitionOf: SCH.ev_eu
    forValues: {from: [2024], to: [2025]}
  table ev_us:
    partitionOf: SCH.ev
    forValues: {in: [us]}
`, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.Plan(live, d, false)
	if len(plan.Creates)+len(plan.Alters) != 0 || len(plan.Warnings) != 0 {
		t.Errorf("everything belongs in the concurrent section:\n%s\nwarnings: %+v", diff.Render(plan), plan.Warnings)
	}
	applyPlan(t, pool, plan)
	var invalid int
	if err := pool.QueryRow(ctx, `select count(*) from pg_index x join pg_class c on c.oid = x.indexrelid where c.relnamespace = $1::regnamespace and not x.indisvalid`, sch).Scan(&invalid); err != nil {
		t.Fatal(err)
	}
	if invalid != 0 {
		t.Errorf("%d indexes left invalid", invalid)
	}
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if sql := diff.Render(diff.PlanWith(live, d, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("second plan not empty:\n%s", sql)
	}
}
