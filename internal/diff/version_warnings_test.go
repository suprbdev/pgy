package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestVersionWarnings(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{
		"app.t": {Name: "t", Columns: map[string]*schema.Column{
			"a": {Type: "int"},
			"v": {Type: "int", Nullable: true, Generated: "a * 2", Virtual: true},
		}, ForeignKeys: []*schema.ForeignKey{{Name: "t_p", Columns: []string{"a"}, RefTable: "app.p", RefColumns: []string{"id"}, NotEnforced: true}}},
	}, Collations: map[string]*schema.Collation{"app.c": {Name: "c", Provider: "icu", Locale: "und", Rules: "&a < b"}}}
	live := emptyLive()
	live.ServerVersion = 150004
	got := []string{}
	for _, w := range Plan(live, d, false).Warnings {
		got = append(got, w.Message)
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{
		"virtual generated column app.t.v needs PostgreSQL 18, and the server runs PostgreSQL 15",
		"foreign key t_p on app.t (enforced: false) needs PostgreSQL 18",
		"collation app.c's rules needs PostgreSQL 16",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	live.ServerVersion = 180001
	for _, w := range Plan(live, d, false).Warnings {
		if strings.Contains(w.Message, "needs PostgreSQL") {
			t.Errorf("PostgreSQL 18 supports them all: %s", w.Message)
		}
	}
}
