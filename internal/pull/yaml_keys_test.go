package pull

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestPullQuotesNamesYAMLWouldRetype(t *testing.T) {
	order := []string{"id", "2024", "null", "true", "1.5", "~", "<<", "2024-01-01", "name"}
	cols := map[string]*diff.LiveColumn{}
	for _, c := range order {
		cols[c] = &diff.LiveColumn{Type: "text", Nullable: c != "id" && c != "2024"}
	}
	lt := &diff.LiveTable{
		Columns:        cols,
		ConstraintDefs: map[string]string{"t_pkey": `PRIMARY KEY (id, "2024")`},
		Constraints:    map[string]bool{"t_pkey": true},
		IndexDefs:      map[string]*diff.LiveIndex{"t_pkey": {Constraint: true}},
	}
	csv := &diff.LiveTable{
		Columns: map[string]*diff.LiveColumn{"a": {Type: "text", Nullable: true, FDWOptions: map[string]string{"null": "x"}}},
		Foreign: &diff.LiveForeignTable{Server: "files", Options: map[string]string{"filename": "/tmp/a.csv", "null": "NULL"}},
	}
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables:  map[string]*diff.LiveTable{"app.t": lt, "app.csv": csv},
		Servers: map[string]*diff.LiveServer{"files": {Wrapper: "file_fdw", Options: map[string]string{"true": "1"}}},
		Details: &diff.LiveDetails{ColumnOrder: map[string][]string{"app.t": order, "app.csv": {"a"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{
		`"2024": {type: text}`,
		`"null": {type: text, nullable: true}`,
		`"true": {type: text, nullable: true}`,
		`"1.5": {type: text, nullable: true}`,
		`"~": {type: text, nullable: true}`,
		`"<<": {type: text, nullable: true}`,
		`"2024-01-01": {type: text, nullable: true}`,
		`name: {type: text, nullable: true}`,
		`primaryKey: [id, "2024"]`,
		`options: {filename: /tmp/a.csv, "null": "NULL"}`,
		`options: {"null": x}`,
		`options: {"true": "1"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	p := filepath.Join(t.TempDir(), "pulled.yml")
	if err := os.WriteFile(p, res.Files[0].Content, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := schema.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	tbl := db.Tables["app.t"]
	if !reflect.DeepEqual(tbl.ColumnOrder, order) {
		t.Errorf("reloaded column order = %v, want %v", tbl.ColumnOrder, order)
	}
	if len(tbl.Columns) != len(order) {
		t.Errorf("reloaded columns = %v", tbl.Columns)
	}
	if got := db.Tables["app.csv"].Foreign.Options; !reflect.DeepEqual(got, csv.Foreign.Options) {
		t.Errorf("reloaded foreign table options = %v", got)
	}
}

func TestPullSerialSequenceOfDottedColumn(t *testing.T) {
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables: map[string]*diff.LiveTable{"app.t": {
			Columns: map[string]*diff.LiveColumn{"a.b": {Type: "integer", Default: `nextval('app."t_a.b_seq"'::regclass)`}},
		}},
		Sequences:      map[string]bool{"app.t_a.b_seq": true},
		SequenceDefs:   map[string]*diff.LiveSequence{"app.t_a.b_seq": {Type: "integer", Owner: "app.t.a.b", Increment: "1", Start: "1", MinValue: "1", MaxValue: "2147483647", Cache: "1"}},
		OwnedSequences: map[string]bool{"app.t_a.b_seq": true},
		RelComments:    map[string]string{"app.t_a.b_seq": "counter"},
		Details:        &diff.LiveDetails{ColumnOrder: map[string][]string{"app.t": {"a.b"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	if !strings.Contains(got, "dependsOn: [table app.t]") {
		t.Errorf("the serial sequence should depend on its table:\n%s", got)
	}
}
