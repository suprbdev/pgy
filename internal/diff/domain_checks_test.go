package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func domainChecksDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, Domains: map[string]*schema.Domain{
		"app.score": {Name: "score", Type: "int", Checks: map[string]string{"score_min": "VALUE >= 0", "score_max": "VALUE <= 100"}},
	}}
}

func TestDomainChecksCreate(t *testing.T) {
	sql := Render(Plan(emptyLive(), domainChecksDB(), false))
	want := `create domain "app"."score" as int constraint "score_max" check (VALUE <= 100) constraint "score_min" check (VALUE >= 0);`
	if !strings.Contains(sql, want) {
		t.Errorf("missing %q in\n%s", want, sql)
	}
}

func TestDomainChecksAlter(t *testing.T) {
	live := Simulate(emptyLive(), domainChecksDB())
	if sql := Render(Plan(live, domainChecksDB(), true)); sql != "" {
		t.Fatalf("simulated state must match, got:\n%s", sql)
	}
	live.DomainDefs["app.score"].Checks["score_legacy"] = "CHECK (VALUE <> 42)"
	d := domainChecksDB()
	d.Domains["app.score"].Checks["score_max"] = "VALUE <= 10"
	d.Domains["app.score"].Checks["score_odd"] = "VALUE % 2 = 1"
	p := Plan(live, d, false)
	if !findAlter(p, `alter domain "app"."score" add constraint "score_odd" check (VALUE % 2 = 1);`) {
		t.Errorf("a new check must be added without --unsafe: %v", p.Alters)
	}
	if len(p.Alters) != 1 {
		t.Errorf("changes and drops need --unsafe, got %v", p.Alters)
	}
	p = Plan(live, d, true)
	for _, want := range []string{
		`alter domain "app"."score" drop constraint "score_legacy";`,
		`alter domain "app"."score" drop constraint "score_max";`,
		`alter domain "app"."score" add constraint "score_max" check (VALUE <= 10);`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
	single := &schema.Database{Tables: map[string]*schema.Table{}, Domains: map[string]*schema.Domain{"app.score": {Name: "score", Type: "int", Check: "VALUE >= 0", ConstraintName: "score_min"}}}
	for _, s := range Plan(live, single, true).Alters {
		if strings.Contains(s, "drop constraint") {
			t.Errorf("without checks, only the single check's name is managed: %s", s)
		}
	}
}
