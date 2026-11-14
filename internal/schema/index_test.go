package schema

import "testing"

func TestIndexOptionsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema public:
  table t:
    columns:
      a: {type: int}
      b: {type: text}
    indexes:
      t_a:
        columns: [a]
        unique: true
        include: [b]
        nullsNotDistinct: true
        with: {fillfactor: 70, deduplicate_items: off}
`))
	if err != nil {
		t.Fatal(err)
	}
	ix := db.Tables["public.t"].Indexes[0]
	if len(ix.Include) != 1 || ix.Include[0] != "b" || !ix.NullsNotDistinct {
		t.Errorf("unexpected index options: %+v", ix)
	}
	if ix.With["fillfactor"] != "70" || ix.With["deduplicate_items"] != "off" {
		t.Errorf("unexpected with: %v", ix.With)
	}
}
