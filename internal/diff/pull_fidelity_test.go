package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func linkedSequenceDB(ownedBy string) *schema.Database {
	return &schema.Database{
		Tables:    map[string]*schema.Table{"app.ticket": {Name: "ticket", Columns: map[string]*schema.Column{"id": {Type: "int"}}, Owner: "app_owner"}},
		Sequences: map[string]*schema.Sequence{"app.seq_ticket": {Schema: "app", Name: "seq_ticket", Start: "1000", OwnedBy: ownedBy, Owner: "app_owner"}},
	}
}

func TestSequenceOwnerSkippedWhenOwnedBy(t *testing.T) {
	p := Plan(emptyLive(), linkedSequenceDB("app.ticket.id"), false)
	if findAlter(p, `alter sequence "app"."seq_ticket" owner to`) {
		t.Errorf("a sequence owned by a column follows its table's owner, got %v", p.Alters)
	}
	if !findAlter(p, `alter table "app"."ticket" owner to "app_owner";`) || !findAlter(p, `alter sequence "app"."seq_ticket" owned by "app"."ticket"."id";`) {
		t.Errorf("missing table owner or OWNED BY in %v", p.Alters)
	}
}

func TestSequenceOwnerSkippedWhenLiveLinked(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Sequences["app.seq_ticket"] = true
	live.SequenceDefs = map[string]*LiveSequence{"app.seq_ticket": {Type: "bigint", Increment: "1", MinValue: "1", MaxValue: "9223372036854775807", Start: "1000", Cache: "1", Owner: "app.ticket.id"}}
	live.Owners = map[string]string{"rel:app.seq_ticket": "postgres"}
	p := Plan(live, linkedSequenceDB(""), false)
	if findAlter(p, `alter sequence "app"."seq_ticket" owner to`) {
		t.Errorf("a live sequence owned by a column follows its table's owner, got %v", p.Alters)
	}
}

func TestSequenceOwnerAppliedWhenOwnedByNone(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Sequences["app.seq_ticket"] = true
	live.SequenceDefs = map[string]*LiveSequence{"app.seq_ticket": {Type: "bigint", Increment: "1", MinValue: "1", MaxValue: "9223372036854775807", Start: "1000", Cache: "1", Owner: "app.ticket.id"}}
	live.Owners = map[string]string{"rel:app.seq_ticket": "postgres"}
	p := Plan(live, linkedSequenceDB("none"), false)
	unlink, owner := -1, -1
	for i, s := range p.Alters {
		switch s {
		case `alter sequence "app"."seq_ticket" owned by none;`:
			unlink = i
		case `alter sequence "app"."seq_ticket" owner to "app_owner";`:
			owner = i
		}
	}
	if unlink < 0 || owner < unlink {
		t.Errorf("an unlinked sequence takes its owner after OWNED BY NONE, got %v", p.Alters)
	}
}

func notValidDomainDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, Domains: map[string]*schema.Domain{
		"ty.pct": {Name: "pct", Type: "numeric(5,2)", Checks: map[string]string{"pct_min": "VALUE >= 0", "pct_not_99": "VALUE <> 99"}, CheckNotValid: map[string]bool{"pct_not_99": true}},
	}}
}

func TestDomainCheckNotValidCreate(t *testing.T) {
	p := Plan(emptyLive(), notValidDomainDB(), false)
	create := -1
	for i, s := range p.Creates {
		if strings.HasPrefix(s, `create domain "ty"."pct"`) {
			create = i
			if strings.Contains(s, "pct_not_99") {
				t.Errorf("a NOT VALID check can't be part of CREATE DOMAIN: %s", s)
			}
			if !strings.Contains(s, `constraint "pct_min" check (VALUE >= 0)`) {
				t.Errorf("the valid check belongs to CREATE DOMAIN: %s", s)
			}
		}
	}
	want := `alter domain "ty"."pct" add constraint "pct_not_99" check (VALUE <> 99) not valid;`
	if create < 0 || create+1 >= len(p.Creates) || p.Creates[create+1] != want {
		t.Errorf("expected %q right after CREATE DOMAIN in %v", want, p.Creates)
	}
}

func TestDomainCheckNotValidAdd(t *testing.T) {
	live := Simulate(emptyLive(), notValidDomainDB())
	if sql := Render(Plan(live, notValidDomainDB(), true)); sql != "" {
		t.Fatalf("simulated state must match, got:\n%s", sql)
	}
	delete(live.DomainDefs["ty.pct"].Checks, "pct_not_99")
	p := Plan(live, notValidDomainDB(), false)
	if !findAlter(p, `alter domain "ty"."pct" add constraint "pct_not_99" check (VALUE <> 99) not valid;`) {
		t.Errorf("a missing notValid check is added NOT VALID: %v", p.Alters)
	}
}

func TestDomainCheckNotValidSkippedIfExists(t *testing.T) {
	live := Simulate(emptyLive(), notValidDomainDB())
	live.DomainDefs["ty.pct"].Checks["pct_not_99"] = "CHECK ((VALUE <> (99)::numeric)) NOT VALID"
	if sql := Render(Plan(live, notValidDomainDB(), true)); sql != "" {
		t.Errorf("a live NOT VALID check matching the YAML must not diff, got:\n%s", sql)
	}
	live.DomainDefs["ty.pct"].Checks["pct_not_99"] = "CHECK ((VALUE <> (99)::numeric))"
	if sql := Render(Plan(live, notValidDomainDB(), true)); sql != "" {
		t.Errorf("a check validated outside pgy while the YAML says notValid must not diff, got:\n%s", sql)
	}
}

func TestDomainCheckNotValidValidated(t *testing.T) {
	live := Simulate(emptyLive(), notValidDomainDB())
	live.DomainDefs["ty.pct"].Checks["pct_not_99"] = "CHECK ((VALUE <> (99)::numeric)) NOT VALID"
	d := notValidDomainDB()
	d.Domains["ty.pct"].CheckNotValid = nil
	p := Plan(live, d, false)
	if len(p.Alters) != 1 || p.Alters[0] != `alter domain "ty"."pct" validate constraint "pct_not_99";` {
		t.Errorf("removing notValid validates the check, got %v", p.Alters)
	}
}

func TestDomainCheckNotValidReplaced(t *testing.T) {
	live := Simulate(emptyLive(), notValidDomainDB())
	d := notValidDomainDB()
	d.Domains["ty.pct"].Checks["pct_not_99"] = "VALUE <> 98"
	p := Plan(live, d, true)
	for _, want := range []string{
		`alter domain "ty"."pct" drop constraint "pct_not_99";`,
		`alter domain "ty"."pct" add constraint "pct_not_99" check (VALUE <> 98) not valid;`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
}

func largeObjectDefaultsDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, DefaultPrivileges: []*schema.DefaultPrivilege{
		{ForRole: "app_owner", Grants: map[string]map[string][]string{"largeObjects": {"app_read": {"select"}, "app_rw": {"all"}}}},
	}}
}

func TestDefaultPrivilegesLargeObjects(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	live.DefaultACLs = map[string]map[string]map[string]bool{"app_owner||largeObjects": {"app_read": {"update": true}}}
	live.DefaultACLRows = map[string]bool{"app_owner||largeObjects": true}
	p := Plan(live, largeObjectDefaultsDB(), false)
	for _, want := range []string{
		`alter default privileges for role "app_owner" revoke update on large objects from "app_read";`,
		`alter default privileges for role "app_owner" grant select on large objects to "app_read";`,
		`alter default privileges for role "app_owner" grant select, update on large objects to "app_rw";`,
	} {
		if !findCreate(p, want) {
			t.Errorf("missing %q in %v", want, p.Creates)
		}
	}
	for _, w := range p.Warnings {
		if strings.Contains(w.Message, "largeObjects") {
			t.Errorf("PostgreSQL 18 supports largeObjects: %s", w.Message)
		}
	}
	live.DefaultACLs = map[string]map[string]map[string]bool{"app_owner||largeObjects": {"app_read": {"select": true}, "app_rw": {"select": true, "update": true}}}
	if sql := Render(Plan(live, largeObjectDefaultsDB(), false)); sql != "" {
		t.Errorf("matching large object defaults must not diff, got:\n%s", sql)
	}
}

func TestDefaultPrivilegesLargeObjectsVersionWarning(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 170000
	p := Plan(live, largeObjectDefaultsDB(), false)
	warned := false
	for _, w := range p.Warnings {
		warned = warned || strings.Contains(w.Message, "largeObjects in defaultPrivileges[0] needs PostgreSQL 18")
	}
	if !warned {
		t.Errorf("largeObjects before PostgreSQL 18 must warn, got %v", p.Warnings)
	}
}

func matViewColumnsDB() *schema.Database {
	stats, nd := 500, 10.0
	return &schema.Database{Tables: map[string]*schema.Table{}, Views: map[string]*schema.View{
		"cz.mv": {Name: "mv", Materialized: true, Query: "select * from cz.t", Columns: map[string]*schema.Column{
			"a": {Compression: "lz4"},
			"b": {Storage: "main"},
			"c": {StatisticsTarget: &stats, NDistinct: &nd},
		}},
	}}
}

func TestMatViewColumnSettingsCreate(t *testing.T) {
	p := Plan(emptyLive(), matViewColumnsDB(), false)
	if !findCreate(p, `create materialized view if not exists "cz"."mv"`) {
		t.Fatalf("missing create in %v", p.Creates)
	}
	for _, want := range []string{
		`alter materialized view "cz"."mv" alter column "a" set compression lz4;`,
		`alter materialized view "cz"."mv" alter column "b" set storage main;`,
		`alter materialized view "cz"."mv" alter column "c" set statistics 500;`,
		`alter materialized view "cz"."mv" alter column "c" set (n_distinct = 10);`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
}

func TestMatViewColumnSettingsSkippedIfSame(t *testing.T) {
	live := Simulate(emptyLive(), matViewColumnsDB())
	if sql := Render(Plan(live, matViewColumnsDB(), false)); sql != "" {
		t.Errorf("simulated state must match, got:\n%s", sql)
	}
	stats := 500
	live = emptyLive()
	live.Schemas["cz"] = true
	live.MatViews["cz.mv"] = true
	live.MatViewTables = map[string]*LiveTable{"cz.mv": {Columns: map[string]*LiveColumn{
		"a": {Compression: "lz4", Storage: "extended", TypeStorage: "extended"},
		"b": {Storage: "main", TypeStorage: "extended"},
		"c": {Storage: "plain", TypeStorage: "plain", StatisticsTarget: &stats, Options: map[string]string{"n_distinct": "10"}},
	}}}
	if sql := Render(Plan(live, matViewColumnsDB(), false)); sql != "" {
		t.Errorf("matching column settings must not diff, got:\n%s", sql)
	}
}

func TestMatViewColumnSettingsAltered(t *testing.T) {
	live := emptyLive()
	live.Schemas["cz"] = true
	live.MatViews["cz.mv"] = true
	live.MatViewTables = map[string]*LiveTable{"cz.mv": {Columns: map[string]*LiveColumn{
		"a": {Compression: "pglz", Storage: "extended", TypeStorage: "extended"},
		"b": {Storage: "extended", TypeStorage: "extended"},
		"c": {Storage: "plain", TypeStorage: "plain", Options: map[string]string{"n_distinct": "5"}},
		"d": {Compression: "lz4"},
	}}}
	d := matViewColumnsDB()
	d.Views["cz.mv"].Columns["a"].Compression = "default"
	p := Plan(live, d, false)
	for _, want := range []string{
		`alter materialized view "cz"."mv" alter column "a" set compression default;`,
		`alter materialized view "cz"."mv" alter column "b" set storage main;`,
		`alter materialized view "cz"."mv" alter column "c" set statistics 500;`,
		`alter materialized view "cz"."mv" alter column "c" set (n_distinct = 10);`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
	if findAlter(p, `alter column "d"`) {
		t.Errorf("a column the YAML doesn't list is unmanaged: %v", p.Alters)
	}
}

func TestMatViewColumnSettingsRestoredOnRecreate(t *testing.T) {
	stats := 500
	live := emptyLive()
	live.MatViews["cz.mv"] = true
	live.MatViewTables = map[string]*LiveTable{"cz.mv": {Columns: map[string]*LiveColumn{
		"a": {Compression: "lz4", Storage: "main", TypeStorage: "extended"},
		"c": {Storage: "plain", TypeStorage: "plain", StatisticsTarget: &stats, Options: map[string]string{"n_distinct": "10"}},
		"d": {Storage: "extended", TypeStorage: "extended"},
	}}}
	vw := &schema.View{Name: "mv", Materialized: true, Query: "select * from cz.t", Columns: map[string]*schema.Column{"a": {Compression: "pglz"}}}
	got := strings.Join(restoreView(live, vw, "cz.mv", false), "\n")
	for _, want := range []string{
		`alter materialized view "cz"."mv" alter column "a" set storage main;`,
		`alter materialized view "cz"."mv" alter column "c" set statistics 500;`,
		`alter materialized view "cz"."mv" alter column "c" set (n_distinct = 10);`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "compression") || strings.Contains(got, `column "d"`) {
		t.Errorf("declared settings and defaults are not restored:\n%s", got)
	}
}
