package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestPullStatisticsInOtherSchema(t *testing.T) {
	lt := &diff.LiveTable{
		Columns: map[string]*diff.LiveColumn{"a": {Type: "integer"}, "b": {Type: "integer"}},
		Statistics: map[string]*diff.LiveStatistic{
			"s":            {Items: []string{"a", "b"}, Kinds: []string{"ndistinct"}, Target: -1},
			`"public"."s"`: {Items: []string{"a", "b"}, Kinds: []string{"mcv"}, Target: -1, Schema: "public", Name: "s"},
			`"public"."o"`: {Items: []string{"a", "b"}, Kinds: []string{"dependencies"}, Target: -1, Schema: "public", Name: "o"},
		},
	}
	live := &diff.Live{
		Schemas: map[string]bool{"app": true, "public": true},
		Tables:  map[string]*diff.LiveTable{"app.t": lt},
		Details: &diff.LiveDetails{ColumnOrder: map[string][]string{"app.t": {"a", "b"}}},
	}
	out, d, warns := buildSingle(t, live)
	got := map[string]string{}
	for _, st := range d.Tables["app.t"].Statistics {
		got[st.Name] = st.Schema + " " + strings.Join(st.Kinds, ",")
	}
	if len(got) != 2 || got["s"] != " ndistinct" || got["o"] != "public dependencies" {
		t.Errorf("pulled statistics = %v\n%s", got, out)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "statistics public.s was left out") {
		t.Errorf("a second statistics object with the same name is reported: %v", warns)
	}
}
