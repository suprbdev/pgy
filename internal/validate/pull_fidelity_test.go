package validate

import (
	"strings"
	"testing"
)

func TestValidateLargeObjectDefaultsInSchema(t *testing.T) {
	path := write(t, t.TempDir(), "dp.yml", `
defaultPrivileges:
  - for: app_owner
    in: app
    largeObjects: {app_read: [select]}
  - for: app_owner
    largeObjects: {app_read: [select, update]}
schema app: {}
`)
	got := messages(Files([]string{path}))
	if !strings.Contains(got, "defaultPrivileges[0]: largeObjects defaults cannot be limited with in") {
		t.Errorf("largeObjects with in must be rejected:\n%s", got)
	}
	if strings.Contains(got, "defaultPrivileges[1]") {
		t.Errorf("largeObjects without in is valid:\n%s", got)
	}
}

func TestValidateOwnedBySequenceOwner(t *testing.T) {
	path := write(t, t.TempDir(), "s.yml", `
schema app:
  table ticket:
    columns: {id: {type: int}}
    owner: app_owner
  sequence seq_ticket:
    ownedBy: ticket.id
    owner: app_owner
  sequence seq_free:
    ownedBy: none
    owner: app_owner
`)
	got := messages(Files([]string{path}))
	if !strings.Contains(got, "sequence app.seq_ticket: is owned by column ticket.id, so it always has the owner of table app.ticket; owner is ignored") {
		t.Errorf("an owner on a sequence owned by a column must be reported:\n%s", got)
	}
	if strings.Contains(got, "seq_free") {
		t.Errorf("a sequence without a column may have an owner:\n%s", got)
	}
}

func TestValidateMatViewColumns(t *testing.T) {
	path := write(t, t.TempDir(), "mv.yml", `
schema app:
  view v:
    query: select 1 as a
    columns: {a: {compression: lz4}}
  materialized view mv:
    query: select 1 as a, 'x'::text as b
    columns: {a: {statisticsTarget: 100}, b: {compression: lz4, storage: main, nDistinct: -1}}
  domain pct:
    type: numeric
    checks: {pct_not_99: {check: VALUE <> 99, notValid: true}}
`)
	got := messages(Files([]string{path}))
	if !strings.Contains(got, "columns are for materialized views") {
		t.Errorf("columns on a plain view must be rejected:\n%s", got)
	}
	if strings.Contains(got, "materialized view app.mv") || strings.Contains(got, "pct") {
		t.Errorf("materialized view columns and notValid domain checks are valid:\n%s", got)
	}
}
