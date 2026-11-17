package validate

import (
	"strings"
	"testing"
)

func TestValidateNonStringKeys(t *testing.T) {
	p := write(t, t.TempDir(), "k.yml", `
extensions: [file_fdw]
servers:
  files: {wrapper: file_fdw}
schema f:
  foreign table csv:
    server: files
    options: {filename: /tmp/a.csv, format: csv, null: "NULL"}
    columns:
      a: {type: text, nullable: true}
  table pivot:
    columns:
      id: {type: integer}
      2024: {type: numeric, nullable: true}
      true: {type: boolean, nullable: true}
      null: {type: text, nullable: true}
      1.5: {type: text, nullable: true}
    primaryKey: [id]
    indexes:
      pivot_years: {columns: ["2024", "true"]}
    data:
      key: [id]
      rows:
        - {id: 1, 2024: 1.5, null: n}
`)
	if issues := Files([]string{p}); len(issues) > 0 {
		t.Errorf("unexpected findings:\n%s", messages(issues))
	}
}

func TestValidateChecksFilesWithNonStringKeys(t *testing.T) {
	p := write(t, t.TempDir(), "k.yml", `
schema f:
  table pivot:
    columns:
      id: {type: integer}
      2024: {type: numeric, nulable: true}
    indexes:
      pivot_missing: {columns: [2025]}
`)
	got := messages(Files([]string{p}))
	if !strings.Contains(got, "nulable") {
		t.Errorf("the JSON Schema check was skipped:\n%s", got)
	}
	if !strings.Contains(got, `references unknown column "2025"`) {
		t.Errorf("the numeric index column was dropped:\n%s", got)
	}
}

func TestValidateSequenceOwnedByDottedColumn(t *testing.T) {
	p := write(t, t.TempDir(), "s.yml", `
schema app:
  table t:
    columns:
      id: {type: integer}
      a.b: {type: integer}
  sequence good:
    ownedBy: app.t.a.b
  sequence bad:
    ownedBy: app.t.a.c
`)
	got := messages(Files([]string{p}))
	if !strings.Contains(got, "sequence app.bad: owned by unknown column app.t.a.c") {
		t.Errorf("missing unknown column error in:\n%s", got)
	}
	if strings.Contains(got, "app.good") {
		t.Errorf("dotted column flagged:\n%s", got)
	}
}
