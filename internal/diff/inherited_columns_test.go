package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func intp(n int) *int { return &n }

func TestInheritedColumnSettings(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{
		"app.m": {Name: "m", Columns: map[string]*schema.Column{"at": {Type: "date"}, "v": {Type: "int", Nullable: true, Default: "0"}}, PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"at"}}},
		"app.m1": {Name: "m1", PartitionOf: "app.m", Partition: &schema.PartitionSpec{From: []string{"2025-01-01"}, To: []string{"2026-01-01"}, Modulus: -1, Remainder: -1},
			Columns: map[string]*schema.Column{"v": {Default: "5", StatisticsTarget: intp(200), Comment: "partition v"}}},
		"app.base": {Name: "base", Columns: map[string]*schema.Column{"a": {Type: "int", Nullable: true, Default: "1"}}},
		"app.kid": {Name: "kid", Inherits: []string{"app.base"}, Columns: map[string]*schema.Column{"a": {Default: "7", Nullable: false, NullableSet: true}, "c": {Type: "int", Nullable: true}}},
	}}
	sql := Render(Plan(emptyLive(), d, false))
	for _, want := range []string{
		`alter table "app"."m1" alter column "v" set default 5;`,
		`alter table "app"."m1" alter column "v" set statistics 200;`,
		`comment on column "app"."m1"."v" is 'partition v';`,
		`create table if not exists "app"."kid" ("c" int) inherits ("app"."base");`,
		`alter table "app"."kid" alter column "a" set default 7;`,
		`alter table "app"."kid" alter column "a" set not null;`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, `alter column "v" set not null`) {
		t.Error("nullability is unmanaged unless nullable is written")
	}

	live := emptyLive()
	live.Schemas["app"] = true
	for fq, cols := range map[string]map[string]*LiveColumn{
		"app.m":  {"at": {Type: "date"}, "v": {Type: "integer", Nullable: true, Default: "0"}},
		"app.m1": {"at": {Type: "date", Inherited: true}, "v": {Type: "integer", Nullable: true, Default: "5", Inherited: true, StatisticsTarget: intp(200), Comment: "partition v"}},
		"app.base": {"a": {Type: "integer", Nullable: true, Default: "1"}},
		"app.kid": {"a": {Type: "integer", Default: "7", Inherited: true}, "c": {Type: "integer", Nullable: true}},
	} {
		lt := newLiveTable()
		lt.Columns = cols
		live.Tables[fq] = lt
	}
	live.Tables["app.m"].Partitioned = true
	live.Tables["app.m1"].IsPartition = true
	live.Tables["app.kid"].Inherits = []string{"app.base"}
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("matching inherited settings must not diff:\n%s", sql)
	}
	live.Tables["app.m1"].Columns["v"].Default = "99"
	if sql := Render(Plan(live, d, false)); sql != `alter table "app"."m1" alter column "v" set default 5;`+"\n" {
		t.Errorf("changed partition default: got:\n%s", sql)
	}
	if sql := Render(Plan(Simulate(emptyLive(), d), d, false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}
