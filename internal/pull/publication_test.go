package pull

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestBuildPublicationExceptAndAllSequences(t *testing.T) {
	all := []string{"insert", "update", "delete", "truncate"}
	live := filterLive()
	live.ServerVersion = 190000
	live.Publications = map[string]*diff.LivePublication{
		"rest": {Publish: all, AllTables: true, AllSequences: true, Tables: map[string]diff.LivePublicationTable{},
			Except: map[string]bool{"app.notes": false, "platform.users": true}},
		"seqs": {Publish: []string{"insert"}, AllSequences: true, Tables: map[string]diff.LivePublicationTable{}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Files[0].Content)
	for _, want := range []string{
		"rest:\n    allTables: true\n    except:\n      - app.notes\n      - {table: platform.users, only: true}\n    allSequences: true\n",
		"seqs:\n    allSequences: true\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tables:") {
		t.Errorf("excepted tables are not published tables:\n%s", out)
	}
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, res.Files[0].Content, 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.PlanWith(live, &schema.Database{Publications: d.Publications}, diff.Options{})
	if sql := diff.Render(plan); strings.Contains(sql, "publication") {
		t.Errorf("pulled publications must diff clean, got:\n%s", sql)
	}
}
