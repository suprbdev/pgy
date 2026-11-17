package diff

import (
	"reflect"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestIndexDefKeys(t *testing.T) {
	cases := map[string][]string{
		`CREATE INDEX zoo_hash ON app.zoo USING hash ((ip::text)) WHERE flag = true`:                                                          {`(ip::text)`},
		`CREATE UNIQUE INDEX "a(b" ON ONLY "s,t"."x(y" USING btree (lower(name) COLLATE "C" DESC NULLS LAST, id) INCLUDE (n)`:                 {`lower(name) COLLATE "C" DESC NULLS LAST`, `id`},
		`CREATE INDEX t_w ON app.t USING gin ((string_to_array(name, ', ('::text)) COLLATE "default", ((a + b)) gist_trgm_ops (siglen='32'))`: {`(string_to_array(name, ', ('::text)) COLLATE "default"`, `((a + b)) gist_trgm_ops (siglen='32')`},
		`CREATE INDEX t_q ON app.t USING btree ((name || 'it''s, (here)'), "Odd ""col"")")`:                                                   {`(name || 'it''s, (here)')`, `"Odd ""col"")"`},
		`not an index`: nil,
	}
	for def, want := range cases {
		if got := indexDefKeys(def); !reflect.DeepEqual(got, want) {
			t.Errorf("indexDefKeys(%s) = %q, want %q", def, got, want)
		}
	}
}

func TestIndexKeyCollated(t *testing.T) {
	def := `CREATE INDEX t_x ON app.t USING hash ((ip::text) COLLATE "C") WHERE flag`
	if !indexKeyCollated(def, `(ip::text)`, 0) {
		t.Error("COLLATE after the key is explicit")
	}
	def = `CREATE INDEX t_x ON app.t USING btree ((name COLLATE "C"), lower(title) COLLATE "default" text_pattern_ops)`
	if indexKeyCollated(def, `(name COLLATE "C")`, 0) {
		t.Error("COLLATE inside the expression is the expression's own collation")
	}
	if !indexKeyCollated(def, `lower(title)`, 1) {
		t.Error("COLLATE on the second key is explicit")
	}
	if indexKeyCollated(def, `lower(title)`, 2) || indexKeyCollated(def, `upper(title)`, 1) {
		t.Error("a key that is not there is not collated")
	}
}

func TestIndexKeyCollationAgainstStoredCollation(t *testing.T) {
	ix := &schema.Index{Name: "zoo_hash", Columns: []string{"(ip::text)"}, Using: "hash"}
	hashed := []LiveIndexOpclass{{Name: "text_ops", Default: true, KeyCollation: "default"}}
	if indexOpclassesDiffer(ix, hashed) {
		t.Error("an expression key indexed with the expression's own collation must match an index without collations")
	}
	ix.Collations = map[string]string{"(ip::text)": "default"}
	if indexOpclassesDiffer(ix, hashed) {
		t.Error("a declared collation that is the expression's own must match")
	}
	ix.Collations = map[string]string{"(ip::text)": `"C"`}
	if !indexOpclassesDiffer(ix, hashed) {
		t.Error("a declared collation other than the stored one must differ")
	}
	explicit := []LiveIndexOpclass{{Name: "text_ops", Default: true, Collation: "C", KeyCollation: "C"}}
	if indexOpclassesDiffer(ix, explicit) {
		t.Error("the explicit collation must match its declaration")
	}
	ix.Collations = nil
	if !indexOpclassesDiffer(ix, explicit) {
		t.Error("an explicit collation the YAML leaves out must differ")
	}
	lower := &schema.Index{Name: "zoo_lower", Columns: []string{"lower(name)"}, Collations: map[string]string{"lower(name)": "C"}}
	if indexOpclassesDiffer(lower, []LiveIndexOpclass{{Name: "text_ops", Default: true, KeyCollation: "C"}}) {
		t.Error("lower() of a C column is collated C by itself, so declaring C must match")
	}
	ext := &schema.Index{Name: "zoo_ext", Columns: []string{"lower(name)"}, Collations: map[string]string{"lower(name)": "app.ci"}}
	if indexOpclassesDiffer(ext, []LiveIndexOpclass{{Name: "text_ops", Default: true, Collation: "app.ci", KeyCollation: "app.ci"}}) {
		t.Error("a schema-qualified collation must match")
	}
}
