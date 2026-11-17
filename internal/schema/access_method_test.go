package schema

import "testing"

const accessMethodYAML = `
schema app:
  table t:
    columns: {id: {type: int}}
    accessMethod: columnar
  table p:
    columns: {id: {type: int}}
    partitionBy: {type: range, columns: [id]}
    accessMethod: columnar
  table p1:
    partitionOf: app.p
    forValues: {from: [0], to: [10]}
    accessMethod: heap
  materialized view mv:
    query: select 1
    accessMethod: columnar
`

func TestAccessMethodParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(accessMethodYAML))
	if err != nil {
		t.Fatal(err)
	}
	for fq, want := range map[string]string{"app.t": "columnar", "app.p": "columnar", "app.p1": "heap"} {
		if tb := db.Tables[fq]; tb == nil || tb.AccessMethod != want {
			t.Errorf("%s: want access method %s, got %+v", fq, want, tb)
		}
	}
	if mv := db.Views["app.mv"]; mv == nil || mv.AccessMethod != "columnar" {
		t.Errorf("matview access method: %+v", mv)
	}
}

func TestAccessMethodMerge(t *testing.T) {
	a, err := parseFlexibleDatabase([]byte("schema app:\n  table t:\n    columns: {id: {type: int}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseFlexibleDatabase([]byte("schema app:\n  table t:\n    accessMethod: columnar\n"))
	if err != nil {
		t.Fatal(err)
	}
	if tb := Merge([]*Database{a, b}).Tables["app.t"]; tb == nil || tb.AccessMethod != "columnar" {
		t.Errorf("merged table: %+v", tb)
	}
}

func TestJSONSchemaAcceptsAccessMethod(t *testing.T) {
	if err := compileEditorSchema(t).Validate(yamlToJSONValue(t, []byte(accessMethodYAML))); err != nil {
		t.Fatalf("accessMethod must validate: %v", err)
	}
	if err := compileEditorSchema(t).Validate(yamlToJSONValue(t, []byte("schema app:\n  table t:\n    columns: {id: {type: int}}\n    accessMethod: ''\n"))); err == nil {
		t.Error("an empty accessMethod must not validate")
	}
}
