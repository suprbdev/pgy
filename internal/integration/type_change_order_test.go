package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

const typeChangeOrderBefore = `
schema SCH:
  table account:
    columns:
      id: {type: int, primaryKey: true}
      code: {type: text}
      ref: {type: text, default: "'0'"}
      note: {type: text, nullable: true}
      note_len: {type: int, generated: length(note), nullable: true}
      note_size: {type: int, generated: octet_length(note), nullable: true}
    indexes:
      account_ref_pat: {columns: [ref], opclass: text_pattern_ops, comment: prefix search}
      account_ref_old: {columns: [ref], opclass: text_pattern_ops, where: "ref <> ''"}
    constraints:
      account_code_key: {type: unique, columns: [code]}
      account_ref_check: {type: check, expression: "ref <> ''", comment: not blank}
  table session:
    columns:
      id: {type: int, primaryKey: true}
      account_code: {type: text}
    foreignKeys:
      session_account_fkey: {columns: [account_code], references: {table: SCH.account, columns: [code]}, onDelete: cascade, comment: owner}
`

const typeChangeOrderAfter = `
schema SCH:
  table account:
    columns:
      id: {type: int, primaryKey: true}
      code: {type: int, using: "code::int"}
      ref: {type: int, default: "0", using: "ref::int"}
      note: {type: varchar(20), nullable: true}
      note_len: {type: int, nullable: true}
    indexes:
      account_ref_pat: {columns: [ref], comment: prefix search}
    constraints:
      account_code_key: {type: unique, columns: [code]}
      account_ref_check: {type: check, expression: "ref >= 0", comment: not blank}
  table session:
    columns:
      id: {type: int, primaryKey: true}
      account_code: {type: int, using: "account_code::int"}
    foreignKeys:
      session_account_fkey: {columns: [account_code], references: {table: SCH.account, columns: [code]}, onDelete: cascade, comment: owner}
`

func TestIntegrationTypeChangeOrdering(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, typeChangeOrderBefore)
	for _, stmt := range []string{
		`insert into ` + sch + `.account (id, code, ref, note) values (1, '10', '5', 'abc')`,
		`insert into ` + sch + `.session (id, account_code) values (1, '10')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	d, err := schema.LoadAndMerge([]string{writeYAML(t, strings.ReplaceAll(typeChangeOrderAfter, "SCH", sch))})
	if err != nil {
		t.Fatal(err)
	}
	opts := diff.Options{Unsafe: true, Prune: true}
	plan := diff.PlanWith(introspect(t, pool, d), d, opts)
	sql := diff.Render(plan) + diff.RenderConcurrent(plan)
	applyPlan(t, pool, plan)
	if again := diff.PlanWith(introspect(t, pool, d), d, opts); diff.Render(again)+diff.RenderConcurrent(again) != "" {
		t.Fatalf("plan did not converge:\n%s%s", diff.Render(again), diff.RenderConcurrent(again))
	}

	last := -1
	for _, want := range []string{
		`drop constraint "session_account_fkey"`,
		`alter column "note_len" drop expression`,
		`alter column "note_size" drop expression`,
		`drop index "` + sch + `"."account_ref_old"`,
		`drop index "` + sch + `"."account_ref_pat"`,
		`drop constraint "account_ref_check"`,
		`alter column "code" type int using code::int`,
		`alter column "ref" drop default`,
		`alter column "ref" type int using ref::int`,
		`alter column "ref" set default 0`,
		`create index if not exists "account_ref_pat"`,
		`add constraint "account_ref_check" check (ref >= 0) not valid`,
		`alter column "account_code" type int using account_code::int`,
		`add constraint "session_account_fkey"`,
		`drop column "note_size"`,
		`validate constraint "session_account_fkey"`,
	} {
		i := strings.Index(sql, want)
		if i < 0 || i < last {
			t.Errorf("%q is missing or out of order in:\n%s", want, sql)
		}
		last = max(last, i)
	}

	var codeType, refDefault, fkComment, checkComment, indexComment string
	var fkValid bool
	var ref, sessionCode int
	if err := pool.QueryRow(ctx, `
		select format_type(a.atttypid, a.atttypmod), pg_get_expr(ad.adbin, ad.adrelid)
		from pg_attribute a, pg_attribute r join pg_attrdef ad on ad.adrelid = r.attrelid and ad.adnum = r.attnum
		where a.attrelid = $1::regclass and a.attname = 'code' and r.attrelid = $1::regclass and r.attname = 'ref'`, sch+".account").Scan(&codeType, &refDefault); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		select convalidated, coalesce(obj_description(oid, 'pg_constraint'), '') from pg_constraint
		where conrelid = $1::regclass and conname = 'session_account_fkey'`, sch+".session").Scan(&fkValid, &fkComment); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		select coalesce(obj_description(oid, 'pg_constraint'), '') from pg_constraint
		where conrelid = $1::regclass and conname = 'account_ref_check'`, sch+".account").Scan(&checkComment); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select coalesce(obj_description($1::regclass, 'pg_class'), '')`, sch+".account_ref_pat").Scan(&indexComment); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select a.ref, s.account_code from `+sch+`.account a join `+sch+`.session s on s.account_code = a.code`).Scan(&ref, &sessionCode); err != nil {
		t.Fatal(err)
	}
	if codeType != "integer" || refDefault != "0" || !fkValid || fkComment != "owner" || checkComment != "not blank" || indexComment != "prefix search" || ref != 5 || sessionCode != 10 {
		t.Errorf("after the type changes: code %s, ref default %s, foreign key valid %v with comment %q, check comment %q, index comment %q, ref %d, session code %d",
			codeType, refDefault, fkValid, fkComment, checkComment, indexComment, ref, sessionCode)
	}
}

func TestIntegrationTypeChangeRecreatesWholeRelationDependents(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      b: {type: int, nullable: true}
  view v0:
    query: select id, b from SCH.t
  view w:
    query: select count(*) as n from SCH.v0
`)
	if _, err := pool.Exec(ctx, `insert into `+sch+`.t values (1, 10), (2, 20)`); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{writeYAML(t, strings.ReplaceAll(`
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      b: {type: bigint, nullable: true}
  view v:
    renamedFrom: v0
    query: select id, b from SCH.t
  view w:
    query: select count(*) as n from SCH.v
`, "SCH", sch))})
	if err != nil {
		t.Fatal(err)
	}
	live := introspect(t, pool, d)
	if deps := live.ViewDependents[sch+".v0"]; len(deps) != 1 || deps[0] != sch+".w" {
		t.Fatalf("w uses v0 as a whole: %v", live.ViewDependents)
	}
	p := diff.Plan(live, d, true)
	for _, w := range p.Warnings {
		if w.Kind == "dependents" {
			t.Errorf("both views are declared: %v", w)
		}
	}
	applyPlan(t, pool, p)
	if again := diff.Plan(introspect(t, pool, d), d, true); diff.Render(again) != "" {
		t.Fatalf("plan did not converge:\n%s", diff.Render(again))
	}
	var typ string
	var n int
	if err := pool.QueryRow(ctx, `select format_type(atttypid, atttypmod) from pg_attribute where attrelid = $1::regclass and attname = 'b'`, sch+".v").Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select n from `+sch+`.w`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if typ != "bigint" || n != 2 {
		t.Errorf("v.b is %s and w counts %d rows", typ, n)
	}
}

func TestIntegrationTypeChangeWarnsAboutGeneratedColumn(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	y := `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      name: {type: %s, default: "''"}
      name_lc: {type: text, generated: lower(name), nullable: true}
`
	convergeYAML(t, pool, sch, strings.Replace(y, "%s", "text", 1))
	d, err := schema.LoadAndMerge([]string{writeYAML(t, strings.ReplaceAll(strings.Replace(y, "%s", "varchar(40)", 1), "SCH", sch))})
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(introspect(t, pool, d), d, true)
	found := false
	for _, w := range p.Warnings {
		found = found || (w.Kind == "dependents" && strings.Contains(w.Message, "generated column name_lc uses it"))
	}
	if sql := diff.Render(p); !found || strings.Contains(sql, "name_lc") {
		t.Errorf("want a warning naming the generated column and no statement for it, got %v\n%s", p.Warnings, sql)
	}
}
