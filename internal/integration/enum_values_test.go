package integration

import "testing"

// TestIntegrationNewEnumLabelsUsedInSameMigration adds an enum label and
// uses it in the same plan, applied the way pgy migrate applies committed
// migrations. PostgreSQL rejects a label until the transaction that added
// it commits, so the ADD VALUE statements must go first, on their own.
func TestIntegrationNewEnumLabelsUsedInSameMigration(t *testing.T) {
	pool := connect(t)
	cases := map[string][2]string{
		"enum_label_default": {`
schema SCH:
  type st: {type: enum, labels: [a, b]}
  table t:
    columns: {id: {type: int, primaryKey: true}, s: {type: SCH.st, nullable: true}}
`, `
schema SCH:
  type st: {type: enum, labels: [a, b, c]}
  table t:
    columns: {id: {type: int, primaryKey: true}, s: {type: SCH.st, default: "'c'"}}
`},
		"enum_label_check": {`
schema SCH:
  type st: {type: enum, labels: [a, b]}
  table t:
    columns: {id: {type: int, primaryKey: true}, s: {type: SCH.st, nullable: true}}
`, `
schema SCH:
  type st: {type: enum, labels: [a, b, c]}
  table t:
    columns: {id: {type: int, primaryKey: true}, s: {type: SCH.st, nullable: true}}
    constraints: {t_not_c: {type: check, expression: "s <> 'c'"}}
`},
		"enum_label_index_where": {`
schema SCH:
  type st: {type: enum, labels: [a, b]}
  table t:
    columns: {id: {type: int, primaryKey: true}, s: {type: SCH.st, nullable: true}}
`, `
schema SCH:
  type st: {type: enum, labels: [a, b, c]}
  table t:
    columns: {id: {type: int, primaryKey: true}, s: {type: SCH.st, nullable: true}}
    indexes: {t_c: {columns: [id], where: "s = 'c'"}}
`},
		"enum_label_view": {`
schema SCH:
  type st: {type: enum, labels: [a, b]}
  table t:
    columns: {id: {type: int, primaryKey: true}, s: {type: SCH.st, nullable: true}}
`, `
schema SCH:
  type st: {type: enum, labels: [a, b, c]}
  table t:
    columns: {id: {type: int, primaryKey: true}, s: {type: SCH.st, nullable: true}}
  view v:
    query: select id from SCH.t where s = 'c'
    dependsOn: [table SCH.t]
`},
		"enum_label_partition": {`
schema SCH:
  type st: {type: enum, labels: [a, b]}
  table t:
    columns: {id: {type: int}, s: {type: SCH.st}}
    partitionBy: {list: [s]}
  table t_a:
    partitionOf: SCH.t
    forValues: {in: [a]}
`, `
schema SCH:
  type st: {type: enum, labels: [a, b, c]}
  table t:
    columns: {id: {type: int}, s: {type: SCH.st}}
    partitionBy: {list: [s]}
  table t_a:
    partitionOf: SCH.t
    forValues: {in: [a]}
  table t_c:
    partitionOf: SCH.t
    forValues: {in: [c]}
`},
		"enum_label_data": {`
schema SCH:
  type st: {type: enum, labels: [a, b]}
  table t:
    columns: {id: {type: int, primaryKey: true}, s: {type: SCH.st, nullable: true}}
`, `
schema SCH:
  type st: {type: enum, labels: [a, b, c]}
  table t:
    columns: {id: {type: int, primaryKey: true}, s: {type: SCH.st, nullable: true}}
    data:
      rows:
        - {id: 1, s: c}
`},
		"new_fk_to_new_column": {`
schema SCH:
  table p:
    columns: {id: {type: int, primaryKey: true}}
`, `
schema SCH:
  table p:
    columns: {id: {type: int, primaryKey: true}, code: {type: text, unique: true, nullable: true}}
  table c:
    columns: {id: {type: int, primaryKey: true}, pcode: {type: text, nullable: true}}
    foreignKeys: {c_p: {columns: [pcode], references: {table: SCH.p, columns: [code]}}}
`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			sch := freshSchema(t, pool)
			convergeYAML(t, pool, sch, c[0])
			convergeYAML(t, pool, sch, c[1])
		})
	}
}
