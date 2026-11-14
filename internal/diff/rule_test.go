package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func ruleTable(rules []*schema.Rule) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"public.t": {
		Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}, Rules: rules,
	}}}
}

func TestRuleCreateSkipAndReplace(t *testing.T) {
	rules := []*schema.Rule{
		{Name: "t_soft_delete", Event: "delete", Instead: true, Where: "old.id > 0", Do: "update public.t set id = -old.id where id = old.id", Comment: "soft delete"},
		{Name: "t_no_update", Event: "update", Instead: true, Do: "nothing"},
	}
	p := Plan(emptyLive(), ruleTable(rules), false)
	for _, want := range []string{
		`create or replace rule "t_soft_delete" as on delete to "public"."t" where old.id > 0 do instead update public.t set id = -old.id where id = old.id;`,
		`create or replace rule "t_no_update" as on update to "public"."t" do instead nothing;`,
	} {
		if !findCreate(p, want) {
			t.Errorf("missing %q in %v", want, p.Creates)
		}
	}
	if !findAlter(p, `comment on rule "t_soft_delete" on "public"."t" is 'soft delete';`) {
		t.Errorf("expected rule comment, got %v", p.Alters)
	}
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "integer"}})
	lt := live.Tables["public.t"]
	lt.Rules = map[string]*LiveRule{
		"t_soft_delete": {Event: "delete", Instead: true, Where: "(old.id > 0)"},
		"t_no_update":   {Event: "update", Instead: true},
		"t_stale":       {Event: "insert"},
	}
	lt.RuleComments = map[string]string{"t_soft_delete": "soft delete"}
	if sql := Render(Plan(live, ruleTable(rules), false)); sql != "" {
		t.Errorf("matching rules must not diff, got:\n%s", sql)
	}
	p = PlanWith(live, ruleTable(rules), Options{Unsafe: true, Prune: true})
	if len(p.Drops) != 1 || p.Drops[0] != `drop rule "t_stale" on "public"."t";` {
		t.Errorf("prune should drop the undeclared rule, got %v", p.Drops)
	}
	rules[1].Instead = false
	rules[0].Replace = true
	if p := Plan(live, ruleTable(rules), false); len(p.Creates) != 2 || !findAlter(p, `comment on rule "t_soft_delete"`) {
		t.Errorf("changed header and replace: true re-emit both rules (and the comment), got creates=%v alters=%v", p.Creates, p.Alters)
	}
}
