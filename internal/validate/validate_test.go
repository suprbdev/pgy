package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func messages(issues []Issue) string {
	out := []string{}
	for _, i := range issues {
		out = append(out, i.String())
	}
	return strings.Join(out, "\n")
}

func TestValidateCleanFile(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "ok.yml", `
schema app:
  function touch():
    returns: trigger
    language: plpgsql
    body: begin return new; end;
  table orgs:
    columns:
      id: {type: bigint, primaryKey: true}
  table users:
    columns:
      id: {type: bigint, primaryKey: true}
      org_id: {type: bigint}
      email: {type: text}
    indexes:
      users_email: {columns: [lower(email), id desc], include: [org_id]}
    foreignKeys:
      users_org_fkey:
        columns: [org_id]
        references: {table: app.orgs, columns: [id]}
    triggers:
      touch: {timing: before, events: [update], level: row, procedure: app.touch()}
    dependsOn: [table app.orgs]
`)
	if issues := Files([]string{p}); len(issues) != 0 {
		t.Errorf("expected no findings, got:\n%s", messages(issues))
	}
}

func TestValidateFindsProblems(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "bad.yml", `
schema app:
  table users:
    columns:
      id: {type: bigint, primarykey: true}
      email: {type: text}
    indexes:
      users_mail: {columns: [mail]}
    foreignKeys:
      users_org_fkey:
        columns: [org_id]
        references: {table: app.orgs, columns: [id]}
    triggers:
      t: {timing: before, events: [update], procedure: app.touch()}
    dependsOn: [table app.nope]
  table child:
    partitionOf: app.users
  type mood:
    type: enum
`)
	issues := Files([]string{p})
	if !HasErrors(issues) {
		t.Fatal("expected errors")
	}
	got := messages(issues)
	for _, want := range []string{
		"columns > id: additionalProperties 'primarykey' not allowed",
		`dependsOn "table app.nope" matches nothing declared`,
		`index users_mail references unknown column "mail"`,
		`foreign key users_org_fkey references unknown column "org_id"`,
		"references app.orgs, which no schema file declares",
		"trigger t calls app.touch, which no schema file declares",
		"partition needs forValues or default: true",
		"enum has no labels",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "expected array") {
		t.Errorf("oneOf alternatives shadowed by a deeper error must be dropped:\n%s", got)
	}
	if issues[0].Severity != Error {
		t.Errorf("errors sort first: %v", issues[0])
	}
}

func TestValidateCycleAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	a := write(t, dir, "a.yml", `
schema app:
  view v1:
    query: select 1
    dependsOn: [view app.v2]
  view v2:
    query: select 2
    dependsOn: [view app.v1]
  function f():
    returns: int
    language: sql
    body: select 1
`)
	b := write(t, dir, "b.yml", `
schema app:
  function f():
    returns: int
    language: sql
    body: select 2
`)
	c := write(t, dir, "c.yml", `
schema app:
  function f():
    returns: int
    language: sql
    body: select 2
`)
	got := messages(Files([]string{a, b, c}))
	if !strings.Contains(got, "dependency cycle detected") {
		t.Errorf("expected cycle error, got:\n%s", got)
	}
	if !strings.Contains(got, "function app.f(): declared differently in") {
		t.Errorf("expected duplicate-declaration warning, got:\n%s", got)
	}
	if got := messages(Files([]string{b, c})); strings.Contains(got, "declared differently") {
		t.Errorf("identical duplicate declarations must not warn:\n%s", got)
	}
}
