package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

const expressionCollationsYAML = `
extensions: [pg_trgm, btree_gist, bloom, citext]
schema SCH:
  table zoo:
    columns:
      id: {type: bigint, primaryKey: true}
      ip: {type: inet, nullable: true}
      name: {type: text, nullable: true, collate: "C"}
      title: {type: text, nullable: true}
      ci: {type: citext, nullable: true}
      words: {type: "text[]", nullable: true}
      flag: {type: boolean, default: "true"}
    indexes:
      zoo_hash: {columns: ["(ip::text)"], using: hash, where: "flag = true"}
      zoo_hash_host: {columns: ["host(ip)"], using: hash}
      zoo_hash_c_column: {columns: ["lower(name)"], using: hash}
      zoo_hash_citext: {columns: ["lower(ci)"], using: hash}
      zoo_hash_array: {columns: ["string_to_array(title, ' ')"], using: hash}
      zoo_hash_explicit: {columns: ["(ip::text)"], using: hash, collations: {"(ip::text)": "C"}}
      zoo_hash_redundant: {columns: ["lower(name)"], using: hash, collations: {"lower(name)": "C"}}
      zoo_hash_default: {columns: ["lower(name)"], using: hash, collations: {"lower(name)": default}}
      zoo_btree: {columns: ["(ip::text)"]}
      zoo_btree_c_column: {columns: ["lower(name)"]}
      zoo_btree_column_default: {columns: [name], collations: {name: default}}
      zoo_btree_column_redundant: {columns: [name], collations: {name: "C"}}
      zoo_btree_two: {columns: ["lower(title)", "upper(name)"], collations: {"upper(name)": default}}
      zoo_gin_array: {columns: ["string_to_array(name, ' ')"], using: gin}
      zoo_gin_array_column: {columns: [words], using: gin}
      zoo_gin_trgm: {columns: ["lower(name)"], using: gin, opclass: gin_trgm_ops}
      zoo_gist_trgm: {columns: ["lower(title)"], using: gist, opclass: gist_trgm_ops, collations: {"lower(title)": "C"}}
      zoo_gist_text: {columns: ["lower(name)"], using: gist}
      zoo_spgist: {columns: ["lower(name)"], using: spgist}
      zoo_brin: {columns: ["lower(name)"], using: brin}
      zoo_brin_bloom: {columns: ["upper(title)"], using: brin, opclass: text_bloom_ops}
      zoo_bloom: {columns: ["lower(name)"], using: bloom}
`

func TestIntegrationIndexExpressionCollationsIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{"expression_collations": expressionCollationsYAML})
}

func TestIntegrationIndexExpressionCollationsPull(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(expressionCollationsYAML, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	applyPlan(t, pool, diff.Plan(introspect(t, pool, d), d, true))
	live := introspect(t, pool, d)
	if err := diff.IntrospectDetails(ctx, pool, live); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(live, pull.Options{Schemas: []string{sch}})
	if err != nil {
		t.Fatal(err)
	}
	var pulled string
	for _, f := range res.Files {
		pulled += string(f.Content)
	}
	collations := []string{}
	for _, line := range strings.Split(pulled, "\n") {
		if strings.Contains(line, "collations:") {
			collations = append(collations, strings.TrimSpace(line))
		}
	}
	want := []string{
		"collations: {name: default}",
		"collations: {upper(name): default}",
		"collations: {lower(title): C}",
		"collations: {lower(name): default}",
		"collations: {'(ip::text)': C}",
	}
	if strings.Join(collations, "\n") != strings.Join(want, "\n") {
		t.Errorf("pulled collations:\n%s\nwant only the explicit ones:\n%s\npulled YAML:\n%s", strings.Join(collations, "\n"), strings.Join(want, "\n"), pulled)
	}
}

func TestIntegrationIndexExpressionCollationDrift(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	desired := &schema.Database{Tables: map[string]*schema.Table{
		sch + ".zoo": {Name: "zoo", Columns: map[string]*schema.Column{"ip": {Type: "inet", Nullable: true}},
			Indexes: []*schema.Index{
				{Name: "zoo_hash", Columns: []string{"(ip::text)"}, Using: "hash"},
				{Name: "zoo_hash_c", Columns: []string{"(ip::text)"}, Using: "hash", Collations: map[string]string{"(ip::text)": "C"}},
			}},
	}}
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
	drift := fmt.Sprintf(`
drop index %[1]q.zoo_hash;
create index zoo_hash on %[1]q.zoo using hash ((ip::text) collate "C");
drop index %[1]q.zoo_hash_c;
create index zoo_hash_c on %[1]q.zoo using hash ((ip::text));`, sch)
	if _, err := pool.Exec(ctx, drift); err != nil {
		t.Fatal(err)
	}
	if sql := plan(false); sql != "" {
		t.Errorf("changed collations are gated behind --unsafe, got:\n%s", sql)
	}
	sql := plan(true)
	for _, want := range []string{
		fmt.Sprintf(`drop index %q."zoo_hash";`, sch),
		`using hash(((ip::text)));`,
		fmt.Sprintf(`drop index %q."zoo_hash_c";`, sch),
		`using hash(((ip::text)) collate "C");`,
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
}
