package integration

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// columnAttributeCases maps a case name to the YAML of column b. Each must
// converge in one migration whether b arrives with a new table, is added to
// an existing table, is changed from or back to a plain nullable column of
// the same type, and must survive a pull round trip.
var columnAttributeCases = map[string]string{
	"default":           `{type: int, default: 5}`,
	"default_expr":      `{type: timestamptz, default: now()}`,
	"default_text":      `{type: text, default: "'it''s'"}`,
	"nullable":          `{type: int, nullable: true}`,
	"unique":            `{type: int, nullable: true, unique: true}`,
	"identity_always":   `{type: bigint, identity: always}`,
	"identity_default":  `{type: bigint, identity: by default}`,
	"identity_options":  `{type: bigint, identity: {generated: always, start: 100, increment: 5, cache: 10}}`,
	"identity_named":    `{type: bigint, identity: {generated: always, sequenceName: b_custom_seq}}`,
	"generated":         `{type: int, nullable: true, generated: id * 2}`,
	"collate":           `{type: text, nullable: true, collate: C}`,
	"compression":       `{type: text, nullable: true, compression: pglz}`,
	"storage":           `{type: text, nullable: true, storage: external}`,
	"statistics":        `{type: int, nullable: true, statisticsTarget: 250}`,
	"statistics_zero":   `{type: int, nullable: true, statisticsTarget: 0}`,
	"ndistinct":         `{type: int, nullable: true, nDistinct: -0.2, nDistinctInherited: 7}`,
	"comment":           `{type: int, nullable: true, comment: "b's comment"}`,
	"grants":            `{type: int, nullable: true, grants: {public: [select, update]}}`,
	"array":             `{type: "text[]", nullable: true, default: "'{}'"}`,
	"numeric":           `{type: "numeric(12,2)", default: 0}`,
	"varchar":           `{type: "varchar(40)", nullable: true}`,
	"serial":            `{type: serial}`,
	"everything":        `{type: text, collate: C, default: "''", compression: pglz, storage: main, statisticsTarget: 50, nDistinct: 3, comment: all of it, unique: true}`,
}

func TestIntegrationColumnAttributeMatrix(t *testing.T) {
	names := make([]string, 0, len(columnAttributeCases))
	for n := range columnAttributeCases {
		names = append(names, n)
	}
	sort.Strings(names)
	created := map[string]string{}
	for _, n := range names {
		created["new_table_"+n] = `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      b: ` + columnAttributeCases[n] + "\n"
	}
	assertYAMLIdempotent(t, created)

	pool := connect(t)
	typeRe := regexp.MustCompile(`type: ("[^"]+"|[^,}]+)`)
	for _, n := range names {
		t.Run("changed_"+n, func(t *testing.T) {
			sch := freshSchema(t, pool)
			typ := typeRe.FindStringSubmatch(columnAttributeCases[n])[1]
			if typ == "serial" {
				typ = "int"
			}
			base := `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
`
			plain := base + "      b: {type: " + typ + ", nullable: true}\n"
			convergeYAML(t, pool, sch, plain)
			convergeYAML(t, pool, sch, base+"      b: "+columnAttributeCases[n]+"\n")
			convergeYAML(t, pool, sch, plain)
		})
		t.Run("added_"+n, func(t *testing.T) {
			sch := freshSchema(t, pool)
			base := `
schema SCH:
  table t:
    columns:
      id: {type: int, primaryKey: true}
`
			convergeYAML(t, pool, sch, base)
			sql := convergeYAML(t, pool, sch, base+"      b: "+columnAttributeCases[n]+"\n")
			if !strings.Contains(sql, `add column "b"`) {
				t.Errorf("expected ADD COLUMN, got:\n%s", sql)
			}
		})
	}
}
