package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

const routineArgTypmods = `
schema SCH:
  function f(x varchar(10), n numeric(6,2) default 1.5):
    returns: int
    language: sql
    body: select 1
  function g(t timestamp(0), out a interval day to second(3), inout b bit(3)):
    returns: record
    language: sql
    body: select interval '1 day', b
  function h(char(5), timestamptz(2)[]):
    returns: "varchar(4)"
    language: sql
    body: select 'abc'::varchar
  procedure p(x numeric(6,2), out y varchar(3)):
    language: sql
    body: select 'a'
  aggregate agg(numeric(6,2)):
    sfunc: numeric_add
    stype: numeric
`

// TestIntegrationRoutineArgTypmods declares routines whose argument types
// carry modifiers, which PostgreSQL discards: once created they must not be
// planned again, with or without --unsafe.
func TestIntegrationRoutineArgTypmods(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	applied := convergeYAML(t, pool, sch, routineArgTypmods)
	if !strings.Contains(applied, `(x varchar(10), n numeric(6,2) default 1.5)`) {
		t.Errorf("the CREATE keeps the declared modifiers, got:\n%s", applied)
	}
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(routineArgTypmods, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.Plan(introspect(t, pool, d), d, false)
	if sql := diff.Render(plan); sql != "" {
		t.Errorf("plain diff must be empty, got:\n%s", sql)
	}
	for _, w := range plan.Warnings {
		t.Errorf("unexpected warning: %s", w.Message)
	}
}
