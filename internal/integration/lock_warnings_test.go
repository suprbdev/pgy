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
	for _, want := range []string{"CREATE INDEX events_kind_idx blocks writes to " + sch + ".events", "SET NOT NULL on " + sch + ".events.kind"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s\nplan:\n%s", want, all, diff.Render(plan))
		}
	}
	applyPlan(t, pool, plan)
}
