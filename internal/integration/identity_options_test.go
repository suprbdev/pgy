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

func TestIntegrationIdentityOptionsIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"identity": `
schema SCH:
  table t:
    columns:
      id: {type: bigint, identity: {generated: always, start: 1000, increment: 5, cache: 10}}
      small: {type: smallint, identity: {generated: by default, maxValue: 999, cycle: true}}
      down: {type: int, identity: {increment: -1, minValue: -100, maxValue: -1, start: -1}}
      plain: {type: int, identity: by default}
`,
	})
}

func TestIntegrationIdentityOptionsAltered(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	load := func(y string) *schema.Database {
		t.Helper()
		p := filepath.Join(t.TempDir(), "s.yml")
		if err := os.WriteFile(p, []byte(strings.ReplaceAll(y, "SCH", sch)), 0o644); err != nil {
			t.Fatal(err)
		}
		d, err := schema.LoadAndMerge([]string{p})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	converge := func(d *schema.Database) {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		applyPlan(t, pool, diff.Plan(live, d, true))
		live, err = diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		if sql := diff.Render(diff.Plan(live, d, true)); sql != "" {
			t.Fatalf("plan did not converge:\n%s", sql)
		}
	}
	converge(load(`
schema SCH:
  table t:
    columns:
      id: {type: bigint, identity: {generated: always, start: 10}}
      n: {type: int, identity: by default}
      legacy: {type: int, default: "0"}
`))
	if _, err := pool.Exec(ctx, "insert into "+sch+".t default values"); err != nil {
		t.Fatal(err)
	}
	converge(load(`
schema SCH:
  table t:
    columns:
      id: {type: bigint, identity: {generated: by default, start: 10, increment: 10, cycle: true}}
      n: {type: int}
      legacy: {type: int, identity: {generated: always, start: 100}}
`))
	var id, legacy int
	if err := pool.QueryRow(ctx, "insert into "+sch+".t (n) values (1) returning id, legacy").Scan(&id, &legacy); err != nil {
		t.Fatal(err)
	}
	if id != 20 || legacy != 100 {
		t.Errorf("id = %d (want 20 after increment 10), legacy = %d (want identity start 100)", id, legacy)
	}
}
