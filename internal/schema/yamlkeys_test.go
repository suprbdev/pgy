package schema

import (
	"reflect"
	"testing"
)

func TestDecodeYAMLKeysAreSourceText(t *testing.T) {
	var got map[string]any
	if _, err := DecodeYAML([]byte(`
null: a
~: b
2024: c
0x10: d
1_000: e
1.50: f
true: g
2024-01-01: h
"quoted": i
plain: {7: {NULL: j}}
list:
  - {8: k}
`), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"null": "a", "~": "b", "2024": "c", "0x10": "d", "1_000": "e", "1.50": "f", "true": "g",
		"2024-01-01": "h", "quoted": "i",
		"plain": map[string]any{"7": map[string]any{"NULL": "j"}},
		"list":  []any{map[string]any{"8": "k"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func TestDecodeYAMLKeepsValuesAndMerges(t *testing.T) {
	var got map[string]any
	if _, err := DecodeYAML([]byte(`
base: &base {n: 1, on: true, none: null}
derived:
  <<: *base
  n: 2
`), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"base":    map[string]any{"n": 1, "on": true, "none": nil},
		"derived": map[string]any{"n": 2, "on": true, "none": nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func TestDecodeYAMLEmptyDocument(t *testing.T) {
	var got map[string]any
	if _, err := DecodeYAML([]byte("# nothing here\n"), &got); err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("got %#v, want nil", got)
	}
}

func TestNonStringKeysParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
database:
  set: {1: x}
servers:
  files:
    wrapper: file_fdw
    options: {null: "N"}
schema f:
  grants: {2024: [usage]}
  foreign table csv:
    server: files
    options: {filename: /tmp/a.csv, format: csv, null: "NULL"}
    columns:
      a: {type: text, options: {1: one}}
      null: {type: text}
  table pivot:
    columns:
      id: {type: integer}
      2024: {type: numeric, grants: {7: [select]}}
      true: {type: boolean}
      null: {type: text}
      1.50: {type: text}
      ~: {type: text}
    primaryKey: [id, 2024]
    grants: {2025: [select]}
    indexes:
      pivot_idx:
        columns: [2024, true]
        include: [1.5]
    constraints:
      pivot_u:
        type: unique
        columns: [2024, null]
    data:
      key: [id]
      rows:
        - {id: 1, 2024: 1.5, null: n, true: false}
  type comp:
    type: composite
    attributes:
      1: integer
      null: text
  type e:
    type: enum
    labels: [1, 2, a, null]
    renamedLabels: {2: 3}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := db.DatabaseSettings["1"]; got != "x" {
		t.Errorf("database setting 1 = %q", got)
	}
	if got := db.Servers["files"].Options; !reflect.DeepEqual(got, map[string]string{"null": "N"}) {
		t.Errorf("server options = %v", got)
	}
	if got := db.SchemaGrants["f"]; !reflect.DeepEqual(got, map[string][]string{"2024": {"usage"}}) {
		t.Errorf("schema grants = %v", got)
	}
	csv := db.Tables["f.csv"]
	if got := csv.Foreign.Options; !reflect.DeepEqual(got, map[string]string{"filename": "/tmp/a.csv", "format": "csv", "null": "NULL"}) {
		t.Errorf("foreign table options = %v", got)
	}
	if got := csv.Columns["a"].FDWOptions; !reflect.DeepEqual(got, map[string]string{"1": "one"}) {
		t.Errorf("foreign column options = %v", got)
	}
	if csv.Columns["null"] == nil {
		t.Errorf("foreign column null missing: %v", csv.Columns)
	}
	pivot := db.Tables["f.pivot"]
	if got, want := pivot.ColumnOrder, []string{"id", "2024", "true", "null", "1.50", "~"}; !reflect.DeepEqual(got, want) {
		t.Errorf("column order = %v, want %v", got, want)
	}
	for _, name := range []string{"id", "2024", "true", "null", "1.50", "~"} {
		if pivot.Columns[name] == nil {
			t.Errorf("column %q missing", name)
		}
	}
	if len(pivot.Columns) != 6 {
		t.Errorf("columns = %v", pivot.Columns)
	}
	if got := pivot.Columns["2024"].Grants; !reflect.DeepEqual(got, map[string][]string{"7": {"select"}}) {
		t.Errorf("column grants = %v", got)
	}
	if got := pivot.Grants; !reflect.DeepEqual(got, map[string][]string{"2025": {"select"}}) {
		t.Errorf("table grants = %v", got)
	}
	if got, want := pivot.PrimaryKey, []string{"id", "2024"}; !reflect.DeepEqual(got, want) {
		t.Errorf("primary key = %v, want %v", got, want)
	}
	if got, want := pivot.Indexes[0].Columns, []string{"2024", "true"}; !reflect.DeepEqual(got, want) {
		t.Errorf("index columns = %v, want %v", got, want)
	}
	if got, want := pivot.Indexes[0].Include, []string{"1.5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("index include = %v, want %v", got, want)
	}
	if got, want := pivot.Constraints[0].Columns, []string{"2024", "null"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unique columns = %v, want %v", got, want)
	}
	row := pivot.Data.Rows[0]
	if _, ok := row["2024"]; !ok || len(row) != 4 {
		t.Errorf("data row = %v", row)
	}
	if _, ok := row["null"]; !ok {
		t.Errorf("data row lacks the null column: %v", row)
	}
	comp := db.Types["f.comp"]
	if got := comp.Attributes; !reflect.DeepEqual(got, map[string]string{"1": "integer", "null": "text"}) {
		t.Errorf("attributes = %v", got)
	}
	if got, want := comp.AttributeOrder, []string{"1", "null"}; !reflect.DeepEqual(got, want) {
		t.Errorf("attribute order = %v, want %v", got, want)
	}
	enum := db.Types["f.e"]
	if got, want := enum.Labels, []string{"1", "2", "a", "null"}; !reflect.DeepEqual(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}
	if got := enum.RenamedLabels; !reflect.DeepEqual(got, map[string]string{"2": "3"}) {
		t.Errorf("renamed labels = %v", got)
	}
}

func TestListFormNumericNames(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
tables:
  - name: 2024
    columns:
      - {name: 1, type: integer}
      - {name: v, type: text}
`))
	if err != nil {
		t.Fatal(err)
	}
	tbl := db.Tables["public.2024"]
	if tbl == nil {
		t.Fatalf("tables = %v", db.Tables)
	}
	if tbl.Columns["1"] == nil || tbl.Columns["v"] == nil {
		t.Errorf("columns = %v", tbl.Columns)
	}
}
