package validate

import (
	"strings"
	"testing"
)

func TestValidateEmptyRoutineBodies(t *testing.T) {
	p := write(t, t.TempDir(), "ok.yml", `
schema app:
  function noop():
    returns: void
    language: sql
    body: ""
  procedure noop_proc():
    language: sql
    body: ""
  function merge(a hstore):
    returns: integer
    language: plpython3u
    transform: [hstore]
    body: |2
          return len(a)
`)
	if issues := Files([]string{p}); len(issues) > 0 {
		t.Errorf("empty bodies and transform lists are valid:\n%s", messages(issues))
	}
}

func TestValidateEmptyBodyStillNeedsItsAttributes(t *testing.T) {
	p := write(t, t.TempDir(), "bad.yml", `
schema app:
  function noop():
    body: ""
  procedure no_body():
    language: sql
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{"function app.noop(): needs returns and language", "procedure app.no_body(): needs body and language"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
