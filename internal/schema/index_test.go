package schema

import "testing"

func TestIndexOptionsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema public:
  table t:
    columns:
      a: {type: int}
      b: {type: text}
    indexes:
      t_a:
        columns: [a]
        unique: true
        include: [b]
        nullsNotDistinct: true
        with: {fillfactor: 70, deduplicate_items: off}
`))
	if err != nil {
		t.Fatal(err)
	}
	ix := db.Tables["public.t"].Indexes[0]
	if len(ix.Include) != 1 || ix.Include[0] != "b" || !ix.NullsNotDistinct {
		t.Errorf("unexpected index options: %+v", ix)
	}
	if ix.With["fillfactor"] != "70" || ix.With["deduplicate_items"] != "off" {
		t.Errorf("unexpected with: %v", ix.With)
	}
}

func TestIndexOpclassParametersParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema public:
  table t:
    columns:
      name: {type: text}
      path: {type: ltree}
    indexes:
      t_name:
        columns: [name]
        using: gist
        opclass: gist_trgm_ops(siglen=32)
      t_both:
        columns: [name, path]
        using: gist
        opclasses: {name: "gist_trgm_ops (siglen='64')", path: gist_ltree_ops(siglen=100)}
      t_bloom:
        columns: [name]
        using: bloom
        with: {length: 80, col1: 2}
`))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*Index{}
	for _, ix := range db.Tables["public.t"].Indexes {
		byName[ix.Name] = ix
	}
	if got := byName["t_name"].Opclass; got != "gist_trgm_ops(siglen=32)" {
		t.Errorf("opclass = %q", got)
	}
	both := byName["t_both"].Opclasses
	if both["name"] != "gist_trgm_ops (siglen='64')" || both["path"] != "gist_ltree_ops(siglen=100)" {
		t.Errorf("opclasses = %v", both)
	}
	if bloom := byName["t_bloom"]; bloom.Using != "bloom" || bloom.With["length"] != "80" || bloom.With["col1"] != "2" {
		t.Errorf("bloom index = %+v", bloom)
	}
}
