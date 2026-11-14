package schema

import "testing"

func TestCollationParseAndOrder(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  table users:
    columns:
      email: {type: text, collate: app.ci}
  collation ci:
    provider: ICU
    locale: und-u-ks-level2
    deterministic: false
    comment: case-insensitive
`))
	if err != nil {
		t.Fatal(err)
	}
	co := db.Collations["app.ci"]
	if co == nil || co.Provider != "icu" || co.Locale != "und-u-ks-level2" || co.Deterministic == nil || *co.Deterministic || co.Comment != "case-insensitive" {
		t.Fatalf("unexpected collation: %+v", co)
	}
	sorted, err := TopologicalSort(db)
	if err != nil {
		t.Fatal(err)
	}
	if sorted[0].Kind != "collation" {
		t.Errorf("collations sort before tables: %v", sorted)
	}
	if got := ResolveDependency("collation app.ci", db); got != "app.ci" {
		t.Errorf("dependsOn collation resolved to %q", got)
	}
}
