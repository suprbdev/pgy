package schema

import "testing"

const routineSetYAML = `
schema app:
  function fuzzy(a text, b text):
    returns: boolean
    language: sql
    set:
      pg_trgm.similarity_threshold: 0.2
      statement_timeout: 0
      enable_seqscan: false
      plpgsql.extra_warnings: all
      search_path: ""
    body: select a % b
  procedure tidy():
    language: sql
    set: {work_mem: 64, jit: true, lock_timeout: 5s}
    body: select 1
`

func TestRoutineSetScalarsParsed(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(routineSetYAML))
	if err != nil {
		t.Fatal(err)
	}
	fn := db.Functions["app.fuzzy(a text, b text)"]
	if fn == nil {
		t.Fatalf("function missing; have %v", keys(db.Functions))
	}
	for k, want := range map[string]string{
		"pg_trgm.similarity_threshold": "0.2",
		"statement_timeout":            "0",
		"enable_seqscan":               "false",
		"plpgsql.extra_warnings":       "all",
		"search_path":                  "",
	} {
		if got, ok := fn.Set[k]; !ok || got != want {
			t.Errorf("function set %s = %q (present %v), want %q", k, got, ok, want)
		}
	}
	pr := db.Procedures["app.tidy()"]
	if pr == nil {
		t.Fatalf("procedure missing; have %v", keys(db.Procedures))
	}
	for k, want := range map[string]string{"work_mem": "64", "jit": "true", "lock_timeout": "5s"} {
		if got, ok := pr.Set[k]; !ok || got != want {
			t.Errorf("procedure set %s = %q (present %v), want %q", k, got, ok, want)
		}
	}
}

func TestJSONSchemaAcceptsRoutineSetScalars(t *testing.T) {
	s := compileEditorSchema(t)
	if err := s.Validate(yamlToJSONValue(t, []byte(routineSetYAML))); err != nil {
		t.Errorf("numbers and booleans in a routine's set block rejected:\n%v", err)
	}
}
