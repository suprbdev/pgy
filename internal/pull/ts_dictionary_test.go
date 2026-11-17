package pull

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestBuildDictionaryWithoutOptions(t *testing.T) {
	live := &diff.Live{
		Schemas:        map[string]bool{"app": true},
		TSDictionaries: map[string]bool{"app.plain": true, "app.stop": true},
		Details: &diff.LiveDetails{TSDictionaries: map[string]*schema.TSDictionary{
			"app.plain": {Template: "public.intdict_template"},
			"app.stop":  {Template: "simple", Options: map[string]string{"stopwords": "'english'"}},
		}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Files[0].Content)
	for _, want := range []string{"template: public.intdict_template\n    options: {}", "options: {stopwords: '''english'''}"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, res.Files[0].Content, 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	if td := d.TSDictionaries["app.plain"]; td == nil || td.Options == nil || len(td.Options) != 0 {
		t.Errorf("options: {} must load as a managed, empty option set: %+v", td)
	}
}
