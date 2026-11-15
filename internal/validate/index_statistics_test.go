package validate

import (
	"strings"
	"testing"
)

func TestValidateIndexStatisticsTargets(t *testing.T) {
	p := write(t, t.TempDir(), "s.yml", `
schema app:
  table a:
    columns: {x: {type: int}, y: {type: text}}
    indexes:
      a_plain: {columns: [x], statisticsTargets: {x: 100}}
      a_other: {columns: [lower(y)], statisticsTargets: {upper(y): 100}}
      a_ok: {columns: [x, lower(y)], statisticsTargets: {lower(y): 500}}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		`index a_plain statisticsTargets: "x" is a plain column`,
		`index a_other statisticsTargets: "upper(y)" is not one of its columns entries`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "a_ok") {
		t.Errorf("valid statisticsTargets flagged:\n%s", got)
	}
}

func TestValidateKeyIncludeColumns(t *testing.T) {
	p := write(t, t.TempDir(), "k.yml", `
schema app:
  table a:
    columns: {x: {type: int}, y: {type: text}}
    primaryKey: {a_pk: {columns: [x], include: [nope]}}
    constraints:
      a_uq: {type: unique, columns: [y], include: [missing]}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{`primary key include references unknown column "nope"`, `constraint a_uq include references unknown column "missing"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
