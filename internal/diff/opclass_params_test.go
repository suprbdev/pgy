package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestIndexOpclassParametersSplit(t *testing.T) {
	cases := []struct {
		in     string
		name   string
		params map[string]string
	}{
		{"gin_trgm_ops", "gin_trgm_ops", nil},
		{"gist_trgm_ops(siglen=32)", "gist_trgm_ops", map[string]string{"siglen": "32"}},
		{"gist_trgm_ops (siglen = '32')", "gist_trgm_ops", map[string]string{"siglen": "32"}},
		{`"ext".gist_trgm_ops ( SIGLEN = 32 )`, `"ext".gist_trgm_ops`, map[string]string{"siglen": "32"}},
		{"int4_bloom_ops(n_distinct_per_range=100, false_positive_rate='0.05')", "int4_bloom_ops", map[string]string{"n_distinct_per_range": "100", "false_positive_rate": "0.05"}},
		{"", "", nil},
	}
	for _, c := range cases {
		name, params := splitOpclass(c.in)
		if name != c.name || !reflect.DeepEqual(params, c.params) {
			t.Errorf("splitOpclass(%q) = %q, %v; want %q, %v", c.in, name, params, c.name, c.params)
		}
	}
}

func TestIndexOpclassParametersMatch(t *testing.T) {
	live := []LiveIndexOpclass{{Name: "gist_trgm_ops", Params: []string{"siglen=32"}}}
	for _, oc := range []string{
		"gist_trgm_ops(siglen=32)",
		"gist_trgm_ops (siglen='32')",
		"public.gist_trgm_ops( SIGLEN = 32 )",
		"gist_trgm_ops(siglen=32.0)",
	} {
		ix := &schema.Index{Name: "t_name", Columns: []string{"name"}, Using: "gist", Opclass: oc}
		if indexOpclassesDiffer(ix, live) {
			t.Errorf("opclass %q must match live gist_trgm_ops (siglen='32')", oc)
		}
	}
	perCol := &schema.Index{Name: "t_name", Columns: []string{"name desc"}, Using: "gist", Opclasses: map[string]string{"name": "gist_trgm_ops(siglen=32)"}}
	if indexOpclassesDiffer(perCol, live) {
		t.Error("a per-column opclass with parameters must match")
	}
	multi := []LiveIndexOpclass{{Name: "int4_bloom_ops", Params: []string{"n_distinct_per_range=100", "false_positive_rate=0.05"}}}
	ix := &schema.Index{Name: "t_n", Columns: []string{"n"}, Using: "brin", Opclass: "int4_bloom_ops(false_positive_rate=0.05, n_distinct_per_range=100)"}
	if indexOpclassesDiffer(ix, multi) {
		t.Error("parameter order must not matter")
	}
}

func TestIndexOpclassParametersChanged(t *testing.T) {
	live := []LiveIndexOpclass{{Name: "gist_trgm_ops", Params: []string{"siglen=32"}}}
	for _, oc := range []string{
		"gist_trgm_ops",
		"gist_trgm_ops(siglen=64)",
		"gist_trgm_ops(siglen=32, other=1)",
	} {
		ix := &schema.Index{Name: "t_name", Columns: []string{"name"}, Using: "gist", Opclass: oc}
		if !indexOpclassesDiffer(ix, live) {
			t.Errorf("opclass %q must differ from live gist_trgm_ops (siglen='32')", oc)
		}
	}
	ix := &schema.Index{Name: "t_name", Columns: []string{"name"}, Using: "gist", Opclass: "gist_trgm_ops(siglen=32)"}
	if !indexOpclassesDiffer(ix, []LiveIndexOpclass{{Name: "gist_trgm_ops"}}) {
		t.Error("declared parameters must differ from a live opclass without any")
	}
}

func TestIndexOpclassParametersOnDefaultOpclass(t *testing.T) {
	live := []LiveIndexOpclass{{Name: "tsvector_ops", Default: true, Params: []string{"siglen=100"}}}
	bare := &schema.Index{Name: "t_tsv", Columns: []string{"tsv"}, Using: "gist"}
	if !indexOpclassesDiffer(bare, live) {
		t.Error("an index without an opclass must differ from a default opclass that has parameters")
	}
	bare.Opclass = "tsvector_ops(siglen=100)"
	if indexOpclassesDiffer(bare, live) {
		t.Error("the default opclass named with its parameters must match")
	}
}

func TestIndexOpclassParametersCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"name": {Type: "text"}, "path": {Type: "text"}},
			Indexes: []*schema.Index{
				{Name: "t_name", Columns: []string{"name"}, Using: "gist", Opclass: "gist_trgm_ops(siglen=32)"},
				{Name: "t_both", Columns: []string{"name", "path desc"}, Using: "gist", Opclasses: map[string]string{"path": "gist_trgm_ops (siglen=64)"}},
			}},
	}}
	sql := Render(Plan(emptyLive(), desired, false))
	for _, want := range []string{
		`using gist("name" gist_trgm_ops(siglen=32))`,
		`using gist("name", "path" gist_trgm_ops (siglen=64) desc)`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestIndexOpclassParametersRecreatedUnsafe(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"name": {Type: "text"}},
			Indexes: []*schema.Index{{Name: "t_name", Columns: []string{"name"}, Using: "gist", Opclass: "gist_trgm_ops(siglen=64)"}}},
	}}
	live := emptyLive()
	lt := newLiveTable()
	lt.Columns["name"] = &LiveColumn{Type: "text"}
	lt.Indexes["t_name"] = true
	lt.IndexDefs["t_name"] = &LiveIndex{Method: "gist", Keys: []string{"name"}, Descending: []bool{false}, NullsFirst: []bool{false}}
	lt.IndexOpclasses["t_name"] = []LiveIndexOpclass{{Name: "gist_trgm_ops", Params: []string{"siglen=32"}}}
	live.Tables["public.t"] = lt
	if sql := Render(Plan(live, desired, false)); sql != "" {
		t.Errorf("a changed opclass parameter needs --unsafe, got:\n%s", sql)
	}
	sql := Render(Plan(live, desired, true))
	if !strings.Contains(sql, `drop index "public"."t_name";`) || !strings.Contains(sql, `gist_trgm_ops(siglen=64)`) {
		t.Errorf("expected drop and recreate with the new parameter, got:\n%s", sql)
	}
	lt.IndexOpclasses["t_name"][0].Params = []string{"siglen=64"}
	if sql := Render(Plan(live, desired, true)); sql != "" {
		t.Errorf("matching parameters must emit nothing, got:\n%s", sql)
	}
}

func TestLiveIndexOpclassDeclaration(t *testing.T) {
	cases := map[string]LiveIndexOpclass{
		"gin_trgm_ops":             {Name: "gin_trgm_ops"},
		"gist_trgm_ops(siglen=32)": {Name: "gist_trgm_ops", Params: []string{"siglen=32"}},
		"ext.int4_bloom_ops(n_distinct_per_range=100, false_positive_rate=0.05)": {Name: "ext.int4_bloom_ops", Params: []string{"n_distinct_per_range=100", "false_positive_rate=0.05"}},
		"some_ops(label='two words')":                                            {Name: "some_ops", Params: []string{"label=two words"}},
	}
	for want, oc := range cases {
		if got := oc.Declaration(); got != want {
			t.Errorf("Declaration() = %q, want %q", got, want)
		}
		name, params := splitOpclass(oc.Declaration())
		if name != oc.Name || !opclassParamsEqual(params, parseProconfig(oc.Params)) {
			t.Errorf("%q does not parse back: %q %v", want, name, params)
		}
	}
}

func TestIndexExpressionDefaultCollation(t *testing.T) {
	ix := &schema.Index{Name: "t_words", Columns: []string{"string_to_array(name, ' ')"}, Using: "gin"}
	live := []LiveIndexOpclass{{Name: "array_ops", Default: true}}
	if indexOpclassesDiffer(ix, live) {
		t.Error("an expression key without a collation must match")
	}
	ix.Collations = map[string]string{"string_to_array(name, ' ')": "default"}
	if indexOpclassesDiffer(ix, live) {
		t.Error("collate default on an expression key is its type's default collation")
	}
	ix.Collations = map[string]string{"string_to_array(name, ' ')": "C"}
	if !indexOpclassesDiffer(ix, live) {
		t.Error("another collation on an expression key must differ")
	}
	col := &schema.Index{Name: "t_name", Columns: []string{"name"}, Collations: map[string]string{"name": "default"}}
	if !indexOpclassesDiffer(col, []LiveIndexOpclass{{Name: "text_ops", Default: true}}) {
		t.Error("collate default on a plain column is compared as written")
	}
}
