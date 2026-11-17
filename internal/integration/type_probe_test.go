package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
)

// typeProbeYAML declares built-in types in spellings PostgreSQL prints
// another way, on columns, arrays, a composite type's attributes and
// domains, with a view on the columns.
const typeProbeYAML = `
schema app:
  type pair:
    type: composite
    attributes:
      a: {type: "numeric(10)"}
      b: {type: "varchar( 5 )"}
  domain d10:
    type: numeric(10)
  domain codes:
    type: "varchar( 5 )[]"
  table t:
    columns:
      id: {type: bigint}
      n: {type: "numeric(10)"}
      v: {type: "varchar( 5 )"}
      ts: {type: "timestamp (3) with time zone"}
      iv: {type: "interval day to second( 3 )"}
      na: {type: "numeric(10)[]"}
      va: {type: "VARCHAR( 5 ) []"}
      p: {type: app.pair, nullable: true}
      dd: {type: app.d10, nullable: true}
    primaryKey: [id]
  view tv:
    query: select id, n, v, va from app.t
    dependsOn: [table app.t]
`

// TestIntegrationProbeTypes compares declared types PostgreSQL prints
// another way through the server: the diff is clean, also under --unsafe,
// where the text comparison changes every column's type and drops the view
// on them, and a real change is still planned.
func TestIntegrationProbeTypes(t *testing.T) {
	pool, d := exprProbeDatabase(t, "pgytest_type_probe", typeProbeYAML)
	text := diff.Plan(introspect(t, pool, d), d, true)
	for _, want := range []string{`drop view "app"."tv";`, `alter column "n" type numeric(10);`, `alter column "iv" type interval day to second( 3 );`, `alter attribute "b" type varchar( 5 )`} {
		if !strings.Contains(diff.Render(text), want) {
			t.Fatalf("the fixture must differ as text, want %q in:\n%s", want, diff.Render(text))
		}
	}

	live := probed(t, pool, d)
	for declared, shown := range map[string]string{
		"numeric(10)":                  "numeric(10,0)",
		"varchar( 5 )":                 "character varying(5)",
		"timestamp (3) with time zone": "timestamp(3) with time zone",
		"interval day to second( 3 )":  "interval day to second(3)",
		"numeric(10)[]":                "numeric(10,0)[]",
		"VARCHAR( 5 ) []":              "character varying(5)[]",
		"varchar( 5 )[]":               "character varying(5)[]",
	} {
		if got := live.TypeProbes[declared]; got != shown {
			t.Errorf("%s: want PostgreSQL's %q, got %q", declared, shown, got)
		}
	}
	if live.ExprProbes != nil {
		t.Errorf("no expression differs, got %v", exprProbeKeys(live))
	}
	for _, opts := range []diff.Options{{Unsafe: true}, {Unsafe: true, Prune: true}} {
		if p := diff.PlanWith(live, d, opts); diff.Render(p)+diff.RenderConcurrent(p) != "" {
			t.Errorf("want a clean diff (%+v), got:\n%s", opts, diff.Render(p))
		}
	}
	if p := diff.PlanReportingUnsafe(live, d, diff.Options{}); diff.Render(p) != "" || len(p.Warnings) > 0 {
		t.Errorf("want nothing planned or reported without --unsafe, got:\n%s%+v", diff.Render(p), p.Warnings)
	}

	changed := loadYAML(t, "app", strings.NewReplacer(`n: {type: "numeric(10)"}`, `n: {type: "numeric(12)"}`, "type: numeric(10)\n", "type: numeric(11)\n").Replace(typeProbeYAML))
	live = probed(t, pool, changed)
	p := diff.Plan(live, changed, true)
	sql := diff.Render(p)
	for _, want := range []string{`drop view "app"."tv";`, `alter table "app"."t" alter column "n" type numeric(12);`} {
		if !strings.Contains(sql, want) {
			t.Errorf("a real change is planned, want %q in:\n%s", want, sql)
		}
	}
	for _, unwanted := range []string{`"v" type`, `"ts" type`, `"iv" type`, `"na" type`, `"va" type`, `alter attribute`} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("only the changed type is planned, got %q in:\n%s", unwanted, sql)
		}
	}
	found := false
	for _, w := range p.Warnings {
		found = found || strings.Contains(w.Message, "base type changed from numeric(10,0) to numeric(11)")
	}
	if !found {
		t.Errorf("the domain's changed base type is reported, got %+v", p.Warnings)
	}
}

// TestIntegrationProbeTypesSearchPath declares a type of a schema that is
// only on the session's search_path without its schema: it is resolved the
// way the migration resolves it and printed like the snapshot.
func TestIntegrationProbeTypesSearchPath(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	base := freshDatabase(t, admin, "pgytest_type_probe_path")
	if _, err := base.Exec(ctx, `create schema app; create schema lib; create domain lib.code as text; alter database pgytest_type_probe_path set search_path = app, lib`); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, base.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	d := loadYAML(t, "app", `
schema app:
  table t:
    columns:
      id: {type: bigint}
      codes: {type: "code []", nullable: true}
`)
	applyPlan(t, pool, diff.Plan(introspect(t, pool, d), d, true))
	if sql := diff.Render(diff.Plan(introspect(t, pool, d), d, true)); !strings.Contains(sql, `type code []`) {
		t.Fatalf("the fixture must differ as text, got:\n%s", sql)
	}
	live := probed(t, pool, d)
	if got := live.TypeProbes["code []"]; got != "lib.code[]" {
		t.Errorf("want the type resolved on the session's search_path and printed with its schema, got %q", got)
	}
	if sql := diff.Render(diff.Plan(live, d, true)); sql != "" {
		t.Errorf("want a clean diff, got:\n%s", sql)
	}
}

// TestIntegrationProbeTypesFallsBack compares types as text where the
// server can't resolve them: a type the same plan creates, and every type
// in a read-only session.
func TestIntegrationProbeTypesFallsBack(t *testing.T) {
	pool, _ := exprProbeDatabase(t, "pgytest_type_probe_fallback", typeProbeYAML)
	ctx := context.Background()
	d := loadYAML(t, "app", strings.Replace(typeProbeYAML, "  domain d10:", "  domain d12:\n    type: numeric(12)\n  domain d10:", 1))
	d.Tables["app.t"].Columns["n"].Type = "app.d12"
	live := probed(t, pool, d)
	if _, ok := live.TypeProbes["app.d12"]; ok || live.TypeProbes["numeric(10)"] != "numeric(10,0)" {
		t.Errorf("a type the plan creates can't be resolved yet and fails alone, got %v", live.TypeProbes)
	}
	sql := diff.Render(diff.Plan(live, d, true))
	if !strings.Contains(sql, `create domain "app"."d12" as numeric(12);`) || !strings.Contains(sql, `alter table "app"."t" alter column "n" type app.d12;`) || strings.Contains(sql, `"v" type`) {
		t.Errorf("want the new domain and the column changed to it, and nothing else, got:\n%s", sql)
	}

	cfg := pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	readOnly, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	d = loadYAML(t, "app", typeProbeYAML)
	live = introspect(t, readOnly, d)
	if err := diff.ProbeExpressions(ctx, readOnly, live, d); err == nil || !strings.Contains(err.Error(), "read-only transaction") || live.TypeProbes != nil {
		t.Fatalf("want an error and no results in a read-only session, got %v, %v", err, live.TypeProbes)
	}
	text := diff.Plan(introspect(t, pool, d), d, true)
	if got, want := diff.Render(diff.Plan(live, d, true)), diff.Render(text); got != want {
		t.Errorf("want the plan the text comparison makes:\n%s\ngot:\n%s", want, got)
	}
}

// TestIntegrationProbeTypesInSyncCostsNothing requires the types pgy pull
// writes to need no probe.
func TestIntegrationProbeTypesInSyncCostsNothing(t *testing.T) {
	pool, _ := exprProbeDatabase(t, "pgytest_type_probe_sync", typeProbeYAML)
	d := pulledYAML(t, pool)
	live := introspect(t, pool, d)
	acquired := pool.Stat().AcquireCount()
	if err := diff.ProbeExpressions(context.Background(), pool, live, d); err != nil {
		t.Fatal(err)
	}
	if n := pool.Stat().AcquireCount() - acquired; n != 0 || live.TypeProbes != nil || live.ExprProbes != nil {
		t.Errorf("pulled YAML must not be probed: %d connections used, types %v", n, live.TypeProbes)
	}
	if got := d.Tables["app.t"].Columns["n"].Type; got != "numeric(10,0)" {
		t.Errorf("want pull to write PostgreSQL's spelling, got %q", got)
	}
}
