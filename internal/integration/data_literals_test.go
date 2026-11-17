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
