package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestExcludeWithContestedOpclasses(t *testing.T) {
	inet := LiveIndexOpclass{Name: "gist_inet_ops", Default: true, Contested: true}
	int4 := LiveIndexOpclass{Name: "gist_int4_ops", Default: true, Contested: true}
	rng := LiveIndexOpclass{Name: "range_ops", Default: true}
	cases := []struct {
		name, def string
		opcs      []LiveIndexOpclass
		want      string
	}{
		{"contested defaults spelled out", "USING gist (tenant WITH =, addr WITH =)", []LiveIndexOpclass{int4, inet},
			"USING gist (tenant gist_int4_ops WITH =, addr gist_inet_ops WITH =)"},
		{"uncontested default left out", "USING gist (room WITH =, during WITH &&)", []LiveIndexOpclass{int4, rng},
			"USING gist (room gist_int4_ops WITH =, during WITH &&)"},
		{"written class kept", "USING gist (block inet_ops WITH &&)", []LiveIndexOpclass{{Name: "inet_ops", Default: true, Contested: true}},
			"USING gist (block inet_ops WITH &&)"},
		{"after the collation, before the ordering", `USING gist (name COLLATE "C" DESC WITH =, (lower(x)) WITH =) WHERE ((a > 0))`,
			[]LiveIndexOpclass{{Name: "ext.gist_text_ops", Default: true, Contested: true}, {Name: "gist_text_ops", Default: true, Contested: true}},
			`USING gist (name COLLATE "C" ext.gist_text_ops DESC WITH =, (lower(x)) gist_text_ops WITH =) WHERE ((a > 0))`},
		{"parameters", "USING gist (a WITH &&)", []LiveIndexOpclass{{Name: "gist__int_ops", Default: true, Contested: true, Params: []string{"numranges=200"}}},
			"USING gist (a gist__int_ops(numranges=200) WITH &&)"},
		{"no classes known", "USING gist (a WITH =)", nil, "USING gist (a WITH =)"},
		{"unparsable", "USING gist a WITH =", []LiveIndexOpclass{inet}, "USING gist a WITH ="},
	}
	for _, c := range cases {
		if got := ExcludeWithContestedOpclasses(c.def, c.opcs); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExcludeContestedOpclassNamedUnchanged(t *testing.T) {
	inet18 := LiveIndexOpclass{Name: "gist_inet_ops", Default: true, Contested: true}
	inet19 := LiveIndexOpclass{Name: "gist_inet_ops", Contested: true}
	int4 := LiveIndexOpclass{Name: "gist_int4_ops", Default: true, Contested: true}
	ct := &schema.Constraint{Name: "x", Type: "exclude", Expression: "USING gist (tenant gist_int4_ops WITH =, addr gist_inet_ops WITH =)"}
	if !constraintMatchesLive(excludeLive("EXCLUDE USING gist (tenant WITH =, addr WITH =)", int4, inet18), "x", ct) {
		t.Error("an exclusion constraint naming the default classes must match one PostgreSQL 18 prints without them")
	}
	if !constraintMatchesLive(excludeLive("EXCLUDE USING gist (tenant WITH =, addr gist_inet_ops WITH =)", int4, inet19), "x", ct) {
		t.Error("an exclusion constraint naming btree_gist's inet class must match it on PostgreSQL 19, where it is not the default")
	}
	if constraintMatchesLive(excludeLive("EXCLUDE USING gist (tenant WITH =, addr WITH =)", int4, LiveIndexOpclass{Name: "inet_ops", Default: true, Contested: true}), "x", ct) {
		t.Error("an exclusion constraint naming btree_gist's inet class must differ from one built with core's inet_ops")
	}
}

func TestIndexContestedOpclassNamedUnchanged(t *testing.T) {
	ix := &schema.Index{Name: "t_addr", Columns: []string{"addr"}, Using: "gist", Opclasses: map[string]string{"addr": "gist_inet_ops"}}
	if indexOpclassesDiffer(ix, []LiveIndexOpclass{{Name: "gist_inet_ops", Default: true, Contested: true}}) {
		t.Error("an index naming the default class must match it")
	}
	if indexOpclassesDiffer(ix, []LiveIndexOpclass{{Name: "gist_inet_ops", Contested: true}}) {
		t.Error("an index naming a class that is not the default must match it")
	}
	if !indexOpclassesDiffer(ix, []LiveIndexOpclass{{Name: "inet_ops", Default: true, Contested: true}}) {
		t.Error("an index naming btree_gist's inet class must differ from one built with core's inet_ops")
	}
}
