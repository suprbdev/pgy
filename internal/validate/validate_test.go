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
  table g:
    columns:
      a: {type: int}
      b: {type: int, generated: a * 2}
    triggers:
      g_touch: {timing: before, events: [update], level: row, when: "old.* is distinct from new.*", procedure: app.touch()}
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
		"a BEFORE trigger's WHEN cannot reference new.* on a table with generated columns",
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

func TestValidateClusterOn(t *testing.T) {
	p := write(t, t.TempDir(), "c.yml", `
schema app:
  table a:
    clusterOn: a_part
    columns: {x: {type: int}}
    indexes:
      a_part: {columns: [x], where: x > 0}
  table b:
    clusterOn: b_missing
    columns: {x: {type: int}}
  table p:
    clusterOn: p_x
    columns: {x: {type: int}}
    indexes:
      p_x: {columns: [x]}
    partitionBy: {type: range, columns: [x]}
  table ok:
    clusterOn: ok_x
    columns: {x: {type: int}}
    indexes:
      ok_x: {columns: [x]}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"clusterOn index a_part is partial",
		"clusterOn index b_missing is not declared on this table",
		"clusterOn is not supported on partitioned tables",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ok_x") {
		t.Errorf("valid clusterOn flagged:\n%s", got)
	}
}

func TestValidateFunctionRows(t *testing.T) {
	p := write(t, t.TempDir(), "f.yml", `
schema app:
  function scalar():
    returns: int
    language: sql
    rows: 10
    body: select 1
  function srf():
    returns: setof int
    language: sql
    rows: 10
    body: select 1
`)
	got := messages(Files([]string{p}))
	if !strings.Contains(got, "function app.scalar(): rows applies only to set-returning functions") {
		t.Errorf("missing rows error in:\n%s", got)
	}
	if strings.Contains(got, "srf") {
		t.Errorf("set-returning function flagged:\n%s", got)
	}
}

func TestValidateDomainCheckDeclaredTwice(t *testing.T) {
	p := write(t, t.TempDir(), "d.yml", `
schema app:
  domain score:
    type: int
    check: VALUE >= 0
    checks: {score_check: VALUE <= 100}
`)
	if got := messages(Files([]string{p})); !strings.Contains(got, "check score_check is declared by both check and checks") {
		t.Errorf("missing duplicate check error in:\n%s", got)
	}
}

func TestValidateRenames(t *testing.T) {
	p := write(t, t.TempDir(), "r.yml", `
schema app:
  table a:
    renamedFrom: b
    columns:
      x: {type: int, renamedFrom: y}
      y: {type: int}
      z: {type: int, renamedFrom: w}
      v: {type: int, renamedFrom: w}
  table b:
    columns: {id: {type: int}}
  table c:
    renamedFrom: old
    columns: {id: {type: int}}
  view d:
    renamedFrom: old
    query: select 1
  type mood:
    type: enum
    labels: [ok, so_so]
    renamedLabels: {so_so: ok, gone: meh}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"table app.a: renamedFrom app.b names an object the YAML also declares",
		"table app.a column x: renamedFrom y names an object the YAML also declares",
		"renamedFrom w is also claimed by table app.a column",
		"renamedFrom app.old is also claimed by table app.c",
		"renamedLabels names gone, which is not one of its labels",
		"renamedLabels source ok is still one of its labels",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestValidateInherits(t *testing.T) {
	p := write(t, t.TempDir(), "i.yml", `
schema app:
  table base:
    columns: {id: {type: int}}
  table child:
    inherits: [app.base, app.missing]
    columns: {extra: {type: int}}
    indexes:
      child_id_idx: {columns: [id]}
  table bad:
    inherits: [app.base]
    partitionBy: {type: range, columns: [id]}
    columns: {x: {type: int}}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{"inherits app.missing, which no schema file declares", "inherits cannot be combined with partitionOf or partitionBy"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, `unknown column "id"`) {
		t.Errorf("a column inherited from a parent is known:\n%s", got)
	}
}
