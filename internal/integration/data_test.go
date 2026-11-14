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

const dataBase = `
schema SCH:
  type tier:
    type: enum
    labels: [low, high]
  table plan:
    columns:
      code: {type: text}
      name: {type: text}
      price: {type: "numeric(6,2)", nullable: true}
      tier: {type: SCH.tier, nullable: true}
      active: {type: boolean, default: "true"}
      tags: {type: "text[]", nullable: true}
      meta: {type: jsonb, nullable: true}
      starts: {type: date, nullable: true}
    primaryKey: [code]
    data:
      rows:
        - {code: free, name: Free, price: 0, tier: low, active: true, tags: [a, "b c"], meta: {x: 1}, starts: 2026-01-01}
        - {code: pro, name: Pro, price: 10, tier: high}
        PRO_EXTRA
  table feature:
    columns:
      id: {type: int, identity: always}
      plan: {type: text}
      label: {type: text}
    primaryKey: [id]
    foreignKeys:
      feature_plan_fkey: {columns: [plan], references: {table: SCH.plan, columns: [code]}}
    data:
      rows:
        - {id: 1, plan: free, label: basics}
        - {id: 2, plan: pro, label: FEATURE2}
`

func TestIntegrationDataIdempotent(t *testing.T) {
	y := strings.NewReplacer("PRO_EXTRA", "- {code: ent, name: Enterprise, price: 99.5, tier: high, tags: []}", "FEATURE2", "everything").Replace(dataBase)
	assertYAMLIdempotent(t, map[string]string{"data": y})
}

func TestIntegrationDataChanges(t *testing.T) {
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
	step := func(y string, prune bool) string {
		t.Helper()
		d := load(y)
		opts := diff.Options{Unsafe: true, Prune: prune}
		p := diff.PlanWith(introspect(t, pool, d), d, opts)
		applyPlan(t, pool, p)
		if again := diff.Render(diff.PlanWith(introspect(t, pool, d), d, opts)); again != "" {
			t.Fatalf("data did not converge:\n%s", again)
		}
		return diff.Render(p)
	}
	v1 := strings.NewReplacer("PRO_EXTRA", "- {code: ent, name: Enterprise, price: 99.5}", "FEATURE2", "everything").Replace(dataBase)
	step(v1, false)
	if _, err := pool.Exec(ctx, `insert into "`+sch+`".plan (code, name) values ('legacy', 'Legacy')`); err != nil {
		t.Fatal(err)
	}
	v2 := strings.NewReplacer("PRO_EXTRA", "- {code: team, name: Team, price: 25}", "FEATURE2", "more").Replace(dataBase)
	sql := step(v2, false)
	for _, want := range []string{
		`insert into "` + sch + `"."plan" ("code", "name", "price") values ('team', 'Team', 25);`,
		`update only "` + sch + `"."feature" set "label" = 'more' where "id" = 2;`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "delete") || strings.Contains(sql, `"free"`) {
		t.Errorf("without --prune nothing is deleted and unchanged rows are left alone:\n%s", sql)
	}
	sql = step(v2, true)
	if !strings.Contains(sql, `delete from only "`+sch+`"."plan" where "code" = 'ent';`) || !strings.Contains(sql, `where "code" = 'legacy';`) {
		t.Errorf("--prune must delete undeclared rows:\n%s", sql)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from "`+sch+`".plan`).Scan(&n); err != nil || n != 3 {
		t.Errorf("expected 3 plans, got %d (%v)", n, err)
	}
}

func TestIntegrationDataSurvivesRenames(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, `
schema SCH:
  table status:
    columns: {code: {type: text}, label: {type: text}}
    primaryKey: [code]
    data:
      rows: [{code: open, label: Open}, {code: done, label: Done}]
`)
	sql := convergeYAML(t, pool, sch, `
schema SCH:
  table state:
    renamedFrom: status
    columns: {key: {type: text, renamedFrom: code}, label: {type: text}}
    primaryKey: [key]
    data:
      rows: [{key: open, label: Open}, {key: done, label: Finished}]
`)
	if !contains(sql, `rename column "code" to "key"`) || contains(sql, "drop table") {
		t.Errorf("expected renames, got:\n%s", sql)
	}
	var label string
	if err := pool.QueryRow(context.Background(), `select label from "`+sch+`".state where key = 'done'`).Scan(&label); err != nil || label != "Finished" {
		t.Errorf("label = %q (%v)", label, err)
	}
}

func TestIntegrationDataKeyColumnRename(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, `
schema SCH:
  table status:
    columns: {code: {type: text}, label: {type: text}}
    primaryKey: [code]
    data:
      rows: [{code: open, label: Open}]
`)
	convergeYAML(t, pool, sch, `
schema SCH:
  table status:
    columns: {key: {type: text, renamedFrom: code}, label: {type: text}}
    primaryKey: [key]
    data:
      rows: [{key: open, label: Opened}]
`)
}

func TestIntegrationDataOnInheritanceParent(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	y := `
schema SCH:
  table event:
    columns: {id: {type: int}, kind: {type: text}}
    primaryKey: [id]
    data:
      rows: [{id: 1, kind: base}]
  table login_event:
    inherits: [SCH.event]
    columns: {ip: {type: text, nullable: true}}
`
	convergeYAML(t, pool, sch, y)
	if _, err := pool.Exec(ctx, `insert into "`+sch+`".login_event (id, kind) values (2, 'login')`); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(y, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	if sql := diff.Render(diff.PlanWith(introspect(t, pool, d), d, diff.Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("rows of an inheriting table are not the parent's data:\n%s", sql)
	}
}

func TestIntegrationDataOnPartitionedTable(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	base := `
schema SCH:
  table region:
    columns: {code: {type: text}, zone: {type: text}, name: {type: text}}
    primaryKey: [code, zone]
    partitionBy: {type: list, columns: [zone]}
    data:
      rows: ROWS
  table region_eu:
    partitionOf: SCH.region
    forValues: {in: [eu]}
  table region_us:
    partitionOf: SCH.region
    forValues: {in: [us]}
`
	convergeYAML(t, pool, sch, strings.ReplaceAll(base, "ROWS", `[{code: de, zone: eu, name: Germany}, {code: ny, zone: us, name: NY}, {code: fr, zone: eu, name: France}]`))
	d := strings.ReplaceAll(base, "ROWS", `[{code: de, zone: eu, name: Deutschland}, {code: ny, zone: us, name: NY}]`)
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(d, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.PlanWith(introspect(t, pool, db), db, diff.Options{Unsafe: true, Prune: true})
	sql := diff.Render(plan)
	if !strings.Contains(sql, `update "`+sch+`"."region" set "name" = 'Deutschland'`) || !strings.Contains(sql, `delete from "`+sch+`"."region" where "code" = 'fr' and "zone" = 'eu';`) {
		t.Fatalf("expected an update and a delete through the partitioned parent:\n%s", sql)
	}
	applyPlan(t, pool, plan)
	if again := diff.Render(diff.PlanWith(introspect(t, pool, db), db, diff.Options{Unsafe: true, Prune: true})); again != "" {
		t.Errorf("not converged:\n%s", again)
	}
}
