package schema

import "testing"

func TestPostgres18OptionsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema public:
  table t:
    columns:
      a: {type: int}
      v: {type: int, generated: a * 2, virtual: true}
      pid: {type: int}
    foreignKeys:
      t_pid_fkey:
        columns: [pid]
        references: {table: public.p, columns: [id]}
        enforced: false
    constraints:
      t_a_check:
        type: check
        expression: a > 0
        enforced: false
      t_a_check2:
        type: check
        expression: a > 1
        enforced: true
`))
	if err != nil {
		t.Fatal(err)
	}
	tbl := db.Tables["public.t"]
	if !tbl.Columns["v"].Virtual {
		t.Error("virtual not parsed")
	}
	if !tbl.ForeignKeys[0].NotEnforced {
		t.Error("FK enforced: false not parsed")
	}
	got := map[string]bool{}
	for _, c := range tbl.Constraints {
		got[c.Name] = c.NotEnforced
	}
	if !got["t_a_check"] || got["t_a_check2"] {
		t.Errorf("check enforced parsed wrong: %v", got)
	}
}
