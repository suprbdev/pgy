package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestConcurrentIndexOnExistingTableOnly(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{"public.t": {
		Name: "t", Columns: map[string]*schema.Column{"a": {Type: "int"}},
		Indexes: []*schema.Index{{Name: "t_a", Columns: []string{"a"}, Concurrently: true}},
	}}}
	p := Plan(emptyLive(), d, false)
	if len(p.Concurrent) != 0 || !findCreate(p, `create index if not exists "t_a"`) {
		t.Errorf("an index on a table created in the same plan needs no CONCURRENTLY, got creates=%v concurrent=%v", p.Creates, p.Concurrent)
	}
	live := liveWithTable("public.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	p = Plan(live, d, false)
	if len(p.Creates) != 0 || len(p.Concurrent) != 1 || p.Concurrent[0] != `create index concurrently if not exists "t_a" on "public"."t"("a");` {
		t.Errorf("expected concurrent create, got creates=%v concurrent=%v", p.Creates, p.Concurrent)
	}
	if got := RenderConcurrent(p); !strings.HasPrefix(got, "-- pgy:no-transaction\n") {
		t.Errorf("concurrent body must start with the directive: %q", got)
	}
	live.Tables["public.t"].Indexes["t_a"] = true
	live.Tables["public.t"].IndexDefs = map[string]*LiveIndex{"t_a": {Method: "btree", Keys: []string{"a"}, Descending: []bool{true}, NullsFirst: []bool{true}}}
	p = Plan(live, d, true)
	if len(p.Alters) != 0 || len(p.Concurrent) != 2 || p.Concurrent[0] != `drop index concurrently if exists "public"."t_a";` {
		t.Errorf("changed concurrent index should be rebuilt concurrently, got alters=%v concurrent=%v", p.Alters, p.Concurrent)
	}
}
