package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestTriggerTransitionTablesAndDefaultLevel(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"a": {Type: "int"}}, Triggers: []*schema.Trigger{
			{Name: "audit", Timing: "after", Events: []string{"update"}, Level: "statement", OldTable: "old_rows", NewTable: "new_rows", Procedure: "public.audit()"},
			{Name: "notify", Timing: "after", Events: []string{"insert"}, Procedure: "public.notify()"},
		}},
	}}
	p := Plan(emptyLive(), d, false)
	for _, want := range []string{
		`create trigger "audit" AFTER UPDATE on "public"."t" referencing old table as "old_rows" new table as "new_rows" for each statement execute procedure public.audit();`,
		`create trigger "notify" AFTER INSERT on "public"."t" for each statement execute procedure public.notify();`,
	} {
		if !findCreate(p, want) {
			t.Errorf("missing %q in %+v", want, p.Creates)
		}
	}
	live := liveWithTable("public.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	live.Tables["public.t"].Triggers["audit"] = "CREATE TRIGGER audit AFTER UPDATE ON public.t REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION audit()"
	live.Tables["public.t"].Triggers["notify"] = "CREATE TRIGGER notify AFTER INSERT ON public.t FOR EACH STATEMENT EXECUTE FUNCTION notify()"
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("equivalent triggers must not diff, got:\n%s", sql)
	}
}
