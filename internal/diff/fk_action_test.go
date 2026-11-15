package diff

import "testing"

func TestFKActionColumnLists(t *testing.T) {
	for in, want := range map[string]string{
		"CASCADE":                   "cascade",
		"no  action":                "",
		`set null ("ParentId")`:     `set null ("ParentId")`,
		"SET NULL (parent_id, org)": `set null ("parent_id", "org")`,
		"set default (Ref)":         `set default ("Ref")`,
	} {
		if got := fkAction(in); got != want {
			t.Errorf("fkAction(%q) = %s, want %s", in, got, want)
		}
	}
}
