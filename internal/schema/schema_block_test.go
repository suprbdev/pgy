package schema

import (
	"reflect"
	"testing"
)

func TestSchemaBlockParseEmpty(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema reports: {}
schema "Odd Name":
schema app:
  grants:
    reader: [usage]
schemas:
  archive: {}
tables:
  t:
    columns:
      id: {type: int}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"reports": true, "Odd Name": true, "app": true, "archive": true}
	if !reflect.DeepEqual(db.Schemas, want) {
		t.Errorf("Schemas = %v, want %v", db.Schemas, want)
	}
	if len(db.Tables) != 1 || db.Tables["public.t"] == nil {
		t.Errorf("tables: %v", db.Tables)
	}
}

func TestSchemaBlockMergeAndDeclaredSchemas(t *testing.T) {
	a, err := parseFlexibleDatabase([]byte("schema reports: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := &Database{
		SchemaComments: map[string]string{"audit": "x"},
		SchemaOwners:   map[string]string{"billing": "o"},
		SchemaGrants:   map[string]map[string][]string{"reports": {"r": {"usage"}}},
	}
	merged := Merge([]*Database{a, b})
	if !merged.Schemas["reports"] {
		t.Errorf("Merge keeps the declared schemas: %v", merged.Schemas)
	}
	if got := merged.DeclaredSchemas(); !reflect.DeepEqual(got, []string{"audit", "billing", "reports"}) {
		t.Errorf("DeclaredSchemas = %v", got)
	}
	if got := (*Database)(nil).DeclaredSchemas(); got != nil {
		t.Errorf("nil database: %v", got)
	}
}
