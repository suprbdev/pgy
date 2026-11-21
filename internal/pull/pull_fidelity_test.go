package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func buildFidelity(t *testing.T, live *diff.Live, opts Options) (string, []string) {
	t.Helper()
	opts.Single = true
	res, err := Build(live, opts)
	if err != nil {
		t.Fatal(err)
	}
	return string(res.Files[0].Content), res.Warnings
}

func TestBuildSequenceOwnedByWithoutOwner(t *testing.T) {
	seq := func(owner string) *diff.LiveSequence {
		return &diff.LiveSequence{Type: "bigint", Increment: "1", MinValue: "1", MaxValue: "9223372036854775807", Start: "1000", Cache: "1", Owner: owner}
	}
	live := &diff.Live{
		CurrentUser:  "pgy",
		Schemas:      map[string]bool{"app": true},
		Tables:       map[string]*diff.LiveTable{"app.ticket": {Columns: map[string]*diff.LiveColumn{"id": {Type: "integer"}}}},
		Sequences:    map[string]bool{"app.seq_ticket": true, "app.seq_free": true},
		SequenceDefs: map[string]*diff.LiveSequence{"app.seq_ticket": seq("app.ticket.id"), "app.seq_free": seq("")},
		Owners:       map[string]string{"rel:app.ticket": "app_owner", "rel:app.seq_ticket": "app_owner", "rel:app.seq_free": "app_owner"},
		Details:      &diff.LiveDetails{ColumnOrder: map[string][]string{"app.ticket": {"id"}}},
	}
	got, _ := buildFidelity(t, live, Options{Owners: true})
	linked := got[strings.Index(got, "sequence seq_ticket:"):strings.Index(got, "table ticket:")]
	if strings.Contains(linked, "owner:") || !strings.Contains(linked, "ownedBy: app.ticket.id") {
		t.Errorf("a sequence owned by a column takes its table's owner, so pull writes none:\n%s", got)
	}
	free := got[strings.Index(got, "sequence seq_free:"):strings.Index(got, "sequence seq_ticket:")]
	if !strings.Contains(free, "owner: app_owner") {
		t.Errorf("a sequence without ownedBy keeps its owner:\n%s", got)
	}
}

func TestBuildDomainCheckNotValid(t *testing.T) {
	live := &diff.Live{
		Schemas: map[string]bool{"ty": true},
		Domains: map[string]bool{"ty.pct": true, "ty.one": true},
		DomainDefs: map[string]*diff.LiveDomain{
			"ty.pct": {Checks: map[string]string{"pct_check": "CHECK ((VALUE >= (0)::numeric))", "pct_not_99": "CHECK ((VALUE <> (99)::numeric)) NOT VALID"}},
			"ty.one": {Checks: map[string]string{"one_check": "CHECK ((VALUE > 0)) NOT VALID"}},
		},
		Details: &diff.LiveDetails{DomainTypes: map[string]diff.LiveDomainType{"ty.pct": {Type: "numeric(5,2)"}, "ty.one": {Type: "integer"}}},
	}
	got, _ := buildFidelity(t, live, Options{})
	for _, want := range []string{
		`pct_check: VALUE >= (0)::numeric`,
		`pct_not_99: {check: 'VALUE <> (99)::numeric', notValid: true}`,
		`one_check: {check: VALUE > 0, notValid: true}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "NOT VALID") {
		t.Errorf("NOT VALID must not stay in the expression:\n%s", got)
	}
}

func TestBuildDefaultPrivilegesLargeObjects(t *testing.T) {
	live := &diff.Live{
		Schemas:        map[string]bool{"app": true},
		DefaultACLs:    map[string]map[string]map[string]bool{"app_owner||largeObjects": {"app_read": {"select": true}}},
		DefaultACLRows: map[string]bool{"app_owner||largeObjects": true},
		Details:        &diff.LiveDetails{},
	}
	got, _ := buildFidelity(t, live, Options{})
	if !strings.Contains(got, "largeObjects: {app_read: [select]}") {
		t.Errorf("large object defaults must be pulled as largeObjects:\n%s", got)
	}
}

func TestBuildMatViewColumnSettings(t *testing.T) {
	stats := 500
	live := &diff.Live{
		Schemas:  map[string]bool{"cz": true},
		MatViews: map[string]bool{"cz.mv": true},
		MatViewTables: map[string]*diff.LiveTable{"cz.mv": {Columns: map[string]*diff.LiveColumn{
			"a": {Compression: "lz4", Storage: "extended", TypeStorage: "extended"},
			"b": {Storage: "main", TypeStorage: "extended"},
			"c": {Storage: "plain", TypeStorage: "plain", StatisticsTarget: &stats, Options: map[string]string{"n_distinct": "10"}},
			"d": {Storage: "extended", TypeStorage: "extended"},
		}}},
		Details: &diff.LiveDetails{ViewQueries: map[string]string{"cz.mv": " SELECT 1"}, Populated: map[string]bool{"cz.mv": true}},
	}
	got, _ := buildFidelity(t, live, Options{})
	for _, want := range []string{"a: {compression: lz4}", "b: {storage: main}", "c: {statisticsTarget: 500, nDistinct: 10}"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "d:") {
		t.Errorf("a column with the default settings is left out:\n%s", got)
	}
}

func TestBuildUserMappingWarns(t *testing.T) {
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Details: &diff.LiveDetails{Unsupported: []diff.UnsupportedObject{{Kind: "user mapping", Name: "for pgy server loop", Owner: "pgy"}}},
	}
	_, warnings := buildFidelity(t, live, Options{})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "user mapping for pgy server loop was left out") {
		t.Errorf("a user mapping must be reported, got %v", warnings)
	}
	_, warnings = buildFidelity(t, live, Options{External: &schema.External{Roles: []string{"pgy"}}})
	if len(warnings) != 0 {
		t.Errorf("a user mapping of an external role's server is not reported, got %v", warnings)
	}
}
