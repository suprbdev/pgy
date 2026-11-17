package pull

import (
	"reflect"
	"strings"
	"testing"
)

func TestPullViewColumnGrants(t *testing.T) {
	live := routineLive()
	live.Views = map[string]bool{"app.v": true}
	live.MatViews = map[string]bool{"app.mv": true}
	live.Details.ViewQueries = map[string]string{"app.v": " SELECT 1 AS a,\n    2 AS b;", "app.mv": " SELECT 1 AS id;"}
	live.Details.Populated = map[string]bool{"app.mv": true}
	live.ColumnGrants = map[string]map[string]map[string]map[string]bool{
		"app.v":  {"a": {"reader": {"select": true}, "editor": {"update*": true, "select": true}}},
		"app.mv": {"id": {"reader": {"select": true}}},
	}
	out, d, _ := buildSingle(t, live)
	if !strings.Contains(out, "columnGrants:") {
		t.Fatalf("view column grants are pulled:\n%s", out)
	}
	if got, want := d.Views["app.v"].ColumnGrants, map[string]map[string][]string{"a": {"editor": {"select", "update*"}, "reader": {"select"}}}; !reflect.DeepEqual(got, want) {
		t.Errorf("view column grants = %v, want %v\n%s", got, want, out)
	}
	if got, want := d.Views["app.mv"].ColumnGrants, map[string]map[string][]string{"id": {"reader": {"select"}}}; !reflect.DeepEqual(got, want) {
		t.Errorf("materialized view column grants = %v, want %v\n%s", got, want, out)
	}
}
