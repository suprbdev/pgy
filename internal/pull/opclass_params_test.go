package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestBuildIndexOpclassParameters(t *testing.T) {
	key := func() *diff.LiveIndex {
		return &diff.LiveIndex{Method: "gist", Keys: []string{"body"}, Descending: []bool{false}, NullsFirst: []bool{false}}
	}
	lt := &diff.LiveTable{
		Columns: map[string]*diff.LiveColumn{"body": {Type: "text"}},
		IndexDefs: map[string]*diff.LiveIndex{
			"t_trgm":    key(),
			"t_default": key(),
			"t_plain":   key(),
		},
		IndexOpclasses: map[string][]diff.LiveIndexOpclass{
			"t_trgm":    {{Name: "gist_trgm_ops", Params: []string{"siglen=32"}}},
			"t_default": {{Name: "tsvector_ops", Default: true, Params: []string{"siglen=100"}}},
			"t_plain":   {{Name: "text_ops", Default: true}},
		},
	}
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables:  map[string]*diff.LiveTable{"app.t": lt},
		Details: &diff.LiveDetails{ColumnOrder: map[string][]string{"app.t": {"body"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{
		"opclasses: {body: gist_trgm_ops(siglen=32)}",
		"opclasses: {body: tsvector_ops(siglen=100)}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "text_ops") {
		t.Errorf("a default opclass without parameters must not be pulled:\n%s", got)
	}
}
