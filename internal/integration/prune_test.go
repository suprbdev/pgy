package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationPrune(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	other := sch + "_other"
	ctx := context.Background()
	t.Cleanup(func() { pool.Exec(context.Background(), fmt.Sprintf("drop schema if exists %q cascade", other)) }) //nolint
	setup := strings.ReplaceAll(`
create schema "OTHER";
create table "OTHER".untouched (id int);
create extension if not exists ltree schema "SCH";
create table "SCH".keep (id serial primary key, legacy text, ref int);
create table "SCH".gone (id int primary key, keep_id int references "SCH".keep(id));
alter table "SCH".keep add constraint keep_ref_fkey foreign key (ref) references "SCH".gone(id);
create index keep_legacy_idx on "SCH".keep (legacy);
create table "SCH".events (id int, at date not null) partition by range (at);
create table "SCH".events_2024 partition of "SCH".events for values from ('2024-01-01') to ('2025-01-01');
create index events_at_idx on "SCH".events (at);
create view "SCH".v1 as select id, legacy from "SCH".keep;
create view "SCH".v2 as select * from "SCH".v1;
create materialized view "SCH".mv as select id from "SCH".gone;
create function "SCH".old_fn(a int, b text) returns int language sql as 'select a';
create function "SCH".touch() returns trigger language plpgsql as 'begin return new; end';
create trigger keep_touch before update on "SCH".keep for each row execute function "SCH".touch();
create procedure "SCH".old_proc() language sql as 'select 1';
create sequence "SCH".loose_seq;
create domain "SCH".old_dom as text;
create type "SCH".old_enum as enum ('a');
create type "SCH".old_comp as (x int);
create aggregate "SCH".my_sum(int) (sfunc = int4pl, stype = int);
`, "SCH", sch)
	setup = strings.ReplaceAll(setup, "OTHER", other)
	if _, err := pool.Exec(ctx, setup); err != nil {
		t.Fatal(err)
	}

	y := strings.ReplaceAll(`
schema SCH:
  table keep:
    columns:
      id: {type: serial, primaryKey: true}
  table events:
    columns:
      id: {type: int, nullable: true}
      at: {type: date}
    partitionBy: {type: range, columns: [at]}
    indexes:
      events_at_idx:
        columns: [at]
  table events_2024:
    partitionOf: SCH.events
    forValues: {from: [2024-01-01], to: [2025-01-01]}
`, "SCH", sch)
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	desired, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	plan := func(opts diff.Options) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.PlanWith(live, desired, opts)
	}

	if strings.Contains(diff.Render(plan(diff.Options{Unsafe: true})), "drop table") {
		t.Fatal("--unsafe alone must not drop tables")
	}
	pruned := plan(diff.Options{Unsafe: true, Prune: true})
	t.Logf("prune plan:\n%s", diff.Render(pruned))
	applyPlan(t, pool, pruned)
	if sql := diff.Render(plan(diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Fatalf("expected no changes after prune, got:\n%s", sql)
	}

	exists := func(q string) bool {
		var ok bool
		if err := pool.QueryRow(ctx, q).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for _, rel := range []string{"gone", "v1", "v2", "mv", "loose_seq", "keep_legacy_idx"} {
		if exists(fmt.Sprintf(`select to_regclass('%q.%s') is not null`, sch, rel)) {
			t.Errorf("%s should have been pruned", rel)
		}
	}
	for _, rel := range []string{"keep", "keep_id_seq", "events", "events_2024", "events_2024_at_idx"} {
		if !exists(fmt.Sprintf(`select to_regclass('%q.%s') is not null`, sch, rel)) {
			t.Errorf("%s must be kept", rel)
		}
	}
	if !exists(fmt.Sprintf(`select to_regclass('%q.untouched') is not null`, other)) {
		t.Error("objects in schemas the YAML does not declare must be kept")
	}
	if exists(fmt.Sprintf(`select count(*) > 0 from pg_proc where pronamespace = '%q'::regnamespace and proname in ('old_fn', 'touch', 'old_proc')`, sch)) {
		t.Error("undeclared functions and procedures should have been pruned")
	}
	if !exists(fmt.Sprintf(`select count(*) = 1 from pg_proc where pronamespace = '%q'::regnamespace and proname = 'my_sum'`, sch)) {
		t.Error("aggregates must be kept")
	}
	if !exists(fmt.Sprintf(`select count(*) > 0 from pg_proc where pronamespace = '%q'::regnamespace and proname = 'ltree_in'`, sch)) {
		t.Error("extension functions must be kept")
	}
	if exists(fmt.Sprintf(`select count(*) > 0 from pg_type where typnamespace = '%q'::regnamespace and typname in ('old_dom', 'old_enum', 'old_comp')`, sch)) {
		t.Error("undeclared domains and types should have been pruned")
	}
	if !exists(fmt.Sprintf(`select count(*) > 0 from pg_type where typnamespace = '%q'::regnamespace and typname = 'ltree'`, sch)) {
		t.Error("extension types must be kept")
	}
}
