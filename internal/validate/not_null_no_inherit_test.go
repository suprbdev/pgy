package validate

import (
	"strings"
	"testing"
)

func TestValidateNotNullNoInherit(t *testing.T) {
	p := write(t, t.TempDir(), "n.yml", `
schema app:
  table t:
    columns:
      id: {type: int, notNullNoInherit: true}
      a: {type: int, nullable: true, notNullNoInherit: true}
  table p:
    columns:
      id: {type: int, notNullNoInherit: true}
    partitionBy: {type: list, columns: [id]}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"table app.t: column a sets notNullNoInherit but is nullable",
		"table app.p: column id: notNullNoInherit is not allowed on a partitioned table",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "app.t: column id") {
		t.Errorf("a NOT NULL column of a plain table can be NO INHERIT:\n%s", got)
	}
}
