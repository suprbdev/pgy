package schema

import "testing"

func TestViewOptionsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema public:
  view v:
    query: select 1
    securityInvoker: true
    securityBarrier: false
    checkOption: LOCAL
  materialized view mv:
    query: select 1 as id
    withData: false
    indexes:
      mv_id:
        columns: [id]
        unique: true
`))
	if err != nil {
		t.Fatal(err)
	}
	v := db.Views["public.v"]
	if v.SecurityInvoker == nil || !*v.SecurityInvoker || v.SecurityBarrier == nil || *v.SecurityBarrier || v.CheckOption != "local" {
		t.Errorf("unexpected view options: %+v", v)
	}
	mv := db.Views["public.mv"]
	if mv.WithData == nil || *mv.WithData || len(mv.Indexes) != 1 || mv.Indexes[0].Name != "mv_id" || !mv.Indexes[0].Unique {
		t.Errorf("unexpected matview options: %+v", mv)
	}
}

func TestMaterializedViewStorageParamsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema public:
  materialized view mv:
    query: select 1 as id
    with: {fillfactor: 70, toast.autovacuum_enabled: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	w := db.Views["public.mv"].With
	if w["fillfactor"] != "70" || w["toast.autovacuum_enabled"] != "false" {
		t.Errorf("with = %v", w)
	}
}

func TestViewColumnGrantsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  view v:
    query: select 1 as a, 2 as b
    columnGrants:
      a: {reader: [SELECT], editor: [update with grant option, select]}
      b: {}
      c: ~
  materialized view mv:
    columnGrants: {id: {reader: [select]}}
`))
	if err != nil {
		t.Fatal(err)
	}
	cg := db.Views["app.v"].ColumnGrants
	if got := cg["a"]["reader"]; len(got) != 1 || got[0] != "select" {
		t.Errorf("a reader = %v", got)
	}
	if got := cg["a"]["editor"]; len(got) != 2 || got[0] != "select" || got[1] != "update*" {
		t.Errorf("a editor = %v", got)
	}
	if g, ok := cg["b"]; !ok || g == nil || len(g) != 0 {
		t.Errorf("an empty block manages the column with no grants: %v %v", g, ok)
	}
	if _, ok := cg["c"]; ok {
		t.Errorf("a column without a block is unmanaged: %v", cg)
	}
	if got := db.Views["app.mv"].ColumnGrants["id"]["reader"]; len(got) != 1 || got[0] != "select" {
		t.Errorf("materialized view column grants = %v", db.Views["app.mv"].ColumnGrants)
	}
}
