package schema

import "testing"

func TestTableStorageOptionsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema public:
  table t:
    unlogged: true
    with: {fillfactor: 70, autovacuum_enabled: false}
    columns:
      a: {type: text, collate: C, compression: LZ4, storage: External}
`))
	if err != nil {
		t.Fatal(err)
	}
	tbl := db.Tables["public.t"]
	if tbl.Unlogged == nil || !*tbl.Unlogged || tbl.With["fillfactor"] != "70" || tbl.With["autovacuum_enabled"] != "false" {
		t.Errorf("unexpected table options: %+v %v", tbl.Unlogged, tbl.With)
	}
	c := tbl.Columns["a"]
	if c.Collate != "C" || c.Compression != "lz4" || c.Storage != "external" {
		t.Errorf("unexpected column options: %+v", c)
	}
}
