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
