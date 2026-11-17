package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func accessMethodDesired() *schema.Database {
	return &schema.Database{
		Tables: map[string]*schema.Table{
			"app.t": {Name: "t", AccessMethod: "columnar", With: map[string]string{"fillfactor": "70"}, Tablespace: "fast",
				Columns: map[string]*schema.Column{"id": {Type: "int"}}},
			"app.p": {Name: "p", AccessMethod: "columnar",
				Columns:     map[string]*schema.Column{"id": {Type: "int"}},
				PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"id"}}},
			"app.p1": {Name: "p1", AccessMethod: "heap", PartitionOf: "app.p",
				Partition: &schema.PartitionSpec{From: []string{"0"}, To: []string{"10"}, Modulus: -1, Remainder: -1}},
		},
		Views: map[string]*schema.View{"app.mv": {Name: "mv", Materialized: true, Query: "select 1", AccessMethod: "columnar"}},
	}
}

func TestAccessMethodCreate(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 170000
	p := Plan(live, accessMethodDesired(), false)
	sql := Render(p)
	for _, want := range []string{
		`create table if not exists "app"."t" ("id" int not null) using "columnar" with (fillfactor = 70) tablespace "fast";`,
		`create table if not exists "app"."p" ("id" int not null) partition by range ("id") using "columnar";`,
		`create table if not exists "app"."p1" partition of "app"."p" for values from (0) to (10) using "heap";`,
		`create materialized view if not exists "app"."mv" using "columnar" as select 1;`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.Warnings)
	}
}

func TestAccessMethodPartitionedBeforePostgres17(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 160004
	p := Plan(live, accessMethodDesired(), false)
	sql := Render(p)
	if !strings.Contains(sql, `create table if not exists "app"."p" ("id" int not null) partition by range ("id");`) || !strings.Contains(sql, `using "heap"`) {
		t.Errorf("a partitioned table gets no USING before PostgreSQL 17, its partitions do:\n%s", sql)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "app.p is partitioned, and PostgreSQL 16 keeps no access method for a partitioned table") {
		t.Errorf("want one warning about app.p: %v", p.Warnings)
	}
}

func accessMethodLive(am string) *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	live.ServerVersion = 170000
	for fq, partitioned := range map[string]bool{"app.t": false, "app.p": true, "app.p1": false} {
		lt := newLiveTable()
		lt.Columns["id"] = &LiveColumn{Type: "integer"}
		lt.Partitioned, lt.AccessMethod = partitioned, am
		live.Tables[fq] = lt
	}
	live.Tables["app.t"].Options = map[string]string{"fillfactor": "70"}
	live.Tables["app.t"].Tablespace = "fast"
	live.Tables["app.p1"].IsPartition = true
	live.Tables["app.p1"].AccessMethod = "heap"
	live.Partitions = map[string][]string{"app.p": {"app.p1"}}
	live.PartitionBounds = map[string]LivePartitionBound{"app.p1": {Parent: "app.p", Bound: "FOR VALUES FROM (0) TO (10)"}}
	live.MatViews["app.mv"] = true
	live.MatViewTables = map[string]*LiveTable{"app.mv": {AccessMethod: am}}
	return live
}

func TestAccessMethodSkippedIfSame(t *testing.T) {
	if p := Plan(accessMethodLive("columnar"), accessMethodDesired(), true); Render(p) != "" || len(p.Warnings) != 0 {
		t.Errorf("matching access methods must be left alone, got:\n%s%v", Render(p), p.Warnings)
	}
	d := accessMethodDesired()
	for _, tb := range d.Tables {
		tb.AccessMethod = ""
	}
	d.Views["app.mv"].AccessMethod = ""
	if sql := Render(Plan(accessMethodLive("heap2"), d, true)); sql != "" {
		t.Errorf("an undeclared access method is unmanaged, got:\n%s", sql)
	}
}

func TestAccessMethodChange(t *testing.T) {
	live := accessMethodLive("heap")
	safe := PlanReportingUnsafe(live, accessMethodDesired(), Options{})
	if sql := Render(safe); sql != `alter table "app"."p" set access method "columnar";`+"\n" {
		t.Errorf("without --unsafe only the partitioned table, which nothing rewrites, changes; got:\n%s", sql)
	}
	left := []string{}
	for _, w := range safe.Warnings {
		if w.Kind == "unsafe" {
			left = append(left, w.Statement)
		}
	}
	wantStatements(t, left, []string{
		`alter table "app"."t" set access method "columnar";`,
		`alter materialized view "app"."mv" set access method "columnar";`,
	})
	p := Plan(live, accessMethodDesired(), true)
	sql := Render(p)
	for _, want := range []string{
		`alter materialized view "app"."mv" set access method "columnar";`,
		`alter table "app"."t" set access method "columnar";`,
		`alter table "app"."p" set access method "columnar";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	locks := []string{}
	for _, w := range p.Warnings {
		if w.Kind == "lock" {
			locks = append(locks, w.Table)
		}
	}
	if strings.Join(locks, ",") != "app.t,app.mv" {
		t.Errorf("want lock warnings for the rewritten table and materialized view only: %+v", p.Warnings)
	}
	live.ServerVersion = 150000
	live.Tables["app.p"].AccessMethod = ""
	p = Plan(live, accessMethodDesired(), true)
	if strings.Contains(Render(p), `"app"."p" set access method`) {
		t.Errorf("PostgreSQL 15 can't set a partitioned table's access method:\n%s", Render(p))
	}
}
