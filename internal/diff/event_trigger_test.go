package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func etDB(et map[string]*schema.EventTrigger) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, EventTriggers: et}
}

func TestEventTriggerCreate(t *testing.T) {
	p := Plan(emptyLive(), etDB(map[string]*schema.EventTrigger{
		"log_ddl": {Name: "log_ddl", Event: "ddl_command_end", Tags: []string{"create table", "ALTER TABLE"}, Function: "audit.log_ddl()", Enabled: "always", Comment: "audit"},
	}), false)
	want := `create event trigger "log_ddl" on ddl_command_end when tag in ('CREATE TABLE', 'ALTER TABLE') execute function "audit"."log_ddl"();`
	if !findCreate(p, want) {
		t.Errorf("want %q, got %+v", want, p.Creates)
	}
	if !findAlter(p, `alter event trigger "log_ddl" enable always;`) || !findAlter(p, `comment on event trigger "log_ddl" is 'audit';`) {
		t.Errorf("expected enable always + comment, got %+v", p.Alters)
	}
}

func TestEventTriggerSkippedReplacedAndPruned(t *testing.T) {
	live := emptyLive()
	live.EventTriggers = map[string]*LiveEventTrigger{
		"log_ddl":  {Event: "ddl_command_end", Tags: []string{"ALTER TABLE", "CREATE TABLE"}, Function: "public.log_ddl", Enabled: "origin", Comment: "audit"},
		"stray":    {Event: "sql_drop", Function: "public.f", Enabled: "origin"},
		"from_ext": {Event: "sql_drop", Function: "public.g", Enabled: "origin", Extension: true},
	}
	same := etDB(map[string]*schema.EventTrigger{
		"log_ddl": {Name: "log_ddl", Event: "ddl_command_end", Tags: []string{"create table", "alter table"}, Function: "log_ddl", Comment: "audit"},
	})
	if sql := Render(Plan(live, same, false)); sql != "" {
		t.Errorf("same event trigger must not diff, got:\n%s", sql)
	}
	p := PlanWith(live, same, Options{Unsafe: true, Prune: true})
	if len(p.Drops) != 1 || p.Drops[0] != `drop event trigger "stray";` {
		t.Errorf("prune should drop only the undeclared, non-extension trigger, got %v", p.Drops)
	}
	changed := etDB(map[string]*schema.EventTrigger{
		"log_ddl": {Name: "log_ddl", Event: "ddl_command_end", Tags: []string{"CREATE TABLE"}, Function: "log_ddl", Comment: "audit", Enabled: "disabled"},
	})
	p = Plan(live, changed, false)
	if len(p.Creates) != 2 || p.Creates[0] != `drop event trigger "log_ddl";` {
		t.Errorf("changed tags should drop and recreate, got %+v", p.Creates)
	}
	if !findAlter(p, `alter event trigger "log_ddl" disable;`) || !findAlter(p, `comment on event trigger "log_ddl" is 'audit';`) {
		t.Errorf("recreated trigger needs its state and comment again, got %+v", p.Alters)
	}
	if sql := Render(Plan(live, &schema.Database{Tables: map[string]*schema.Table{}}, false)); sql != "" {
		t.Errorf("no eventTriggers block means unmanaged, got:\n%s", sql)
	}
}
