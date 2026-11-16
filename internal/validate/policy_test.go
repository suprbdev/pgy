package validate

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func policyDB(t *testing.T, y string) *schema.Database {
	t.Helper()
	d, err := schema.LoadAndMerge([]string{write(t, t.TempDir(), "p.yml", y)})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPolicyRules(t *testing.T) {
	d := policyDB(t, `
schema app:
  table open:
    columns: {id: {type: int}}
  table locked:
    columns: {id: {type: int}}
    owner: app_owner
    grants: {reader: [select]}
    rowLevelSecurity: true
    policies: {p: {for: select, using: "true"}}
  table rls_no_policy:
    columns: {id: {type: int}}
    rowLevelSecurity: true
  function risky():
    returns: int
    language: sql
    security: definer
    body: select 1
  function safe():
    returns: int
    language: sql
    security: definer
    set: {search_path: app}
    revokePublic: true
    body: select 1
  function safe_grants():
    returns: int
    language: sql
    security: definer
    set: {search_path: app}
    grants: {public: [], reader: [execute]}
    body: select 1
`)
	p := Policy{
		SecurityDefinerHardened: true,
		RequireGrants:           map[string]bool{"tables": true},
		RequireOwners:           map[string]bool{"tables": true},
		RowLevelSecurity:        map[string]bool{"app": true},
	}
	got := messages(CheckPolicy(d, p))
	for _, want := range []string{
		"table app.open: policy: tables must declare a grants block",
		"table app.open: policy: tables must declare an owner",
		"table app.open: policy: tables in schema app must enable row-level security",
		"table app.rls_no_policy: policy: tables in schema app must declare at least one policy",
		"function app.risky(): policy: SECURITY DEFINER routines must set search_path",
		"function app.risky(): policy: SECURITY DEFINER routines must revoke PUBLIC's EXECUTE",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, clean := range []string{"app.locked", "app.safe()", "app.safe_grants()"} {
		if strings.Contains(got, clean+":") {
			t.Errorf("%s follows the policy but was flagged:\n%s", clean, got)
		}
	}
	if issues := CheckPolicy(d, Policy{}); len(issues) != 0 || !(Policy{}).Empty() {
		t.Errorf("an empty policy must not flag anything: %v", issues)
	}
}
