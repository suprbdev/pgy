package schema

import "testing"

func TestColumnNotNullNoInheritParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  table t:
    columns:
      a: {type: int, notNullName: a_here, notNullNoInherit: true}
      b: {type: int}
tables:
  - name: app.l
    columns:
      - {name: a, type: int, notNullNoInherit: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"app.t", "app.l"} {
		if !db.Tables[table].Columns["a"].NotNullNoInherit {
			t.Errorf("%s.a: NotNullNoInherit not set", table)
		}
	}
	if db.Tables["app.t"].Columns["b"].NotNullNoInherit {
		t.Error("a column without notNullNoInherit keeps an inheritable NOT NULL")
	}
}

func TestInheritedColumnOverridesParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  table m:
    columns:
      id: {type: int}
      s: {type: int, generated: id * 2, notValid: true}
    partitionBy: {type: range, columns: [id]}
  table m1:
    partitionOf: app.m
    forValues: {from: [0], to: [10]}
    columns:
      s: {generated: id * 20, notValid: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	c := db.Tables["app.m1"].Columns["s"]
	if c.Generated != "id * 20" || c.NotValid || !c.NotValidSet {
		t.Errorf("partition column: got generated %q, notValid %v, notValidSet %v", c.Generated, c.NotValid, c.NotValidSet)
	}
	if p := db.Tables["app.m"].Columns["s"]; !p.NotValid || !p.NotValidSet {
		t.Errorf("parent column: notValid %v, notValidSet %v", p.NotValid, p.NotValidSet)
	}
	if id := db.Tables["app.m"].Columns["id"]; id.NotValidSet {
		t.Error("a column without notValid leaves NotValidSet unset")
	}
}
