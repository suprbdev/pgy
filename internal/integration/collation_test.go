package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationCollationsIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"collations": `
schema SCH:
  collation ci:
    provider: icu
    locale: und-u-ks-level2
    deterministic: false
    comment: case-insensitive
  collation c_copy:
    from: pg_catalog.C
  table users:
    columns:
      email: {type: text, collate: SCH.ci}
      code: {type: text, collate: '"C"'}
      tag: {type: text, collate: SCH.c_copy}
`,
	})
}

func TestIntegrationCollationCaseInsensitiveAndPruned(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	d := &schema.Database{
		Tables: map[string]*schema.Table{sch + ".u": {Name: "u", Columns: map[string]*schema.Column{"e": {Type: "text", Collate: sch + ".ci"}},
			Indexes: []*schema.Index{{Name: "u_e", Columns: []string{"e"}, Unique: true}}}},
		Collations: map[string]*schema.Collation{sch + ".ci": {Name: "ci", Provider: "icu", Locale: "und-u-ks-level2", Deterministic: schema.BoolPtr(false)}},
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`create collation %q.stale (provider = icu, locale = 'und')`, sch)); err != nil {
		t.Fatal(err)
	}
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.PlanWith(live, d, diff.Options{Unsafe: true, Prune: true})
	applyPlan(t, pool, p)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`insert into %q.u values ('A@x.io')`, sch)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`insert into %q.u values ('a@X.IO')`, sch)); err == nil {
		t.Error("unique index on a nondeterministic ICU collation column must reject case variants")
	}
	var stale bool
	if err := pool.QueryRow(ctx, fmt.Sprintf(`select exists (select 1 from pg_collation where collnamespace = '%q'::regnamespace and collname = 'stale')`, sch)).Scan(&stale); err != nil || stale {
		t.Errorf("undeclared collation should be pruned (exists=%v err=%v)", stale, err)
	}
}
