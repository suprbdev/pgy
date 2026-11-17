package schema

import (
	"reflect"
	"testing"
)

func TestStorageParameterOrderParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  table t:
    columns:
      id: {type: int}
      k: {type: int}
    with: {toast_tuple_target: 4000, fillfactor: 90, autovacuum_enabled: false}
    primaryKey:
      t_pkey: {columns: [id], with: {fillfactor: 80, deduplicate_items: off}}
    constraints:
      t_k_key: {type: unique, columns: [k], with: {fillfactor: 70, deduplicate_items: off}}
    indexes:
      t_k: {columns: [k], with: {fillfactor: 50, deduplicate_items: off}}
      t_plain: {columns: [id]}
  materialized view mv:
    query: select 1 as id
    with: {fillfactor: 60, autovacuum_enabled: false}
    indexes:
      mv_id: {columns: [id], with: {fillfactor: 40, deduplicate_items: off}}
`))
	if err != nil {
		t.Fatal(err)
	}
	tbl, mv := db.Tables["app.t"], db.Views["app.mv"]
	index := func(ixs []*Index, name string) *Index {
		for _, ix := range ixs {
			if ix.Name == name {
				return ix
			}
		}
		t.Fatalf("no index %s", name)
		return nil
	}
	for what, c := range map[string]struct{ got, want []string }{
		"table":                {tbl.WithOrder, []string{"toast_tuple_target", "fillfactor", "autovacuum_enabled"}},
		"primary key":          {tbl.PrimaryKeyWithOrder, []string{"fillfactor", "deduplicate_items"}},
		"unique constraint":    {tbl.Constraints[0].WithOrder, []string{"fillfactor", "deduplicate_items"}},
		"index":                {index(tbl.Indexes, "t_k").WithOrder, []string{"fillfactor", "deduplicate_items"}},
		"index without with":   {index(tbl.Indexes, "t_plain").WithOrder, nil},
		"materialized view":    {mv.WithOrder, []string{"fillfactor", "autovacuum_enabled"}},
		"materialized view ix": {index(mv.Indexes, "mv_id").WithOrder, []string{"fillfactor", "deduplicate_items"}},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: with order %v, want %v", what, c.got, c.want)
		}
	}
}
