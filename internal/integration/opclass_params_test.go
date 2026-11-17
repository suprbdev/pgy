package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationIndexOpclassParametersIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"spellings": `
extensions: [pg_trgm]
schema SCH:
  table doc:
    columns:
      id: {type: int, primaryKey: true}
      name: {type: text}
      last: {type: text}
      tsv: {type: tsvector, nullable: true}
      n: {type: int}
    indexes:
      doc_name: {columns: [name], using: gist, opclass: gist_trgm_ops(siglen=32)}
      doc_names:
        columns: [name, last]
        using: gist
        opclasses: {name: "gist_trgm_ops (siglen = '64')", last: gist_trgm_ops}
      doc_lower: {columns: [lower(name)], using: gist, opclass: "GIST_TRGM_OPS( SIGLEN = 48 )"}
      doc_tsv: {columns: [tsv], using: gist, opclass: tsvector_ops(siglen=100)}
      doc_n: {columns: [n], using: brin, opclass: "int4_bloom_ops(false_positive_rate=0.050, n_distinct_per_range=100)"}
  materialized view doc_summary:
    query: select id, name from SCH.doc
    indexes:
      doc_summary_sig: {columns: [name], using: gist, opclass: gist_trgm_ops(siglen=40)}
`,
		"array_expressions": `
schema SCH:
  table person:
    columns:
      id: {type: int, primaryKey: true}
      name: {type: text}
      tags: {type: "text[]"}
    indexes:
      person_words: {columns: ["string_to_array(name, ' ')"], using: gin}
      person_tags_removed: {columns: ["array_remove(tags, 'x')"], using: gin}
      person_tags: {columns: [tags], using: gin}
      person_words_default:
        columns: ["string_to_array(name, ',')"]
        using: gin
        collations: {"string_to_array(name, ',')": default}
      person_words_c:
        columns: ["string_to_array(name, ';')"]
        using: gin
        collations: {"string_to_array(name, ';')": C}
`,
	})
}

func TestIntegrationIndexOpclassParameterDrift(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	desired := &schema.Database{
		Extensions: []*schema.Extension{{Name: "pg_trgm", IfNotExists: true}},
		Tables: map[string]*schema.Table{
			sch + ".doc": {Name: "doc", Columns: map[string]*schema.Column{"name": {Type: "text"}, "tsv": {Type: "tsvector", Nullable: true}},
				Indexes: []*schema.Index{
					{Name: "doc_name", Columns: []string{"name"}, Using: "gist", Opclass: "gist_trgm_ops(siglen=32)"},
					{Name: "doc_tsv", Columns: []string{"tsv"}, Using: "gist"},
				}},
		},
	}
	plan := func(unsafe bool) string {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Render(diff.Plan(live, desired, unsafe))
	}
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	applyPlan(t, pool, diff.Plan(live, desired, true))
	if sql := plan(true); sql != "" {
		t.Fatalf("second plan not empty:\n%s", sql)
	}
	options := func(index string) string {
		t.Helper()
		var got string
		q := `select coalesce(array_to_string(a.attoptions, ','), '') from pg_attribute a where a.attrelid = $1::regclass and a.attnum = 1`
		if err := pool.QueryRow(ctx, q, fmt.Sprintf("%q.%s", sch, index)).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := options("doc_name"); got != "siglen=32" {
		t.Fatalf("created index has parameters %q, want siglen=32", got)
	}

	drift := fmt.Sprintf(`
drop index %[1]q.doc_name;
create index doc_name on %[1]q.doc using gist (name gist_trgm_ops (siglen = 64));
drop index %[1]q.doc_tsv;
create index doc_tsv on %[1]q.doc using gist (tsv tsvector_ops (siglen = 100));`, sch)
	if _, err := pool.Exec(ctx, drift); err != nil {
		t.Fatal(err)
	}
	if sql := plan(false); sql != "" {
		t.Errorf("changed opclass parameters are gated behind --unsafe, got:\n%s", sql)
	}
	sql := plan(true)
	for _, want := range []string{
		fmt.Sprintf(`drop index %q."doc_name";`, sch),
		`using gist("name" gist_trgm_ops(siglen=32))`,
		fmt.Sprintf(`drop index %q."doc_tsv";`, sch),
		`using gist("tsv");`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	applyPlan(t, pool, diff.Plan(live, desired, true))
	if sql := plan(true); sql != "" {
		t.Errorf("plan after repairing the drift not empty:\n%s", sql)
	}
	if got := options("doc_name"); got != "siglen=32" {
		t.Errorf("repaired index has parameters %q, want siglen=32", got)
	}
	if got := options("doc_tsv"); got != "" {
		t.Errorf("repaired default-opclass index has parameters %q, want none", got)
	}
}
