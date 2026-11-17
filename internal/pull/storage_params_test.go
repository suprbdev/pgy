package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestBuildStorageParametersInCatalogOrder(t *testing.T) {
	lt := &diff.LiveTable{
		Columns:     map[string]*diff.LiveColumn{"k": {Type: "integer"}},
		Options:     map[string]string{"toast_tuple_target": "4000", "fillfactor": "90", "autovacuum_enabled": "false"},
		OptionOrder: []string{"toast_tuple_target", "fillfactor", "autovacuum_enabled"},
		IndexDefs: map[string]*diff.LiveIndex{"t_k": {Method: "btree", Keys: []string{"k"}, Descending: []bool{false}, NullsFirst: []bool{false},
			Options: map[string]string{"fillfactor": "50", "deduplicate_items": "off"}, OptionOrder: []string{"fillfactor", "deduplicate_items"}}},
		IndexOpclasses: map[string][]diff.LiveIndexOpclass{"t_k": {{Name: "int4_ops", Default: true, Column: "k"}}},
	}
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables:  map[string]*diff.LiveTable{"app.t": lt},
		Details: &diff.LiveDetails{ColumnOrder: map[string][]string{"app.t": {"k"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{
		`with: {toast_tuple_target: "4000", fillfactor: "90", autovacuum_enabled: "false"}`,
		`with: {fillfactor: "50", deduplicate_items: off}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
