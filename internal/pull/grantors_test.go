package pull

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestPullForeignGrantorsWarned(t *testing.T) {
	live := routineLive()
	live.CurrentUser = "pgy"
	addFunction(live, "f", "", "sql", "select 1", nil)
	live.FunctionGrants = map[string]map[string]map[string]bool{"app.f()": {"reader": {"execute": true}, "admin": {"execute*": true}}}
	live.Grantors = map[string]diff.LiveGrantors{
		"proc:app.f()":     {"reader": {"execute": {"", "admin"}}, "admin": {"execute*": {""}}},
		"proc:other.g()":   {"reader": {"execute": {"admin"}}},
		"schema:app":       {"reader": {"usage": {"pgy"}}},
		"language:plpgsql": {"reader": {"usage": {"admin"}}},
	}
	live.ColumnGrantors = map[string]map[string]diff.LiveGrantors{"app.t": {"c": {"reader": {"update*": {"admin"}}}}}
	res, err := Build(live, Options{Single: true, ExcludeSchemas: []string{"other"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, w := range res.Warnings {
		if strings.Contains(w, "grants blocks don't record grantors") {
			got = append(got, w)
		}
	}
	suffix := "; grants blocks don't record grantors, so a database built from this YAML gets these privileges granted by the owner or the connecting role"
	want := []string{
		"function app.f(): reader's execute, granted by admin" + suffix,
		"language plpgsql: reader's usage, granted by admin" + suffix,
		"column c of relation app.t: reader's update with grant option, granted by admin" + suffix,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pull warns about the grants of roles other than the owner and the connecting role in the pulled schemas:\n got %q\nwant %q", got, want)
	}
}
