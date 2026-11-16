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

func TestValidateKeyColumnsAndPolicies(t *testing.T) {
	path := write(t, t.TempDir(), "k.yml", `
schema app:
  table t:
    columns: {a: {type: int}, b: {type: int}}
    primaryKey: [a, a]
    constraints:
      t_b: {type: unique, columns: [b, b]}
    policies:
      p: {for: select, using: "true"}
  table rls:
    columns: {a: {type: int, primaryKey: true}}
    rowLevelSecurity: true
    policies:
      p: {for: select, using: "true"}
`)
	got := messages(Files([]string{path}))
	for _, want := range []string{
		`table app.t: primary key lists column "a" twice`,
		`table app.t: constraint t_b lists column "b" twice`,
		"table app.t: declares policies but not rowLevelSecurity: true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "table app.rls:") {
		t.Errorf("a table with RLS enabled must not be flagged:\n%s", got)
	}
}

func TestValidateForeignKeyTargets(t *testing.T) {
	path := write(t, t.TempDir(), "fk.yml", `
schema app:
  table orgs:
    columns:
      id: {type: int, primaryKey: true}
      code: {type: varchar(10), unique: true}
      slug: {type: text}
      name: {type: text}
      region: {type: text}
    indexes:
      orgs_slug: {columns: [slug], unique: true}
      orgs_name_partial: {columns: [name], unique: true, where: "id > 0"}
    constraints:
      orgs_region_name: {type: unique, columns: [region, name]}
  table users:
    columns:
      id: {type: int, primaryKey: true}
      org_id: {type: bigint}
      org_code: {type: text}
      org_slug: {type: text}
      org_name: {type: text}
      org_region: {type: text}
      org_uuid: {type: uuid}
    foreignKeys:
      by_id: {columns: [org_id], references: {table: app.orgs, columns: [id]}}
      by_code: {columns: [org_code], references: {table: app.orgs, columns: [code]}}
      by_slug: {columns: [org_slug], references: {table: app.orgs, columns: [slug]}}
      by_pair: {columns: [org_name, org_region], references: {table: app.orgs, columns: [name, region]}}
      by_name: {columns: [org_name], references: {table: app.orgs, columns: [name]}}
      by_uuid: {columns: [org_uuid], references: {table: app.orgs, columns: [id]}}
`)
	got := messages(Files([]string{path}))
	for _, want := range []string{
		"foreign key by_name references app.orgs (name), which has no primary key or unique constraint on exactly those columns",
		"foreign key by_uuid: column org_uuid is uuid but references app.orgs.id, which is int",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, fine := range []string{"by_id", "by_code", "by_slug", "by_pair"} {
		if strings.Contains(got, "foreign key "+fine+" ") || strings.Contains(got, "foreign key "+fine+":") {
			t.Errorf("%s is valid but was flagged:\n%s", fine, got)
		}
	}
}

func TestValidateNameClashes(t *testing.T) {
	path := write(t, t.TempDir(), "n.yml", `
schema app:
  type status:
    type: enum
    labels: [a]
  table status:
    columns: {id: {type: int, primaryKey: true}}
  table a:
    columns: {id: {type: int, primaryKey: true}, x: {type: int}}
    indexes: {a_x: {columns: [x]}}
    constraints: {Shared: {type: unique, columns: [x]}}
  table b:
    columns: {id: {type: int, primaryKey: true}, x: {type: int}}
    indexes: {a_x: {columns: [x]}, shared: {columns: [x]}}
  sequence b:
`)
	got := messages(Files([]string{path}))
	for _, want := range []string{
		"type app.status: has the same name as table app.status",
		"index a_x on app.b: has the same name as index a_x on app.a",
		"sequence app.b: has the same name as table app.b",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "shared on app.b: has the same name") {
		t.Errorf("names are case-sensitive, so shared and Shared don't clash:\n%s", got)
	}
}

func TestValidateNameLengths(t *testing.T) {
	long := strings.Repeat("x", 64)
	ok := strings.Repeat("y", 63)
	path := write(t, t.TempDir(), "l.yml", `
schema app:
  table `+ok+`:
    columns: {id: {type: int, primaryKey: true}, `+long+`: {type: int}}
    indexes: {`+long+`: {columns: [id]}}
  type mood:
    type: enum
    labels: [`+long+`]
  function `+long+`():
    returns: int
    language: sql
    body: select 1
`)
	got := messages(Files([]string{path}))
	for _, want := range []string{`column "` + long + `" is 64 bytes`, `index "` + long + `" is 64 bytes`, `enum label "` + long + `" is 64 bytes`, `function "` + long + `" is 64 bytes`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, `table "`+ok+`"`) {
		t.Errorf("a 63-byte name is fine:\n%s", got)
	}
}

func TestValidateDDLTimeErrors(t *testing.T) {
	path := write(t, t.TempDir(), "d.yml", `
eventTriggers:
  log: {event: ddl_command_end, function: app.not_event()}
schema app:
  function not_trigger():
    returns: int
    language: sql
    body: select 1
  function not_event():
    returns: trigger
    language: plpgsql
    body: begin return null; end
  function touch():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table t:
    columns:
      id: {type: bigint, primaryKey: true, identity: always}
      code: {type: text, identity: always}
    rowLevelSecurity: true
    triggers:
      bad: {timing: before, events: [update], procedure: app.not_trigger(), level: row}
      good: {timing: before, events: [update], procedure: app.touch(), level: row}
    policies:
      ins: {for: insert, using: "true"}
      del: {for: delete, withCheck: "true"}
      upd: {for: update, using: "true", withCheck: "true"}
      ins_ok: {for: insert, withCheck: "true"}
`)
	got := messages(Files([]string{path}))
	for _, want := range []string{
		"trigger bad calls app.not_trigger, which returns int",
		"event trigger log: function app.not_event() returns trigger",
		"policy ins: an insert policy can only have withCheck",
		"policy del: a delete policy can only have using",
		"column code: identity columns must be smallint, integer or bigint, not text",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, fine := range []string{"trigger good", "policy upd", "policy ins_ok", "column id:"} {
		if strings.Contains(got, fine) {
			t.Errorf("%s is valid but was flagged:\n%s", fine, got)
		}
	}
}
