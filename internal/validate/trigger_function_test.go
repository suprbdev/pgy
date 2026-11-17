package validate

import (
	"strings"
	"testing"
)

const triggerFunctionTables = `
schema app:
  table doc:
    columns:
      id: {type: bigint, primaryKey: true}
      tsv: {type: tsvector, nullable: true}
      body: {type: text}
      updated_at: {type: timestamptz}
    triggers:
      noop: {timing: before, events: [update], level: row, procedure: suppress_redundant_updates_trigger()}
      noop_q: {timing: before, events: [update], level: row, procedure: pg_catalog.suppress_redundant_updates_trigger()}
      tsv: {timing: before, events: [insert, update], level: row, procedure: "tsvector_update_trigger(tsv, 'pg_catalog.english', body)"}
      mdt: {timing: before, events: [update], level: row, procedure: moddatetime(updated_at)}
      mdt_public: {timing: before, events: [update], level: row, procedure: public.moddatetime(updated_at)}
      mdt_ext: {timing: before, events: [update], level: row, procedure: ext.moddatetime(updated_at)}
      typo: {timing: before, events: [update], level: row, procedure: app.tuoch()}
`

func triggerFunctionWarnings(t *testing.T, yml string) string {
	t.Helper()
	p := write(t, t.TempDir(), "s.yml", yml)
	out := []string{}
	for _, i := range Files([]string{p}) {
		if strings.Contains(i.String(), "which no schema file declares") {
			out = append(out, i.String())
		}
	}
	return strings.Join(out, "\n")
}

func TestValidateTriggerFunctionWithoutExtensions(t *testing.T) {
	got := triggerFunctionWarnings(t, triggerFunctionTables)
	for _, name := range []string{"noop", "noop_q", "tsv"} {
		if strings.Contains(got, "trigger "+name+" calls") {
			t.Errorf("built-in trigger function of %s must not be flagged:\n%s", name, got)
		}
	}
	for _, name := range []string{"mdt", "mdt_public", "mdt_ext", "typo"} {
		if !strings.Contains(got, "trigger "+name+" calls") {
			t.Errorf("undeclared function of %s must be flagged when no extension could provide it:\n%s", name, got)
		}
	}
}

func TestValidateTriggerFunctionFromExtension(t *testing.T) {
	got := triggerFunctionWarnings(t, "extensions: [moddatetime]\n"+triggerFunctionTables)
	for _, name := range []string{"mdt", "mdt_public"} {
		if strings.Contains(got, "trigger "+name+" calls") {
			t.Errorf("%s may call a function of the declared extension:\n%s", name, got)
		}
	}
	for _, name := range []string{"mdt_ext", "typo"} {
		if !strings.Contains(got, "trigger "+name+" calls") {
			t.Errorf("%s names a schema no declared extension is installed in:\n%s", name, got)
		}
	}

	got = triggerFunctionWarnings(t, "extensions:\n  - {name: moddatetime, schema: ext}\n"+triggerFunctionTables)
	for _, name := range []string{"mdt", "mdt_ext"} {
		if strings.Contains(got, "trigger "+name+" calls") {
			t.Errorf("%s may call a function of the extension in schema ext:\n%s", name, got)
		}
	}
	for _, name := range []string{"mdt_public", "typo"} {
		if !strings.Contains(got, "trigger "+name+" calls") {
			t.Errorf("%s names a schema no declared extension is installed in:\n%s", name, got)
		}
	}
}
