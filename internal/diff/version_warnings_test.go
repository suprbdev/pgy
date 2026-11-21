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
		}, ForeignKeys: []*schema.ForeignKey{{Name: "t_p", Columns: []string{"a"}, RefTable: "app.p", RefColumns: []string{"id"}, NotEnforced: true}},
			Grants: map[string][]string{"ops": {"select", "maintain"}}},
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
		"the maintain privilege in table app.t's grants needs PostgreSQL 17",
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

func TestVersionWarningsPostgres19(t *testing.T) {
	d := supportAggregate("app.total_support")
	d.Aggregates["app.plain(int8)"] = &schema.Aggregate{Schema: "app", Name: "plain", ArgsSig: "(int8)", SFunc: "int8pl", SType: "int8", Options: map[string]string{"support": ""}}
	d.ForeignDataWrappers = map[string]*schema.ForeignDataWrapper{
		"pg2":  {Name: "pg2", Handler: "postgres_fdw_handler", Connection: "postgres_fdw_connection"},
		"none": {Name: "none", ConnectionSet: true},
	}
	d.Tables = map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"a": {Type: "int", NotNullName: "t_a_nn"}}}}
	for _, v := range []int{150004, 180001} {
		live := emptyLive()
		live.ServerVersion = v
		got := []string{}
		for _, w := range Plan(live, d, false).Warnings {
			got = append(got, w.Message)
		}
		all := strings.Join(got, "\n")
		for _, want := range []string{
			"the support function of aggregate app.total(int8) needs PostgreSQL 19",
			"the connection function of foreign-data wrapper pg2 needs PostgreSQL 19",
		} {
			if !strings.Contains(all, want) {
				t.Errorf("%d: missing %q in:\n%s", v, want, all)
			}
		}
		for _, unwanted := range []string{"aggregate app.plain", "wrapper none"} {
			if strings.Contains(all, unwanted) {
				t.Errorf("%d: an empty value works on every version: %s", v, all)
			}
		}
		if notNull := strings.Contains(all, "notNullName on column app.t.a is ignored"); notNull != (v < 180000) {
			t.Errorf("%d: the notNullName warning belongs to servers before 18 only:\n%s", v, all)
		}
	}
	live := emptyLive()
	live.ServerVersion = 190000
	for _, w := range Plan(live, d, false).Warnings {
		if strings.Contains(w.Message, "needs PostgreSQL") {
			t.Errorf("PostgreSQL 19 supports them all: %s", w.Message)
		}
	}
}
