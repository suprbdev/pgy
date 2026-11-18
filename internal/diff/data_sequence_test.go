package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func sequenceDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{
		"app.t": {Name: "t", ColumnOrder: []string{"id", "name"},
			Columns:    map[string]*schema.Column{"id": {Type: "serial"}, "name": {Type: "text"}},
			PrimaryKey: []string{"id"},
			Data:       &schema.TableData{Rows: []map[string]any{{"id": 1, "name": "a"}, {"id": 2, "name": "b"}}}},
		"app.always": {Name: "always", ColumnOrder: []string{"id", "name"},
			Columns:    map[string]*schema.Column{"id": {Type: "bigint", Identity: "always"}, "name": {Type: "text"}},
			PrimaryKey: []string{"id"},
			Data:       &schema.TableData{Rows: []map[string]any{{"id": 7, "name": "a"}}}},
		"app.shared": {Name: "shared", ColumnOrder: []string{"id", "code"},
			Columns:    map[string]*schema.Column{"id": {Type: "integer", Default: "nextval('app.ids'::regclass)"}, "code": {Type: "integer"}},
			PrimaryKey: []string{"code"},
			Data:       &schema.TableData{Rows: []map[string]any{{"id": 3, "code": 10}, {"code": 11}}}},
	}}
}

const sequenceGuardSQL = ` join pg_sequence q on q.seqrelid = c.seq::regclass where q.seqincrement > 0 and case when has_sequence_privilege(q.seqrelid, 'update') and has_sequence_privilege(q.seqrelid, 'select, usage') then c.top between coalesce(pg_sequence_last_value(q.seqrelid) + q.seqincrement, q.seqstart) and q.seqmax end;`

func TestDataSequenceAdvancedAfterInserts(t *testing.T) {
	p := Plan(emptyLive(), sequenceDB(), false)
	sql := Render(p)
	for _, want := range []string{
		`select setval(q.seqrelid, c.top) from (values (pg_get_serial_sequence('"app"."t"', 'id'), (select max("id") from "app"."t"))) c(seq, top)` + sequenceGuardSQL,
		`select setval(q.seqrelid, c.top) from (values (pg_get_serial_sequence('"app"."always"', 'id'), (select max("id") from "app"."always"))) c(seq, top)` + sequenceGuardSQL,
		`select setval(q.seqrelid, c.top) from (values ('app.ids', (select max("id") from "app"."shared"))) c(seq, top)` + sequenceGuardSQL,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %s in\n%s", want, sql)
		}
	}
	if insert, advance := strings.Index(sql, `insert into "app"."t" ("id", "name") values (2, 'b');`), strings.Index(sql, `pg_get_serial_sequence('"app"."t"'`); insert < 0 || advance < insert {
		t.Errorf("the sequence is advanced after the rows are inserted:\n%s", sql)
	}
	if strings.Contains(sql, `'code'`) {
		t.Errorf("a column no sequence numbers is left alone:\n%s", sql)
	}
	if !strings.Contains(sql, `insert into "app"."always" ("id", "name") overriding system value values (7, 'a');`) {
		t.Errorf("rows of an always identity column override it:\n%s", sql)
	}
}

func TestDataSequenceOnlyWhenRowsAreWritten(t *testing.T) {
	live := Simulate(emptyLive(), sequenceDB())
	if sql := Render(PlanWith(live, sequenceDB(), Options{Unsafe: true, Prune: true})); strings.Contains(sql, "setval(q.seqrelid") {
		t.Fatalf("rows in sync and sequences past them advance nothing, got:\n%s", sql)
	}
	live.TableData["app.t"] = &LiveTableData{Changed: map[int][]string{0: {"name"}}}
	if sql := Render(Plan(live, sequenceDB(), false)); strings.Contains(sql, "setval(q.seqrelid") {
		t.Errorf("an update that leaves the numbered column alone doesn't advance its sequence:\n%s", sql)
	}
	for name, ld := range map[string]*LiveTableData{
		"a missing row":     {Missing: []int{1}},
		"a changed id":      {Changed: map[int][]string{1: {"id"}}},
		"a sequence behind": {SequenceBehind: true},
		"no introspection":  nil,
	} {
		live.TableData["app.t"] = ld
		if ld == nil {
			delete(live.TableData, "app.t")
		}
		if sql := Render(Plan(live, sequenceDB(), false)); !strings.Contains(sql, `select setval(q.seqrelid, c.top) from (values (pg_get_serial_sequence('"app"."t"', 'id')`) {
			t.Errorf("%s advances the sequence, got:\n%s", name, sql)
		}
	}
}

func TestDataSequenceOfUndeclaredColumns(t *testing.T) {
	d := &schema.Database{Extensions: []*schema.Extension{{Name: "postgis_topology"}}, Tables: map[string]*schema.Table{
		"topology.topology": {Name: "topology.topology", Data: &schema.TableData{Key: []string{"id"}, Rows: []map[string]any{{"id": 1, "name": "city", "srid": 4326}, {"id": "2", "name": "town", "srid": 4326}}}},
	}}
	sql := Render(Plan(emptyLive(), d, false))
	if want := `select setval(q.seqrelid, c.top) from (values (pg_get_serial_sequence('"topology"."topology"', 'id'), 2), (pg_get_serial_sequence('"topology"."topology"', 'srid'), 4326)) c(seq, top)` + sequenceGuardSQL; !strings.Contains(sql, want) {
		t.Errorf("a table an extension of the plan creates has every integer column checked for a sequence, want %s in:\n%s", want, sql)
	}

	live := emptyLive()
	lt := newLiveTable()
	lt.Columns = map[string]*LiveColumn{
		"id":   {Type: "integer", Default: "nextval('topology.topology_id_seq'::regclass)"},
		"name": {Type: "character varying"},
		"srid": {Type: "integer"},
	}
	live.Tables["topology.topology"] = lt
	live.TableData = map[string]*LiveTableData{"topology.topology": {Missing: []int{1}}}
	sql = Render(Plan(live, d, false))
	if want := `select setval(q.seqrelid, c.top) from (values ('topology.topology_id_seq', (select max("id") from "topology"."topology"))) c(seq, top)` + sequenceGuardSQL; !strings.Contains(sql, want) {
		t.Errorf("an existing table's live defaults name the sequence, want %s in:\n%s", want, sql)
	}
}

func TestDataSequenceOfInheritedColumn(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{
		"app.events": {Name: "events", Columns: map[string]*schema.Column{"id": {Type: "bigserial"}, "at": {Type: "date"}}, PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"at"}}},
		"app.events_2026": {Name: "events_2026", PartitionOf: "app.events", Partition: &schema.PartitionSpec{From: []string{"2026-01-01"}, To: []string{"2027-01-01"}, Modulus: -1, Remainder: -1},
			Data: &schema.TableData{Key: []string{"id", "at"}, Rows: []map[string]any{{"id": 5, "at": "2026-02-01"}}}},
	}}
	sql := Render(Plan(emptyLive(), d, false))
	if want := `(values (pg_get_serial_sequence('"app"."events"', 'id'), (select max("id") from "app"."events_2026")))`; !strings.Contains(sql, want) {
		t.Errorf("a partition's column is numbered by its parent's sequence, want %s in:\n%s", want, sql)
	}
}

func TestNextvalSequence(t *testing.T) {
	for in, want := range map[string]string{
		"nextval('app.ids'::regclass)":  "app.ids",
		"nextval('ids')":                "ids",
		`nextval('"Odd"''s'::regclass)`: `"Odd"'s`,
		"NEXTVAL ( 'x' :: regclass )":   "x",
		"nextval('a') + 1":              "",
		"now()":                         "",
	} {
		got, ok := nextvalSequence(in)
		if ok != (want != "") || got != want {
			t.Errorf("nextvalSequence(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestDataInteger(t *testing.T) {
	for _, c := range []struct {
		in   any
		want int64
		ok   bool
	}{
		{1, 1, true}, {int64(-4), -4, true}, {uint64(9), 9, true}, {uint64(1 << 63), 0, false},
		{"9007199254740993", 9007199254740993, true}, {"12a", 0, false}, {1.5, 0, false}, {nil, 0, false},
	} {
		if got, ok := dataInteger(c.in); got != c.want || ok != c.ok {
			t.Errorf("dataInteger(%v) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
