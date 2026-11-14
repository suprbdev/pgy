package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

// TestIntegrationYAMLIdempotent applies each YAML schema to a fresh schema
// and asserts that a second --unsafe diff against the result is empty. SCH in
// the YAML is replaced by the per-case schema name.
func TestIntegrationYAMLIdempotent(t *testing.T) {
	cases := map[string]string{
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
`,
	}
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
			for _, s := range append(append(plan.Creates, plan.Alters...), plan.Drops...) {
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
		})
	}
}
