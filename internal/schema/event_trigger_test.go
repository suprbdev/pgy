package schema

import "testing"

func TestEventTriggersParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
eventTriggers:
  log_ddl:
    event: DDL_COMMAND_END
    tags: [CREATE TABLE]
    function: public.log_ddl()
    enabled: false
  guard:
    event: sql_drop
    function: guard()
    enabled: replica
`))
	if err != nil {
		t.Fatal(err)
	}
	if et := db.EventTriggers["log_ddl"]; et == nil || et.Event != "ddl_command_end" || len(et.Tags) != 1 || et.Function != "public.log_ddl()" || et.Enabled != "disabled" {
		t.Errorf("unexpected log_ddl: %+v", et)
	}
	if et := db.EventTriggers["guard"]; et == nil || et.Enabled != "replica" {
		t.Errorf("unexpected guard: %+v", et)
	}
	if empty, _ := parseFlexibleDatabase([]byte("tables: {}\n")); empty.EventTriggers != nil {
		t.Error("no eventTriggers block must leave EventTriggers nil (unmanaged)")
	}
}
