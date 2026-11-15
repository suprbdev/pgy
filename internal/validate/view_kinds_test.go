package validate

import (
	"strings"
	"testing"
)

func TestValidateViewAndMaterializedViewOnlyKeys(t *testing.T) {
	p := write(t, t.TempDir(), "v.yml", `
schema app:
  table t:
    columns: {a: {type: int}}
  view v:
    query: select a from app.t
    withData: false
    indexes: {v_a: {columns: [a]}}
  materialized view mv:
    query: select a from app.t
    securityInvoker: true
    checkOption: local
    rules: {mv_r: {event: delete, instead: true, do: nothing}}
  materialized view ok:
    query: select a from app.t
    withData: false
    indexes: {ok_a: {columns: [a]}}
  view ok2:
    query: select a from app.t
    securityInvoker: true
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"view app.v: indexes are for materialized views",
		"view app.v: withData is for materialized views and is ignored",
		"materialized view app.mv: securityInvoker is for views and is ignored",
		"materialized view app.mv: checkOption is for views and is ignored",
		"materialized view app.mv: rules are for views",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "app.ok") {
		t.Errorf("valid views flagged:\n%s", got)
	}
}
