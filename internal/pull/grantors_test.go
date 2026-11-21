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
		if strings.Contains(w, "no grantedBy block") {
			got = append(got, w)
		}
	}
	suffix := "; the YAML has no grantedBy block for them, so a database built from it gets these privileges granted by the owner or the connecting role"
	want := []string{
		"language plpgsql: reader's usage, granted by admin" + suffix,
		"column c of relation app.t: reader's update with grant option, granted by admin" + suffix,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pull warns about the grants of roles other than the owner and the connecting role that it writes no grantedBy block for:\n got %q\nwant %q", got, want)
	}
}

func TestPullGrantedByWritten(t *testing.T) {
	live := routineLive()
	live.CurrentUser = "pgy"
	addFunction(live, "f", "", "sql", "select 1", nil)
	live.FunctionGrants = map[string]map[string]map[string]bool{"app.f()": {"reader": {"execute": true}, "writer": {"execute": true}, "admin": {"execute*": true}}}
	live.Grantors = map[string]diff.LiveGrantors{
		"proc:app.f()": {"reader": {"execute": {"", "admin"}}, "writer": {"execute": {"admin"}}, "admin": {"execute*": {""}}},
		"schema:app":   {"reader": {"usage": {"pgy"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Files[0].Content)
	for _, want := range []string{
		"grants: {admin: [execute with grant option], reader: [execute]}",
		"grantedBy: {admin: {reader: [execute], writer: [execute]}}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pull writes %q: the grants of a role other than the owner and the connecting role go in a grantedBy block, a privilege the owner grants too stays in grants:\n%s", want, out)
		}
	}
	if strings.Count(out, "grantedBy") != 1 {
		t.Errorf("a grant of the connecting role gets no grantedBy block:\n%s", out)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "granted by") {
			t.Errorf("pull warns about a grant its grantedBy block writes: %s", w)
		}
	}
}
