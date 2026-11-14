package integration

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/pull"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationYAMLIdempotent(t *testing.T) {
	cases := map[string]string{
		"long_names": `
schema SCH:
  table a_really_quite_long_table_name_for_testing:
    columns:
      some_really_long_column_name_here: {type: text, unique: true}
      other: {type: text}
    indexes:
      "":
        columns: [some_really_long_column_name_here]
        comment: auto-named
      an_index_name_that_is_definitely_longer_than_sixty_three_bytes_long:
        columns: [other]
    constraints:
      a_check_constraint_name_that_is_definitely_longer_than_sixty_three:
        type: check
        expression: "other <> ''"
`,
		"index_ordering": `
schema SCH:
  table t:
    columns:
      created: {type: timestamptz}
      name: {type: text}
    indexes:
      t_created_desc:
        columns: [created desc nulls last, lower(name) asc]
`,
		"overloads": `
schema SCH:
  function area(r float8):
    returns: float8
    language: sql
    body: select pi() * r * r
  function area(w float8, h float8):
    returns: float8
    language: sql
    body: select w * h
    comment: rectangle
  procedure note(msg text):
    language: sql
    body: select 1
  procedure note(msg text, n int):
    language: sql
    body: select 1
  view areas:
    dependsOn: [function SCH.area]
    query: select SCH.area(1) as c, SCH.area(2, 3) as r
`,
		"fk_constraint_options": `
schema SCH:
  table p:
    columns:
      id: {type: int, primaryKey: true}
  table c:
    columns:
      id: {type: int}
      pid: {type: int, nullable: true}
      a: {type: int, nullable: true}
      b: {type: int, nullable: true}
    foreignKeys:
      c_pid_fkey:
        columns: [pid]
        references: {table: SCH.p, columns: [id]}
        match: full
        onUpdate: cascade
        onDelete: set null
        initiallyDeferred: true
        notValid: true
      c_pid_fkey2:
        columns: [pid]
        references: {table: SCH.p, columns: [id]}
        onDelete: no action
        deferrable: true
    constraints:
      c_a_check:
        type: check
        expression: a > 0
        noInherit: true
        notValid: true
      c_ab_key:
        type: unique
        columns: [a, b]
        initiallyDeferred: true
      c_b_key:
        type: unique
        columns: [b]
        nullsNotDistinct: true
`,
		"generated_columns": `
schema SCH:
  table t:
    columns:
      price: {type: numeric}
      qty: {type: int}
      name: {type: text}
      total: {type: numeric, nullable: true, generated: "price * qty"}
      lname: {type: text, nullable: true, generated: "lower(name)"}
    indexes:
      t_total:
        columns: [total desc]
`,
		"defaults": `
schema SCH:
  table t:
    columns:
      id: {type: int}
      n: {type: int, default: -1}
      x: {type: numeric, default: -1.5}
      iv: {type: timestamptz, default: "now() + interval '1 day'"}
      d: {type: date, default: "current_date"}
      f: {type: float}
      ts: {type: timestamptz, default: "CURRENT_TIMESTAMP"}
      arr: {type: "text[]", default: "'{}'"}
      j: {type: jsonb, default: "'{}'::jsonb"}
`,
		"partition": `
schema SCH:
  table parent:
    columns:
      id: {type: int}
      created: {type: date}
    partitionBy: {type: range, columns: [created]}
  table child:
    partitionOf: parent
    forValues: {from: [2024-01-01], to: [2025-01-01]}
`,
		"pubtype": `
schema public:
  type SCH_status:
    type: enum
    labels: [a, b]
  table SCH_t:
    columns:
      s: {type: public.SCH_status}
`,
		"constraints": `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      status: {type: text}
      email: {type: text}
      amt: {type: numeric}
      p: {type: int, nullable: true}
    constraints:
      t_status_check:
        type: check
        expression: "status in ('a', 'b')"
      t_email_check:
        type: check
        expression: "email ~* '^.+@.+$'"
      t_amt_check:
        type: check
        expression: "amt between 0 and 100"
      t_u:
        type: unique
        columns: [email, status]
    foreignKeys:
      t_p_fkey:
        columns: [p]
        references: {table: SCH.t, columns: [id]}
        onDelete: set null
    indexes:
      t_lower_email:
        columns: ["lower(email)"]
        where: "status <> 'b'"
`,
		"triggers_policies": `
schema SCH:
  function touch():
    returns: trigger
    language: plpgsql
    body: "begin return new; end;"
  table t:
    columns:
      id: {type: int}
      a: {type: int}
    triggers:
      t_upd_of:
        timing: before
        events: [update of a, insert]
        level: row
        procedure: SCH.touch()
      t_stmt:
        timing: after
        events: [truncate]
        level: statement
        procedure: SCH.touch()
      t_audit:
        timing: after
        events: [update]
        referencing: {old: old_rows, new: new_rows}
        procedure: SCH.touch()
    rowLevelSecurity: true
    policies:
      p1:
        for: select
        using: "a > 0 and id in (select 1)"
      p2:
        for: update
        to: [pgy]
        using: "true"
        withCheck: "a is not null"
      p3:
        for: insert
        to: public
        withCheck: "a > 0"
      p4:
        as: restrictive
        using: "id <> 0"
`,
	}
	assertYAMLIdempotent(t, cases)
}

// assertYAMLIdempotent applies each YAML case (SCH replaced by a fresh
// per-case schema) with an --unsafe plan and fails if a second --unsafe plan
// against the result is not empty.
func assertYAMLIdempotent(t *testing.T, cases map[string]string) {
	t.Helper()
	pool := connect(t)
	ctx := context.Background()
	for name, y := range cases {
		t.Run(name, func(t *testing.T) {
			sch := freshSchema(t, pool)
			y = strings.ReplaceAll(y, "SCH", sch)
			p := filepath.Join(t.TempDir(), "s.yml")
			if err := os.WriteFile(p, []byte(y), 0o644); err != nil {
				t.Fatal(err)
			}
			d, err := schema.LoadAndMerge([]string{p})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				pool.Exec(context.Background(), "drop table if exists public."+sch+"_t; drop type if exists public."+sch+"_status") //nolint
			})
			live, err := diff.Introspect(ctx, pool)
			if err != nil {
				t.Fatal(err)
			}
			plan := diff.Plan(live, d, true)
			for _, s := range append(append(append(plan.Creates, plan.Alters...), plan.Drops...), plan.Concurrent...) {
				if _, err := pool.Exec(ctx, s); err != nil {
					t.Fatalf("apply %q: %v", s, err)
				}
			}
			live, err = diff.Introspect(ctx, pool)
			if err != nil {
				t.Fatal(err)
			}
			if sql := diff.Render(diff.Plan(live, d, true)); sql != "" {
				t.Errorf("second plan not empty:\n%s", sql)
			}
			assertPullRoundTrip(t, ctx, pool, live, sch)
		})
	}
}

// assertPullRoundTrip pulls schema sch from the database and fails unless
// the pulled YAML diffs clean against it, with --unsafe and with --prune.
func assertPullRoundTrip(t *testing.T, ctx context.Context, pool *pgxpool.Pool, live *diff.Live, sch string) {
	t.Helper()
	if err := diff.IntrospectDetails(ctx, pool, live); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(live, pull.Options{Schemas: []string{sch}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := []string{}
	for _, f := range res.Files {
		p := filepath.Join(dir, f.Name)
		if err := os.WriteFile(p, f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	pulled, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, opts := range []diff.Options{{Unsafe: true}, {Unsafe: true, Prune: true}} {
		if sql := diff.Render(diff.PlanWith(live, pulled, opts)); sql != "" {
			for _, f := range res.Files {
				t.Logf("%s:\n%s", f.Name, f.Content)
			}
			t.Errorf("pulled YAML must diff clean (%+v), got:\n%s", opts, sql)
			return
		}
	}
}

func TestIntegrationQuotedIdentifiersIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"quotes": `
schema SCH:
  'table odd"table':
    columns:
      'col"umn': {type: text, comment: it's quoted}
      Mixed: {type: int}
    indexes:
      'odd"idx': {columns: [Mixed]}
    comment: it's quoted
`,
		"triggers": `
schema SCH:
  function touch():
    returns: trigger
    language: plpgsql
    body: begin return null; end
  table Order Items:
    columns:
      Weird Col: {type: int}
    triggers:
      Upd Trg: {timing: before, events: [update of "Weird Col"], level: row, when: "old.\"Weird Col\" is distinct from new.\"Weird Col\"", procedure: SCH.touch()}
      Ref Trg: {timing: after, events: [update], referencing: {old: Old Rows, new: new_rows}, procedure: SCH.touch()}
`,
	})
}
