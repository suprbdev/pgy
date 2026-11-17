package schema

import "testing"

func TestJSONSchemaAcceptsForeignTableFeatures(t *testing.T) {
	s := compileEditorSchema(t)
	doc := `
servers:
  warehouse: {wrapper: postgres_fdw, renamedFrom: wh, version: 15.10, options: {}}
schema rep:
  table events:
    columns: {id: {type: bigint}, region: {type: text}}
    partitionBy: {type: list, columns: [region]}
  table parent:
    columns: {id: {type: int}}
  foreign table orders:
    server: warehouse
    options: {}
    columns:
      id: {type: int, options: {}}
      qty: {type: int, nullable: true}
      dbl: {type: int, generated: qty * 2}
      trp: {type: int, generated: qty * 3, virtual: true}
      big: {type: text, storage: external, nullable: true}
    triggers:
      orders_audit: {timing: after, events: [insert], level: row, procedure: rep.audit()}
  foreign table events_us:
    server: warehouse
    partitionOf: rep.events
    forValues: {in: [us]}
    columns:
      id: {options: {column_name: event_id}}
      region: {default: "'us'", storage: main}
  foreign table child:
    server: warehouse
    inherits: [parent]
    columns:
      id: {options: {}}
      extra: {type: int, nullable: true}
`
	if err := s.Validate(yamlToJSONValue(t, []byte(doc))); err != nil {
		t.Errorf("foreign table features rejected:\n%v", err)
	}
}

func TestJSONSchemaRejectsInvalidForeignColumns(t *testing.T) {
	s := compileEditorSchema(t)
	for name, doc := range map[string]string{
		"a column without a type on a plain foreign table": `
schema rep:
  foreign table orders:
    server: warehouse
    columns:
      id: {options: {column_name: ident}}
`,
		"an identity column": `
schema rep:
  foreign table orders:
    server: warehouse
    columns:
      id: {type: int, identity: always}
`,
		"a compression setting": `
schema rep:
  foreign table orders:
    server: warehouse
    columns:
      id: {type: text, compression: lz4}
`,
	} {
		if err := s.Validate(yamlToJSONValue(t, []byte(doc))); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}
