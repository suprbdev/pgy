package diff

import "testing"

func TestTriggerEventsKeepColumnSpelling(t *testing.T) {
	got := renderTriggerEvents([]string{"insert", `update of "Weird Col", email, two words`})
	want := `INSERT OR UPDATE OF "Weird Col", email, "two words"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
