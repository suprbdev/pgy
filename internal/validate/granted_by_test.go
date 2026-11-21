package validate

import (
	"strings"
	"testing"
)

func TestValidateGrantedByPullWrites(t *testing.T) {
	p := write(t, t.TempDir(), "app.yml", `
schema app:
  grants: {admin: [usage with grant option]}
  grantedBy: {admin: {reader: [usage]}}
  table doc:
    columns:
      id: {type: integer, grantedBy: {admin: {reader: [update]}}}
    grants: {admin: [select with grant option, update with grant option]}
    grantedBy:
      admin: {lead: [select with grant option]}
      lead: {reader: [select]}
  function f():
    returns: integer
    language: sql
    body: select 1
    grantedBy: {admin: {reader: [execute]}}
`)
	if issues := Files([]string{p}); len(issues) != 0 {
		t.Errorf("expected no findings, got:\n%s", messages(issues))
	}
}

func TestValidateGrantedByMistakes(t *testing.T) {
	p := write(t, t.TempDir(), "app.yml", `
languages:
  plpgsql:
    grantedBy: {admin: {reader: [execute]}}
schema app:
  table doc:
    owner: app_owner
    columns:
      id: {type: integer}
    grants: {admin: [select]}
    grantedBy:
      public: {reader: [select]}
      app_owner: {reader: [select]}
      admin: {reader: [select, execute]}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		`language plpgsql: grantedBy: admin grants execute to reader, a privilege languages don't have; write usage (or all)`,
		`table app.doc: grantedBy names "public" as a grantor`,
		`table app.doc: grantedBy lists the owner app_owner, whose grants are what the grants block makes`,
		`table app.doc: grantedBy: admin grants execute to reader, a privilege tables don't have`,
		`table app.doc: grantedBy: admin grants select to reader, but neither the grants block nor another grantor gives admin select with grant option`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "app_owner grants select") {
		t.Errorf("the owner needs no grant option:\n%s", got)
	}
}
