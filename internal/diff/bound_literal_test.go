package diff

import "testing"

func TestPartitionBoundLiteral(t *testing.T) {
	for in, want := range map[string]string{
		"2024-01-01":             "'2024-01-01'",
		"'2024-01-01'":           "'2024-01-01'",
		"'2024-01-01'::date":     "'2024-01-01'::date",
		"it's":                   "'it''s'",
		"42":                     "42",
		"MAXVALUE":               "maxvalue",
		"'a' || 'b'":             "'''a'' || ''b'''",
	} {
		if got := partitionBoundLiteral(in); got != want {
			t.Errorf("partitionBoundLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}
