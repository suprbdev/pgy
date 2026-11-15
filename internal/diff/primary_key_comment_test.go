package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func pkCommentTable(name, comment string) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}},
		PrimaryKey: []string{"id"}, PrimaryKeyName: name, PrimaryKeyComment: comment,
	}}}
}

func TestPrimaryKeyCommentCreate(t *testing.T) {
	for name, want := range map[string]string{
		"":      `comment on constraint "t_pkey" on "app"."t" is 'the key';`,
		"t_key": `comment on constraint "t_key" on "app"."t" is 'the key';`,
	} {
		if sql := Render(Plan(emptyLive(), pkCommentTable(name, "the key"), false)); !strings.Contains(sql, want) {
			t.Errorf("name %q: missing %q in:\n%s", name, want, sql)
		}
	}
}

func TestPrimaryKeyCommentDiffedAgainstLive(t *testing.T) {
	live := liveWithTablePK("app.t", map[string]*LiveColumn{"id": {Type: "integer"}})
	live.Schemas["app"] = true
	lt := live.Tables["app.t"]
	lt.ConstraintDefs = map[string]string{"t_old": "PRIMARY KEY (id)"}
	lt.ConstraintComments = map[string]string{"t_old": "the key"}
	lt.Constraints["t_old"] = true
	if sql := Render(Plan(live, pkCommentTable("", "the key"), false)); sql != "" {
		t.Errorf("matching comment on the live key must not diff, got:\n%s", sql)
	}
	if sql := Render(Plan(live, pkCommentTable("", "new note"), false)); !strings.Contains(sql, `comment on constraint "t_old" on "app"."t" is 'new note';`) {
		t.Errorf("want the comment on the live key's name, got:\n%s", sql)
	}
	sql := Render(Plan(live, pkCommentTable("t_key", "the key"), false))
	if !strings.Contains(sql, `rename constraint "t_old" to "t_key"`) || strings.Contains(sql, "comment on") {
		t.Errorf("a renamed key keeps its comment, got:\n%s", sql)
	}
}

func TestSimulatePrimaryKeyNameAndComment(t *testing.T) {
	live := liveWithTablePK("app.t", map[string]*LiveColumn{"id": {Type: "integer"}})
	live.Schemas["app"] = true
	lt := live.Tables["app.t"]
	lt.ConstraintDefs = map[string]string{"t_old": "PRIMARY KEY (id)"}
	lt.Constraints["t_old"] = true
	for _, base := range []*Live{emptyLive(), live} {
		d := pkCommentTable("t_key", "the key")
		if sql := Render(Plan(Simulate(base, d), d, false)); sql != "" {
			t.Errorf("expected no changes after simulate, got:\n%s", sql)
		}
	}
}
