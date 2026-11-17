package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestPartitionIndexName(t *testing.T) {
	part := "s.partition_with_a_rather_long_name_that_is_also_quite_wordy_x"
	long := "a_very_long_column_name_that_goes_on_and_on"
	cols := map[string]*LiveColumn{long: {Type: "integer"}, "b": {Type: "integer"}, "Odd Col": {Type: "integer"}, "k": {Type: "integer"}}
	cases := []struct {
		partition string
		li        *LiveIndex
		opclasses []LiveIndexOpclass
		want      string
	}{
		{"s.p1", &LiveIndex{Keys: []string{"k"}}, nil, "p1_k_idx"},
		{"s.p1", &LiveIndex{Keys: []string{"k"}, Include: []string{`"Odd Col"`}}, nil, "p1_k_Odd Col_idx"},
		{"s.p1", &LiveIndex{Keys: []string{"lower(name)"}}, []LiveIndexOpclass{{Column: "lower"}}, "p1_lower_idx"},
		{"s.p1", &LiveIndex{Keys: []string{"(a + 1)"}}, nil, "p1_expr_idx"},
		{part, &LiveIndex{Keys: []string{long, "b"}, Include: []string{`"Odd Col"`}}, nil, "partition_with_a_rather_long__a_very_long_column_name_that__idx"},
		{part, &LiveIndex{Keys: []string{"b", "b"}}, nil, "partition_with_a_rather_long_name_that_is_also_quite_w_b_b1_idx"},
		{part, &LiveIndex{Keys: []string{`"Odd Col"`}}, nil, "partition_with_a_rather_long_name_that_is_also_quit_Odd Col_idx"},
		{part, &LiveIndex{Keys: []string{"(" + long + " + 1)", "(b + 1)"}}, []LiveIndexOpclass{{Column: "expr"}, {Column: "expr1"}}, "partition_with_a_rather_long_name_that_is_also_q_expr_expr1_idx"},
	}
	for _, c := range cases {
		if got := PartitionIndexName(c.partition, c.li, c.opclasses, cols); got != c.want {
			t.Errorf("PartitionIndexName(%s, %v) = %q, want %q", c.partition, c.li.Keys, got, c.want)
		}
	}
}

func partitionTree(parentIndexes []*schema.Index, partIndexes map[string][]*schema.Index) *schema.Database {
	cols := map[string]*schema.Column{"id": {Type: "int"}, "k": {Type: "int"}}
	d := &schema.Database{Tables: map[string]*schema.Table{
		"s.p": {Name: "p", Columns: cols, PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"k"}}, Indexes: parentIndexes},
	}}
	for i, part := range []string{"p1", "p2"} {
		d.Tables["s."+part] = &schema.Table{Name: part, PartitionOf: "s.p", Partition: &schema.PartitionSpec{From: []string{string(rune('0' + i))}, To: []string{string(rune('1' + i))}}, Indexes: partIndexes[part]}
	}
	return d
}

func stmtIndex(stmts []string, substr string) int {
	for i, s := range stmts {
		if strings.Contains(s, substr) {
			return i
		}
	}
	return -1
}

func TestPartitionIndexHeldUntilPartitionsDeclareTheirs(t *testing.T) {
	d := partitionTree([]*schema.Index{{Name: "p_k", Columns: []string{"k"}}}, map[string][]*schema.Index{"p1": {{Name: "p1_k_named", Columns: []string{"k"}}}})
	p := Plan(emptyLive(), d, false)
	parent, child, lastTable := stmtIndex(p.Creates, `create index if not exists "p_k"`), stmtIndex(p.Creates, `create index if not exists "p1_k_named"`), stmtIndex(p.Creates, `"s"."p2" partition of`)
	if parent < 0 || child < 0 || lastTable < 0 || parent < child || parent < lastTable {
		t.Errorf("the parent's index must follow the partitions and their own indexes, got:\n%s", strings.Join(p.Creates, "\n"))
	}

	d = partitionTree([]*schema.Index{{Name: "p_k", Columns: []string{"k"}}}, nil)
	p = Plan(emptyLive(), d, false)
	if parent, first := stmtIndex(p.Creates, `create index if not exists "p_k"`), stmtIndex(p.Creates, `"s"."p1" partition of`); parent < 0 || parent > first {
		t.Errorf("without partition indexes the parent's index stays with its table, got:\n%s", strings.Join(p.Creates, "\n"))
	}
}

func TestPartitionIndexHeldDeepestFirst(t *testing.T) {
	d := partitionTree([]*schema.Index{{Name: "p_k", Columns: []string{"k"}}}, map[string][]*schema.Index{"p1": {{Name: "p1_k_named", Columns: []string{"k"}}}})
	d.Tables["s.p1"].PartitionBy = &schema.PartitionBy{Type: "range", Columns: []string{"id"}}
	d.Tables["s.p1_a"] = &schema.Table{Name: "p1_a", PartitionOf: "s.p1", Partition: &schema.PartitionSpec{From: []string{"0"}, To: []string{"5"}},
		Indexes: []*schema.Index{{Name: "p1_a_k_named", Columns: []string{"k"}}}}
	p := Plan(emptyLive(), d, false)
	leaf, mid, top := stmtIndex(p.Creates, `"p1_a_k_named"`), stmtIndex(p.Creates, `"p1_k_named"`), stmtIndex(p.Creates, `"p_k"`)
	if leaf < 0 || mid < 0 || top < 0 || leaf > mid || mid > top {
		t.Errorf("held indexes must be created from the deepest partition up, got:\n%s", strings.Join(p.Creates, "\n"))
	}
}

func liveParentWithIndex() *Live {
	live := emptyLive()
	live.Schemas["s"] = true
	cols := map[string]*LiveColumn{"id": {Type: "integer"}, "k": {Type: "integer"}}
	live.Tables["s.p"] = newLiveTable()
	live.Tables["s.p"].Columns = cols
	live.Tables["s.p"].Partitioned = true
	live.Tables["s.p"].Indexes["p_k"] = true
	live.Tables["s.p"].IndexDefs["p_k"] = &LiveIndex{Method: "btree", Keys: []string{"k"}, Descending: []bool{false}, NullsFirst: []bool{false}}
	live.Tables["s.p"].IndexOpclasses["p_k"] = []LiveIndexOpclass{{Name: "int4_ops", Default: true, Column: "k"}}
	live.Partitions = map[string][]string{"s.p": {"s.p1"}}
	live.Tables["s.p1"] = newLiveTable()
	live.Tables["s.p1"].Columns = cols
	live.Tables["s.p1"].IsPartition = true
	live.Tables["s.p1"].Indexes["p1_k_idx"] = true
	live.Tables["s.p1"].IndexDefs["p1_k_idx"] = &LiveIndex{Method: "btree", Keys: []string{"k"}, Descending: []bool{false}, NullsFirst: []bool{false}, Inherited: true, Parent: "s.p_k"}
	live.Tables["s.p1"].IndexOpclasses["p1_k_idx"] = []LiveIndexOpclass{{Name: "int4_ops", Default: true, Column: "k"}}
	return live
}

func TestPartitionIndexAdoptsAttachedCopy(t *testing.T) {
	d := partitionTree([]*schema.Index{{Name: "p_k", Columns: []string{"k"}}}, map[string][]*schema.Index{"p1": {
		{Name: "p1_id", Columns: []string{"id"}},
		{Name: "p1_k_named", Columns: []string{"k"}, Comment: "own name"},
	}})
	delete(d.Tables, "s.p2")
	p := Plan(liveParentWithIndex(), d, false)
	if !findAlter(p, `alter index "s"."p1_k_idx" rename to "p1_k_named";`) || findCreate(p, `"p1_k_named"`) {
		t.Errorf("the partition's copy of p_k must be renamed, not duplicated, got creates=%v alters=%v", p.Creates, p.Alters)
	}
	if !findAlter(p, `comment on index "s"."p1_k_named" is 'own name';`) {
		t.Errorf("the renamed index gets its comment, got %v", p.Alters)
	}
	if !findCreate(p, `create index if not exists "p1_id"`) {
		t.Errorf("an index unlike any copy is created, got %v", p.Creates)
	}
}

func TestPartitionIndexRenamesCopyOfNewPartition(t *testing.T) {
	d := partitionTree([]*schema.Index{{Name: "p_k", Columns: []string{"k"}}}, map[string][]*schema.Index{"p2": {
		{Name: "p2_k_named", Columns: []string{"k"}},
		{Name: "p2_k_hash", Columns: []string{"k"}, Using: "hash"},
	}})
	delete(d.Tables, "s.p1")
	p := Plan(liveParentWithIndex(), d, false)
	table, rename := stmtIndex(p.Creates, `"s"."p2" partition of`), stmtIndex(p.Creates, `alter index "s"."p2_k_idx" rename to "p2_k_named";`)
	if table < 0 || rename < table || findCreate(p, `create index if not exists "p2_k_named"`) {
		t.Errorf("the copy of p_k that p2 gets must be renamed after p2 is created, got:\n%s", strings.Join(p.Creates, "\n"))
	}
	if !findCreate(p, `create index if not exists "p2_k_hash"`) {
		t.Errorf("an index unlike p_k is created, got:\n%s", strings.Join(p.Creates, "\n"))
	}

	live := liveParentWithIndex()
	live.Tables["s.p"].Indexes["p_k_hash"] = true
	live.Tables["s.p"].IndexDefs["p_k_hash"] = &LiveIndex{Method: "hash", Keys: []string{"k"}, Descending: []bool{false}, NullsFirst: []bool{false}}
	live.Tables["s.p"].IndexOpclasses["p_k_hash"] = []LiveIndexOpclass{{Name: "int4_ops", Default: true, Column: "k"}}
	p = Plan(live, d, false)
	if findCreate(p, `"p2_k_named"`) || findCreate(p, "rename to") {
		t.Errorf("with two copies named alike, which one gets which name is unknown, got:\n%s", strings.Join(p.Creates, "\n"))
	}
	warned := false
	for _, w := range p.Warnings {
		warned = warned || strings.Contains(w.Message, "index p2_k_named:")
	}
	if !warned {
		t.Errorf("expected a warning about p2_k_named, got %+v", p.Warnings)
	}
}

func TestPartitionedConcurrentIndexUsesDeclaredNames(t *testing.T) {
	live := liveParentWithIndex()
	delete(live.Tables["s.p"].Indexes, "p_k")
	delete(live.Tables["s.p"].IndexDefs, "p_k")
	delete(live.Tables["s.p1"].Indexes, "p1_k_idx")
	delete(live.Tables["s.p1"].IndexDefs, "p1_k_idx")
	d := partitionTree([]*schema.Index{{Name: "p_k", Columns: []string{"k"}, Concurrently: true}}, map[string][]*schema.Index{"p2": {{Name: "p2_k_named", Columns: []string{"k"}}}})
	p := Plan(live, d, false)
	want := []string{
		`create index if not exists "p_k" on only "s"."p"("k");`,
		`create index concurrently if not exists "p1_p_k" on "s"."p1"("k");`,
		`alter index "s"."p_k" attach partition "s"."p1_p_k";`,
		`create index concurrently if not exists "p2_k_named" on "s"."p2"("k");`,
		`alter index "s"."p_k" attach partition "s"."p2_k_named";`,
	}
	if strings.Join(p.Concurrent, "\n") != strings.Join(want, "\n") {
		t.Errorf("concurrent build:\n%s\nwant:\n%s", strings.Join(p.Concurrent, "\n"), strings.Join(want, "\n"))
	}
}

func TestPartitionedConcurrentIndexSkipsAttachedPartitions(t *testing.T) {
	live := liveParentWithIndex()
	live.Tables["s.p"].IndexDefs["p_k"].Invalid = true
	live.Partitions["s.p"] = []string{"s.p1", "s.p2"}
	live.Tables["s.p2"] = newLiveTable()
	live.Tables["s.p2"].IsPartition = true
	d := partitionTree([]*schema.Index{{Name: "p_k", Columns: []string{"k"}, Concurrently: true}}, nil)
	p := Plan(live, d, false)
	want := []string{
		`create index if not exists "p_k" on only "s"."p"("k");`,
		`create index concurrently if not exists "p2_p_k" on "s"."p2"("k");`,
		`alter index "s"."p_k" attach partition "s"."p2_p_k";`,
	}
	if strings.Join(p.Concurrent, "\n") != strings.Join(want, "\n") {
		t.Errorf("finishing the build must leave p1's attached index alone:\n%s\nwant:\n%s", strings.Join(p.Concurrent, "\n"), strings.Join(want, "\n"))
	}
}

func liveParentWithNamedPartitionIndex() *Live {
	live := liveParentWithIndex()
	p1 := live.Tables["s.p1"]
	p1.Indexes = map[string]bool{"p1_k_named": true}
	p1.IndexDefs = map[string]*LiveIndex{"p1_k_named": p1.IndexDefs["p1_k_idx"]}
	p1.IndexOpclasses = map[string][]LiveIndexOpclass{"p1_k_named": p1.IndexOpclasses["p1_k_idx"]}
	return live
}

func TestPartitionIndexRebuiltWithParent(t *testing.T) {
	d := partitionTree([]*schema.Index{{Name: "p_k", Columns: []string{"k desc"}}}, map[string][]*schema.Index{"p1": {{Name: "p1_k_named", Columns: []string{"k desc"}}}})
	delete(d.Tables, "s.p2")
	p := Plan(liveParentWithNamedPartitionIndex(), d, true)
	want := []string{
		`drop index "s"."p_k";`,
		`create index if not exists "p1_k_named" on "s"."p1"("k" desc);`,
		`create index if not exists "p_k" on "s"."p"("k" desc);`,
	}
	if strings.Join(p.Alters, "\n") != strings.Join(want, "\n") {
		t.Errorf("dropping p_k drops p1_k_named, which must be created again before p_k:\n%s\nwant:\n%s", strings.Join(p.Alters, "\n"), strings.Join(want, "\n"))
	}

	d.Tables["s.p1"].Indexes[0].Columns = []string{"k"}
	p = Plan(liveParentWithNamedPartitionIndex(), d, true)
	if !findAlter(p, `create index if not exists "p1_k_named" on "s"."p1"("k");`) {
		t.Errorf("an unchanged partition index dropped with its parent is created again, got %v", p.Alters)
	}

	d.Tables["s.p"].Indexes[0].Columns = []string{"k"}
	d.Tables["s.p1"].Indexes[0].Columns = []string{"k desc"}
	p = Plan(liveParentWithNamedPartitionIndex(), d, true)
	if len(p.Creates)+len(p.Alters)+len(p.Drops) != 0 || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "is attached to index s.p_k") {
		t.Errorf("an attached partition index is not rebuilt on its own, got creates=%v alters=%v drops=%v warnings=%+v", p.Creates, p.Alters, p.Drops, p.Warnings)
	}
}

func TestPartitionIndexRebuiltConcurrentlyWithParent(t *testing.T) {
	d := partitionTree([]*schema.Index{{Name: "p_k", Columns: []string{"k desc"}, Concurrently: true}}, map[string][]*schema.Index{"p1": {{Name: "p1_k_named", Columns: []string{"k desc"}, Comment: "named"}}})
	delete(d.Tables, "s.p2")
	p := Plan(liveParentWithNamedPartitionIndex(), d, true)
	want := []string{
		`drop index if exists "s"."p_k";`,
		`create index if not exists "p_k" on only "s"."p"("k" desc);`,
		`create index concurrently if not exists "p1_k_named" on "s"."p1"("k" desc);`,
		`alter index "s"."p_k" attach partition "s"."p1_k_named";`,
		`comment on index "s"."p1_k_named" is 'named';`,
	}
	if strings.Join(p.Concurrent, "\n") != strings.Join(want, "\n") || len(p.Alters) != 0 {
		t.Errorf("rebuilding p_k concurrently builds p1_k_named again under its name:\n%s\nwant:\n%s\nalters: %v", strings.Join(p.Concurrent, "\n"), strings.Join(want, "\n"), p.Alters)
	}
}
