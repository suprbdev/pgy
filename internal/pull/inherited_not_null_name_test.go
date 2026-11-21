package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestBuildInheritedNotNullName(t *testing.T) {
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables: map[string]*diff.LiveTable{
			"app.m": {Partitioned: true, Columns: map[string]*diff.LiveColumn{
				"id": {Type: "integer", NotNullName: "m_id_not_null"},
				"k":  {Type: "text", NotNullName: "m_k_not_null"},
				"v":  {Type: "integer", Nullable: true},
				"w":  {Type: "integer", Nullable: true},
			}},
			"app.m_b": {IsPartition: true, Columns: map[string]*diff.LiveColumn{
				"id": {Type: "integer", NotNullName: "m_b_id_not_null", Inherited: true},
				"k":  {Type: "text", NotNullName: "m_k_not_null", Inherited: true},
				"v":  {Type: "integer", NotNullName: "m_b_v_nn", Inherited: true},
				"w":  {Type: "integer", NotNullName: "m_b_w_not_null", Inherited: true},
			}},
		},
		Details: &diff.LiveDetails{
			ColumnOrder:     map[string][]string{"app.m": {"id", "k", "v", "w"}, "app.m_b": {"id", "k", "v", "w"}},
			PartitionOf:     map[string]string{"app.m_b": "app.m"},
			PartitionKeys:   map[string]string{"app.m": "LIST (k)"},
			PartitionBounds: map[string]string{"app.m_b": "FOR VALUES IN ('b')"},
		},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{"id: {notNullName: m_b_id_not_null}", "v: {nullable: false, notNullName: m_b_v_nn}", "w: {nullable: false}"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "k: {notNullName") {
		t.Errorf("a copy named like the parent's constraint is not written:\n%s", got)
	}
}
