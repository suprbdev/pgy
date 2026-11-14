package schema

import "testing"

func TestPublicationsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
publications:
  app_pub:
    tables:
      - users
      - table: billing.invoices
        columns: [id, total]
        where: total > 0
    schemas: [audit]
    publish: [Insert, update]
    viaPartitionRoot: true
    comment: cdc
  all_pub:
    allTables: true
`))
	if err != nil {
		t.Fatal(err)
	}
	p := db.Publications["app_pub"]
	if p == nil || len(p.Tables) != 2 || p.Tables[0].Table != "public.users" || p.Tables[1].Table != "billing.invoices" ||
		len(p.Tables[1].Columns) != 2 || p.Tables[1].Where != "total > 0" || len(p.Schemas) != 1 ||
		len(p.Publish) != 2 || p.Publish[0] != "insert" || p.ViaPartitionRoot == nil || !*p.ViaPartitionRoot || p.Comment != "cdc" {
		t.Errorf("unexpected publication: %+v", p)
	}
	if a := db.Publications["all_pub"]; a == nil || !a.AllTables {
		t.Errorf("unexpected all_pub: %+v", a)
	}
}
