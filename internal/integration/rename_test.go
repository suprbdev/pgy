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

func TestIntegrationRenames(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	moved := sch + "_moved"
	if _, err := pool.Exec(ctx, `drop schema if exists "`+moved+`" cascade`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `drop schema if exists "`+moved+`" cascade`) }) //nolint
	load := func(y string) *schema.Database {
		t.Helper()
		p := filepath.Join(t.TempDir(), "s.yml")
		y = strings.ReplaceAll(strings.ReplaceAll(y, "MOVED", moved), "SCH", sch)
		if err := os.WriteFile(p, []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
		d, err := schema.LoadAndMerge([]string{p})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	plan := func(d *schema.Database) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.PlanWith(live, d, diff.Options{Unsafe: true, Prune: true})
	}
	v1 := load(`
schema SCH:
  type mood:
    type: enum
    labels: [ok, meh]
  domain code:
    type: text
    check: length(VALUE) > 0
  sequence counter: {}
  function touch():
    returns: trigger
    language: plpgsql
    body: begin new.note := coalesce(new.note, 'touched'); return new; end
  function twice(x int):
    returns: int
    language: sql
    immutable: true
    body: select x * 2
  procedure reset_counter():
    language: sql
    body: select 1
  table owners:
    columns:
      id: {type: int}
    primaryKey: [id]
  table items:
    columns:
      id: {type: int}
      owner_id: {type: int, nullable: true}
      qty: {type: int, default: "0"}
      mood: {type: SCH.mood, nullable: true}
      code: {type: SCH.code, nullable: true}
      note: {type: text, nullable: true}
      doubled: {type: int, generated: qty * 2}
    primaryKey: [id]
    indexes:
      items_qty_idx: {columns: [qty], where: qty > 0}
    constraints:
      items_qty_check: {type: check, expression: qty >= 0}
    foreignKeys:
      items_owner_fkey:
        columns: [owner_id]
        references: {table: SCH.owners, columns: [id]}
    triggers:
      items_touch: {timing: before, events: [insert, update of qty], level: row, procedure: SCH.touch()}
    rowLevelSecurity: true
    policies:
      items_pos: {using: qty >= 0}
  view items_view:
    query: select id, qty from SCH.items
  materialized view items_mv:
    query: select id, qty from SCH.items
    indexes:
      items_mv_id: {columns: [id], unique: true}
`)
	applyPlan(t, pool, plan(v1))
	if _, err := pool.Exec(ctx, `insert into "`+sch+`".owners values (1); insert into "`+sch+`".items (id, owner_id, qty, mood) values (1, 1, 5, 'meh')`); err != nil {
		t.Fatal(err)
	}
	v2 := load(`
schema SCH:
  type feeling:
    renamedFrom: mood
    type: enum
    labels: [ok, so_so]
    renamedLabels: {so_so: meh}
  domain sku:
    renamedFrom: code
    type: text
    check: length(VALUE) > 0
    constraintName: code_check
  sequence ticker: {renamedFrom: counter}
  function stamp():
    renamedFrom: touch
    returns: trigger
    language: plpgsql
    body: begin new.note := coalesce(new.note, 'touched'); return new; end
  function double_it(x int):
    renamedFrom: twice
    returns: int
    language: sql
    immutable: true
    body: select x * 2
  procedure reset_ticker():
    renamedFrom: reset_counter
    language: sql
    body: select 1
  table people:
    renamedFrom: owners
    columns:
      id: {type: int}
    primaryKey: [id]
    constraints: {}
  table products:
    renamedFrom: items
    columns:
      id: {type: int}
      person_id: {type: int, nullable: true, renamedFrom: owner_id}
      quantity: {type: int, default: "0", renamedFrom: qty}
      mood: {type: SCH.feeling, nullable: true}
      code: {type: SCH.sku, nullable: true}
      note: {type: text, nullable: true}
      doubled: {type: int, generated: quantity * 2}
    primaryKey: [id]
    indexes:
      products_quantity_idx: {columns: [quantity], where: quantity > 0, renamedFrom: items_qty_idx}
    constraints:
      products_quantity_check: {type: check, expression: quantity >= 0, renamedFrom: items_qty_check}
    foreignKeys:
      products_person_fkey:
        renamedFrom: items_owner_fkey
        columns: [person_id]
        references: {table: SCH.people, columns: [id]}
    triggers:
      items_touch: {timing: before, events: [insert, update of quantity], level: row, procedure: SCH.stamp()}
    rowLevelSecurity: true
    policies:
      items_pos: {using: quantity >= 0}
  view products_view:
    renamedFrom: items_view
    query: select id, quantity from SCH.products
  materialized view products_mv:
    renamedFrom: items_mv
    query: select id, quantity from SCH.products
    indexes:
      products_mv_id: {columns: [id], unique: true, renamedFrom: items_mv_id}
schema MOVED:
  table archive:
    renamedFrom: SCH.archive_old
    columns:
      id: {type: int}
`)
	if _, err := pool.Exec(ctx, `create table "`+sch+`".archive_old (id int); insert into "`+sch+`".archive_old values (7)`); err != nil {
		t.Fatal(err)
	}
	p := plan(v2)
	sql := diff.Render(p)
	for _, bad := range []string{"drop table", "drop function", "drop type", "drop domain", "drop sequence", "drop view", "drop materialized view", "drop index", "drop constraint", "drop trigger", "drop policy", "create table if not exists \"" + sch + "\".\"products\"", "create function", "create type", "add column"} {
		if strings.Contains(sql, bad) {
			t.Errorf("rename plan must not contain %q:\n%s", bad, sql)
		}
	}
	applyPlan(t, pool, p)
	if again := diff.Render(plan(v2)); again != "" {
		t.Errorf("plan after renames not empty:\n%s", again)
	}
	var quantity, person, archived int
	var mood string
	if err := pool.QueryRow(ctx, `select quantity, person_id, mood::text from "`+sch+`".products where id = 1`).Scan(&quantity, &person, &mood); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select id from "`+moved+`".archive`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if quantity != 5 || person != 1 || mood != "so_so" || archived != 7 {
		t.Errorf("data lost across renames: quantity=%d person=%d mood=%s archived=%d", quantity, person, mood, archived)
	}
}
