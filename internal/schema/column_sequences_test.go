package schema

import (
	"reflect"
	"strings"
	"testing"
)

func TestColumnSequences(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  table t:
    columns:
      id: {type: bigint, identity: always}
      named: {type: bigint, identity: {generated: by default, sequenceName: named_ids}}
      elsewhere: {type: int, identity: {generated: always, sequenceName: other.ids}}
      s: {type: bigserial}
      n: {type: int}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]SequenceColumn{
		"app.t_id_seq":  {"app.t", "id"},
		"app.named_ids": {"app.t", "named"},
		"other.ids":     {"app.t", "elsewhere"},
		"app.t_s_seq":   {"app.t", "s"},
	}
	if got := db.ColumnSequences(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestColumnSequenceName(t *testing.T) {
	long := strings.Repeat("t", 40)
	for _, c := range []struct{ table, column, want string }{
		{"t", "id", "t_id_seq"},
		{long, "id", long + "_id_seq"},
		{long, strings.Repeat("c", 40), strings.Repeat("t", 29) + "_" + strings.Repeat("c", 29) + "_seq"},
		{strings.Repeat("x", 70), "id", strings.Repeat("x", 56) + "_id_seq"},
		{strings.Repeat("x", 55) + "é" + strings.Repeat("x", 10), "id", strings.Repeat("x", 55) + "_id_seq"},
	} {
		if got := ColumnSequenceName(c.table, c.column); got != c.want || len(got) > 63 {
			t.Errorf("ColumnSequenceName(%q, %q) = %q, want %q", c.table, c.column, got, c.want)
		}
	}
}
