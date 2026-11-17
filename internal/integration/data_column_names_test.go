package integration

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

const dataColumnNames = `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      v: {type: int, nullable: true}
      i: {type: int, nullable: true}
      j: {type: text, nullable: true}
      k: {type: text, nullable: true}
      first: {type: int, nullable: true}
      pgy_rec: {type: int, nullable: true}
      pgy_row: {type: int, nullable: true}
    data:
      rows:
        - {id: 1, v: 10, i: 1, j: a, k: b, first: 0, pgy_rec: 5, pgy_row: 6}
        - {id: 2, v: V2}
`

// TestIntegrationDataColumnNames declares rows for a table whose columns are
// named like the aliases of the queries that compare rows (v, i, j, k,
// first, pgy_rec, pgy_row): the rows are written, changed and pruned like
// those of any other table.
func TestIntegrationDataColumnNames(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	opts := diff.Options{Unsafe: true, Prune: true}
	convergeYAMLWith(t, pool, sch, strings.ReplaceAll(dataColumnNames, "V2", "20"), opts)
	if _, err := pool.Exec(context.Background(), fmt.Sprintf(`insert into %q.t (id, v) values (3, 30)`, sch)); err != nil {
		t.Fatal(err)
	}
	applied := convergeYAMLWith(t, pool, sch, strings.ReplaceAll(dataColumnNames, "V2", "21"), opts)
	for _, want := range []string{
		fmt.Sprintf(`update only "%s"."t" set "v" = 21 where "id" = 2;`, sch),
		fmt.Sprintf(`delete from only "%s"."t" where "id" = '3';`, sch),
	} {
		if !strings.Contains(applied, want) {
			t.Errorf("want %q, got:\n%s", want, applied)
		}
	}
	rows, err := diff.TableRows(context.Background(), pool, sch+".t", false)
	if err != nil {
		t.Fatalf("pull --data reads the rows: %v", err)
	}
	got := []string{}
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%v %v %v", r["id"], r["v"], r["pgy_row"]))
	}
	sort.Strings(got)
	if strings.Join(got, ", ") != "1 10 6, 2 21 <nil>" {
		t.Errorf("rows = %v", rows)
	}
}

// TestIntegrationDataPullColumnPgyRow pulls the rows of a table with a column
// named pgy_row and one whose values are read back as text (hstore).
func TestIntegrationDataPullColumnPgyRow(t *testing.T) {
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_pull_pgy_row")
	ctx := context.Background()
	if _, err := db.Exec(ctx, `create extension hstore`); err != nil {
		t.Skipf("hstore is not available: %v", err)
	}
	if _, err := db.Exec(ctx, `
		create table t (id int primary key, pgy_row int, h hstore);
		insert into t values (1, 7, 'a=>1');`); err != nil {
		t.Fatal(err)
	}
	rows, err := diff.TableRows(ctx, db, "public.t", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || fmt.Sprintf("%v %v %v", rows[0]["id"], rows[0]["pgy_row"], rows[0]["h"]) != `1 7 "a"=>"1"` {
		t.Errorf("rows = %v", rows)
	}
}
