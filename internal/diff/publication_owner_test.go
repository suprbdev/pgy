package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestPublicationOwner(t *testing.T) {
	d := &schema.Database{Publications: map[string]*schema.Publication{"Pub": {Name: "Pub", AllTables: true, Owner: "Pgy_Owner"}}}
	live := emptyLive()
	live.CurrentUser = "pgy"
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, `alter publication "Pub" owner to "Pgy_Owner";`) {
		t.Errorf("new publication: missing owner in:\n%s", sql)
	}
	live.Publications = map[string]*LivePublication{"Pub": {AllTables: true, Publish: []string{"insert", "update", "delete", "truncate"}, Owner: "pgy", Tables: map[string]LivePublicationTable{}}}
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, `alter publication "Pub" owner to "Pgy_Owner";`) {
		t.Errorf("changed owner: got:\n%s", sql)
	}
	live.Publications["Pub"].Owner = "Pgy_Owner"
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("matching owner must not diff:\n%s", sql)
	}
	d.Publications["Pub"].Owner = ""
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("unset owner is unmanaged:\n%s", sql)
	}
	d.Publications["Pub"].Owner = "Pgy_Owner"
	sim := emptyLive()
	sim.CurrentUser = "pgy"
	if sql := Render(Plan(Simulate(sim, d), d, false)); sql != "" {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}
