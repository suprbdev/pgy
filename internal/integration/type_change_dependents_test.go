package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

const typeChangeDependents = `
schema SCH:
  function touch():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table t:
    columns: {id: {type: int, primaryKey: true}, b: {type: %s, nullable: true}, c: {type: text, nullable: true}}
    rowLevelSecurity: true
    policies: {t_b: {for: select, using: b is not null}}
    triggers: {t_touch: {timing: before, events: [update of b], level: row, procedure: SCH.touch()}}
  view v:
    query: select id, b from SCH.t
    dependsOn: [table SCH.t]
  view vv:
    query: select id, b + 1 as b1 from SCH.v
    dependsOn: [view SCH.v]
  materialized view mv:
    query: select b from SCH.t
    dependsOn: [table SCH.t]
    indexes: {mv_b: {columns: [b]}}
`

func TestIntegrationColumnTypeChangeRecreatesDependents(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, strings.Replace(typeChangeDependents, "%s", "int", 1))
	for _, stmt := range []string{
		`comment on view ` + sch + `.v is 'unmanaged comment'`,
		`comment on column ` + sch + `.vv.b1 is 'plus one'`,
		`grant select on ` + sch + `.vv to public`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	sql := convergeYAML(t, pool, sch, strings.Replace(typeChangeDependents, "%s", "bigint", 1))
	for _, want := range []string{"drop view", "drop materialized view", "drop trigger", "drop policy", "type bigint"} {
		if !contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	var typ, comment, colComment string
	var granted bool
	if err := pool.QueryRow(ctx, `select format_type(atttypid, atttypmod) from pg_attribute where attrelid = $1::regclass and attname = 'b1'`, sch+".vv").Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select coalesce(obj_description($1::regclass, 'pg_class'), ''), coalesce(col_description($2::regclass, 2), ''), has_table_privilege('public', $2::regclass, 'select')`, sch+".v", sch+".vv").Scan(&comment, &colComment, &granted); err != nil {
		t.Fatal(err)
	}
	if typ != "bigint" || comment != "unmanaged comment" || colComment != "plus one" || !granted {
		t.Errorf("after recreation: type %s, comment %q, column comment %q, public select %v", typ, comment, colComment, granted)
	}
}

func TestIntegrationRecreatedViewsRestoreUnmanaged(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	reader := freshRole(t, pool, "reader")
	y := `
schema SCH:
  function noop():
    returns: trigger
    language: plpgsql
    body: begin return null; end
  table t:
    columns: {id: {type: int, primaryKey: true}, b: {type: %s, nullable: true}}
  view v:
    query: select id, b from SCH.t
  view x:
    query: %s
`
	convergeYAML(t, pool, sch, fmt.Sprintf(y, "int", "select id from SCH.t"))
	if _, err := pool.Exec(ctx, strings.NewReplacer("READER", reader, "SCH", sch).Replace(`
		grant select (b) on SCH.v to "READER";
		create trigger v_ins instead of insert on SCH.v for each row execute function SCH.noop();
		comment on trigger v_ins on SCH.v is 'unmanaged trigger';
		create rule v_del as on delete to SCH.v do instead nothing;
		comment on view SCH.x is 'unmanaged comment';
		grant select on SCH.x to "READER";
		alter view SCH.x set (security_barrier = true);`)); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{writeYAML(t, strings.ReplaceAll(fmt.Sprintf(y, "bigint", "select id::text as id from SCH.t"), "SCH", sch))})
	if err != nil {
		t.Fatal(err)
	}
	probed := func() *diff.Live {
		live := introspect(t, pool, d)
		if err := diff.ProbeViews(ctx, pool, live, d); err != nil {
			t.Fatal(err)
		}
		return live
	}
	p := diff.Plan(probed(), d, true)
	sql := diff.Render(p)
	for _, want := range []string{`drop view "` + sch + `"."v";`, `drop view if exists "` + sch + `"."x";`} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	applyPlan(t, pool, p)
	if again := diff.Plan(probed(), d, true); diff.Render(again) != "" {
		t.Fatalf("plan did not converge:\n%s", diff.Render(again))
	}
	var colGranted, granted, rule bool
	var trigger, comment, options string
	if err := pool.QueryRow(ctx, `
		select has_column_privilege($1, $2::regclass, 'b', 'select'),
		       coalesce((select obj_description(oid, 'pg_trigger') from pg_trigger where tgrelid = $2::regclass and tgname = 'v_ins'), 'missing'),
		       exists (select 1 from pg_rewrite where ev_class = $2::regclass and rulename = 'v_del'),
		       coalesce(obj_description($3::regclass, 'pg_class'), ''),
		       has_table_privilege($1, $3::regclass, 'select'),
		       coalesce(array_to_string((select reloptions from pg_class where oid = $3::regclass), ','), '')`,
		reader, sch+".v", sch+".x").Scan(&colGranted, &trigger, &rule, &comment, &granted, &options); err != nil {
		t.Fatal(err)
	}
	if !colGranted || trigger != "unmanaged trigger" || !rule || comment != "unmanaged comment" || !granted || options != "security_barrier=true" {
		t.Errorf("after recreation: v column grant %v, trigger %q, rule %v; x comment %q, grant %v, options %q", colGranted, trigger, rule, comment, granted, options)
	}
}

func TestIntegrationColumnTypeChangeWarnsAboutUndeclaredView(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	y := "\nschema SCH:\n  table t:\n    columns: {id: {type: int, primaryKey: true}, b: {type: %s, nullable: true}}\n"
	convergeYAML(t, pool, sch, strings.Replace(y, "%s", "int", 1))
	if _, err := pool.Exec(ctx, `create view `+sch+`.hidden as select b from `+sch+`.t`); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{writeYAML(t, strings.ReplaceAll(strings.Replace(y, "%s", "bigint", 1), "SCH", sch))})
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(introspect(t, pool, d), d, true)
	found := false
	for _, w := range p.Warnings {
		found = found || strings.Contains(w.Message, "view "+sch+".hidden depends on it and isn't declared")
	}
	if !found || strings.Contains(diff.Render(p), "drop view") {
		t.Errorf("want a warning and no drop, got %v\n%s", p.Warnings, diff.Render(p))
	}
}

func writeYAML(t *testing.T, y string) string {
	t.Helper()
	p := t.TempDir() + "/s.yml"
	if err := os.WriteFile(p, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
