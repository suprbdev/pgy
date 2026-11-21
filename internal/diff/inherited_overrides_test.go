package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func generatedOverrideDB(virtual bool) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{
		"app.m": {Name: "m", Columns: map[string]*schema.Column{"id": {Type: "integer"}, "k": {Type: "text"}, "s": {Type: "integer", Nullable: true, Generated: "id * 2", Virtual: virtual}},
			PartitionBy: &schema.PartitionBy{Type: "list", Columns: []string{"k"}}},
		"app.m_a": {Name: "m_a", PartitionOf: "app.m", Partition: &schema.PartitionSpec{In: []string{"a"}, Modulus: -1, Remainder: -1},
			Columns: map[string]*schema.Column{"s": {Generated: "id * 20"}}},
		"app.base": {Name: "base", Columns: map[string]*schema.Column{"id": {Type: "integer", Nullable: true}, "g": {Type: "integer", Nullable: true, Generated: "id + 1", Collate: "C"}}},
		"app.kid":  {Name: "kid", Inherits: []string{"app.base"}, Columns: map[string]*schema.Column{"g": {Generated: "id + 100"}}},
	}}
}

func generatedOverrideLive(partitionExpr, kidExpr string) *Live {
	live := emptyLive()
	live.ServerVersion = 180000
	live.Schemas["app"] = true
	for fq, cols := range map[string]map[string]*LiveColumn{
		"app.m":    {"id": {Type: "integer"}, "k": {Type: "text"}, "s": {Type: "integer", Nullable: true, Generated: "(id * 2)"}},
		"app.m_a":  {"id": {Type: "integer", Inherited: true}, "k": {Type: "text", Inherited: true}, "s": {Type: "integer", Nullable: true, Generated: partitionExpr, Inherited: true}},
		"app.base": {"id": {Type: "integer", Nullable: true}, "g": {Type: "integer", Nullable: true, Generated: "(id + 1)", Collation: "C"}},
		"app.kid":  {"id": {Type: "integer", Nullable: true, Inherited: true}, "g": {Type: "integer", Nullable: true, Generated: kidExpr, Inherited: true}},
	} {
		lt := newLiveTable()
		lt.Columns = cols
		live.Tables[fq] = lt
	}
	live.Tables["app.m"].Partitioned = true
	live.Tables["app.m_a"].IsPartition = true
	live.Tables["app.kid"].Inherits = []string{"app.base"}
	live.Partitions = map[string][]string{"app.m": {"app.m_a"}}
	live.PartitionBounds = map[string]LivePartitionBound{"app.m_a": {Parent: "app.m", Bound: "FOR VALUES IN ('a')"}}
	return live
}

func TestInheritedGeneratedCreate(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	sql := Render(Plan(live, generatedOverrideDB(false), false))
	for _, want := range []string{
		`create table if not exists "app"."m_a" partition of "app"."m" ("s" generated always as (id * 20) stored) for values in ('a');`,
		`create table if not exists "app"."kid" ("g" integer collate "C" generated always as (id + 100) stored) inherits ("app"."base");`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "set expression") {
		t.Errorf("a new table gets its expression in CREATE TABLE:\n%s", sql)
	}
	if sql := Render(Plan(live, generatedOverrideDB(true), false)); !strings.Contains(sql, `partition of "app"."m" ("s" generated always as (id * 20) virtual)`) {
		t.Errorf("a virtual parent column gets a virtual override:\n%s", sql)
	}
	if sql := Render(Plan(Simulate(live, generatedOverrideDB(false)), generatedOverrideDB(false), false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}

func TestInheritedGeneratedSkippedIfSame(t *testing.T) {
	if p := Plan(generatedOverrideLive("(id * 20)", "(id + 100)"), generatedOverrideDB(false), true); Render(p) != "" || len(p.Warnings) != 0 {
		t.Errorf("matching overrides need nothing, got %q, %+v", Render(p), p.Warnings)
	}
	d := generatedOverrideDB(false)
	d.Tables["app.m_a"].Columns = nil
	d.Tables["app.kid"].Columns = nil
	if sql := Render(Plan(generatedOverrideLive("(id * 20)", "(id + 100)"), d, true)); sql != "" {
		t.Errorf("an override the YAML leaves out is unmanaged, got:\n%s", sql)
	}
}

func TestInheritedGeneratedAlter(t *testing.T) {
	live := generatedOverrideLive("(id * 2)", "(id + 1)")
	if sql := Render(Plan(live, generatedOverrideDB(false), false)); sql != "" {
		t.Errorf("changing an expression rewrites the table and needs --unsafe, got:\n%s", sql)
	}
	sql := Render(Plan(live, generatedOverrideDB(false), true))
	for _, want := range []string{
		`alter table only "app"."m_a" alter column "s" set expression as (id * 20);`,
		`alter table only "app"."kid" alter column "g" set expression as (id + 100);`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	live.ServerVersion = 160004
	p := Plan(live, generatedOverrideDB(false), true)
	if strings.Contains(Render(p), "set expression") {
		t.Errorf("PostgreSQL 16 has no SET EXPRESSION:\n%s", Render(p))
	}
	warned := false
	for _, w := range p.Warnings {
		warned = warned || strings.Contains(w.Message, "column app.m_a.s overrides its parent's generation expression, which needs ALTER COLUMN ... SET EXPRESSION (PostgreSQL 17+)")
	}
	if !warned {
		t.Errorf("expected a warning, got %+v", p.Warnings)
	}
}

func TestInheritedGeneratedKeptWhenParentChanges(t *testing.T) {
	live := generatedOverrideLive("(id * 20)", "(id + 100)")
	d := generatedOverrideDB(false)
	d.Tables["app.m"].Columns["s"].Generated = "id * 3"
	p := Plan(live, d, true)
	parent, child := -1, -1
	for i, s := range p.Alters {
		switch s {
		case `alter table "app"."m" alter column "s" set expression as (id * 3);`:
			parent = i
		case `alter table only "app"."m_a" alter column "s" set expression as (id * 20);`:
			child = i
		}
	}
	if parent < 0 || child < parent {
		t.Errorf("the parent's SET EXPRESSION reaches its partitions, so the partition's own is set again after it: %v", p.Alters)
	}
	if strings.Contains(Render(p), `"app"."kid"`) {
		t.Errorf("a child of an unchanged parent keeps its expression:\n%s", Render(p))
	}
}

func TestInheritedGeneratedOnAddedColumn(t *testing.T) {
	live := generatedOverrideLive("", "(id + 100)")
	delete(live.Tables["app.m"].Columns, "s")
	delete(live.Tables["app.m_a"].Columns, "s")
	p := Plan(live, generatedOverrideDB(false), false)
	if !findAlter(p, `alter table only "app"."m_a" alter column "s" set expression as (id * 20);`) {
		t.Errorf("a column the parent adds gets the partition's expression after it:\n%s", Render(p))
	}
}

func notValidCopyDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{
		"app.base": {Name: "base", Columns: map[string]*schema.Column{"e": {Type: "text", NotValid: true, NotNullName: "base_e_nn"}}},
		"app.kid":  {Name: "kid", Inherits: []string{"app.base"}, Columns: map[string]*schema.Column{"e": {NotValidSet: true}}},
	}}
}

func TestInheritedNotNullValidatedCopyCreate(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	p := Plan(live, notValidCopyDB(), false)
	add, validate := -1, -1
	for i, s := range p.Alters {
		switch s {
		case `alter table "app"."base" add constraint "base_e_nn" not null "e" not valid;`:
			add = i
		case `alter table "app"."kid" validate constraint "base_e_nn";`:
			validate = i
		}
	}
	if add < 0 || validate < add {
		t.Errorf("the child's copy of the parent's NOT VALID constraint is validated after the parent adds it: %v", p.Alters)
	}
	d := notValidCopyDB()
	d.Tables["app.kid"].Columns["e"].NotValidSet = false
	if strings.Contains(Render(Plan(live, d, false)), "validate constraint") {
		t.Errorf("without notValid: false the child's copy is left NOT VALID:\n%s", Render(Plan(live, d, false)))
	}
}

func TestInheritedNotNullValidatedCopyAlter(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	live.Schemas["app"] = true
	base, kid := newLiveTable(), newLiveTable()
	base.Columns["e"] = &LiveColumn{Type: "text", NotNullName: "base_e_nn", NotNullNotValid: "base_e_nn"}
	kid.Columns["e"] = &LiveColumn{Type: "text", NotNullName: "base_e_nn", Inherited: true}
	kid.Inherits = []string{"app.base"}
	live.Tables["app.base"], live.Tables["app.kid"] = base, kid
	if p := Plan(live, notValidCopyDB(), false); Render(p) != "" || len(p.Concurrent) != 0 {
		t.Errorf("a validated copy needs nothing, got %q, %v", Render(p), p.Concurrent)
	}
	kid.Columns["e"].NotNullNotValid = "base_e_nn"
	p := Plan(live, notValidCopyDB(), false)
	if Render(p) != "" || len(p.Concurrent) != 1 || p.Concurrent[0] != `alter table "app"."kid" validate constraint "base_e_nn";` {
		t.Errorf("a NOT VALID copy is validated in the no-transaction migration, got %q, %v", Render(p), p.Concurrent)
	}
}

func TestInheritedNotNullValidatedCopyReadded(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	live.Schemas["app"] = true
	base, kid := newLiveTable(), newLiveTable()
	base.Columns["e"] = &LiveColumn{Type: "text", Nullable: true}
	kid.Columns["e"] = &LiveColumn{Type: "text", Nullable: true, Inherited: true}
	kid.Inherits = []string{"app.base"}
	live.Tables["app.base"], live.Tables["app.kid"] = base, kid
	p := Plan(live, notValidCopyDB(), false)
	if !findAlter(p, `alter table "app"."base" add constraint "base_e_nn" not null "e" not valid;`) {
		t.Fatalf("the parent's NOT VALID constraint is added again:\n%s", Render(p))
	}
	if len(p.Concurrent) != 1 || p.Concurrent[0] != `alter table "app"."kid" validate constraint "base_e_nn";` {
		t.Errorf("the child's copy is validated in the no-transaction migration after the parent adds it, got %v", p.Concurrent)
	}
}
