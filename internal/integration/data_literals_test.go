package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

const dataArrays = `
schema SCH:
  domain code:
    type: "varchar(3)"
  table t:
    dependsOn: [domain SCH.code]
    columns:
      id: {type: int, primaryKey: true}
      boxes: {type: "box[]", nullable: true}
      grid: {type: "int[][]", nullable: true}
      codes: {type: "SCH.code[]", nullable: true}
      fixed: {type: "char(2)[]", nullable: true}
      prices: {type: "numeric(6,2)[]", nullable: true}
      short: {type: "varchar(3)[]", nullable: true}
    data:
      rows:
        - {id: 1, boxes: ["(1,1),(0,0)", "(3,3),(2,2)"], grid: [[1, null], [3, 4]], codes: [abc, null], fixed: [ab, c], prices: [1.005, "2"]}
        - {id: 2, boxes: BOXES2, grid: [], codes: [x], short: [xyz]}
`

const dataKeysWithTypmods = `
schema SCH:
  domain money2:
    type: "numeric(6,2)"
  table price:
    dependsOn: [domain SCH.money2]
    columns:
      amount: {type: "numeric(6,2)"}
      at: {type: "timestamp(0)"}
      span: {type: "interval day"}
      cost: {type: SCH.money2}
      label: {type: text}
    primaryKey: [amount, at, span, cost]
    data:
      rows:
        - {amount: "1.005", at: "2026-01-01 10:00:00.6", span: "1 day 3 hours", cost: 2.345, label: LABEL}
`

// TestIntegrationDataKeysWithTypmods declares keys whose value the column's
// type modifier changes (numeric(6,2) stores 1.005 as 1.01, timestamp(0)
// rounds the seconds, interval day drops the hours, and so does a domain over
// numeric(6,2)): a changed row is updated once and the next diff is clean,
// and an undeclared row is deleted.
func TestIntegrationDataKeysWithTypmods(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	opts := diff.Options{Unsafe: true, Prune: true}
	convergeYAMLWith(t, pool, sch, strings.ReplaceAll(dataKeysWithTypmods, "LABEL", "one"), opts)
	if _, err := pool.Exec(context.Background(), fmt.Sprintf(`insert into %q.price values (9.99, '2026-01-02', '2 days', 1, 'old')`, sch)); err != nil {
		t.Fatal(err)
	}
	applied := convergeYAMLWith(t, pool, sch, strings.ReplaceAll(dataKeysWithTypmods, "LABEL", "uno"), opts)
	for _, want := range []string{
		`set "label" = 'uno' where "amount" = '1.005'::numeric(6,2) and "at" = '2026-01-01 10:00:00.6'::timestamp(0) without time zone and "span" = '1 day 3 hours'::interval day and "cost" = 2.345::numeric(6,2);`,
		`delete from only "` + sch + `"."price" where "amount" = '9.99'::numeric(6,2)`,
	} {
		if !strings.Contains(applied, want) {
			t.Errorf("want %q, got:\n%s", want, applied)
		}
	}
	if got := rowTexts(t, pool, fmt.Sprintf(`select concat_ws(' ', amount, at, span, cost, label) from %q.price`, sch)); len(got) != 1 || got[0] != "1.01 2026-01-01 10:00:01 1 day 2.35 uno" {
		t.Errorf("rows = %q", got)
	}
}

const dataTypeChange = `
schema SCH:
  table t:
    columns:
      id: {type: T_ID}
      code: {type: T_CODE}
      price: {type: T_PRICE}
    primaryKey: [id]
    data:
      rows:
        - {id: 1, code: V_CODE, price: V_PRICE}
`

// TestIntegrationDataTypeChange changes column types in the same plan as
// data rows that only fit the new types: the rows are compared in the
// declared types and written after the change, and a changed key column
// type gets an upsert.
func TestIntegrationDataTypeChange(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	opts := diff.Options{Unsafe: true, Prune: true}
	yaml := func(pairs ...string) string { return strings.NewReplacer(pairs...).Replace(dataTypeChange) }
	convergeYAMLWith(t, pool, sch, yaml("T_ID", "int", "T_CODE", "varchar(3)", "T_PRICE", `"numeric(6,2)"`, "V_CODE", "abc", "V_PRICE", `"1.25"`), opts)
	applied := convergeYAMLWith(t, pool, sch, yaml("T_ID", "int", "T_CODE", "varchar(5)", "T_PRICE", `"numeric(8,3)"`, "V_CODE", "abcde", "V_PRICE", `"1.0005"`), opts)
	if !strings.Contains(applied, `update only "`+sch+`"."t" set "code" = 'abcde', "price" = '1.0005' where "id" = 1;`) {
		t.Errorf("want an update after the type change, got:\n%s", applied)
	}
	applied = convergeYAMLWith(t, pool, sch, yaml("T_ID", "bigint", "T_CODE", "varchar(5)", "T_PRICE", `"numeric(8,3)"`, "V_CODE", "xyz", "V_PRICE", `"2"`), opts)
	if !strings.Contains(applied, `on conflict ("id") do update set "code" = excluded."code", "price" = excluded."price";`) {
		t.Errorf("want an upsert when the key column's type changes, got:\n%s", applied)
	}
	if got := rowTexts(t, pool, fmt.Sprintf(`select concat_ws(' ', id, code, price) from %q.t`, sch)); len(got) != 1 || got[0] != "1 xyz 2.000" {
		t.Errorf("rows = %q", got)
	}
}

// TestIntegrationDataFollowsMovedExtension moves an extension whose type a
// data table's columns use, the key included: the column types follow the
// extension, so the rows are compared as usual and nothing is written.
func TestIntegrationDataFollowsMovedExtension(t *testing.T) {
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_data_ext_move")
	ctx := context.Background()
	if _, err := db.Exec(ctx, `
		create extension citext schema public;
		create schema app;
		create table app.t (code citext primary key, name citext);
		insert into app.t values ('a', 'A');`); err != nil {
		t.Fatal(err)
	}
	d := loadSpellingYAML(t, `
extensions:
  - {name: citext, schema: ext}
schema app:
  table t:
    columns:
      code: {type: ext.citext, primaryKey: true}
      name: {type: ext.citext, nullable: true}
    data:
      rows:
        - {code: a, name: A}
`)
	plan := func() *diff.PlanDiff {
		live, err := diff.Introspect(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if err := diff.IntrospectExtensions(ctx, db, live, d, diff.Options{}); err != nil {
			t.Fatal(err)
		}
		if err := diff.IntrospectData(ctx, db, live, d); err != nil {
			t.Fatal(err)
		}
		return diff.PlanWith(live, d, diff.Options{Unsafe: true, Prune: true})
	}
	p := plan()
	if sql := strings.TrimSpace(diff.Render(p)); sql != "create schema if not exists \"ext\";\nalter extension \"citext\" set schema \"ext\";" {
		t.Errorf("only the extension moves, got:\n%s", sql)
	}
	applyPlan(t, db, p)
	if sql := diff.Render(plan()); sql != "" {
		t.Errorf("the next diff is clean, got:\n%s", sql)
	}
}

const dataListsForOtherTypes = `
schema SCH:
  domain ints:
    type: "int[]"
    check: "cardinality(VALUE) < 4"
  table t:
    dependsOn: [domain SCH.ints]
    columns:
      id: {type: int, primaryKey: true}
      r: {type: int4range, nullable: true}
      n: {type: SCH.ints, nullable: true}
    data:
      rows:
        - {id: 1, r: [1, 3], n: [1, 2]}
        - {id: 2, r: R2, n: []}
`

// TestIntegrationDataListsForOtherTypes gives lists to columns that are not
// declared as arrays: a type whose input reads the JSON text of the list (a
// range reads [1, 3]) and a domain over an array type. They are written
// through the conversion that compares them.
func TestIntegrationDataListsForOtherTypes(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	opts := diff.Options{Unsafe: true, Prune: true}
	convergeYAMLWith(t, pool, sch, strings.ReplaceAll(dataListsForOtherTypes, "R2", "[2, 4]"), opts)
	convergeYAMLWith(t, pool, sch, strings.ReplaceAll(dataListsForOtherTypes, "R2", "[2, 5]"), opts)
	got := rowTexts(t, pool, fmt.Sprintf(`select concat_ws(' ', id, r, n) from %q.t order by id`, sch))
	if want := []string{"1 [1,4) {1,2}", "2 [2,6) {}"}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows = %q, want %q", got, want)
	}
}

// TestIntegrationDataArrays writes data rows with lists for array columns
// whose element type does not use a comma between elements (box uses ;),
// multidimensional arrays with nulls, arrays of a domain and arrays whose
// element type has a modifier, first into a new table, then as changes.
func TestIntegrationDataArrays(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	opts := diff.Options{Unsafe: true, Prune: true}
	convergeYAMLWith(t, pool, sch, strings.ReplaceAll(dataArrays, "BOXES2", "[]"), opts)
	applied := convergeYAMLWith(t, pool, sch, strings.ReplaceAll(dataArrays, "BOXES2", `["(5,5),(4,4)", null]`), opts)
	if !strings.Contains(applied, `set "boxes" = array['(5,5),(4,4)', null]::box[] where "id" = 2;`) {
		t.Errorf("want an update of the boxes, got:\n%s", applied)
	}
	got := rowTexts(t, pool, fmt.Sprintf(`select concat_ws(' ', id, boxes, grid, codes, fixed, prices, short) from %q.t order by id`, sch))
	want := []string{"1 {(1,1),(0,0);(3,3),(2,2)} {{1,NULL},{3,4}} {abc,NULL} {ab,\"c \"} {1.01,2.00}", "2 {(5,5),(4,4);NULL} {} {x} {xyz}"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows = %q, want %q", got, want)
	}
	tooLong := strings.ReplaceAll(strings.ReplaceAll(dataArrays, "BOXES2", "[]"), "short: [xyz]", "short: [wxyz]")
	if _, err := pool.Exec(context.Background(), fmt.Sprintf("drop table %q.t", sch)); err != nil {
		t.Fatal(err)
	}
	d := loadSpellingYAML(t, strings.ReplaceAll(tooLong, "SCH", sch))
	_, err := applyAsMigrations(context.Background(), pool, diff.PlanWith(introspect(t, pool, d), d, opts))
	if err == nil || !strings.Contains(err.Error(), "value too long for type character varying(3)") {
		t.Errorf("an element too long for the element type must fail as in an array literal, got %v", err)
	}
}
