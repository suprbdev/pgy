package pull

import (
	"strings"
	"testing"
)

func TestBoundValuesKeepStringCase(t *testing.T) {
	for in, want := range map[string]string{
		"'X', 'MixedCase'":               "X|MixedCase",
		"MINVALUE, 10, NULL, true":       "minvalue|10|null|true",
		"'null', 'TRUE', '42', ' p '":    "'null'|'TRUE'|'42'|' p '",
		"'it''s', '''quoted'''":          "it's|'''quoted'''",
		"'', '2025-01-01 00:00:00+00'":   "''|2025-01-01 00:00:00+00",
	} {
		if got := strings.Join(boundValues(in), "|"); got != want {
			t.Errorf("boundValues(%s) = %s, want %s", in, got, want)
		}
	}
}
