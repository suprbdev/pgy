package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func viewDB(vw *schema.View) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, Views: map[string]*schema.View{"public.v": vw}}
}

func TestViewOptionsCreate(t *testing.T) {
	vw := &schema.View{Name: "v", Query: "select 1", SecurityInvoker: schema.BoolPtr(true), SecurityBarrier: schema.BoolPtr(false), CheckOption: "cascaded"}
	p := Plan(emptyLive(), viewDB(vw), false)
	want := `create or replace view "public"."v" with (check_option = cascaded, security_invoker = true) as select 1;`
	if !findCreate(p, want) {
		t.Errorf("want %q, got %+v", want, p.Creates)
	}
}

func TestViewOptionsAlteredOnLiveView(t *testing.T) {
	live := emptyLive()
	live.Views["public.v"] = true
	live.ViewOptions = map[string]map[string]string{"public.v": {"security_barrier": "true", "check_option": "local"}}
	vw := &schema.View{Name: "v", Query: "select 1", SecurityInvoker: schema.BoolPtr(true), SecurityBarrier: schema.BoolPtr(false), CheckOption: "none"}
	p := Plan(live, viewDB(vw), false)
	want := []string{
		`alter view "public"."v" set (security_invoker = true);`,
		`alter view "public"."v" reset (check_option, security_barrier);`,
	}
	if len(p.Creates) != 0 || len(p.Alters) != 2 || p.Alters[0] != want[0] || p.Alters[1] != want[1] {
		t.Errorf("want %v, got creates=%v alters=%v", want, p.Creates, p.Alters)
	}
}

func TestViewOptionsSameOrUnmanagedSkipped(t *testing.T) {
	live := emptyLive()
	live.Views["public.v"] = true
	live.ViewOptions = map[string]map[string]string{"public.v": {"security_invoker": "true", "security_barrier": "false"}}
	vw := &schema.View{Name: "v", Query: "select 1", SecurityInvoker: schema.BoolPtr(true), SecurityBarrier: schema.BoolPtr(false)}
	if sql := Render(Plan(live, viewDB(vw), false)); sql != "" {
		t.Errorf("same options must not diff, got:\n%s", sql)
	}
	if sql := Render(Plan(live, viewDB(&schema.View{Name: "v", Query: "select 1"}), false)); sql != "" {
		t.Errorf("unmanaged options must not diff, got:\n%s", sql)
	}
}

func TestViewReplaceKeepsUnmanagedLiveOptions(t *testing.T) {
	live := emptyLive()
	live.Views["public.v"] = true
	live.ViewOptions = map[string]map[string]string{"public.v": {"security_invoker": "true", "check_option": "local"}}
	vw := &schema.View{Name: "v", Query: "select 2", Replace: true, CheckOption: "none"}
	p := Plan(live, viewDB(vw), false)
	want := `create or replace view "public"."v" with (security_invoker = true) as select 2;`
	if len(p.Creates) != 1 || p.Creates[0] != want {
		t.Errorf("want %q, got %+v", want, p.Creates)
	}
}

func TestMaterializedViewWithNoDataAndIndexes(t *testing.T) {
	vw := &schema.View{Name: "mv", Materialized: true, Query: "select 1 as id", WithData: schema.BoolPtr(false),
		Indexes: []*schema.Index{{Name: "mv_id", Columns: []string{"id"}, Unique: true, Comment: "for refresh concurrently"}}}
	d := &schema.Database{Tables: map[string]*schema.Table{}, Views: map[string]*schema.View{"public.mv": vw}}
	p := Plan(emptyLive(), d, false)
	want := []string{
		`create materialized view if not exists "public"."mv" as select 1 as id with no data;`,
		`create unique index if not exists "mv_id" on "public"."mv"("id");`,
	}
	if len(p.Creates) != 2 || p.Creates[0] != want[0] || p.Creates[1] != want[1] {
		t.Errorf("want %v, got %+v", want, p.Creates)
	}
	if !findAlter(p, `comment on index "public"."mv_id" is 'for refresh concurrently';`) {
		t.Errorf("expected index comment, got %+v", p.Alters)
	}

	live := emptyLive()
	live.MatViews["public.mv"] = true
	lt := newLiveTable()
	lt.Indexes["mv_id"] = true
	lt.IndexComments["mv_id"] = "for refresh concurrently"
	lt.IndexDefs["mv_id"] = &LiveIndex{Unique: true, Method: "btree", Keys: []string{"id"}, Descending: []bool{false}, NullsFirst: []bool{false}}
	live.MatViewTables = map[string]*LiveTable{"public.mv": lt}
	if sql := Render(Plan(live, d, true)); sql != "" {
		t.Errorf("existing matview with same index must not diff, got:\n%s", sql)
	}
	vw.Indexes = append(vw.Indexes, &schema.Index{Name: "mv_id2", Columns: []string{"id"}})
	if p := Plan(live, d, false); len(p.Creates) != 1 || !findCreate(p, `"mv_id2" on "public"."mv"`) {
		t.Errorf("missing matview index should be created, got %+v", p.Creates)
	}
}
