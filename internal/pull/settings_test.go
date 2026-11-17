package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestBuildSettingsThatNeedQuotes(t *testing.T) {
	live := &diff.Live{
		DatabaseConfig: map[string]string{
			"log_statement":    "all",
			"application_name": "a,b",
			"search_path":      `""`,
			"myapp.motto":      "it's, really",
			"myapp.literal":    "'quoted'",
		},
		Details: &diff.LiveDetails{},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	var out string
	for _, f := range res.Files {
		out += string(f.Content)
	}
	for _, want := range []string{
		"log_statement: all",
		"application_name: 'a,b'",
		`search_path: '""'`,
		`myapp.motto: "'it''s, really'"`,
		`myapp.literal: "'''quoted'''"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
