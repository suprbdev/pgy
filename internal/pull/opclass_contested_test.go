package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestBuildContestedOpclassesSpelledOut(t *testing.T) {
	key := func(col string) *diff.LiveIndex {
		return &diff.LiveIndex{Method: "gist", Keys: []string{col}, Descending: []bool{false}, NullsFirst: []bool{false}}
	}
	lt := &diff.LiveTable{
		Columns: map[string]*diff.LiveColumn{"tenant": {Type: "integer"}, "addr": {Type: "inet"}, "body": {Type: "text"}},
		IndexDefs: map[string]*diff.LiveIndex{
			"t_addr": key("addr"),
			"t_body": key("body"),
			"t_excl": {Method: "gist", Keys: []string{"tenant", "addr"}, Descending: []bool{false, false}, NullsFirst: []bool{false, false}, Constraint: true},
		},
		IndexOpclasses: map[string][]diff.LiveIndexOpclass{
			"t_addr": {{Name: "gist_inet_ops", Default: true, Contested: true, Column: "addr"}},
			"t_body": {{Name: "text_ops", Default: true, Column: "body"}},
			"t_excl": {{Name: "gist_int4_ops", Default: true, Contested: true, Column: "tenant"}, {Name: "inet_ops", Default: true, Contested: true, Column: "addr"}},
		},
		Constraints:    map[string]bool{"t_excl": true},
		ConstraintDefs: map[string]string{"t_excl": "EXCLUDE USING gist (tenant WITH =, addr WITH &&)"},
	}
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables:  map[string]*diff.LiveTable{"app.t": lt},
		Details: &diff.LiveDetails{ColumnOrder: map[string][]string{"app.t": {"tenant", "addr", "body"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{
		"opclasses: {addr: gist_inet_ops}",
		"expression: USING gist (tenant gist_int4_ops WITH =, addr inet_ops WITH &&)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "text_ops") {
		t.Errorf("a default class no extension contests must not be pulled:\n%s", got)
	}
}
