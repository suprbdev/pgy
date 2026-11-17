package pull

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestPullOptionsAndSettingsInCatalogOrder(t *testing.T) {
	live := routineLive()
	addFunction(live, "f", "", "sql", "select 1", nil)
	live.FunctionDefs["app.f()"].Config = map[string]string{"work_mem": "1MB", "lock_timeout": "1s", "search_path": "app, public"}
	live.FunctionDefs["app.f()"].ConfigOrder = []string{"work_mem", "search_path", "lock_timeout"}
	live.TSDictionaries = map[string]bool{"app.d": true}
	live.Details.TSDictionaries = map[string]*schema.TSDictionary{
		"app.d": {Template: "simple", Options: map[string]string{"stopwords": "'english'", "accept": "'false'"}, OptionOrder: []string{"stopwords", "accept"}},
	}
	out, d, _ := buildSingle(t, live)
	if want := "options: {stopwords: '''english''', accept: '''false'''}"; !strings.Contains(out, want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
	if got, want := d.Functions["app.f()"].SetOrder, []string{"work_mem", "search_path", "lock_timeout"}; !reflect.DeepEqual(got, want) {
		t.Errorf("set order = %v, want %v\n%s", got, want, out)
	}
	if got, want := d.TSDictionaries["app.d"].OptionOrder, []string{"stopwords", "accept"}; !reflect.DeepEqual(got, want) {
		t.Errorf("option order = %v, want %v\n%s", got, want, out)
	}
}
