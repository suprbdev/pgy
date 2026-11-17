package integration

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

// partitionIndexCase loads setup into a fresh schema (SCH stands for its
// name), plans yml against it with --unsafe, applies the plan, requires the
// next plan to be empty and returns the schema and the applied SQL.
func partitionIndexCase(t *testing.T, pool *pgxpool.Pool, setup, yml string) (string, string) {
	t.Helper()
	ctx := context.Background()
	sch := freshSchema(t, pool)
	q := func(s string) string { return strings.ReplaceAll(s, "SCH", sch) }
	if setup != "" {
		if _, err := pool.Exec(ctx, q(setup)); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(q(yml)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.PlanWith(introspect(t, pool, d), d, diff.Options{Unsafe: true})
	sql := diff.Render(plan) + strings.Join(plan.Concurrent, "\n")
	applyPlan(t, pool, plan)
	next := diff.PlanWith(introspect(t, pool, d), d, diff.Options{Unsafe: true, Prune: true})
	if s := diff.Render(next) + strings.Join(next.Concurrent, "\n"); s != "" {
		t.Errorf("second plan not empty:\n%s\nafter applying:\n%s", s, sql)
	}
	return sch, sql
}

// partitionIndexState maps each index in schema sch to the index it is
// attached to, with a trailing ! when it is invalid.
func partitionIndexState(t *testing.T, pool *pgxpool.Pool, sch string) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		select c.relname, coalesce(p.relname, '') || case when x.indisvalid then '' else '!' end
		from pg_index x
		join pg_class c on c.oid = x.indexrelid
		left join pg_inherits i on i.inhrelid = c.oid
		left join pg_class p on p.oid = i.inhparent
		where c.relnamespace = $1::regnamespace`, sch)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, parent string
		if err := rows.Scan(&name, &parent); err != nil {
			t.Fatal(err)
		}
		out[name] = parent
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertPartitionIndexes(t *testing.T, pool *pgxpool.Pool, sch string, want map[string]string, sql string) {
	t.Helper()
	if got := partitionIndexState(t, pool, sch); !reflect.DeepEqual(got, want) {
		t.Errorf("indexes and what they are attached to:\n got %v\nwant %v\nafter applying:\n%s", got, want, sql)
	}
}

const partitionTreeYAML = `
schema SCH:
  table ev:
    columns:
      id: {type: int, nullable: true}
      region: {type: text, nullable: true}
      yr: {type: int, nullable: true}
      note: {type: text, nullable: true}
    partitionBy: {type: list, columns: [region]}
    indexes:
      ev_yr: {columns: [yr]}
      ev_note: {columns: [lower(note)]}
  table ev_eu:
    partitionOf: SCH.ev
    forValues: {in: [eu]}
    partitionBy: {type: range, columns: [yr]}
    indexes:
      ev_eu_yr_named: {columns: [yr]}
  table ev_eu_2024:
    partitionOf: SCH.ev_eu
    forValues: {from: [2024], to: [2025]}
    indexes:
      ev_eu_2024_yr_named: {columns: [yr]}
      ev_eu_2024_id: {columns: [id]}
  table ev_eu_2025:
    partitionOf: SCH.ev_eu
    forValues: {from: [2025], to: [2026]}
  table ev_us:
    partitionOf: SCH.ev
    forValues: {in: [us]}
    indexes:
      ev_us_note_named: {columns: [lower(note)]}
  table ev_other:
    partitionOf: SCH.ev
    default: true
`

func TestIntegrationPartitionIndexesAttachDeclaredOnCreate(t *testing.T) {
	pool := connect(t)
	sch, sql := partitionIndexCase(t, pool, "", partitionTreeYAML)
	assertPartitionIndexes(t, pool, sch, map[string]string{
		"ev_yr": "", "ev_note": "",
		"ev_eu_yr_named": "ev_yr", "ev_eu_2024_yr_named": "ev_eu_yr_named", "ev_eu_2025_yr_idx": "ev_eu_yr_named",
		"ev_us_yr_idx": "ev_yr", "ev_other_yr_idx": "ev_yr",
		"ev_eu_lower_idx": "ev_note", "ev_eu_2024_lower_idx": "ev_eu_lower_idx", "ev_eu_2025_lower_idx": "ev_eu_lower_idx",
		"ev_us_note_named": "ev_note", "ev_other_lower_idx": "ev_note",
		"ev_eu_2024_id": "",
	}, sql)
}

func TestIntegrationPartitionIndexesAdoptCopies(t *testing.T) {
	pool := connect(t)
	setup := `
		create table SCH.ev (id int, region text, yr int, note text) partition by list (region);
		create table SCH.ev_eu partition of SCH.ev for values in ('eu') partition by range (yr);
		create table SCH.ev_eu_2024 partition of SCH.ev_eu for values from (2024) to (2025);
		create table SCH.ev_eu_2025 partition of SCH.ev_eu for values from (2025) to (2026);
		create table SCH.ev_us partition of SCH.ev for values in ('us');
		create table SCH.ev_other partition of SCH.ev default;
		create index ev_yr on SCH.ev (yr);
		create index ev_note on SCH.ev (lower(note));`
	sch, sql := partitionIndexCase(t, pool, setup, partitionTreeYAML)
	for _, want := range []string{
		`alter index "` + sch + `"."ev_eu_yr_idx" rename to "ev_eu_yr_named";`,
		`alter index "` + sch + `"."ev_eu_2024_yr_idx" rename to "ev_eu_2024_yr_named";`,
		`alter index "` + sch + `"."ev_us_lower_idx" rename to "ev_us_note_named";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	assertPartitionIndexes(t, pool, sch, map[string]string{
		"ev_yr": "", "ev_note": "",
		"ev_eu_yr_named": "ev_yr", "ev_eu_2024_yr_named": "ev_eu_yr_named", "ev_eu_2025_yr_idx": "ev_eu_yr_named",
		"ev_us_yr_idx": "ev_yr", "ev_other_yr_idx": "ev_yr",
		"ev_eu_lower_idx": "ev_note", "ev_eu_2024_lower_idx": "ev_eu_lower_idx", "ev_eu_2025_lower_idx": "ev_eu_lower_idx",
		"ev_us_note_named": "ev_note", "ev_other_lower_idx": "ev_note",
		"ev_eu_2024_id": "",
	}, sql)
}

func TestIntegrationPartitionIndexesOnNewPartitions(t *testing.T) {
	pool := connect(t)
	setup := `
		create table SCH.ev (id int, region text, yr int, note text) partition by list (region);
		create table SCH.ev_other partition of SCH.ev default;
		create index ev_yr on SCH.ev (yr);
		create index ev_note on SCH.ev (lower(note));`
	sch, sql := partitionIndexCase(t, pool, setup, partitionTreeYAML)
	for _, want := range []string{
		`alter index "` + sch + `"."ev_eu_yr_idx" rename to "ev_eu_yr_named";`,
		`alter index "` + sch + `"."ev_eu_2024_yr_idx" rename to "ev_eu_2024_yr_named";`,
		`alter index "` + sch + `"."ev_us_lower_idx" rename to "ev_us_note_named";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if n := strings.Count(sql, "create index"); n != 1 {
		t.Errorf("only ev_eu_2024_id is a new index, got %d creates:\n%s", n, sql)
	}
	assertPartitionIndexes(t, pool, sch, map[string]string{
		"ev_yr": "", "ev_note": "",
		"ev_eu_yr_named": "ev_yr", "ev_eu_2024_yr_named": "ev_eu_yr_named", "ev_eu_2025_yr_idx": "ev_eu_yr_named",
		"ev_us_yr_idx": "ev_yr", "ev_other_yr_idx": "ev_yr",
		"ev_eu_lower_idx": "ev_note", "ev_eu_2024_lower_idx": "ev_eu_lower_idx", "ev_eu_2025_lower_idx": "ev_eu_lower_idx",
		"ev_us_note_named": "ev_note", "ev_other_lower_idx": "ev_note",
		"ev_eu_2024_id": "",
	}, sql)
}

func TestIntegrationPartitionIndexesNewOnExistingTree(t *testing.T) {
	pool := connect(t)
	setup := `
		create table SCH.ev (id int, region text, yr int, note text) partition by list (region);
		create table SCH.ev_eu partition of SCH.ev for values in ('eu') partition by range (yr);
		create table SCH.ev_eu_2024 partition of SCH.ev_eu for values from (2024) to (2025);
		create table SCH.ev_eu_2025 partition of SCH.ev_eu for values from (2025) to (2026);
		create table SCH.ev_us partition of SCH.ev for values in ('us');
		create table SCH.ev_other partition of SCH.ev default;`
	sch, sql := partitionIndexCase(t, pool, setup, partitionTreeYAML)
	assertPartitionIndexes(t, pool, sch, map[string]string{
		"ev_yr": "", "ev_note": "",
		"ev_eu_yr_named": "ev_yr", "ev_eu_2024_yr_named": "ev_eu_yr_named", "ev_eu_2025_yr_idx": "ev_eu_yr_named",
		"ev_us_yr_idx": "ev_yr", "ev_other_yr_idx": "ev_yr",
		"ev_eu_lower_idx": "ev_note", "ev_eu_2024_lower_idx": "ev_eu_lower_idx", "ev_eu_2025_lower_idx": "ev_eu_lower_idx",
		"ev_us_note_named": "ev_note", "ev_other_lower_idx": "ev_note",
		"ev_eu_2024_id": "",
	}, sql)
}

func TestIntegrationPartitionIndexesConcurrentKeepDeclaredNames(t *testing.T) {
	pool := connect(t)
	setup := `
		create table SCH.ev (id int, region text, yr int) partition by list (region);
		create table SCH.ev_eu partition of SCH.ev for values in ('eu') partition by range (yr);
		create table SCH.ev_eu_2024 partition of SCH.ev_eu for values from (2024) to (2025);
		create table SCH.ev_us partition of SCH.ev for values in ('us');
		create index ev_us_yr_named on SCH.ev_us (yr);
		insert into SCH.ev values (1, 'eu', 2024), (2, 'us', 2023);`
	yml := `
schema SCH:
  table ev:
    columns:
      id: {type: int, nullable: true}
      region: {type: text, nullable: true}
      yr: {type: int, nullable: true}
    partitionBy: {type: list, columns: [region]}
    indexes:
      ev_yr: {columns: [yr], concurrently: true}
  table ev_eu:
    partitionOf: SCH.ev
    forValues: {in: [eu]}
    partitionBy: {type: range, columns: [yr]}
  table ev_eu_2024:
    partitionOf: SCH.ev_eu
    forValues: {from: [2024], to: [2025]}
    indexes:
      ev_eu_2024_yr_named: {columns: [yr]}
  table ev_eu_2025:
    partitionOf: SCH.ev_eu
    forValues: {from: [2025], to: [2026]}
  table ev_us:
    partitionOf: SCH.ev
    forValues: {in: [us]}
    indexes:
      ev_us_yr_named: {columns: [yr]}
  table ev_asia:
    partitionOf: SCH.ev
    forValues: {in: [asia]}
    indexes:
      ev_asia_yr_named: {columns: [yr]}
  table ev_other:
    partitionOf: SCH.ev
    default: true
`
	sch, sql := partitionIndexCase(t, pool, setup, yml)
	assertPartitionIndexes(t, pool, sch, map[string]string{
		"ev_yr":       "",
		"ev_eu_ev_yr": "ev_yr", "ev_eu_2024_yr_named": "ev_eu_ev_yr", "ev_eu_2025_ev_eu_ev_yr": "ev_eu_ev_yr",
		"ev_us_yr_named": "ev_yr", "ev_asia_yr_named": "ev_yr", "ev_other_ev_yr": "ev_yr",
	}, sql)
}

func TestIntegrationPartitionIndexConcurrentCompletesHandAttached(t *testing.T) {
	pool := connect(t)
	setup := `
		create table SCH.p (id int, k int) partition by range (k);
		create table SCH.p1 partition of SCH.p for values from (0) to (10);
		create table SCH.p2 partition of SCH.p for values from (10) to (20);
		create index p_k_only on only SCH.p (k);
		create index p1_k on SCH.p1 (k);
		alter index SCH.p_k_only attach partition SCH.p1_k;`
	yml := `
schema SCH:
  table p:
    columns:
      id: {type: int, nullable: true}
      k: {type: int, nullable: true}
    partitionBy: {type: range, columns: [k]}
    indexes:
      p_k_only: {columns: [k], concurrently: true}
  table p1:
    partitionOf: SCH.p
    forValues: {from: [0], to: [10]}
    indexes:
      p1_k: {columns: [k]}
  table p2:
    partitionOf: SCH.p
    forValues: {from: [10], to: [20]}
`
	sch, sql := partitionIndexCase(t, pool, setup, yml)
	assertPartitionIndexes(t, pool, sch, map[string]string{"p_k_only": "", "p1_k": "p_k_only", "p2_p_k_only": "p_k_only"}, sql)
}

// TestIntegrationPartialPartitionIndexPull checks a parent index that only
// some partitions are attached to: pgy pull writes the hand-named partition
// index, the pulled YAML diffs clean against the source, which keeps its
// invalid parent index, and a rebuild keeps the name and completes the
// parent index.
func TestIntegrationPartialPartitionIndexPull(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	q := func(s string) string { return strings.ReplaceAll(s, "SCH", `"`+sch+`"`) }
	if _, err := pool.Exec(ctx, q(`
		create table SCH.p (id int, k int) partition by range (k);
		create table SCH.p1 partition of SCH.p for values from (0) to (10);
		create table SCH.p2 partition of SCH.p for values from (10) to (20);
		create index p_k_only on only SCH.p (k);
		create index p1_k on SCH.p1 (k);
		alter index SCH.p_k_only attach partition SCH.p1_k;`)); err != nil {
		t.Fatal(err)
	}
	live := introspectAll(t, pool)
	res, err := pull.Build(live, pull.Options{Schemas: []string{sch}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := []string{}
	var pulled string
	for _, f := range res.Files {
		p := filepath.Join(dir, f.Name)
		if err := os.WriteFile(p, f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
		pulled += string(f.Content)
	}
	if !strings.Contains(pulled, "p1_k:") || strings.Contains(pulled, "p2_") {
		t.Errorf("pull must write the hand-named partition index only:\n%s", pulled)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "index p_k_only is invalid") {
		t.Errorf("pull must warn about the invalid parent index, got %q", res.Warnings)
	}
	d, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}
	if sql := diff.Render(diff.PlanWith(live, d, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("pulled YAML must diff clean against its source:\n%s\n%s", sql, pulled)
	}
	if _, err := pool.Exec(ctx, q(`drop table SCH.p`)); err != nil {
		t.Fatal(err)
	}
	applyPlan(t, pool, diff.Plan(introspectAll(t, pool), d, false))
	if sql := diff.Render(diff.PlanWith(introspectAll(t, pool), d, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("rebuilt schema must diff clean:\n%s", sql)
	}
	assertPartitionIndexes(t, pool, sch, map[string]string{"p_k_only": "", "p1_k": "p_k_only", "p2_k_idx": "p_k_only"}, "")
}

const rebuiltPartitionIndexSetup = `
	create table SCH.p (id int, k int) partition by range (k);
	create table SCH.p1 partition of SCH.p for values from (0) to (10) partition by range (id);
	create table SCH.p1_a partition of SCH.p1 for values from (0) to (100);
	create table SCH.p2 partition of SCH.p for values from (10) to (20);
	create index p_k on only SCH.p (k);
	create index p1_k_named on only SCH.p1 (k);
	create index p1_a_k_named on SCH.p1_a (k);
	alter index SCH.p1_k_named attach partition SCH.p1_a_k_named;
	alter index SCH.p_k attach partition SCH.p1_k_named;
	create index p2_k_named on SCH.p2 (k);
	alter index SCH.p_k attach partition SCH.p2_k_named;
	comment on index SCH.p2_k_named is 'kept';`

func rebuiltPartitionIndexYAML(def string) string {
	return `
schema SCH:
  table p:
    columns:
      id: {type: int, nullable: true}
      k: {type: int, nullable: true}
    partitionBy: {type: range, columns: [k]}
    indexes:
      p_k: ` + def + `
  table p1:
    partitionOf: SCH.p
    forValues: {from: [0], to: [10]}
    partitionBy: {type: range, columns: [id]}
    indexes:
      p1_k_named: {columns: [k desc]}
  table p1_a:
    partitionOf: SCH.p1
    forValues: {from: [0], to: [100]}
    indexes:
      p1_a_k_named: {columns: [k desc]}
  table p2:
    partitionOf: SCH.p
    forValues: {from: [10], to: [20]}
    indexes:
      p2_k_named: {columns: [k desc], comment: kept}
`
}

func TestIntegrationPartitionIndexesRebuiltWithParent(t *testing.T) {
	for name, def := range map[string]string{
		"plain":        `{columns: [k desc]}`,
		"concurrently": `{columns: [k desc], concurrently: true}`,
	} {
		t.Run(name, func(t *testing.T) {
			pool := connect(t)
			sch, sql := partitionIndexCase(t, pool, rebuiltPartitionIndexSetup, rebuiltPartitionIndexYAML(def))
			assertPartitionIndexes(t, pool, sch, map[string]string{"p_k": "", "p1_k_named": "p_k", "p1_a_k_named": "p1_k_named", "p2_k_named": "p_k"}, sql)
			var desc int
			if err := pool.QueryRow(context.Background(), `select count(*) from pg_index x join pg_class c on c.oid = x.indexrelid where c.relnamespace = $1::regnamespace and x.indoption[0] & 1 = 1`, sch).Scan(&desc); err != nil {
				t.Fatal(err)
			}
			if desc != 4 {
				t.Errorf("%d of 4 indexes rebuilt descending:\n%s", desc, sql)
			}
		})
	}
}

func TestIntegrationPartitionIndexChangedAloneIsNotDropped(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	if _, err := pool.Exec(ctx, strings.ReplaceAll(rebuiltPartitionIndexSetup, "SCH", sch)); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "s.yml")
	yml := strings.ReplaceAll(rebuiltPartitionIndexYAML(`{columns: [k]}`), "[k desc], comment", "[k], comment")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(yml, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.Plan(introspect(t, pool, d), d, true)
	if sql := diff.Render(plan) + strings.Join(plan.Concurrent, "\n"); sql != "" {
		t.Errorf("an attached partition index cannot be dropped on its own, got:\n%s", sql)
	}
	warned := 0
	for _, w := range plan.Warnings {
		if strings.Contains(w.Message, "is attached to index") {
			warned++
		}
	}
	if warned != 2 {
		t.Errorf("expected warnings for p1_k_named and p1_a_k_named, got %+v", plan.Warnings)
	}
}
