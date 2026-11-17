package validate

import (
	"strings"
	"testing"
)

func TestValidateDottedObjectNames(t *testing.T) {
	p := write(t, t.TempDir(), "dots.yml", `
schema app:
  table "x.y":
    columns:
      k.v: {type: integer}
      n: {type: integer, nullable: true}
    primaryKey: [k.v]
  table "in.h":
    inherits: [app."x.y"]
    columns:
      extra: {type: integer, nullable: true}
  table plain:
    columns:
      ref: {type: integer, nullable: true}
    foreignKeys:
      plain_ref_fkey:
        columns: [ref]
        references: {table: app."x.y", columns: [k.v]}
  sequence "se.q":
    ownedBy: app."x.y".n
  view "v.w":
    dependsOn: [table app."x.y"]
    query: select n from app."x.y"
  function "f.n"(a integer):
    returns: integer
    language: sql
    body: select a
  function uses():
    returns: integer
    language: sql
    dependsOn: [function app."f.n"(integer), view app."v.w"]
    body: select app."f.n"(1)
`)
	if issues := Files([]string{p}); len(issues) > 0 {
		t.Errorf("unexpected findings:\n%s", messages(issues))
	}
}

func TestValidateDottedOwnedByUnknownColumn(t *testing.T) {
	p := write(t, t.TempDir(), "dots.yml", `
schema app:
  table "x.y":
    columns:
      n: {type: integer, nullable: true}
  sequence s:
    ownedBy: app."x.y".missing
`)
	if got := messages(Files([]string{p})); !strings.Contains(got, `sequence app.s: owned by unknown column app."x.y".missing`) {
		t.Errorf("want the unknown column reported, got:\n%s", got)
	}
}

func TestValidateDottedSchemaName(t *testing.T) {
	p := write(t, t.TempDir(), "dots.yml", `
schema "app.sub":
  table t:
    columns:
      id: {type: integer}
schema other.sub:
  table u:
    columns:
      id: {type: integer}
schemas:
  third.sub:
    v:
      columns:
        id: {type: integer}
schema "fine":
  table w:
    columns:
      id: {type: integer}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{`schema "app.sub": the schema's name contains a dot`, `schema other.sub: the schema's name contains a dot`, `schema third.sub: the schema's name contains a dot`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "fine") {
		t.Errorf("a quoted name without a dot is fine, got:\n%s", got)
	}
}
