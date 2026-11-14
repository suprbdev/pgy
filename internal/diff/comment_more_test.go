package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func commentTable() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"public.t": {
		Name:             "t",
		Columns:          map[string]*schema.Column{"a": {Type: "int"}},
		Constraints:      []*schema.Constraint{{Name: "t_a_check", Type: "check", Expression: "a > 0", Comment: "@name positive"}},
		Triggers:         []*schema.Trigger{{Name: "t_trg", Timing: "before", Events: []string{"insert"}, Level: "row", Procedure: "f()", Comment: "trg"}},
		RowLevelSecurity: schema.BoolPtr(true),
		Policies:         []*schema.Policy{{Name: "t_pol", Using: "true", Comment: "pol"}},
	}}}
}

func TestCommentsOnConstraintsTriggersPolicies(t *testing.T) {
	p := Plan(emptyLive(), commentTable(), false)
	for _, want := range []string{
		`comment on constraint "t_a_check" on "public"."t" is '@name positive';`,
		`comment on trigger "t_trg" on "public"."t" is 'trg';`,
		`comment on policy "t_pol" on "public"."t" is 'pol';`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %+v", want, p.Alters)
		}
	}
}

func TestCommentsReemittedWhenObjectRecreated(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	lt := live.Tables["public.t"]
	lt.RLSEnabled = true
	lt.Constraints["t_a_check"] = true
	lt.ConstraintDefs = map[string]string{"t_a_check": "CHECK ((a > 0))"}
	lt.ConstraintComments = map[string]string{"t_a_check": "@name positive"}
	lt.Triggers["t_trg"] = "CREATE TRIGGER t_trg BEFORE INSERT ON public.t FOR EACH ROW EXECUTE FUNCTION f()"
	lt.TriggerComments = map[string]string{"t_trg": "trg"}
	lt.Policies = map[string]*LivePolicy{"t_pol": {Cmd: "all", Using: "true"}}
	lt.PolicyComments = map[string]string{"t_pol": "pol"}
	if sql := Render(Plan(live, commentTable(), true)); sql != "" {
		t.Errorf("same objects and comments must not diff, got:\n%s", sql)
	}
	d := commentTable()
	d.Tables["public.t"].Constraints[0].Expression = "a > 1"
	d.Tables["public.t"].Triggers[0].Events = []string{"update"}
	d.Tables["public.t"].Policies[0].Using = "a > 0"
	p := Plan(live, d, true)
	for _, want := range []string{
		`comment on constraint "t_a_check" on "public"."t" is '@name positive';`,
		`comment on trigger "t_trg" on "public"."t" is 'trg';`,
		`comment on policy "t_pol" on "public"."t" is 'pol';`,
	} {
		if !findAlter(p, want) {
			t.Errorf("recreated object must get its comment back: missing %q in %+v", want, p.Alters)
		}
	}
}
