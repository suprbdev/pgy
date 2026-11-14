package schema

import "testing"

func TestTextSearchParseAndOrder(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  table docs:
    columns:
      body: {type: text}
  text search configuration english_custom:
    copy: pg_catalog.english
    mappings:
      AsciiWord: [app.nostop]
      email: []
  text search dictionary nostop:
    template: snowball
    options: {language: english}
`))
	if err != nil {
		t.Fatal(err)
	}
	tc := db.TSConfigs["app.english_custom"]
	if tc == nil || tc.Copy != "pg_catalog.english" || len(tc.Mappings["asciiword"]) != 1 || tc.Mappings["email"] == nil || len(tc.Mappings["email"]) != 0 {
		t.Fatalf("unexpected config: %+v", tc)
	}
	if td := db.TSDictionaries["app.nostop"]; td == nil || td.Template != "snowball" || td.Options["language"] != "english" {
		t.Fatalf("unexpected dictionary: %+v", td)
	}
	sorted, err := TopologicalSort(db)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{}
	for _, e := range sorted {
		order = append(order, e.Kind)
	}
	if len(order) != 3 || order[0] != "tsdictionary" || order[1] != "tsconfig" || order[2] != "table" {
		t.Errorf("want dictionary, configuration, table; got %v", order)
	}
}
