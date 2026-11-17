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

func TestIntegrationLockWarnings(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	if _, err := pool.Exec(ctx, `create table "`+sch+`".old_events (id int primary key, kind text); insert into "`+sch+`".old_events values (1, 'a')`); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(`
schema SCH:
  table events:
    renamedFrom: old_events
    columns:
      id: {type: int}
      kind: {type: text}
    primaryKey: [id]
    indexes:
      events_kind_idx: {columns: [kind]}
`, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.Plan(live, d, false)
	msgs := []string{}
	for _, w := range plan.Warnings {
		msgs = append(msgs, w.Message)
	}
	all := strings.Join(msgs, "\n")
	wants := []string{"CREATE INDEX events_kind_idx blocks writes to " + sch + ".events"}
	if live.ServerVersion < 180000 {
		wants = append(wants, "SET NOT NULL on "+sch+".events.kind")
	} else if strings.Contains(all, "SET NOT NULL") || !strings.Contains(diff.Render(plan), `not null "kind" not valid`) {
		t.Errorf("PostgreSQL 18 adds the not-null constraint NOT VALID instead of warning about SET NOT NULL:\n%s\n%s", all, diff.Render(plan))
	}
	for _, want := range wants {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s\nplan:\n%s", want, all, diff.Render(plan))
		}
	}
	applyPlan(t, pool, plan)
}

// TestIntegrationLockWarningVolatileDefault adds columns whose defaults call
// extension functions and functions the YAML declares: the volatile ones
// rewrite the table, whether the catalog or the YAML says they are.
func TestIntegrationLockWarningVolatileDefault(t *testing.T) {
	admin := connect(t)
	pool := freshDatabase(t, admin, "pgytest_lock_volatile")
	const base = `
extensions:
  - {name: pgcrypto, schema: public}
  - {name: uuid-ossp, schema: ext}
schema app:
  function stamp():
    returns: text
    language: sql
    stable: true
    body: select 'x'
  table t:
    columns:
      id: {type: int}
COLUMNS
    primaryKey: [id]
`
	convergeDatabase(t, pool, loadYAML(t, "app", strings.Replace(base, "COLUMNS", "", 1)))
	d := loadYAML(t, "app", strings.Replace(base, "COLUMNS", `
      token: {type: bytea, default: gen_random_bytes(8)}
      salt: {type: text, default: "gen_salt('bf')"}
      ref: {type: uuid, default: ext.uuid_generate_v1mc()}
      v4: {type: uuid, default: ext.uuid_generate_v4()}
      minted: {type: text, default: app.mint()}
      sha: {type: bytea, default: "digest('x', 'sha256')"}
      zero: {type: uuid, default: ext.uuid_nil()}
      stamped: {type: text, default: app.stamp()}`, 1)+`
  function mint():
    returns: text
    language: sql
    body: select 'x'
`)
	plan := diff.PlanWith(introspect(t, pool, d), d, diff.Options{})
	warned := map[string]bool{}
	for _, w := range plan.Warnings {
		if strings.Contains(w.Message, "with a volatile default rewrites app.t") {
			warned[strings.Fields(strings.TrimPrefix(w.Message, "adding column "))[0]] = true
		}
	}
	for _, cn := range []string{"token", "salt", "ref", "v4", "minted"} {
		if !warned[cn] {
			t.Errorf("column %s has a volatile default; warned: %v", cn, warned)
		}
	}
	for _, cn := range []string{"sha", "zero", "stamped"} {
		if warned[cn] {
			t.Errorf("column %s has an immutable or stable default; warned: %v", cn, warned)
		}
	}
	applyPlan(t, pool, plan)
}
