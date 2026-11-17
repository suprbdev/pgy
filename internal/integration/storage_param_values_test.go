package integration

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

const storageParamStringValuesYAML = `
extensions: [pg_trgm]
schema SCH:
  table doc:
    columns:
      id: {type: int, primaryKey: true}
      body: {type: text}
    with: {vacuum_index_cleanup: AUTO, autovacuum_enabled: "False"}
    indexes:
      doc_body: {columns: [body], using: gist, opclass: gist_trgm_ops, with: {buffering: "ON"}}
    constraints:
      doc_body_key: {type: unique, columns: [body], with: {deduplicate_items: "Off"}}
  materialized view doc_ids:
    query: select id from SCH.doc
    with: {vacuum_index_cleanup: "On"}
`

func TestIntegrationStorageParamStringValues(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{"string_values": storageParamStringValuesYAML})
	pool := connect(t)
	sch, sql := partitionIndexCase(t, pool, "", storageParamStringValuesYAML)
	for rel, want := range map[string][]string{
		"doc":          {"vacuum_index_cleanup=AUTO", "autovacuum_enabled=False"},
		"doc_body":     {"buffering=ON"},
		"doc_body_key": {"deduplicate_items=Off"},
		"doc_ids":      {"vacuum_index_cleanup=On"},
	} {
		var got []string
		if err := pool.QueryRow(context.Background(), `select reloptions from pg_class where oid = $1::regclass`, `"`+sch+`".`+rel).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(sortedCopy(got), sortedCopy(want)) {
			t.Errorf("%s: reloptions %v, want the values as written %v, after:\n%s", rel, got, want, sql)
		}
	}
}

func sortedCopy(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}
