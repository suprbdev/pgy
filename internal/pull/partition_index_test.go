package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestBuildPartitionIndexes(t *testing.T) {
	cols := map[string]*diff.LiveColumn{"k": {Type: "integer"}, "note": {Type: "text"}}
	index := func(key, parent string) *diff.LiveIndex {
		return &diff.LiveIndex{Method: "btree", Keys: []string{key}, Descending: []bool{false}, NullsFirst: []bool{false}, Inherited: parent != "", Parent: parent}
	}
	parent := &diff.LiveTable{
		Columns:   cols,
		IndexDefs: map[string]*diff.LiveIndex{"p_k": index("k", ""), "p_note": index("lower(note)", "")},
		IndexOpclasses: map[string][]diff.LiveIndexOpclass{
			"p_k":    {{Name: "int4_ops", Default: true, Column: "k"}},
			"p_note": {{Name: "text_ops", Default: true, Column: "lower"}},
		},
		Partitioned: true,
	}
	part := func(k, note string) *diff.LiveTable {
		return &diff.LiveTable{
			Columns:     cols,
			IsPartition: true,
			IndexDefs:   map[string]*diff.LiveIndex{k: index("k", "app.p_k"), note: index("lower(note)", "app.p_note")},
			IndexOpclasses: map[string][]diff.LiveIndexOpclass{
				k:    {{Name: "int4_ops", Default: true, Column: "k"}},
				note: {{Name: "text_ops", Default: true, Column: "lower"}},
			},
			IndexComments: map[string]string{},
		}
	}
	p1, p2 := part("p1_k_idx", "p1_lower_idx"), part("p2_k_named", "p2_lower_idx")
	p2.IndexComments["p2_lower_idx"] = "commented"
	live := &diff.Live{
		Schemas:         map[string]bool{"app": true},
		Tables:          map[string]*diff.LiveTable{"app.p": parent, "app.p1": p1, "app.p2": p2},
		Partitions:      map[string][]string{"app.p": {"app.p1", "app.p2"}},
		PartitionBounds: map[string]diff.LivePartitionBound{"app.p1": {Parent: "app.p", Bound: "FOR VALUES FROM (0) TO (10)"}, "app.p2": {Parent: "app.p", Bound: "FOR VALUES FROM (10) TO (20)"}},
		Details:         &diff.LiveDetails{ColumnOrder: map[string][]string{"app.p": {"k", "note"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{"p2_k_named:", "p2_lower_idx:", "comment: commented"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"p1_k_idx", "p1_lower_idx"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("%s has the name PostgreSQL gives it and must not be pulled:\n%s", unwanted, got)
		}
	}
}
