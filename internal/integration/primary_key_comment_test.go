package integration

import "testing"

func TestIntegrationPrimaryKeyComments(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	table := "\nschema SCH:\n  table t:\n    columns: {id: {type: int}, b: {type: text, nullable: true}}\n"
	for _, step := range []struct{ pk, want string }{
		{"    primaryKey: {t_pkey: {columns: [id], comment: the key}}\n", "is 'the key'"},
		{"    primaryKey: {t_pkey: {columns: [id], comment: \"it's the key\"}}\n", "is 'it''s the key'"},
		{"    primaryKey: {t_id: {columns: [id], comment: \"it's the key\"}}\n", "rename constraint"},
		{"    primaryKey: [id]\n", ""},
	} {
		if sql := convergeYAML(t, pool, sch, table+step.pk); !contains(sql, step.want) {
			t.Errorf("want %q, got:\n%s", step.want, sql)
		}
	}
}
