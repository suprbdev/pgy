package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestIndexIncludeNullsNotDistinctWithRendered(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"a": {Type: "int"}, "b": {Type: "text"}},
			Indexes: []*schema.Index{{
				Name: "t_a", Columns: []string{"a"}, Unique: true, Include: []string{"b"}, NullsNotDistinct: true,
				With: map[string]string{"fillfactor": "70", "deduplicate_items": "off"}, Where: "a > 0",
			}},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	want := `create unique index if not exists "t_a" on "public"."t"("a") include ("b") nulls not distinct with (deduplicate_items = off, fillfactor = 70) where (a > 0);`
	if !findCreate(p, want) {
		t.Errorf("want %q, got %+v", want, p.Creates)
	}
}

func liveIndexTable(name string, li *LiveIndex) *Live {
	live := liveWithTable("public.t", map[string]*LiveColumn{"a": {Type: "integer"}, "b": {Type: "text"}})
	live.Tables["public.t"].Indexes[name] = true
	live.Tables["public.t"].IndexDefs = map[string]*LiveIndex{name: li}
	return live
}

func indexTable(ix *schema.Index) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"a": {Type: "int"}, "b": {Type: "text"}}, Indexes: []*schema.Index{ix}},
	}}
}

func TestIndexDefinitionEquivalentSkipped(t *testing.T) {
	live := liveIndexTable("t_a", &LiveIndex{
		Unique: true, Method: "btree",
		Keys: []string{"lower(b)", "a"}, Descending: []bool{true, false}, NullsFirst: []bool{false, false},
		Include: []string{"b"}, Predicate: "a > 0", NullsNotDistinct: true,
		Options: map[string]string{"fillfactor": "70"},
	})
	ix := &schema.Index{Name: "t_a", Columns: []string{"lower(b) DESC NULLS LAST", "a"}, Unique: true, Using: "BTREE",
		Include: []string{"b"}, Where: "(a > 0)", NullsNotDistinct: true, With: map[string]string{"FillFactor": "'70'"}}
	if sql := Render(Plan(live, indexTable(ix), true)); sql != "" {
		t.Errorf("equivalent index must not diff, got:\n%s", sql)
	}
}

func TestIndexDescDefaultsToNullsFirst(t *testing.T) {
	live := liveIndexTable("t_a", &LiveIndex{Method: "btree", Keys: []string{"a"}, Descending: []bool{true}, NullsFirst: []bool{true}})
	if sql := Render(Plan(live, indexTable(&schema.Index{Name: "t_a", Columns: []string{"a desc"}}), true)); sql != "" {
		t.Errorf("DESC implies NULLS FIRST, got:\n%s", sql)
	}
	if p := Plan(live, indexTable(&schema.Index{Name: "t_a", Columns: []string{"a desc nulls last"}}), true); len(p.Alters) != 2 {
		t.Errorf("explicit NULLS LAST differs from DESC default, got %+v", p.Alters)
	}
}

func TestIndexDefinitionChangesDetected(t *testing.T) {
	base := func() *LiveIndex {
		return &LiveIndex{Method: "btree", Keys: []string{"a"}, Descending: []bool{false}, NullsFirst: []bool{false}}
	}
	cases := map[string]*schema.Index{
		"unique":    {Name: "t_a", Columns: []string{"a"}, Unique: true},
		"method":    {Name: "t_a", Columns: []string{"a"}, Using: "hash"},
		"columns":   {Name: "t_a", Columns: []string{"a", "b"}},
		"column":    {Name: "t_a", Columns: []string{"b"}},
		"ordering":  {Name: "t_a", Columns: []string{"a desc"}},
		"include":   {Name: "t_a", Columns: []string{"a"}, Include: []string{"b"}},
		"predicate": {Name: "t_a", Columns: []string{"a"}, Where: "a > 0"},
		"nulls":     {Name: "t_a", Columns: []string{"a"}, NullsNotDistinct: true},
		"with":      {Name: "t_a", Columns: []string{"a"}, With: map[string]string{"fillfactor": "50"}},
	}
	for name, ix := range cases {
		live := liveIndexTable("t_a", base())
		if p := Plan(live, indexTable(ix), false); len(p.Alters) != 0 {
			t.Errorf("%s: index changes are gated behind --unsafe, got %+v", name, p.Alters)
		}
		p := Plan(live, indexTable(ix), true)
		if len(p.Alters) != 2 || p.Alters[0] != `drop index "public"."t_a";` {
			t.Errorf("%s: expected drop + recreate, got %+v", name, p.Alters)
		}
	}
}
