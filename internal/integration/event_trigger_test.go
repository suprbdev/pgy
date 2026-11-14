package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationEventTriggers(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	name := sch + "_et"
	t.Cleanup(func() { pool.Exec(context.Background(), fmt.Sprintf("drop event trigger if exists %q", name)) }) //nolint

	desired := func(tags []string, enabled string) *schema.Database {
		return &schema.Database{
			Tables: map[string]*schema.Table{sch + ".ddl_log": {Name: "ddl_log", Columns: map[string]*schema.Column{"tag": {Type: "text"}}}},
			Functions: map[string]*schema.Function{sch + ".log_ddl()": {Schema: sch, Name: "log_ddl", ArgsSig: "()", Returns: "event_trigger", Language: "plpgsql",
				Body: fmt.Sprintf("begin insert into %q.ddl_log values (tg_tag); end;", sch), DependsOn: []string{"table " + sch + ".ddl_log"}}},
			EventTriggers: map[string]*schema.EventTrigger{name: {Name: name, Event: "ddl_command_end", Tags: tags, Function: sch + ".log_ddl()", Enabled: enabled, Comment: "audit"}},
		}
	}
	plan := func(d *schema.Database) *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, d, false)
	}
	steps := []*schema.Database{
		desired([]string{"create sequence"}, ""),
		desired([]string{"CREATE SEQUENCE", "drop sequence"}, "always"),
		desired([]string{"CREATE SEQUENCE", "DROP SEQUENCE"}, "disabled"),
	}
	for i, d := range steps {
		applyPlan(t, pool, plan(d))
		if sql := diff.Render(plan(d)); sql != "" {
			t.Fatalf("step %d: expected no changes after apply, got:\n%s", i, sql)
		}
		if i == 1 {
			if _, err := pool.Exec(ctx, fmt.Sprintf("create sequence %q.s1; drop sequence %q.s1", sch, sch)); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := pool.QueryRow(ctx, fmt.Sprintf("select count(*) from %q.ddl_log", sch)).Scan(&n); err != nil || n != 2 {
				t.Errorf("event trigger should have logged 2 commands, got %d (%v)", n, err)
			}
		}
	}
}
