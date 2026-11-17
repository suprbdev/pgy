package schema

import "testing"

const validateModeYAML = `
schema app:
  table t:
    validate: immediate
    columns:
      id: {type: int, primaryKey: true}
      ref: {type: int, validate: later}
    foreignKeys:
      t_ref_fkey: {columns: [ref], references: {table: app.t, columns: [id]}, validate: later}
    constraints:
      t_ref_check: {type: check, expression: ref > 0, validate: immediate}
`

func TestValidateModeParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(validateModeYAML))
	if err != nil {
		t.Fatal(err)
	}
	tb := db.Tables["app.t"]
	if tb == nil || tb.Validate != ValidateImmediate || tb.Columns["ref"].Validate != ValidateLater ||
		tb.ForeignKeys[0].Validate != ValidateLater || tb.Constraints[0].Validate != ValidateImmediate {
		t.Fatalf("parsed: %+v", tb)
	}
	if !ValidatesImmediately(tb, "") || ValidatesImmediately(tb, ValidateLater) || ValidatesImmediately(&Table{}, "") || !ValidatesImmediately(nil, ValidateImmediate) {
		t.Error("a constraint's own validate wins over its table's, which defaults to later")
	}
}

func TestJSONSchemaAcceptsValidateModes(t *testing.T) {
	s := compileEditorSchema(t)
	if err := s.Validate(yamlToJSONValue(t, []byte(validateModeYAML))); err != nil {
		t.Fatalf("validate modes must validate: %v", err)
	}
	if err := s.Validate(yamlToJSONValue(t, []byte("schema app:\n  table t:\n    validate: sometimes\n    columns: {id: {type: int}}\n"))); err == nil {
		t.Error("an unknown validate mode must not validate")
	}
}
