package integration

import "testing"

func TestIntegrationToastStorageOptionsIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"toast": `
schema SCH:
  table doc:
    columns: {id: {type: int}, body: {type: text}}
    with: {fillfactor: 90, toast.autovacuum_enabled: false, toast.vacuum_truncate: false}
`,
	})
}
