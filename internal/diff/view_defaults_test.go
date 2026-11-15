package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestViewColumnDefaults(t *testing.T) {
	d := &schema.Database{Views: map[string]*schema.View{"app.v": {Name: "v", Query: "select 1 as a, 2 as b", ColumnDefaults: map[string]string{"a": "0", "b": "'x'"}}}}
	sql := Render(Plan(emptyLive(), d, false))
	for _, want := range []string{`alter view "app"."v" alter column "a" set default 0;`, `alter view "app"."v" alter column "b" set default 'x';`} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	live := emptyLive()
	live.Views = map[string]bool{"app.v": true}
	live.ViewColumnDefaults = map[string]map[string]string{"app.v": {"a": "0", "b": "'y'::text"}}
	sql = Render(Plan(live, d, false))
	if strings.Contains(sql, `"a" set default`) || !strings.Contains(sql, `"b" set default 'x';`) {
		t.Errorf("only the changed default must be set, got:\n%s", sql)
	}
	if sql := Render(Plan(Simulate(emptyLive(), d), d, false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}
