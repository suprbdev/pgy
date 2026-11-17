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

func TestBuildExtensionDictionaryOptions(t *testing.T) {
	live := &diff.Live{
		Schemas:          map[string]bool{"public": true},
		TSDictionaries:   map[string]bool{"public.intdict": true, "public.xsyn": true, "public.mine": true},
		ExtensionMembers: map[string]bool{"tsdict:public.intdict": true, "tsdict:public.xsyn": true},
		TSComments:       map[string]string{"dict:public.intdict": "from the extension"},
		Owners:           map[string]string{"tsdict:public.mine": "dict_owner", "tsdict:public.intdict": "dict_owner"},
		CurrentUser:      "pgy",
		Details: &diff.LiveDetails{TSDictionaries: map[string]*schema.TSDictionary{
			"public.intdict": {Template: "public.intdict_template", Options: map[string]string{"maxlen": "4", "rejectlong": "'true'"}},
			"public.xsyn":    {Template: "public.xsyn_template"},
			"public.mine":    {Template: "simple"},
		}},
	}
	res, err := Build(live, Options{Single: true, Owners: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Files[0].Content)
	if want := "text search dictionary intdict:\n    options: {maxlen: \"4\", rejectlong: '''true'''}\n"; !strings.Contains(out, want) {
		t.Errorf("an extension's dictionary is written without a template, with its options, want %q in:\n%s", want, out)
	}
	if strings.Contains(out, "xsyn") {
		t.Errorf("an extension's dictionary without options is left out:\n%s", out)
	}
	if !strings.Contains(out, "text search dictionary mine:\n    template: simple\n    options: {}\n    owner: dict_owner\n") {
		t.Errorf("a dictionary's owner is pulled:\n%s", out)
	}
	if strings.Count(out, "owner:") != 1 || strings.Contains(out, "from the extension") {
		t.Errorf("only the options of an extension's dictionary are written:\n%s", out)
	}
}
