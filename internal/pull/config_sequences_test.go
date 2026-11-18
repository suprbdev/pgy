package pull

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func configSequenceLive() *diff.Live {
	live := memberLive()
	live.Details.ColumnOrder = map[string][]string{"public.cfg": {"id", "name"}}
	live.Tables["public.cfg"] = &diff.LiveTable{
		Columns: map[string]*diff.LiveColumn{
			"id":   {Type: "integer", Default: "nextval('cfg_id_seq'::regclass)"},
			"name": {Type: "text"},
		},
		HasPK:          true,
		Constraints:    map[string]bool{"cfg_pkey": true},
		ConstraintDefs: map[string]string{"cfg_pkey": "PRIMARY KEY (id)"},
	}
	live.SequenceDefs = map[string]*diff.LiveSequence{
		"public.cfg_id_seq": {Name: "public.cfg_id_seq", Owner: "public.cfg.id"},
		"public.counter":    {Name: "public.counter"},
		"public.unread":     {Name: "public.unread"},
	}
	live.ExtensionConfig = map[string]string{"public.cfg": ""}
	live.ExtensionConfigSequences = map[string]bool{"public.cfg_id_seq": true, "public.counter": true, "public.unread": true}
	live.MemberExtension = map[string]string{}
	for _, rel := range []string{"public.cfg", "public.cfg_id_seq", "public.counter", "public.unread"} {
		live.ExtensionMembers["rel:"+rel] = true
		live.MemberExtension["rel:"+rel] = "pgcrypto"
	}
	return live
}

func TestPullReportsConfigurationSequences(t *testing.T) {
	seqs := map[string]string{"public.cfg_id_seq": "5", "public.counter": "9", "public.unread": ""}
	res, err := Build(configSequenceLive(), Options{Single: true, ConfigRows: map[string]int64{"public.cfg": 2}, ConfigSequences: seqs})
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		"extension pgcrypto: configuration table public.cfg has 2 rows that pg_dump keeps and this YAML does not carry, and the position of its sequence public.cfg_id_seq (at 5); a database rebuilt from it lacks them",
		"extension pgcrypto: configuration sequence public.counter has reached 9, which pg_dump keeps; a database rebuilt from this YAML starts it from its start value; advance it with setval in a migration, or list it under external.relations",
		"extension pgcrypto: configuration sequence public.unread could not be read",
	} {
		if !strings.Contains(warnings, want) {
			t.Errorf("missing %q in:\n%s", want, warnings)
		}
	}
	if len(res.Warnings) != 3 {
		t.Errorf("want 3 warnings, got:\n%s", warnings)
	}

	rows := []map[string]any{{"id": json.Number("1"), "name": "a"}, {"id": json.Number("3"), "name": "b"}}
	res, err = Build(configSequenceLive(), Options{Single: true, ConfigSequences: seqs, Data: map[string][]map[string]any{"public.cfg": rows}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "configuration sequence public.cfg_id_seq has reached 5, which pg_dump keeps; a database rebuilt from this YAML advances it past the rows --data public.cfg pulls, but they take it to 3 only"; !strings.Contains(strings.Join(res.Warnings, "\n"), want) {
		t.Errorf("missing %q in:\n%s", want, strings.Join(res.Warnings, "\n"))
	}

	rows = append(rows, map[string]any{"id": json.Number("5"), "name": "c"})
	res, err = Build(configSequenceLive(), Options{Single: true, ConfigSequences: map[string]string{"public.cfg_id_seq": "5"}, Data: map[string][]map[string]any{"public.cfg": rows}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("rows pulled up to the sequence's position restore it, got %v", res.Warnings)
	}
}
