package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestRecreateColumnDependents(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"b": {Type: "integer"}, "c": {Type: "integer"}})
	live.Schemas["app"] = true
	live.Views = map[string]bool{"app.v": true, "app.vv": true}
	live.CurrentUser = "owner"
	live.RelComments = map[string]string{"app.v": "kept"}
	live.TableGrants = map[string]map[string]map[string]bool{"app.vv": {"reader": {"select": true}}}
	live.Owners = map[string]string{"rel:app.v": "other"}
	live.Tables["app.t"].Triggers = map[string]string{"t_b": "CREATE TRIGGER t_b ..."}
	live.ColumnDependents = map[string][]ColumnDependent{
		"app.t.b":   {{Kind: "view", Name: "app.v", Table: "app.v"}, {Kind: "trigger", Name: "t_b", Table: "app.t"}},
		"app.t.c":   {{Kind: "view", Name: "app.v", Table: "app.v"}},
		"app.v.b":   {{Kind: "view", Name: "app.vv", Table: "app.vv"}},
	}
	d := &schema.Database{
		Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"b": {Type: "bigint"}, "c": {Type: "bigint"}},
			Triggers: []*schema.Trigger{{Name: "t_b", Timing: "before", Events: []string{"update of b"}, Level: "row", Procedure: "app.touch()"}}}},
		Views: map[string]*schema.View{"app.v": {Name: "v", Query: "select b, c from app.t"}, "app.vv": {Name: "vv", Query: "select b from app.v"}},
	}
	out, _, pre, post, warns := recreateColumnDependents(live, d, Options{Unsafe: true})
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	want := `drop trigger "t_b" on "app"."t";|drop view "app"."vv";|drop view "app"."v";`
	if got := strings.Join(pre["app.t"], "|"); got != want {
		t.Errorf("pre:\n got %s\nwant %s", got, want)
	}
	if out.Views["app.v"] || out.Tables["app.t"].Triggers["t_b"] != "" || !live.Views["app.v"] {
		t.Error("dependents must be removed from the returned copy only")
	}
	joined := strings.Join(post, "\n")
	for _, w := range []string{`grant select on table "app"."vv" to "reader";`, `comment on view "app"."v" is 'kept';`, `alter view "app"."v" owner to "other";`} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing %q in post:\n%s", w, joined)
		}
	}
	if _, _, pre, _, _ := recreateColumnDependents(live, d, Options{}); len(pre) != 0 {
		t.Error("without --unsafe types don't change, so nothing is dropped")
	}
	delete(d.Views, "app.vv")
	_, _, pre, _, warns = recreateColumnDependents(live, d, Options{Unsafe: true})
	if len(pre) != 0 || len(warns) != 2 || !strings.Contains(warns[0].Message, "view app.vv depends on it and isn't declared") {
		t.Errorf("undeclared view: pre %v, warnings %v", pre, warns)
	}
}
