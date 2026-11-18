package integration

import (
	"context"
	"strings"
	"testing"
)

const dataSequenceYAML = `
schema SCH:
  sequence ids: {}
  table t:
    columns:
      id: {type: serial, primaryKey: true}
      name: {type: text}
    data:
      rows:
        - {id: 1, name: a}
        - {id: 2, name: b}
        EXTRA
  table always:
    columns:
      id: {type: bigint, identity: always, primaryKey: true}
      name: {type: text}
    data:
      rows: [{id: 5, name: a}]
  table bydefault:
    columns:
      id: {type: int, identity: byDefault, primaryKey: true}
      name: {type: text}
    data:
      rows: [{id: 3, name: a}]
  table left:
    columns:
      id: {type: int, default: "nextval('SCH.ids'::regclass)", primaryKey: true}
    data:
      rows: [{id: 4}]
  table right:
    columns:
      id: {type: int, default: "nextval('SCH.ids'::regclass)", primaryKey: true}
    data:
      rows: [{id: 9}]
`

// TestIntegrationDataAdvancesSequences inserts data rows with explicit
// values for serial, identity and nextval columns, two of which share a
// sequence: afterwards each sequence hands out the value after the largest
// row, a row added later moves it on, and one the application has moved
// past the rows is never moved back.
func TestIntegrationDataAdvancesSequences(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	sql := convergeYAML(t, pool, sch, strings.Replace(dataSequenceYAML, "EXTRA", "", 1))
	if n := strings.Count(sql, "select setval(q.seqrelid"); n != 5 {
		t.Errorf("want 5 sequence statements, got %d:\n%s", n, sql)
	}
	next := func(table string) int64 {
		t.Helper()
		var id int64
		col := "(name) values ('x')"
		if table == "left" || table == "right" {
			col = "default values"
		}
		if err := pool.QueryRow(ctx, `insert into "`+sch+`"."`+table+`" `+col+` returning id`).Scan(&id); err != nil {
			t.Fatalf("insert into %s: %v", table, err)
		}
		return id
	}
	for table, want := range map[string]int64{"t": 3, "always": 6, "bydefault": 4, "left": 10} {
		if got := next(table); got != want {
			t.Errorf("next id of %s = %d, want %d", table, got, want)
		}
	}
	if _, err := pool.Exec(ctx, `delete from "`+sch+`".t where id = 3; delete from "`+sch+`".always where id = 6; delete from "`+sch+`".bydefault where id = 4; delete from "`+sch+`"."left" where id = 10`); err != nil {
		t.Fatal(err)
	}
	sql = convergeYAML(t, pool, sch, strings.Replace(dataSequenceYAML, "EXTRA", "- {id: 7, name: c}", 1))
	if !strings.Contains(sql, `values (7, 'c')`) || !strings.Contains(sql, `pg_get_serial_sequence('"`+sch+`"."t"', 'id')`) {
		t.Errorf("a row added later inserts and advances the sequence:\n%s", sql)
	}
	if got := next("t"); got != 8 {
		t.Errorf("after row 7, next id of t = %d, want 8", got)
	}
	if _, err := pool.Exec(ctx, `delete from "`+sch+`".t where id = 8; select setval('"`+sch+`".t_id_seq', 100)`); err != nil {
		t.Fatal(err)
	}
	convergeYAML(t, pool, sch, strings.Replace(dataSequenceYAML, "EXTRA", "- {id: 7, name: c}\n        - {id: 50, name: d}", 1))
	if got := next("t"); got != 101 {
		t.Errorf("a sequence past the rows is never moved back: next id = %d, want 101", got)
	}
}

// TestIntegrationDataSequenceBehindRowsIsAdvanced adopts rows inserted with
// explicit ids, as older releases inserted data: rows, without advancing
// the sequence: the plan advances it, once.
func TestIntegrationDataSequenceBehindRowsIsAdvanced(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	if _, err := pool.Exec(ctx, `create table "`+sch+`".t (id serial primary key, name text not null); insert into "`+sch+`".t values (1, 'a'), (2, 'b')`); err != nil {
		t.Fatal(err)
	}
	sql := convergeYAML(t, pool, sch, `
schema SCH:
  table t:
    columns:
      id: {type: serial, primaryKey: true}
      name: {type: text}
    data:
      rows: [{id: 1, name: a}, {id: 2, name: b}]
`)
	if want := `select setval(q.seqrelid, c.top) from (values (pg_get_serial_sequence('"` + sch + `"."t"', 'id'), (select max("id") from "` + sch + `"."t")))`; !strings.Contains(sql, want) || strings.Contains(sql, "insert") {
		t.Errorf("want only the sequence advanced, got:\n%s", sql)
	}
	var id int
	if err := pool.QueryRow(ctx, `insert into "`+sch+`".t (name) values ('c') returning id`).Scan(&id); err != nil || id != 3 {
		t.Errorf("next id = %d (%v), want 3", id, err)
	}
}
