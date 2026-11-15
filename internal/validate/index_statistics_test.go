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

func TestValidateTypelessColumns(t *testing.T) {
	p := write(t, t.TempDir(), "i.yml", `
schema app:
  table plain:
    columns: {x: {default: 1}}
  table m:
    columns: {at: {type: date}, v: {type: int}}
    partitionBy: {range: [at]}
  table m1:
    partitionOf: app.m
    forValues: {from: ["2025-01-01"], to: ["2026-01-01"]}
    columns: {v: {default: 5}}
  table base:
    columns: {a: {type: int}}
  table kid:
    inherits: [app.base]
    columns: {a: {default: 7}, b: {default: 1}}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{"app.plain: column x has no type", "app.kid: column b has no type"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"column v has no type", "column a has no type"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "table plain > columns > x: missing properties: 'type'") || strings.Contains(got, "m1") || strings.Contains(got, "table kid > columns > a") {
		t.Errorf("the JSON Schema must require types except on partitions and children:\n%s", got)
	}
}

func TestValidateTypedTables(t *testing.T) {
	p := write(t, t.TempDir(), "tt.yml", `
schema app:
  type addr: {type: composite, attributes: {street: text, city: text}}
  table ok:
    of: app.addr
    columns: {city: {default: x}}
  table typed_column:
    of: app.addr
    columns: {city: {type: text}}
  table unknown:
    of: app.addr
    columns: {zip: {default: 1}}
  table base:
    columns: {a: {type: int}}
  table both:
    of: app.addr
    inherits: [app.base]
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"app.typed_column: column city: a typed table's columns get their types from app.addr",
		"app.unknown: column zip is not an attribute of app.addr",
		"app.both: of cannot be combined with partitionOf or inherits",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "app.ok") || strings.Contains(got, "table ok > columns") {
		t.Errorf("valid typed table flagged:\n%s", got)
	}
}

func TestValidateTypedTableKeysUseTypeAttributes(t *testing.T) {
	p := write(t, t.TempDir(), "tk.yml", `
schema app:
  type addr: {type: composite, attributes: {street: text, city: text}}
  table a:
    of: app.addr
    primaryKey: [street]
    indexes: {a_city: {columns: [city]}, a_bad: {columns: [zip]}}
`)
	got := messages(Files([]string{p}))
	if !strings.Contains(got, `index a_bad references unknown column "zip"`) || strings.Contains(got, `"street"`) || strings.Contains(got, `"city"`) {
		t.Errorf("typed table key and index columns must come from the type:\n%s", got)
	}
}
