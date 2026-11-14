package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

const pullFixture = `
extensions: [pg_trgm, btree_gist]
schema SCH:
  comment: pulled schema
  type mood:
    type: enum
    labels: [sad, ok, happy]
  type addr:
    type: composite
    attributes:
      street: {type: text}
      zip: {type: "varchar(10)"}
  type floatrange:
    type: range
    subtype: float8
    subtypeDiff: float8mi
  domain email:
    type: text
    notNull: true
    check: "VALUE ~ '@'"
    comment: an email
  collation ci:
    provider: icu
    locale: und-u-ks-level2
    deterministic: false
  sequence order_no:
    as: integer
    increment: 10
    start: 100
    cache: 5
  text search configuration simple_unaccent:
    copy: pg_catalog.simple
  function add_one(a int, b int default 1):
    returns: int
    language: sql
    immutable: true
    strict: true
    body: select a + b
    comment: adds
  function touch():
    returns: trigger
    language: plpgsql
    security: definer
    set: {search_path: "pg_catalog, public"}
    body: |
      begin
        new.updated_at = now();
        return new;
      end;
  procedure archive(n int):
    language: sql
    body: select n
  table orgs:
    columns:
      id: {type: bigint, identity: always}
      name: {type: text, collate: SCH.ci}
    primaryKey: [id]
  table users:
    columns:
      id: {type: serial}
      org_id: {type: bigint, nullable: true}
      email: {type: SCH.email}
      mood: {type: SCH.mood, default: "'ok'"}
      score: {type: numeric, default: -1.5}
      price: {type: numeric, nullable: true}
      qty: {type: int, nullable: true}
      total: {type: numeric, nullable: true, generated: price * qty}
      updated_at: {type: timestamptz, default: now()}
      period: {type: SCH.floatrange, nullable: true}
      note: {type: text, nullable: true, comment: free text}
    primaryKey: [id]
    with: {fillfactor: 90}
    foreignKeys:
      users_org_fkey:
        columns: [org_id]
        references: {table: SCH.orgs, columns: [id]}
        onDelete: set null
        onUpdate: cascade
        deferrable: true
    constraints:
      users_score_check: {type: check, expression: "score between -10 and 10"}
      users_email_key: {type: unique, columns: [email], nullsNotDistinct: true}
      users_period_excl: {type: exclude, expression: "using gist (period with &&)"}
    indexes:
      users_note_trgm: {columns: [note], using: gin, opclass: gin_trgm_ops}
      users_updated: {columns: [updated_at desc nulls last, id], include: [mood], where: "qty > 0"}
    triggers:
      users_touch:
        timing: before
        events: [update]
        level: row
        when: old.note is distinct from new.note
        procedure: SCH.touch()
    rules:
      users_no_delete: {event: delete, instead: true, do: nothing}
    rowLevelSecurity: true
    policies:
      users_visible: {for: select, using: "mood <> 'sad'"}
      users_guard: {as: restrictive, using: "true"}
    comment: people
  table events:
    columns:
      id: {type: bigint}
      at: {type: date}
    partitionBy: {type: range, columns: [at]}
  table events_2024:
    partitionOf: SCH.events
    forValues: {from: [2024-01-01], to: [2025-01-01]}
  table events_rest:
    partitionOf: SCH.events
    default: true
  view active_users:
    query: select id, email from SCH.users where qty > 0
    securityBarrier: true
    checkOption: local
  view active_ids:
    dependsOn: [view SCH.active_users]
    query: select id from SCH.active_users
  materialized view user_counts:
    query: select org_id, count(*) as n from SCH.users group by org_id
    indexes:
      user_counts_org: {columns: [org_id], unique: true}
`

func TestIntegrationPullRoundTrip(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.yml")
	if err := os.WriteFile(src, []byte(strings.ReplaceAll(pullFixture, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	desired, err := schema.LoadAndMerge([]string{src})
	if err != nil {
		t.Fatal(err)
	}
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	applyPlan(t, pool, diff.Plan(live, desired, true))

	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectDetails(ctx, pool, live); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(live, pull.Options{Schemas: []string{sch}})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		t.Logf("pull warning: %s", w)
	}
	outDir := filepath.Join(dir, "pulled")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, f := range res.Files {
		p := filepath.Join(outDir, f.Name)
		if err := os.WriteFile(p, f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	pulled, err := schema.LoadAndMerge(paths)
	if err != nil {
		for _, f := range res.Files {
			t.Logf("%s:\n%s", f.Name, f.Content)
		}
		t.Fatal(err)
	}
	for _, opts := range []diff.Options{{Unsafe: true}, {Unsafe: true, Prune: true}} {
		if sql := diff.Render(diff.PlanWith(live, pulled, opts)); sql != "" {
			for _, f := range res.Files {
				t.Logf("%s:\n%s", f.Name, f.Content)
			}
			t.Fatalf("pulled YAML must diff clean (%+v), got:\n%s", opts, sql)
		}
	}
}
