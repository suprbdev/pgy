package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func inheritedNotNullNameDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{
		"app.m": {Name: "m", Columns: map[string]*schema.Column{"id": {Type: "integer"}, "k": {Type: "text"}, "v": {Type: "integer", Nullable: true}},
			PartitionBy: &schema.PartitionBy{Type: "list", Columns: []string{"k"}}},
		"app.m_b": {Name: "m_b", PartitionOf: "app.m", Partition: &schema.PartitionSpec{In: []string{"b"}, Modulus: -1, Remainder: -1},
			Columns: map[string]*schema.Column{"id": {NotNullName: "m_b_id_not_null"}, "v": {NullableSet: true, NotNullName: "m_b_v_nn"}}},
		"app.base": {Name: "base", Columns: map[string]*schema.Column{"b": {Type: "text"}}},
		"app.kid":  {Name: "kid", Inherits: []string{"app.base"}, Columns: map[string]*schema.Column{"b": {NotNullName: "kid_b_nn"}}},
	}}
}

func TestInheritedNotNullNameCreate(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	sql := Render(Plan(live, inheritedNotNullNameDB(), false))
	for _, want := range []string{
		`create table if not exists "app"."m_b" partition of "app"."m" ("id" constraint "m_b_id_not_null" not null) for values in ('b');`,
		`alter table "app"."m_b" add constraint "m_b_v_nn" not null "v";`,
		`create table if not exists "app"."kid" ("b" text constraint "kid_b_nn" not null) inherits ("app"."base");`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if sql := Render(Plan(Simulate(live, inheritedNotNullNameDB()), inheritedNotNullNameDB(), false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
	live.ServerVersion = 170004
	if sql := Render(Plan(live, inheritedNotNullNameDB(), false)); strings.Contains(sql, "m_b_id_not_null") || !strings.Contains(sql, `alter table "app"."m_b" alter column "v" set not null;`) {
		t.Errorf("before PostgreSQL 18 the names are left out:\n%s", sql)
	}
}

func TestInheritedNotNullNameAlter(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	live.Schemas["app"] = true
	for fq, cols := range map[string]map[string]*LiveColumn{
		"app.m":    {"id": {Type: "integer", NotNullName: "m_id_not_null"}, "k": {Type: "text", NotNullName: "m_k_not_null"}, "v": {Type: "integer", Nullable: true}},
		"app.m_b":  {"id": {Type: "integer", NotNullName: "m_id_not_null", Inherited: true}, "k": {Type: "text", NotNullName: "m_k_not_null", Inherited: true}, "v": {Type: "integer", NotNullName: "m_b_v_not_null", Inherited: true}},
		"app.base": {"b": {Type: "text", NotNullName: "base_b_not_null"}},
		"app.kid":  {"b": {Type: "text", NotNullName: "kid_b_nn", Inherited: true}},
	} {
		lt := newLiveTable()
		lt.Columns = cols
		live.Tables[fq] = lt
	}
	live.Tables["app.m"].Partitioned = true
	live.Tables["app.m_b"].IsPartition = true
	live.Tables["app.kid"].Inherits = []string{"app.base"}
	live.Partitions = map[string][]string{"app.m": {"app.m_b"}}
	live.PartitionBounds = map[string]LivePartitionBound{"app.m_b": {Parent: "app.m", Bound: "FOR VALUES IN ('b')"}}
	p := Plan(live, inheritedNotNullNameDB(), false)
	if sql := Render(p); sql != `alter table "app"."m_b" rename constraint "m_b_v_not_null" to "m_b_v_nn";`+"\n" {
		t.Errorf("only the partition's own constraint can be renamed, got:\n%s", sql)
	}
	warned := false
	for _, w := range p.Warnings {
		warned = warned || strings.Contains(w.Message, "column app.m_b.id: its NOT NULL constraint m_id_not_null is inherited from app.m, and PostgreSQL cannot rename an inherited constraint")
	}
	if !warned {
		t.Errorf("expected a warning about the inherited name, got %+v", p.Warnings)
	}
}
