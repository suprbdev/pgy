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
