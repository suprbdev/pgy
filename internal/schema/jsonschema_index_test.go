package schema

import "testing"

func TestJSONSchemaAcceptsExtensionAccessMethodsAndOpclassParameters(t *testing.T) {
	s := compileEditorSchema(t)
	doc := `
extensions: [bloom, pg_trgm]
schema public:
  table t:
    columns:
      id: {type: int}
      name: {type: text}
    indexes:
      t_bloom: {columns: [id, name], using: bloom, with: {length: 80, col1: 2}}
      t_trgm: {columns: [name], using: gist, opclass: gist_trgm_ops(siglen=32)}
      t_both: {columns: [name], using: gist, opclasses: {name: "gist_trgm_ops (siglen='64')"}}
  materialized view m:
    query: select 1 as id, 'x'::text as name
    indexes:
      m_bloom: {columns: [id], using: bloom}
`
	if err := s.Validate(yamlToJSONValue(t, []byte(doc))); err != nil {
		t.Errorf("extension access method or opclass parameters rejected:\n%v", err)
	}
	for _, bad := range []string{
		"schema public:\n  table t:\n    columns: {id: {type: int}}\n    indexes:\n      i: {columns: [id], using: 5}\n",
		"schema public:\n  table t:\n    columns: {id: {type: int}}\n    indexes:\n      i: {columns: [id], using: [gin]}\n",
	} {
		if err := s.Validate(yamlToJSONValue(t, []byte(bad))); err == nil {
			t.Errorf("a non-string access method must be rejected:\n%s", bad)
		}
	}
}
