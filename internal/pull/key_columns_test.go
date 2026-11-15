package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestPrimaryKeyColumnsStopAtTheirParenthesis(t *testing.T) {
	for def, want := range map[string]string{
		"PRIMARY KEY (id)":                                       "id",
		"PRIMARY KEY (id) INCLUDE (code) WITH (fillfactor='80')": "id",
		`PRIMARY KEY (a, "odd)name") INCLUDE (b)`:                `a,odd)name`,
		"PRIMARY KEY (id, valid WITHOUT OVERLAPS)":               "id,valid without overlaps",
	} {
		got := pkColumns(&diff.LiveTable{ConstraintDefs: map[string]string{"k": def}})
		if strings.Join(got, ",") != want {
			t.Errorf("%s: got %q, want %q", def, got, want)
		}
	}
}
