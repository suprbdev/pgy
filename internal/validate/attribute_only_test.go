package validate

import (
	"strings"
	"testing"
)

func TestValidateAttributeOnlyDeclarations(t *testing.T) {
	p := write(t, t.TempDir(), "ok.yml", `
extensions:
  - {name: pgcrypto, schema: public}
  - {name: pg_stat_statements, schema: public}
  - {name: hstore, schema: public}
roles:
  app: {}
  monitoring: {}
schema public:
  function gen_random_bytes(integer):
    revokePublic: true
    grants: {app: [execute]}
  function crypt(text, text):
    parallel: restricted
    leakproof: true
  procedure tidy(integer):
    security: definer
    set: {search_path: public}
  view pg_stat_statements:
    grants: {monitoring: [select], public: []}
    comment: statement statistics
  type hstore:
    revokePublic: true
    grants: {app: [usage]}
`)
	if issues := Files([]string{p}); len(issues) > 0 {
		t.Errorf("declarations that manage an existing object are valid:\n%s", messages(issues))
	}
}

func TestValidateAttributeOnlyKeepsDefinitionsComplete(t *testing.T) {
	p := write(t, t.TempDir(), "bad.yml", `
schema app:
  function half():
    returns: int
    language: sql
  procedure half_proc():
    language: sql
  view options_only:
    securityInvoker: true
  type labels_only:
    labels: [a, b]
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"function half(): property 'body' is required, if 'returns' property exists",
		"function app.half(): needs body",
		"procedure half_proc(): property 'body' is required, if 'language' property exists",
		"procedure app.half_proc(): needs body",
		"view options_only: property 'query' is required, if 'securityInvoker' property exists",
		"type labels_only: property 'type' is required, if 'labels' property exists",
		"type app.labels_only: needs type: enum, composite or range",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestValidateExtensionTablesSequencesAggregates(t *testing.T) {
	p := write(t, t.TempDir(), "ok.yml", `
extensions:
  - {name: postgis, schema: public}
roles:
  app: {}
schema public:
  table spatial_ref_sys:
    grants: {app: [select, insert]}
    comment: spatial reference systems
    data:
      key: [srid]
      rows:
        - {srid: 990001, auth_name: local}
  sequence topology_seq:
    grants: {app: [usage]}
  aggregate st_union(geometry):
    revokePublic: true
    grants: {app: [execute]}
  aggregate percentile(float8 order by anyelement):
    grants: {app: [execute]}
`)
	if issues := Files([]string{p}); len(issues) > 0 {
		t.Errorf("declarations that manage an extension's table, sequence and aggregate are valid:\n%s", messages(issues))
	}
}

func TestValidateExtensionTablesSequencesAggregatesIncomplete(t *testing.T) {
	p := write(t, t.TempDir(), "bad.yml", `
schema app:
  table events:
    clusterOn: events_idx
  table config:
    data:
      rows:
        - {id: 1}
  aggregate half(integer):
    finalfunc: f
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"table app.events: has clusterOn but no columns, so it is created as a table without columns",
		"table app.config: data needs data.key on a table declared without columns",
		"aggregate half(integer): property 'sfunc' is required, if 'finalfunc' property exists",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
