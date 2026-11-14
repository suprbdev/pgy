package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func tsDB() *schema.Database {
	return &schema.Database{
		Tables: map[string]*schema.Table{},
		TSDictionaries: map[string]*schema.TSDictionary{
			"app.english_stem_nostop": {Name: "english_stem_nostop", Template: "snowball", Options: map[string]string{"language": "english"}, Comment: "no stopwords"},
		},
		TSConfigs: map[string]*schema.TSConfig{
			"app.english_custom": {Name: "english_custom", Copy: "pg_catalog.english", Comment: "custom",
				Mappings: map[string][]string{"asciiword": {"app.english_stem_nostop"}, "email": {}}},
		},
	}
}

func TestTextSearchCreate(t *testing.T) {
	p := Plan(emptyLive(), tsDB(), false)
	want := []string{
		`create schema if not exists "app";`,
		`create text search dictionary "app"."english_stem_nostop" (template = snowball, language = english);`,
		`create text search configuration "app"."english_custom" (copy = "pg_catalog"."english");`,
		`alter text search configuration "app"."english_custom" drop mapping if exists for asciiword;`,
		`alter text search configuration "app"."english_custom" add mapping for asciiword with "app"."english_stem_nostop";`,
		`alter text search configuration "app"."english_custom" drop mapping if exists for email;`,
	}
	if len(p.Creates) != len(want) {
		t.Fatalf("want %v, got %v", want, p.Creates)
	}
	for i := range want {
		if p.Creates[i] != want[i] {
			t.Errorf("creates[%d] = %q, want %q", i, p.Creates[i], want[i])
		}
	}
	for _, c := range []string{`comment on text search dictionary "app"."english_stem_nostop" is 'no stopwords';`, `comment on text search configuration "app"."english_custom" is 'custom';`} {
		if !findAlter(p, c) {
			t.Errorf("missing %q in %v", c, p.Alters)
		}
	}
}

func TestTextSearchMappingsReconciled(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.TSDictionaries = map[string]bool{"app.english_stem_nostop": true}
	live.TSConfigs = map[string]map[string][]string{"app.english_custom": {"asciiword": {"english_stem_nostop"}, "word": {"english_stem"}}}
	live.TSComments = map[string]string{"dict:app.english_stem_nostop": "no stopwords", "config:app.english_custom": "custom"}
	d := tsDB()
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("matching config must not diff, got:\n%s", sql)
	}
	d.TSConfigs["app.english_custom"].Mappings = map[string][]string{"asciiword": {"simple"}, "word": {}, "email": {"simple"}}
	p := Plan(live, d, false)
	want := []string{
		`alter text search configuration "app"."english_custom" alter mapping for asciiword with "simple";`,
		`alter text search configuration "app"."english_custom" add mapping for email with "simple";`,
		`alter text search configuration "app"."english_custom" drop mapping for word;`,
	}
	if len(p.Creates) != 3 || p.Creates[0] != want[0] || p.Creates[1] != want[1] || p.Creates[2] != want[2] {
		t.Errorf("want %v, got %v", want, p.Creates)
	}
}
