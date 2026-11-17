package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestExtensionComment(t *testing.T) {
	d := &schema.Database{Extensions: []*schema.Extension{{Name: "pg_trgm", IfNotExists: true, Comment: "trigram matching"}}}
	if sql := Render(Plan(emptyLive(), d, false)); !strings.Contains(sql, `comment on extension "pg_trgm" is 'trigram matching';`) {
		t.Errorf("new extension: missing comment in:\n%s", sql)
	}
	live := emptyLive()
	live.Extensions["pg_trgm"] = true
	live.ExtensionDefs = map[string]*LiveExtension{"pg_trgm": {Schema: "public", Comment: "text similarity", DefaultComment: "text similarity"}}
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, `comment on extension "pg_trgm" is 'trigram matching';`) {
		t.Errorf("changed comment: got:\n%s", sql)
	}
	live.ExtensionDefs["pg_trgm"].Comment = "trigram matching"
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("matching comment must not diff:\n%s", sql)
	}
	d.Extensions[0].Comment = ""
	live.ExtensionDefs["pg_trgm"].Comment = "anything"
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("unset comment is unmanaged:\n%s", sql)
	}
	d.Extensions[0].Comment = "trigram matching"
	if sql := Render(Plan(Simulate(emptyLive(), d), d, false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}

func TestExtensionCommentNull(t *testing.T) {
	d := &schema.Database{Extensions: []*schema.Extension{{Name: "hstore", NoComment: true}}}
	wantSQL(t, Render(Plan(emptyLive(), d, false)),
		`create extension "hstore";`,
		`comment on extension "hstore" is null;`,
	)
	live := emptyLive()
	live.Extensions["hstore"] = true
	live.ExtensionDefs = map[string]*LiveExtension{"hstore": {Schema: "public", Comment: "data type for storing sets of (key, value) pairs", DefaultComment: "data type for storing sets of (key, value) pairs"}}
	wantSQL(t, Render(Plan(live, d, false)), `comment on extension "hstore" is null;`)
	live.ExtensionDefs["hstore"].Comment = ""
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("an extension without a comment matches comment: null, got:\n%s", sql)
	}
	if sql := Render(Plan(Simulate(emptyLive(), d), d, false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}
