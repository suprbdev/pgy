package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestViewInsteadOfTrigger(t *testing.T) {
	vw := &schema.View{Name: "v", Query: "select 1 as id", Triggers: []*schema.Trigger{
		{Name: "v_ins", Timing: "instead of", Events: []string{"insert"}, Procedure: "public.v_insert()", Comment: "writable"},
	}}
	d := &schema.Database{Tables: map[string]*schema.Table{}, Views: map[string]*schema.View{"public.v": vw}}
	p := Plan(emptyLive(), d, false)
	want := `create trigger "v_ins" INSTEAD OF INSERT on "public"."v" for each row execute procedure public.v_insert();`
	if !findCreate(p, want) || !findAlter(p, `comment on trigger "v_ins" on "public"."v" is 'writable';`) {
		t.Errorf("want %q + comment, got creates=%v alters=%v", want, p.Creates, p.Alters)
	}
	live := emptyLive()
	live.Views["public.v"] = true
	vt := newLiveTable()
	vt.Triggers["v_ins"] = "CREATE TRIGGER v_ins INSTEAD OF INSERT ON public.v FOR EACH ROW EXECUTE FUNCTION v_insert()"
	vt.TriggerComments["v_ins"] = "writable"
	vt.Triggers["v_old"] = "CREATE TRIGGER v_old INSTEAD OF DELETE ON public.v FOR EACH ROW EXECUTE FUNCTION v_insert()"
	live.ViewTables = map[string]*LiveTable{"public.v": vt}
	if sql := Render(Plan(live, d, true)); sql != "" {
		t.Errorf("existing view trigger must not diff, got:\n%s", sql)
	}
	p = PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if len(p.Drops) != 1 || p.Drops[0] != `drop trigger "v_old" on "public"."v";` {
		t.Errorf("prune should drop the undeclared view trigger, got %v", p.Drops)
	}
}
