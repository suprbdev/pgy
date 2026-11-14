package schema

import "testing"

func TestRangeTypeParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema public:
  type floatrange:
    type: range
    subtype: float8
    subtypeDiff: float8mi
`))
	if err != nil {
		t.Fatal(err)
	}
	td := db.Types["public.floatrange"]
	if td == nil || td.Kind != "range" || td.Range == nil || td.Range.Subtype != "float8" || td.Range.SubtypeDiff != "float8mi" {
		t.Errorf("unexpected range type: %+v", td)
	}
}
