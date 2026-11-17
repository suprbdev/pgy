package validate

import (
	"strings"
	"testing"
)

func TestValidateForeignTableColumnsPullWrites(t *testing.T) {
	p := write(t, t.TempDir(), "foreign.yml", `
servers:
  wh: {wrapper: postgres_fdw, options: {}}
schema rep:
  table events:
    columns: {id: {type: bigint}, region: {type: text}}
    partitionBy: {type: list, columns: [region]}
  table parent:
    columns: {id: {type: int}, name: {type: text, nullable: true}}
  foreign table orders:
    server: wh
    options: {}
    columns:
      qty: {type: int, nullable: true, options: {}}
      dbl: {type: int, generated: qty * 2, options: {}}
      big: {type: text, storage: external, nullable: true, options: {}}
  foreign table events_us:
    server: wh
    options: {table_name: events}
    partitionOf: rep.events
    forValues: {in: [us]}
    columns:
      id: {options: {column_name: event_id}}
      region: {options: {}}
  foreign table child:
    server: wh
    options: {}
    inherits: [rep.parent]
    columns:
      id: {options: {}}
      name: {options: {}}
      extra: {type: int, nullable: true, options: {}}
`)
	if issues := Files([]string{p}); len(issues) != 0 {
		t.Errorf("expected no findings, got:\n%s", messages(issues))
	}
}

func TestValidateForeignColumnNeedsATypeUnlessInherited(t *testing.T) {
	p := write(t, t.TempDir(), "foreign.yml", `
servers:
  wh: {wrapper: postgres_fdw}
schema rep:
  foreign table orders:
    server: wh
    columns:
      id: {options: {column_name: ident}}
`)
	if got := messages(Files([]string{p})); !strings.Contains(got, "column id has no type") {
		t.Errorf("expected a missing type finding, got:\n%s", got)
	}
}

func TestValidateServerRenames(t *testing.T) {
	p := write(t, t.TempDir(), "servers.yml", `
servers:
  wh: {wrapper: postgres_fdw}
  warehouse: {wrapper: postgres_fdw, renamedFrom: wh}
  lake: {wrapper: postgres_fdw, renamedFrom: pond}
  basin: {wrapper: postgres_fdw, renamedFrom: pond}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"server warehouse: renamedFrom wh names an object the YAML also declares",
		"renamedFrom pond is also claimed by server",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
