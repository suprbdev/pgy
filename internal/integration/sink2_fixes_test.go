package integration

import (
	"strings"
	"testing"
)

func TestIntegrationSubPartitionsAndRangeCollation(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{"subparts": `
schema SCH:
  type textrange: {type: range, subtype: text, collation: C}
  table r:
    columns: {id: {type: int, nullable: true}, at: {type: date}, region: {type: text}}
    partitionBy: {range: [at]}
  table r2025:
    partitionOf: SCH.r
    forValues: {from: ["2025-01-01"], to: ["2026-01-01"]}
    partitionBy: {list: [region]}
  table r2025_eu:
    partitionOf: SCH.r2025
    forValues: {in: [eu]}
  table r2025_rest:
    partitionOf: SCH.r2025
    default: true
`})
}

func TestIntegrationIndexAndAttributeCollations(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{"collations": `
schema SCH:
  type labelled:
    type: composite
    attributes:
      label: {type: text, collate: C}
      n: {type: int}
  table items:
    columns: {id: {type: int, primaryKey: true}, name: {type: text}}
    indexes:
      items_name_c: {columns: [name desc nulls last], collations: {name desc nulls last: C}}
      items_lower: {columns: [lower(name)], collations: {lower(name): C}}
`})
}

func TestIntegrationIndexCollationChange(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	base := `
schema SCH:
  table items:
    columns: {id: {type: int, primaryKey: true}, name: {type: text}}
    indexes:
      items_name: {columns: [name]COLL}
`
	convergeYAML(t, pool, sch, strings.Replace(base, "COLL", "", 1))
	sql := convergeYAML(t, pool, sch, strings.Replace(base, "COLL", ", collations: {name: C}", 1))
	if !strings.Contains(sql, `drop index`) || !strings.Contains(sql, `("name" collate "C")`) {
		t.Errorf("a changed index collation is rebuilt under --unsafe:\n%s", sql)
	}
}

func TestIntegrationPrimaryKeyRenameAndForceRLS(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, `
schema SCH:
  table t:
    columns: {id: {type: int}}
    primaryKey: [id]
    rowLevelSecurity: true
`)
	sql := convergeYAML(t, pool, sch, `
schema SCH:
  table t:
    columns: {id: {type: int}}
    primaryKey: {t_pk: {columns: [id]}}
    rowLevelSecurity: true
    forceRowLevelSecurity: true
`)
	for _, want := range []string{`rename constraint "t_pkey" to "t_pk"`, "force row level security"} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestIntegrationExpressionIndexWithStorageTypeIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{"trgm": `
extensions: [pg_trgm]
schema SCH:
  table names:
    columns: {id: {type: int, primaryKey: true}, name: {type: text}}
    indexes:
      names_lower_trgm: {columns: [lower(name)], using: gin, opclass: gin_trgm_ops}
      names_lower: {columns: [lower(name)]}
`})
}
