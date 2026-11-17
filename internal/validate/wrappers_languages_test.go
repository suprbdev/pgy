package validate

import (
	"strings"
	"testing"
)

func TestValidateForeignDataWrappersAndLanguagesPullWrites(t *testing.T) {
	p := write(t, t.TempDir(), "database.yml", `
extensions:
  - {name: postgres_fdw, ifNotExists: true, schema: public}
  - {name: plperl, ifNotExists: true, schema: pg_catalog}
languages:
  plpgsql:
    grants: {app_dev: [usage], public: []}
  plperl:
    grants: {app_dev: [usage with grant option]}
foreignDataWrappers:
  legacy:
    options: {debug: "true"}
  pg2:
    handler: public.postgres_fdw_handler
    validator: public.postgres_fdw_validator
    options: {}
    owner: admin
    grants: {app_owner: [usage]}
    comment: second wrapper
  postgres_fdw:
    grants: {app_owner: [usage]}
servers:
  on_pg2: {wrapper: pg2, options: {}}
`)
	if issues := Files([]string{p}); len(issues) != 0 {
		t.Errorf("expected no findings, got:\n%s", messages(issues))
	}
}

func TestValidateForeignDataWrapperOfExtensionTakesNoHandler(t *testing.T) {
	p := write(t, t.TempDir(), "wrappers.yml", `
extensions: [mysql_fdw, hstore]
foreignDataWrappers:
  postgres_fdw: {handler: postgres_fdw_handler}
  file_fdw: {validator: public.file_fdw_validator}
  dblink_fdw: {validator: dblink_fdw_validator, comment: links}
  mysql_fdw: {handler: mysql_fdw_handler, validator: mysql_fdw_validator}
  oracle_fdw: {handler: oracle_fdw_handler}
  mine: {handler: postgres_fdw_handler, validator: postgres_fdw_validator}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"error: foreign-data wrapper postgres_fdw: extension postgres_fdw creates this wrapper with its own handler and validator",
		"error: foreign-data wrapper file_fdw: extension file_fdw creates this wrapper",
		"error: foreign-data wrapper dblink_fdw: extension dblink creates this wrapper",
		"error: foreign-data wrapper mysql_fdw: extension mysql_fdw creates this wrapper",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, name := range []string{"oracle_fdw", "mine"} {
		if strings.Contains(got, "foreign-data wrapper "+name) {
			t.Errorf("a wrapper no declared extension creates may have a handler, got:\n%s", got)
		}
	}
}

func TestValidateForeignDataWrapperRenames(t *testing.T) {
	p := write(t, t.TempDir(), "wrappers.yml", `
foreignDataWrappers:
  old: {}
  new: {renamedFrom: old}
  a: {renamedFrom: gone}
  b: {renamedFrom: gone}
  pg: {renamedFrom: postgres_fdw}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"foreign-data wrapper new: renamedFrom old names an object the YAML also declares",
		"renamedFrom gone is also claimed by foreign-data wrapper",
		"foreign-data wrapper pg: renamedFrom postgres_fdw names the wrapper of extension postgres_fdw",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestValidateLanguageMustBeTrusted(t *testing.T) {
	p := write(t, t.TempDir(), "languages.yml", `
languages:
  c: {grants: {app_dev: [usage]}}
  plpython3u: {grants: {app_dev: [usage]}}
  plpgsql: {grants: {app_dev: [usage]}}
  sql: {grants: {public: []}}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"error: language c: is not a trusted language",
		"error: language plpython3u: is not a trusted language",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "language plpgsql") || strings.Contains(got, "language sql") {
		t.Errorf("trusted languages take privileges, got:\n%s", got)
	}
}

func TestValidateLanguageNeedsGrants(t *testing.T) {
	p := write(t, t.TempDir(), "languages.yml", "languages:\n  plpgsql: {}\n")
	if got := messages(Files([]string{p})); !strings.Contains(got, "grants") {
		t.Errorf("a languages entry without grants declares nothing, got:\n%s", got)
	}
}

func TestValidateWrapperAndLanguagePrivilegesAreUsage(t *testing.T) {
	p := write(t, t.TempDir(), "grants.yml", `
languages:
  plpgsql: {grants: {app_dev: [all], lead: [usage with grant option], public: []}}
foreignDataWrappers:
  pg2: {grants: {app_owner: [select], lead: [USAGE]}}
`)
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"error: language plpgsql: grants all to app_dev; write usage",
		"error: foreign-data wrapper pg2: grants select to app_owner; write usage",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "lead") || strings.Contains(got, "public") {
		t.Errorf("usage, with or without the grant option, and an empty list are fine, got:\n%s", got)
	}
}
