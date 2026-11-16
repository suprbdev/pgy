package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestPartitionBoundsEqual(t *testing.T) {
	for _, c := range []struct {
		desired, live string
		want          bool
	}{
		{"for values from ('2024-01-01') to ('2025-01-01')", "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')", true},
		{"for values from ('2024-01-01') to ('2025-01-01')", "FOR VALUES FROM ('2024-01-01 00:00:00+00') TO ('2025-01-01 00:00:00+00')", true},
		{"for values from ('2024-01-01') to ('2025-01-01')", "FOR VALUES FROM ('2024-01-01 00:00:00+01') TO ('2025-01-01 00:00:00+01')", true},
		{"for values from ('2024-01-01') to ('2026-01-01')", "FOR VALUES FROM ('2024-01-01') TO ('2025-01-01')", false},
		{"for values from (1) to (100)", "FOR VALUES FROM (1) TO (100)", true},
		{"for values from ('1') to ('100')", "FOR VALUES FROM (1) TO (100)", true},
		{"for values from (minvalue) to (10)", "FOR VALUES FROM (MINVALUE) TO (10)", true},
		{"for values in ('de', 'fr')", "FOR VALUES IN ('de', 'fr')", true},
		{"for values in ('de', 'fr')", "FOR VALUES IN ('de', 'fr', 'it')", false},
		{"for values in ('it''s')", "FOR VALUES IN ('it''s'::text)", true},
		{"for values with (modulus 2, remainder 0)", "FOR VALUES WITH (modulus 2, remainder 0)", true},
		{"default", "DEFAULT", true},
		{"default", "FOR VALUES IN ('de')", false},
	} {
		if got := partitionBoundsEqual(c.desired, c.live); got != c.want {
			t.Errorf("partitionBoundsEqual(%q, %q) = %v, want %v", c.desired, c.live, got, c.want)
		}
	}
}

func TestPartitionBoundChangeReattaches(t *testing.T) {
	live := liveWithTable("app.p1", map[string]*LiveColumn{"id": {Type: "integer"}})
	live.Tables["app.p1"].IsPartition = true
	live.Tables["app.p"] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer"}}, Partitioned: true}
	live.PartitionBounds = map[string]LivePartitionBound{"app.p1": {Parent: "app.p", Bound: "FOR VALUES FROM (1) TO (100)"}}
	d := &schema.Database{Tables: map[string]*schema.Table{
		"app.p":  {Name: "p", Columns: map[string]*schema.Column{"id": {Type: "int"}}, PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"id"}}},
		"app.p1": {Name: "p1", PartitionOf: "app.p", Partition: &schema.PartitionSpec{From: []string{"1"}, To: []string{"200"}, Modulus: -1, Remainder: -1}},
	}}
	if sql := Render(Plan(live, d, false)); strings.Contains(sql, "partition") {
		t.Errorf("without --unsafe the partition stays, got:\n%s", sql)
	}
	p := Plan(live, d, true)
	sql := Render(p)
	detach, attach := strings.Index(sql, `alter table "app"."p" detach partition "app"."p1";`), strings.Index(sql, `alter table "app"."p" attach partition "app"."p1" for values from (1) to (200);`)
	if detach < 0 || attach < detach {
		t.Errorf("want DETACH then ATTACH, got:\n%s", sql)
	}
	if len(p.Warnings) == 0 || p.Warnings[0].Kind != "lock" {
		t.Errorf("want a lock warning: %+v", p.Warnings)
	}
	d.Tables["app.p1"].Partition.To = []string{"100"}
	if sql := Render(Plan(live, d, true)); strings.Contains(sql, "partition") {
		t.Errorf("an unchanged bound must be left alone, got:\n%s", sql)
	}
}

func TestPartitionBoundSwapDetachesFirst(t *testing.T) {
	live := liveWithTable("app.a", map[string]*LiveColumn{"id": {Type: "integer"}})
	live.Tables["app.a"].IsPartition = true
	live.Tables["app.b"] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer"}}, IsPartition: true}
	live.Tables["app.p"] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer"}}, Partitioned: true}
	live.PartitionBounds = map[string]LivePartitionBound{
		"app.a": {Parent: "app.p", Bound: "FOR VALUES FROM (0) TO (100)"},
		"app.b": {Parent: "app.p", Bound: "FOR VALUES FROM (100) TO (200)"},
	}
	bound := func(from, to string) *schema.PartitionSpec {
		return &schema.PartitionSpec{From: []string{from}, To: []string{to}, Modulus: -1, Remainder: -1}
	}
	d := &schema.Database{Tables: map[string]*schema.Table{
		"app.p": {Name: "p", Columns: map[string]*schema.Column{"id": {Type: "int"}}, PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"id"}}},
		"app.a": {Name: "a", PartitionOf: "app.p", Partition: bound("100", "200")},
		"app.b": {Name: "b", PartitionOf: "app.p", Partition: bound("0", "100")},
		"app.c": {Name: "c", PartitionOf: "app.p", Partition: bound("200", "300")},
	}}
	sql := Render(Plan(live, d, true))
	lastDetach := strings.LastIndex(sql, "detach partition")
	firstAttach := strings.Index(sql, "attach partition")
	create := strings.Index(sql, `create table if not exists "app"."c"`)
	if lastDetach < 0 || firstAttach < lastDetach || create < lastDetach {
		t.Errorf("every DETACH must come before any ATTACH or new partition, so swapped bounds don't overlap:\n%s", sql)
	}
}
