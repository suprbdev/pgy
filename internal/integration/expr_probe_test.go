package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

// exprProbeExtensionTypes spells constants of extension types the way a
// person writes them, which is not the way their output functions print
// them, in every kind of definition the probe covers.
const exprProbeExtensionTypes = `
extensions:
  - {name: hstore, schema: public}
  - {name: cube, schema: public}
  - {name: seg, schema: public}
  - {name: isn, schema: public}
  - {name: ltree, schema: public}
  - {name: citext, schema: public}
schema app:
  domain props:
    type: hstore
    default: "'v=>1'"
    check: "value @> 'v=>1'"
  function f(h hstore default 'a=>1', c cube default '(0,0)', out o int):
    returns: integer
    language: sql
    body: select 1
  procedure p(s seg default '0 .. 10', e ean13 default '9780306406157'):
    language: sql
    body: select 1
  function trg():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table item:
    columns:
      id: {type: bigint}
      attrs: {type: hstore, default: "'a=>1'"}
      c: {type: cube, nullable: true, default: "'(0,0,0)'::cube"}
      s: {type: seg, nullable: true, default: "'0 .. 10'::seg"}
      old: {type: isbn, nullable: true}
      code: {type: issn, nullable: true, default: "issn('1436-4522')"}
      e: {type: ean13, nullable: true, generated: "ean13(old)"}
      path: {type: ltree, nullable: true}
      q: {type: ltxtquery, nullable: true, default: "'a&b'"}
      email: {type: citext, nullable: true, default: "'A@B'"}
    primaryKey: [id]
    constraints:
      item_box: {type: check, expression: "c <@ cube '(0, 0),(1, 1)'"}
      item_seg: {type: check, expression: "s <@ '0 .. 10'"}
      item_path: {type: check, expression: "path @ 'a&b'"}
      item_excl: {type: exclude, expression: "using gist (c with &&) where (s <@ '0 .. 10')"}
    indexes:
      item_widgets: {columns: [id], where: "attrs @> 'kind=>widget'"}
      item_kind: {columns: ["(attrs -> 'kind')", "lower(email::text)"], where: "c <@ '(0,0),(10,10)'::cube"}
    rowLevelSecurity: true
    policies:
      item_p: {using: "s && '0 .. 10'::seg and item.attrs ? 'k'", withCheck: "attrs @> 'kind=>widget'"}
    triggers:
      item_t:
        timing: before
        events: [update]
        level: row
        when: "new.attrs @> 'locked=>true' and old.c is distinct from new.c"
        procedure: app.trg('a=>1', 007)
  view item_v:
    query: select id, attrs from app.item
    columnDefaults:
      attrs: "'a=>1'::hstore"
  materialized view item_m:
    query: select id, attrs, c from app.item
    indexes:
      item_m_part: {columns: [id], where: "attrs @> 'kind=>widget' and c <@ '(0,0),(1,1)'"}
`

// exprProbeBuiltinTypes does the same with built-in types.
const exprProbeBuiltinTypes = `
schema app:
  domain span:
    type: interval
    default: "'1 hour'"
    check: "value > '0 seconds'"
  function g(i interval default '1 hour', j jsonb default '[1,  2]', d date default '2020-1-1', n numeric default 1e3):
    returns: "table(x int)"
    language: sql
    body: select 1
  function trg():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table lit:
    columns:
      id: {type: bigint}
      iv: {type: interval, default: "'1 hour'::interval"}
      iv2: {type: interval, default: "'1 hour'"}
      j: {type: jsonb, default: "'{ \"a\" : 1 }'::jsonb"}
      d: {type: date, default: "'2020-1-1'::date"}
      n: {type: "numeric(6,2)", default: "'1.5'"}
      big: {type: numeric, default: "1e3"}
      ia: {type: "int[]", default: "'{1, 2}'"}
      ip: {type: inet, default: "'10.0.0.1/32'"}
      r: {type: int4range, default: "'[1,5]'"}
      secs: {type: numeric, nullable: true, generated: "big * 1e2 + extract(epoch from interval '1 hour')"}
    primaryKey: [id]
    constraints:
      lit_j: {type: check, expression: "j @> '{ \"a\" : 1 }'"}
      lit_iv: {type: check, expression: "iv > '1 hour'"}
      lit_r: {type: check, expression: "r && '[1,5]'"}
      lit_d: {type: check, expression: "d between '2020-1-1' and '2030-12-31'"}
      lit_excl: {type: exclude, expression: "using gist (r with &&) where (iv > '1 hour')"}
    indexes:
      lit_recent: {columns: [d], where: "d > '2020-1-1'::date"}
      lit_expr: {columns: ["(j -> 'a')", "(d + 7)"], where: "r @> 3 and iv < '1 day 2 hours'"}
    rowLevelSecurity: true
    policies:
      lit_p: {for: select, using: "iv > '90 minutes' and lit.d >= '2020-1-1'"}
    triggers:
      lit_t:
        timing: after
        events: [update of iv, insert]
        level: row
        when: "new.iv > '1 hour'"
        procedure: app.trg()
  table ev:
    columns:
      id: {type: bigint}
      at: {type: date, default: "'2020-1-1'"}
    partitionBy: {range: [at]}
    constraints:
      ev_at: {type: check, expression: "at >= '2000-1-1'"}
    indexes:
      ev_late: {columns: [id], where: "at > '2020-6-1'"}
  table ev_2020:
    partitionOf: app.ev
    forValues:
      from: ["2020-01-01"]
      to: ["2021-01-01"]
    columns:
      at: {default: "'2020-6-1'"}
`

func exprProbeDatabase(t *testing.T, name, y string) (*pgxpool.Pool, *schema.Database) {
	t.Helper()
	pool := freshDatabase(t, connect(t), name)
	d := loadYAML(t, "app", y)
	applyPlan(t, pool, diff.Plan(introspect(t, pool, d), d, true))
	return pool, d
}

// probed introspects pool and probes the definitions of d that differ from
// it as text.
func probed(t *testing.T, pool *pgxpool.Pool, d *schema.Database) *diff.Live {
	t.Helper()
	live := introspect(t, pool, d)
	if err := diff.ProbeExpressions(context.Background(), pool, live, d); err != nil {
		t.Fatal(err)
	}
	return live
}

func exprProbeKeys(live *diff.Live) []string {
	out := []string{}
	for k, p := range live.ExprProbes {
		out = append(out, strings.TrimSpace(fmt.Sprintf("%s %s %s", k.Kind, k.Object, k.Name))+": "+p.Err)
	}
	sort.Strings(out)
	return out
}

// assertExprProbeClean requires d, applied to pool, to diff clean once its
// definitions are compared through the server, and every kind in kinds to
// have been probed.
func assertExprProbeClean(t *testing.T, pool *pgxpool.Pool, d *schema.Database, kinds ...diff.ExprKind) *diff.Live {
	t.Helper()
	if p := diff.Plan(introspect(t, pool, d), d, true); diff.Render(p)+diff.RenderConcurrent(p) == "" {
		t.Fatal("the fixture diffs clean as text, so it doesn't exercise the probe")
	}
	live := probed(t, pool, d)
	seen := map[diff.ExprKind]bool{}
	for k, p := range live.ExprProbes {
		seen[k.Kind] = true
		if p.Err != "" {
			t.Errorf("%s %s %s: %s", k.Kind, k.Object, k.Name, p.Err)
		}
	}
	for _, kind := range kinds {
		if !seen[kind] {
			t.Errorf("no %s was probed: %v", kind, exprProbeKeys(live))
		}
	}
	for _, opts := range []diff.Options{{Unsafe: true}, {Unsafe: true, Prune: true}} {
		p := diff.PlanWith(live, d, opts)
		if sql := diff.Render(p) + diff.RenderConcurrent(p); sql != "" {
			t.Errorf("want a clean diff (%+v), got:\n%s\nprobes: %s", opts, sql, strings.Join(exprProbeKeys(live), "\n"))
		}
	}
	if p := diff.PlanReportingUnsafe(live, d, diff.Options{}); diff.Render(p) != "" || len(p.Warnings) > 0 {
		t.Errorf("want nothing planned or reported without --unsafe, got:\n%s%+v", diff.Render(p), p.Warnings)
	}
	return live
}

var exprProbeAllKinds = []diff.ExprKind{
	diff.ExprDefault, diff.ExprGenerated, diff.ExprConstraint, diff.ExprIndex, diff.ExprPolicy, diff.ExprTrigger,
	diff.ExprDomainDefault, diff.ExprDomainCheck, diff.ExprFunctionArgs,
}

func TestIntegrationProbeExpressionsExtensionTypes(t *testing.T) {
	pool, d := exprProbeDatabase(t, "pgytest_expr_probe_ext", exprProbeExtensionTypes)
	live := assertExprProbeClean(t, pool, d, append([]diff.ExprKind{diff.ExprViewDefault, diff.ExprProcedureArgs}, exprProbeAllKinds...)...)
	for _, key := range []diff.ExprKey{
		{Kind: diff.ExprDefault, Object: "app.item", Name: "attrs"},
		{Kind: diff.ExprDefault, Object: "app.item", Name: "code"},
		{Kind: diff.ExprGenerated, Object: "app.item", Name: "e"},
		{Kind: diff.ExprConstraint, Object: "app.item", Name: "item_box"},
		{Kind: diff.ExprConstraint, Object: "app.item", Name: "item_excl"},
		{Kind: diff.ExprIndex, Object: "app.item", Name: "item_kind"},
		{Kind: diff.ExprIndex, Object: "app.item_m", Name: "item_m_part"},
		{Kind: diff.ExprPolicy, Object: "app.item", Name: "item_p"},
		{Kind: diff.ExprTrigger, Object: "app.item", Name: "item_t"},
		{Kind: diff.ExprViewDefault, Object: "app.item_v", Name: "attrs"},
	} {
		if live.ExprProbes[key] == nil {
			t.Errorf("%s %s %s was not probed: %v", key.Kind, key.Object, key.Name, exprProbeKeys(live))
		}
	}
	if p := live.ExprProbes[diff.ExprKey{Kind: diff.ExprDefault, Object: "app.item", Name: "attrs"}]; p != nil && (len(p.Parts) != 1 || p.Parts[0] != `'"a"=>"1"'::hstore`) {
		t.Errorf("want the default as PostgreSQL stores it, got %v", p.Parts)
	}
}

func TestIntegrationProbeExpressionsBuiltinTypes(t *testing.T) {
	pool, d := exprProbeDatabase(t, "pgytest_expr_probe_builtin", exprProbeBuiltinTypes)
	live := assertExprProbeClean(t, pool, d, exprProbeAllKinds...)
	for _, key := range []diff.ExprKey{
		{Kind: diff.ExprDefault, Object: "app.lit", Name: "iv2"},
		{Kind: diff.ExprDefault, Object: "app.lit", Name: "big"},
		{Kind: diff.ExprDefault, Object: "app.ev_2020", Name: "at"},
		{Kind: diff.ExprConstraint, Object: "app.ev", Name: "ev_at"},
		{Kind: diff.ExprIndex, Object: "app.ev", Name: "ev_late"},
		{Kind: diff.ExprPolicy, Object: "app.lit", Name: "lit_p"},
	} {
		if live.ExprProbes[key] == nil {
			t.Errorf("%s %s %s was not probed: %v", key.Kind, key.Object, key.Name, exprProbeKeys(live))
		}
	}
}

func TestIntegrationProbeExpressionsVirtualGenerated(t *testing.T) {
	requireServerVersion(t, 180000)
	pool, d := exprProbeDatabase(t, "pgytest_expr_probe_virtual", `
schema app:
  table v:
    columns:
      id: {type: bigint}
      d: {type: date, nullable: true}
      late: {type: boolean, nullable: true, generated: "d > '2020-1-1'", virtual: true}
      days: {type: interval, nullable: true, generated: "(d - date '2020-1-1') * interval '1 day'"}
`)
	assertExprProbeClean(t, pool, d, diff.ExprGenerated)
}

// TestIntegrationProbeExpressionsRealChange changes definitions for real,
// in another spelling than PostgreSQL's: they are still planned, the ones
// that didn't change are not, and the result converges.
func TestIntegrationProbeExpressionsRealChange(t *testing.T) {
	pool, _ := exprProbeDatabase(t, "pgytest_expr_probe_change", exprProbeBuiltinTypes)
	changed := exprProbeBuiltinTypes
	for old, new := range map[string]string{
		`iv2: {type: interval, default: "'1 hour'"}`:                                            `iv2: {type: interval, default: "'2 hours'"}`,
		`lit_iv: {type: check, expression: "iv > '1 hour'"}`:                                    `lit_iv: {type: check, expression: "iv > '2 hours'"}`,
		`lit_recent: {columns: [d], where: "d > '2020-1-1'::date"}`:                             `lit_recent: {columns: [d], where: "d > '2021-1-1'::date"}`,
		`using: "iv > '90 minutes' and lit.d >= '2020-1-1'"`:                                    `using: "iv > '91 minutes' and lit.d >= '2020-1-1'"`,
		`when: "new.iv > '1 hour'"`:                                                             `when: "new.iv > '2 hours'"`,
		`default: "'1 hour'"` + "\n    check":                                                   `default: "'2 hours'"` + "\n    check",
		`function g(i interval default '1 hour',`:                                               `function g(i interval default '2 hours',`,
		`generated: "big * 1e2 + extract(epoch from interval '1 hour')"`:                        `generated: "big * 1e2 + extract(epoch from interval '2 hours')"`,
		`lit_excl: {type: exclude, expression: "using gist (r with &&) where (iv > '1 hour')"}`: `lit_excl: {type: exclude, expression: "using gist (r with &&) where (iv > '2 hours')"}`,
	} {
		if !strings.Contains(changed, old) {
			t.Fatalf("fixture has no %q", old)
		}
		changed = strings.Replace(changed, old, new, 1)
	}
	d := loadYAML(t, "app", changed)
	live := probed(t, pool, d)
	plan := diff.Plan(live, d, true)
	sql := diff.Render(plan) + diff.RenderConcurrent(plan)
	want := []string{
		`alter table "app"."lit" alter column "iv2" set default '2 hours';`,
		`alter table "app"."lit" add constraint "lit_iv" check (iv > '2 hours')`,
		`create index if not exists "lit_recent" on "app"."lit"("d") where (d > '2021-1-1'::date);`,
		`create policy "lit_p" on "app"."lit" for select using (iv > '91 minutes' and lit.d >= '2020-1-1');`,
		`when (new.iv > '2 hours') execute procedure app.trg();`,
		`alter domain "app"."span" set default '2 hours';`,
		`create or replace function "app"."g"(i interval default '2 hours',`,
		`add constraint "lit_excl" exclude using gist (r with &&) where (iv > '2 hours')`,
	}
	if live.ServerVersion >= 170000 {
		want = append(want, `alter column "secs" set expression as (big * 1e2 + extract(epoch from interval '2 hours'));`)
	}
	for _, stmt := range want {
		if !strings.Contains(sql, stmt) {
			t.Errorf("a real change must be planned, want %q in:\n%s", stmt, sql)
		}
	}
	for _, unchanged := range []string{`"iv" set default`, `"j" set default`, `"lit_j"`, `"lit_r"`, `"lit_d"`, `"lit_expr"`, `"ev_at"`, `"ev_late"`, `"ev_2020"`, `"span" add constraint`} {
		if strings.Contains(sql, unchanged) {
			t.Errorf("a definition that only looks different must not be planned, got %q in:\n%s", unchanged, sql)
		}
	}
	if live.ServerVersion < 170000 {
		d.Tables["app.lit"].Columns["secs"].Generated = "big * 1e2 + extract(epoch from interval '1 hour')"
		plan = diff.Plan(live, d, true)
	}
	applyPlan(t, pool, plan)
	again := diff.Plan(probed(t, pool, d), d, true)
	if sql := diff.Render(again) + diff.RenderConcurrent(again); sql != "" {
		t.Errorf("the changed definitions did not converge:\n%s", sql)
	}
}

// TestIntegrationProbeExpressionsFailureIsolation declares definitions
// PostgreSQL can't attach yet next to one it can: each failure stays with
// its own definition, which is then planned exactly as without the probe.
func TestIntegrationProbeExpressionsFailureIsolation(t *testing.T) {
	pool, _ := exprProbeDatabase(t, "pgytest_expr_probe_isolation", `
schema app:
  function trg():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table t:
    columns:
      id: {type: bigint}
      iv: {type: interval, default: "'1 hour'"}
      d: {type: date, nullable: true}
    constraints:
      t_d: {type: check, expression: "d > '2020-1-1'"}
      t_iv: {type: check, expression: "iv > '1 hour'"}
    indexes:
      t_recent: {columns: [id], where: "d > '2020-1-1'"}
    policies:
      t_p: {using: "d > '2020-1-1'"}
    triggers:
      t_t:
        timing: before
        events: [update]
        level: row
        when: "new.d > '2020-1-1'"
        procedure: app.trg()
  table other:
    columns:
      id: {type: bigint}
      at: {type: date, default: "'2020-1-1'"}
`)
	d := loadYAML(t, "app", `
schema app:
  function trg():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  function later():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  function ok(d date):
    returns: boolean
    language: sql
    volatility: immutable
    body: select true
  table t:
    columns:
      id: {type: bigint}
      iv: {type: interval, default: "'1 hour'"}
      d: {type: date, nullable: true}
      added: {type: date, nullable: true}
    constraints:
      t_d: {type: check, expression: "added > '2020-1-1'"}
      t_iv: {type: check, expression: "nope(iv) > '1 hour'"}
    indexes:
      t_recent: {columns: [id], where: "app.ok(d) and d > '2020-1-1'"}
    policies:
      t_p: {using: "d > '2020-1-1' and (select true from app.missing)"}
    triggers:
      t_t:
        timing: before
        events: [update]
        level: row
        when: "new.d > '2020-1-1'"
        procedure: app.later()
  table other:
    columns:
      id: {type: bigint}
      at: {type: date, default: "'2020-1-1'"}
`)
	unprobed := introspect(t, pool, d)
	live := probed(t, pool, d)
	for key, want := range map[diff.ExprKey]string{
		{Kind: diff.ExprDefault, Object: "app.t", Name: "iv"}:      "",
		{Kind: diff.ExprDefault, Object: "app.other", Name: "at"}:  "",
		{Kind: diff.ExprConstraint, Object: "app.t", Name: "t_d"}:  `column "added" does not exist`,
		{Kind: diff.ExprConstraint, Object: "app.t", Name: "t_iv"}: `function nope(interval) does not exist`,
		{Kind: diff.ExprIndex, Object: "app.t", Name: "t_recent"}:  `function app.ok(date) does not exist`,
		{Kind: diff.ExprPolicy, Object: "app.t", Name: "t_p"}:      `relation "app.missing" does not exist`,
		{Kind: diff.ExprTrigger, Object: "app.t", Name: "t_t"}:     `function app.later() does not exist`,
	} {
		p := live.ExprProbes[key]
		switch {
		case p == nil:
			t.Errorf("%s %s %s was not probed: %v", key.Kind, key.Object, key.Name, exprProbeKeys(live))
		case p.Err != want:
			t.Errorf("%s %s %s: err %q, want %q", key.Kind, key.Object, key.Name, p.Err, want)
		}
	}
	plan := diff.Plan(live, d, true)
	sql := diff.Render(plan) + diff.RenderConcurrent(plan)
	for _, clean := range []string{`"iv" set default`, `"at" set default`} {
		if strings.Contains(sql, clean) {
			t.Errorf("the definitions PostgreSQL accepted must compare equal, got %q in:\n%s", clean, sql)
		}
	}
	before := diff.Plan(unprobed, d, true)
	rejected := func(p *diff.PlanDiff) []string {
		out := []string{}
		for _, s := range append(append(append([]string{}, p.Creates...), p.Alters...), p.Concurrent...) {
			if !strings.Contains(s, `set default`) {
				out = append(out, s)
			}
		}
		return out
	}
	if !reflect.DeepEqual(rejected(plan), rejected(before)) {
		t.Errorf("the rejected definitions must be planned as without the probe:\n%s\nwant:\n%s", strings.Join(rejected(plan), "\n"), strings.Join(rejected(before), "\n"))
	}
}

// TestIntegrationProbeExpressionsUnreadableTable probes as a role that may
// not read one of the tables: that table can't be copied, so its
// definitions are compared as text, and the other table's are unaffected.
func TestIntegrationProbeExpressionsUnreadableTable(t *testing.T) {
	pool, d := exprProbeDatabase(t, "pgytest_expr_probe_unreadable", `
schema app:
  table open:
    columns:
      id: {type: bigint}
      ttl: {type: interval, default: "'1 hour'"}
  table closed:
    columns:
      id: {type: bigint}
      ttl: {type: interval, default: "'2 hours'"}
`)
	ctx := context.Background()
	const role = "pgytest_expr_probe_reader"
	admin := connect(t)
	dropRole := func() {
		admin.Exec(context.Background(), `drop role if exists `+role) //nolint
	}
	dropRole()
	t.Cleanup(dropRole)
	for _, stmt := range []string{
		`create role ` + role + ` login password 'pgy'`,
		`grant usage on schema app to ` + role,
		`grant select on app.open to ` + role,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `drop owned by `+role) //nolint
	})
	cfg := pool.Config().Copy()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = role, "pgy"
	reader, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	live := probed(t, reader, d)
	if p := live.ExprProbes[diff.ExprKey{Kind: diff.ExprDefault, Object: "app.open", Name: "ttl"}]; p == nil || p.Err != "" {
		t.Errorf("the readable table's default must be probed, got %+v", p)
	}
	if p := live.ExprProbes[diff.ExprKey{Kind: diff.ExprDefault, Object: "app.closed", Name: "ttl"}]; p == nil || !strings.Contains(p.Err, "permission denied") {
		t.Errorf("the unreadable table's default can't be probed, got %+v", p)
	}
	want := []string{`alter table "app"."closed" alter column "ttl" set default '2 hours';`}
	if got := diff.Plan(live, d, true).Alters; !reflect.DeepEqual(got, want) {
		t.Errorf("want only the unreadable table's default planned, got %v", got)
	}
}

// TestIntegrationProbeExpressionsManyTables probes more tables than one
// connection takes, so the probe is split across several, and checks that
// every result still reaches its own definition.
func TestIntegrationProbeExpressionsManyTables(t *testing.T) {
	const tables = 130
	var y strings.Builder
	y.WriteString("schema app:\n")
	for i := 0; i < tables; i++ {
		fmt.Fprintf(&y, "  table t%d:\n    columns:\n      id: {type: bigint}\n      ttl: {type: interval, default: \"'%d hours'\"}\n", i, i+1)
		fmt.Fprintf(&y, "    constraints:\n      t%d_ttl: {type: check, expression: \"ttl > '%d minutes'\"}\n", i, i+1)
	}
	pool, d := exprProbeDatabase(t, "pgytest_expr_probe_many", y.String())
	live := assertExprProbeClean(t, pool, d, diff.ExprDefault, diff.ExprConstraint)
	if len(live.ExprProbes) != 2*tables {
		t.Fatalf("want a default and a check probed per table, got %d probes", len(live.ExprProbes))
	}
	for i := 0; i < tables; i++ {
		fq := fmt.Sprintf("app.t%d", i)
		p := live.ExprProbes[diff.ExprKey{Kind: diff.ExprDefault, Object: fq, Name: "ttl"}]
		if want := live.Tables[fq].Columns["ttl"].Default; p == nil || len(p.Parts) != 1 || p.Parts[0] != want {
			t.Fatalf("%s: want its own default %q read back, got %+v", fq, want, p)
		}
	}

	changed := loadYAML(t, "app", strings.Replace(strings.Replace(y.String(), `"ttl > '77 minutes'"`, `"nope(ttl) > '77 minutes'"`, 1), `default: "'100 hours'"`, `default: "'1000 hours'"`, 1))
	live = probed(t, pool, changed)
	for k, p := range live.ExprProbes {
		if want := map[bool]string{true: "function nope(interval) does not exist"}[k.Name == "t76_ttl"]; p.Err != want {
			t.Errorf("%s %s %s: err %q, want %q", k.Kind, k.Object, k.Name, p.Err, want)
		}
	}
	plan := diff.Plan(live, changed, true)
	want := []string{
		`alter table "app"."t76" drop constraint "t76_ttl";`,
		`alter table "app"."t76" add constraint "t76_ttl" check (nope(ttl) > '77 minutes') not valid;`,
		`alter table "app"."t99" alter column "ttl" set default '1000 hours';`,
	}
	if !reflect.DeepEqual(plan.Alters, want) {
		t.Errorf("want the two changed definitions planned and nothing else, got:\n%s", strings.Join(plan.Alters, "\n"))
	}
}

// exprProbeLeftovers counts what a probe must not leave behind.
func exprProbeLeftovers(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		select (select count(*) from pg_class where relname like 'pgy\_probe%' or relnamespace::regnamespace::text like 'pg\_temp%')
		     + (select count(*) from pg_proc where proname like 'pgy\_probe%')
		     + (select count(*) from pg_type where typname like 'pgy\_probe%')
		     + (select count(*) from pg_constraint where conname like 'pgy\_probe%')
		     + (select count(*) from pg_policy where polname like 'pgy\_probe%')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestIntegrationProbeExpressionsLocksAndLeftovers runs the probe while
// another session holds every table in EXCLUSIVE mode, which only ACCESS
// SHARE gets past, and checks that the database is as it was afterwards.
func TestIntegrationProbeExpressionsLocksAndLeftovers(t *testing.T) {
	pool, d := exprProbeDatabase(t, "pgytest_expr_probe_locks", exprProbeBuiltinTypes)
	ctx := context.Background()
	before := introspectAll(t, pool)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `lock table app.lit, app.ev, app.ev_2020 in exclusive mode`); err != nil {
		t.Fatal(err)
	}
	live := introspect(t, pool, d)
	timeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := diff.ProbeExpressions(timeout, pool, live, d); err != nil {
		t.Fatalf("the probe must not need more than ACCESS SHARE on a real table: %v", err)
	}
	if len(live.ExprProbes) == 0 {
		t.Fatal("nothing was probed")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if n := exprProbeLeftovers(t, pool); n != 0 {
		t.Errorf("the probe left %d objects behind", n)
	}
	after := introspectAll(t, pool)
	if !reflect.DeepEqual(before.Tables, after.Tables) || !reflect.DeepEqual(before.DomainDefs, after.DomainDefs) || !reflect.DeepEqual(before.FunctionDefs, after.FunctionDefs) {
		t.Error("the probe changed the database")
	}
}

// pulledYAML returns the schema pgy pull writes for pool.
func pulledYAML(t *testing.T, pool *pgxpool.Pool) *schema.Database {
	t.Helper()
	res, err := pull.Build(introspectAll(t, pool), pull.Options{})
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
	d, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestIntegrationProbeExpressionsInSyncOpensNoTransaction requires YAML as
// pgy pull writes it to cost nothing: the pool hands out no connection, and
// the plan is the one made without the probe.
func TestIntegrationProbeExpressionsInSyncOpensNoTransaction(t *testing.T) {
	for name, y := range map[string]string{"extension types": exprProbeExtensionTypes, "builtin types": exprProbeBuiltinTypes} {
		t.Run(name, func(t *testing.T) {
			pool, _ := exprProbeDatabase(t, "pgytest_expr_probe_sync", y)
			ctx := context.Background()
			d := pulledYAML(t, pool)
			live := introspect(t, pool, d)
			acquired := pool.Stat().AcquireCount()
			if err := diff.ProbeExpressions(ctx, pool, live, d); err != nil {
				t.Fatal(err)
			}
			if n := pool.Stat().AcquireCount() - acquired; n != 0 || live.ExprProbes != nil || live.TypeProbes != nil {
				t.Errorf("a schema that is in sync must not be probed: %d connections used, probes %v, types %v", n, exprProbeKeys(live), live.TypeProbes)
			}
			if p := diff.PlanReportingUnsafe(live, d, diff.Options{}); diff.Render(p) != "" || len(p.Warnings) > 0 {
				t.Errorf("pulled YAML must diff clean, got:\n%s%+v", diff.Render(p), p.Warnings)
			}
		})
	}
}

// TestIntegrationProbeExpressionsFallsBack runs the probe where it can't
// work, without the TEMPORARY privilege and in a read-only session: it
// reports why and the plan is the one the text comparison makes.
func TestIntegrationProbeExpressionsFallsBack(t *testing.T) {
	pool, d := exprProbeDatabase(t, "pgytest_expr_probe_fallback", exprProbeBuiltinTypes)
	ctx := context.Background()
	textPlan := diff.Plan(introspect(t, pool, d), d, true)
	want := diff.Render(textPlan) + diff.RenderConcurrent(textPlan)
	if want == "" {
		t.Fatal("the fixture diffs clean as text")
	}

	const role = "pgytest_expr_probe_notemp"
	admin := connect(t)
	dropRole := func() {
		admin.Exec(context.Background(), `drop role if exists `+role) //nolint
	}
	dropRole()
	t.Cleanup(dropRole)
	for _, stmt := range []string{
		`create role ` + role + ` login password 'pgy'`,
		`revoke temporary on database pgytest_expr_probe_fallback from public`,
		`grant usage on schema app to ` + role,
		`grant select on all tables in schema app to ` + role,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `drop owned by `+role) //nolint
	})
	noTemp := pool.Config().Copy()
	noTemp.ConnConfig.User, noTemp.ConnConfig.Password = role, "pgy"
	readOnly := pool.Config().Copy()
	readOnly.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"

	for name, c := range map[string]struct {
		cfg  *pgxpool.Config
		want string
	}{
		"no TEMPORARY privilege": {noTemp, "permission denied"},
		"read-only session":      {readOnly, "read-only transaction"},
	} {
		t.Run(name, func(t *testing.T) {
			limited, err := pgxpool.NewWithConfig(ctx, c.cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer limited.Close()
			live := introspect(t, limited, d)
			err = diff.ProbeExpressions(ctx, limited, live, d)
			if err == nil || !strings.Contains(err.Error(), c.want) || live.ExprProbes != nil {
				t.Fatalf("want an error mentioning %q and no probes, got %v, %v", c.want, err, exprProbeKeys(live))
			}
			p := diff.Plan(live, d, true)
			if got := diff.Render(p) + diff.RenderConcurrent(p); got != want {
				t.Errorf("want the plan the text comparison makes:\n%s\ngot:\n%s", want, got)
			}
			var n int
			if err := limited.QueryRow(ctx, `select 1`).Scan(&n); err != nil {
				t.Errorf("the connection must be usable after a failed probe: %v", err)
			}

			inSync := pulledYAML(t, pool)
			live = introspect(t, limited, inSync)
			if err := diff.ProbeExpressions(ctx, limited, live, inSync); err != nil {
				t.Errorf("a schema that is in sync isn't probed, so nothing can fail: %v", err)
			}
		})
	}
	if n := exprProbeLeftovers(t, pool); n != 0 {
		t.Errorf("the failed probes left %d objects behind", n)
	}
}

// TestIntegrationProbeExpressionsSearchPath declares objects of schemas
// that are only on the session's search_path without their schema. The
// probe has to resolve them the way the migration does and still print
// them like the snapshot, which is read with search_path = public.
func TestIntegrationProbeExpressionsSearchPath(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	base := freshDatabase(t, admin, "pgytest_expr_probe_path")
	if _, err := base.Exec(ctx, `create schema app; create schema ext; alter database pgytest_expr_probe_path set search_path = app, ext`); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, base.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	d := loadYAML(t, "app", `
extensions:
  - {name: hstore, schema: ext}
  - {name: seg, schema: ext}
schema app:
  function is_open(s seg):
    returns: boolean
    language: sql
    volatility: immutable
    body: select true
  function trg():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  domain props:
    type: hstore
    default: "'v=>1'"
  table item:
    columns:
      id: {type: bigint}
      attrs: {type: hstore, default: "'a=>1'"}
      s: {type: seg, nullable: true, default: "'0 .. 10'"}
      kind: {type: text, nullable: true, generated: "(attrs || 'k=>v') -> 'kind'"}
    constraints:
      item_s: {type: check, expression: "is_open(s) and s <@ '0 .. 10'"}
    indexes:
      item_open: {columns: ["(attrs -> 'kind')"], where: "is_open(s) and attrs @> 'a=>1'"}
    rowLevelSecurity: true
    policies:
      item_p: {using: "exists (select 1 from other o where o.id = item.id and o.attrs @> 'k=>v')"}
    triggers:
      item_t:
        timing: before
        events: [update]
        level: row
        when: "new.attrs @> 'locked=>true'"
        procedure: trg()
  table other:
    columns:
      id: {type: bigint}
      attrs: {type: hstore, nullable: true}
`)
	applyPlan(t, pool, diff.Plan(introspect(t, pool, d), d, true))
	live := assertExprProbeClean(t, pool, d, diff.ExprDefault, diff.ExprGenerated, diff.ExprConstraint, diff.ExprIndex, diff.ExprPolicy, diff.ExprTrigger, diff.ExprDomainDefault)
	if !reflect.DeepEqual(live.SearchPath, []string{"app", "ext"}) {
		t.Errorf("want the session's search_path on the snapshot, got %v", live.SearchPath)
	}
}

// TestIntegrationProbeExpressionsRenames renames a table, a column, a
// constraint, an index, a domain and a function whose definitions are
// spelled another way than PostgreSQL prints them: the plan holds the
// renames and nothing else.
func TestIntegrationProbeExpressionsRenames(t *testing.T) {
	pool, _ := exprProbeDatabase(t, "pgytest_expr_probe_renames", `
schema app:
  domain span:
    type: interval
    default: "'1 hour'"
  function f(i interval default '1 hour'):
    returns: int
    language: sql
    body: select 1
  table old_t:
    columns:
      id: {type: bigint}
      ttl: {type: interval, default: "'1 hour'"}
      at: {type: date, nullable: true}
    constraints:
      c_ttl: {type: check, expression: "ttl > '0 seconds'"}
      c_at: {type: check, expression: "at > '2020-1-1'"}
    indexes:
      i_at: {columns: [id], where: "at > '2020-1-1'"}
    policies:
      p: {using: "at > '2020-1-1' and old_t.ttl > '1 hour'"}
`)
	d := loadYAML(t, "app", `
schema app:
  domain span2:
    renamedFrom: span
    type: interval
    default: "'1 hour'"
  function f2(i interval default '1 hour'):
    renamedFrom: f
    returns: int
    language: sql
    body: select 1
  table new_t:
    renamedFrom: old_t
    columns:
      id: {type: bigint}
      ttl: {type: interval, default: "'1 hour'"}
      on_day: {type: date, nullable: true, renamedFrom: at}
    constraints:
      c_ttl2: {type: check, expression: "ttl > '0 seconds'", renamedFrom: c_ttl}
      c_at: {type: check, expression: "on_day > '2020-1-1'"}
    indexes:
      i_day: {columns: [id], where: "on_day > '2020-1-1'", renamedFrom: i_at}
    policies:
      p: {using: "on_day > '2020-1-1' and new_t.ttl > '1 hour'"}
`)
	live := probed(t, pool, d)
	for k, p := range live.ExprProbes {
		if p.Err != "" {
			t.Errorf("%s %s %s: %s", k.Kind, k.Object, k.Name, p.Err)
		}
	}
	plan := diff.Plan(live, d, true)
	want := []string{
		`alter table "app"."old_t" rename to "new_t";`,
		`alter domain "app"."span" rename to "span2";`,
		`alter function "app"."f"(i interval) rename to "f2";`,
		`alter table "app"."new_t" rename column "at" to "on_day";`,
		`alter index "app"."i_at" rename to "i_day";`,
		`alter table "app"."new_t" rename constraint "c_ttl" to "c_ttl2";`,
	}
	if !reflect.DeepEqual(plan.Creates, want) || len(plan.Alters)+len(plan.Drops)+len(plan.Concurrent) != 0 {
		t.Fatalf("want the renames only, got:\n%s%s", diff.Render(plan), diff.RenderConcurrent(plan))
	}
	applyPlan(t, pool, plan)
	again := diff.Plan(probed(t, pool, d), d, true)
	if sql := diff.Render(again) + diff.RenderConcurrent(again); sql != "" {
		t.Errorf("the renamed schema did not converge:\n%s", sql)
	}
}

// TestIntegrationProbeExpressionsNames probes tables whose temporary copy
// could capture a name: tables of one name in two schemas, a table named
// like a type its check uses, policies that qualify columns with their own
// table and read it or its namesake through a qualified name, and names
// that need quoting.
func TestIntegrationProbeExpressionsNames(t *testing.T) {
	pool, d := exprProbeDatabase(t, "pgytest_expr_probe_names", `
extensions:
  - {name: hstore, schema: public}
schema public:
  type status:
    type: enum
    labels: [new, done]
  table docs:
    columns:
      id: {type: bigint}
      team_id: {type: int, nullable: true}
      attrs: {type: hstore, nullable: true, default: "'a=>1'"}
    primaryKey: [id]
    rowLevelSecurity: true
    policies:
      team: {using: "exists (select 1 from members m where m.team_id = docs.team_id and m.attrs @> 'k=>v') and attrs @> 'x=>y'"}
      own: {using: "exists (select 1 from public.docs d where d.id = docs.id and d.attrs @> 'k=>v')"}
  table members:
    columns:
      team_id: {type: int}
      attrs: {type: hstore, nullable: true, default: "'m=>1'"}
    rowLevelSecurity: true
    policies:
      peers: {using: "exists (select 1 from docs d where d.team_id = members.team_id and d.attrs @> 'k=>v')"}
schema app:
  function trg():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table docs:
    columns:
      id: {type: bigint}
      attrs: {type: hstore, nullable: true, default: "'b=>2'"}
      st: {type: public.status, default: "'new'::public.status"}
    primaryKey: [id]
    constraints:
      docs_st: {type: check, expression: "st <> 'done' and attrs @> 'ok=>1' and docs is not null"}
    rowLevelSecurity: true
    policies:
      pub: {using: "exists (select 1 from public.docs d where d.id = docs.id and d.attrs @> 'k=>v')"}
      own: {using: "exists (select 1 from app.docs d where d.id = docs.id and d.attrs @> 'k=>v')"}
  table status:
    columns:
      id: {type: bigint}
      st: {type: public.status, default: "'new'"}
      label: {type: text, nullable: true, default: "'a' || 1e1::text"}
    constraints:
      status_st: {type: check, expression: "st = 'new'::public.status or label > 'a' || 1e1::text"}
  table Order Items:
    columns:
      id: {type: bigint}
      "Qty": {type: int, default: "01"}
      "user": {type: text, nullable: true}
      attrs: {type: hstore, nullable: true}
    constraints:
      qty: {type: check, expression: "\"Qty\" < 1e3", notValid: true}
    indexes:
      oi_expr: {columns: ["lower(\"user\")", "(\"Qty\" % 3)"], where: "\"Qty\" > 01"}
    rowLevelSecurity: true
    policies:
      own: {for: update, using: "\"user\" = current_user and \"Order Items\".\"Qty\" > 01", withCheck: "\"Qty\" between 01 and 1e2"}
    triggers:
      Check It:
        timing: after
        events: [update]
        level: row
        when: "old.\"Qty\" > 01"
        procedure: app.trg('it''s', 1e1)
`)
	live := assertExprProbeClean(t, pool, d, diff.ExprDefault, diff.ExprConstraint, diff.ExprIndex, diff.ExprPolicy, diff.ExprTrigger)
	for _, key := range []diff.ExprKey{
		{Kind: diff.ExprPolicy, Object: "public.docs", Name: "team"},
		{Kind: diff.ExprPolicy, Object: "public.docs", Name: "own"},
		{Kind: diff.ExprPolicy, Object: "public.members", Name: "peers"},
		{Kind: diff.ExprPolicy, Object: "app.docs", Name: "pub"},
		{Kind: diff.ExprPolicy, Object: "app.docs", Name: "own"},
		{Kind: diff.ExprConstraint, Object: "app.docs", Name: "docs_st"},
		{Kind: diff.ExprConstraint, Object: "app.status", Name: "status_st"},
		{Kind: diff.ExprPolicy, Object: "app.Order Items", Name: "own"},
		{Kind: diff.ExprTrigger, Object: "app.Order Items", Name: "Check It"},
	} {
		if live.ExprProbes[key] == nil {
			t.Errorf("%s %s %s was not probed: %v", key.Kind, key.Object, key.Name, exprProbeKeys(live))
		}
	}

	selfJoin := loadYAML(t, "app", `
extensions:
  - {name: hstore, schema: public}
schema public:
  table docs:
    columns:
      id: {type: bigint}
      team_id: {type: int, nullable: true}
      attrs: {type: hstore, nullable: true, default: "'a=>1'"}
    primaryKey: [id]
    rowLevelSecurity: true
    policies:
      own: {using: "exists (select 1 from docs d where d.id = docs.id and d.attrs @> 'k=>v')"}
`)
	live = probed(t, pool, selfJoin)
	p := live.ExprProbes[diff.ExprKey{Kind: diff.ExprPolicy, Object: "public.docs", Name: "own"}]
	if p == nil || !strings.Contains(p.Err, "reads its own table") {
		t.Fatalf("a policy that reads its own table through an unqualified name is not compared, got %+v", p)
	}
	want := []string{
		`drop policy "own" on "public"."docs";`,
		`create policy "own" on "public"."docs" using (exists (select 1 from docs d where d.id = docs.id and d.attrs @> 'k=>v'));`,
	}
	if got := diff.Plan(live, selfJoin, false); !reflect.DeepEqual(got.Alters, want) {
		t.Errorf("want the policy planned as the text comparison plans it, and nothing else, got %v", got.Alters)
	}
}
