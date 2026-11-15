package integration

import (
	"strings"
	"testing"
)

func TestIntegrationKeyIndexParametersAndIdentitySequenceName(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	table := `
schema SCH:
  table t:
    columns:
      id: {type: bigint, identity: {generated: always, sequenceName: %s, start: 5}}
      code: {type: text}
      extra: {type: text, nullable: true}
    primaryKey: {t_pk: {columns: [id], include: [code], with: {fillfactor: 80}}}
    constraints:
      t_code_uq: {type: unique, columns: [code], include: [extra], with: {fillfactor: 70, deduplicate_items: false}}
`
	step := func(seq string) string {
		return convergeYAML(t, pool, sch, strings.Replace(table, "%s", seq, 1))
	}
	if sql := step("custom_id_seq"); !contains(sql, `sequence name "custom_id_seq"`) || !contains(sql, `include ("code") with (fillfactor = 80)`) {
		t.Errorf("unexpected create:\n%s", sql)
	}
	if sql := step("renamed_id_seq"); !contains(sql, `rename to "renamed_id_seq"`) || contains(sql, "drop constraint") {
		t.Errorf("want only a sequence rename, got:\n%s", sql)
	}
	table = strings.NewReplacer("fillfactor: 80", "fillfactor: 60", "fillfactor: 70, deduplicate_items: false", "fillfactor: 90").Replace(table)
	sql := step("renamed_id_seq")
	if !contains(sql, `alter index`) || !contains(sql, "reset (deduplicate_items)") || contains(sql, "drop constraint") {
		t.Errorf("want in-place index parameter changes, got:\n%s", sql)
	}
}
