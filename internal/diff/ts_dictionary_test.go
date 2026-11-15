package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestTSDictionaryOptionsAltered(t *testing.T) {
	td := &schema.TSDictionary{Template: "simple", Options: map[string]string{"stopwords": "english", "accept": "false"}}
	ld := &schema.TSDictionary{Template: "simple", Options: map[string]string{"stopwords": "'english'", "accept": "'true'", "extra": "'x'"}}
	stmts, warn := tsDictionaryAlterStmts("app.d", td, ld)
	if warn != "" || strings.Join(stmts, "\n") != `alter text search dictionary "app"."d" (accept = false, extra);` {
		t.Errorf("got %v %q", stmts, warn)
	}
	ld.Options = map[string]string{"stopwords": "'english'", "accept": "'false'"}
	if stmts, _ := tsDictionaryAlterStmts("app.d", td, ld); len(stmts) != 0 {
		t.Errorf("matching options must not diff: %v", stmts)
	}
	td.Options = nil
	ld.Template = "snowball"
	if stmts, warn := tsDictionaryAlterStmts("app.d", td, ld); len(stmts) != 0 || !strings.Contains(warn, "template changed") {
		t.Errorf("template change: %v %q", stmts, warn)
	}
}
