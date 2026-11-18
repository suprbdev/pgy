package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestPullWarnsAboutViewsReadingExternalRelations(t *testing.T) {
	live := externalLive()
	live.Views = map[string]bool{"app.hourly": true, "app.recent": true, "app.users": true, "app.chunks": true}
	live.MatViews = map[string]bool{"app.daily": true}
	live.Details.ViewQueries = map[string]string{
		"app.hourly": "SELECT bucket FROM _internal._materialized_hypertable_2",
		"app.recent": "SELECT id FROM app.notes",
		"app.users":  "SELECT id FROM platform.users",
		"app.chunks": "SELECT id FROM app._hyper_1_1_chunk",
		"app.daily":  "SELECT bucket FROM _internal._materialized_hypertable_2 JOIN _internal._other USING (bucket)",
	}
	live.Details.ViewRelations = map[string][]string{
		"app.hourly": {"_internal._materialized_hypertable_2"},
		"app.recent": {"app.notes"},
		"app.users":  {"platform.users"},
		"app.chunks": {"app._hyper_1_1_chunk"},
		"app.daily":  {"_internal._materialized_hypertable_2", "_internal._other"},
	}
	ext := *pullExternal
	ext.Relations = append([]string{"_internal.*", "platform.*"}, ext.Relations...)
	res, err := Build(live, Options{Single: true, External: &ext})
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		"view app.hourly reads _internal._materialized_hypertable_2, an external relation that this YAML leaves out, so a database rebuilt from it cannot create the view; list the view under external.relations too, or declare it the way the extension that makes it does",
		"view app.chunks reads app._hyper_1_1_chunk, an external relation",
		"materialized view app.daily reads _internal._materialized_hypertable_2, _internal._other, external relations that this YAML leaves out, so a database rebuilt from it cannot create the materialized view",
	} {
		if !strings.Contains(warnings, want) {
			t.Errorf("missing %q in:\n%s", want, warnings)
		}
	}
	if strings.Contains(warnings, "app.recent") || strings.Contains(warnings, "app.users") || len(res.Warnings) != 3 {
		t.Errorf("only views reading external relations outside external schemas are reported, got:\n%s", warnings)
	}
	if got := string(res.Files[0].Content); !strings.Contains(got, "view hourly:") || !strings.Contains(got, "materialized view daily:") {
		t.Errorf("the views are still pulled, so the YAML describes the database:\n%s", got)
	}

	ext.Relations = append(ext.Relations, "app.hourly", "app.daily", "app.chunks")
	res, err = Build(live, Options{Single: true, External: &schema.External{Schemas: ext.Schemas, Roles: ext.Roles, Relations: ext.Relations}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 || strings.Contains(string(res.Files[0].Content), "hourly") {
		t.Errorf("a view listed under external.relations is left out without a warning, got %v", res.Warnings)
	}
}
