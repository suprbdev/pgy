package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// languageLive has the languages of a new database: plpgsql, which the
// plpgsql extension owns, and sql, both with their default privileges.
func languageLive() *Live {
	live := memberLive()
	live.ServerVersion = 180000
	live.MemberExtension = map[string]string{"language:plpgsql": "plpgsql"}
	live.ExtensionMembers["language:plpgsql"] = true
	live.Extensions["plpgsql"] = true
	live.Languages = map[string]*LiveLanguage{
		"plpgsql":  {Owner: "owner", Trusted: true, Grants: map[string]map[string]bool{"public": {"usage": true}}},
		"sql":      {Owner: "owner", Trusted: true, Grants: map[string]map[string]bool{"public": {"usage": true}}},
		"c":        {Owner: "owner", Grants: map[string]map[string]bool{"public": {"usage": true}}},
		"internal": {Owner: "owner", Grants: map[string]map[string]bool{"public": {"usage": true}}},
	}
	return live
}

func languageDB(grants map[string]map[string][]string) *schema.Database {
	d := &schema.Database{Languages: map[string]*schema.Language{}}
	for name, g := range grants {
		d.Languages[name] = &schema.Language{Name: name, Grants: g}
	}
	return d
}

func TestLanguageGrants(t *testing.T) {
	d := languageDB(map[string]map[string][]string{
		"plpgsql": {"public": {}, "app_dev": {"usage"}},
		"sql":     {"app_dev": {"usage*"}},
	})
	sql, warns := planSQL(t, languageLive(), d, Options{})
	wantSQL(t, sql,
		`revoke usage on language "plpgsql" from public;`,
		`grant usage on language "plpgsql" to "app_dev";`,
		`grant usage on language "sql" to "app_dev" with grant option;`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestLanguageGrantsSkippedIfExists(t *testing.T) {
	live := languageLive()
	live.Languages["plpgsql"].Grants = map[string]map[string]bool{"app_dev": {"usage": true}}
	live.Languages["sql"].Grants = map[string]map[string]bool{"app_dev": {"usage*": true}, "public": {"usage": true}}
	d := languageDB(map[string]map[string][]string{
		"plpgsql": {"public": {}, "app_dev": {"usage"}, "owner": {"usage"}},
		"sql":     {"app_dev": {"usage*"}},
	})
	if sql, warns := planSQL(t, live, d, pruneAll); sql != "" || len(warns) > 0 {
		t.Errorf("languages with the declared privileges must be skipped, got:\n%s%v", sql, warns)
	}
}

func TestLanguageGrantsLeavePublicAloneUnlessListed(t *testing.T) {
	live := languageLive()
	live.Languages["plpgsql"].Grants["stale"] = map[string]bool{"usage": true}
	live.Languages["plpgsql"].Grants["optioned"] = map[string]bool{"usage*": true}
	d := languageDB(map[string]map[string][]string{"plpgsql": {"app_dev": {"usage"}, "optioned": {"usage"}}})
	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`revoke grant option for usage on language "plpgsql" from "optioned";`,
		`revoke usage on language "plpgsql" from "stale";`,
		`grant usage on language "plpgsql" to "app_dev";`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}

	d.Languages["plpgsql"].Grants["public"] = []string{"usage"}
	if sql, _ := planSQL(t, live, d, Options{}); strings.Contains(sql, "public") {
		t.Errorf("public: [usage] keeps the default, got:\n%s", sql)
	}
	live.Languages["plpgsql"].Grants = map[string]map[string]bool{"app_dev": {"usage": true}, "optioned": {"usage": true}}
	sql, _ = planSQL(t, live, d, Options{})
	wantSQL(t, sql, `grant usage on language "plpgsql" to public;`)
}

func TestLanguageGrantsPublicAuthoritative(t *testing.T) {
	d := languageDB(map[string]map[string][]string{"sql": {"app_dev": {"usage"}}})
	sql, warns := planSQL(t, languageLive(), d, Options{PublicGrants: PublicAuthoritative})
	wantSQL(t, sql,
		`revoke usage on language "sql" from public;`,
		`grant usage on language "sql" to "app_dev";`,
	)
	if len(warns) != 1 || warns[0].Kind != "public_revoke" || !strings.Contains(warns[0].Message, "policy.public_grants: authoritative") {
		t.Errorf("want a warning about the PUBLIC revoke, got %v", warns)
	}
}

func TestLanguageGrantsOfExtensionLeftAloneUnlessNamed(t *testing.T) {
	live := languageLive()
	live.Languages["plperl"] = &LiveLanguage{Owner: "owner", Trusted: true, Grants: map[string]map[string]bool{"ext_role": {"usage": true}, "later": {"usage": true}}}
	live.ExtensionMembers["language:plperl"] = true
	live.InitPrivs["language:plperl"] = map[string]map[string]bool{"ext_role": {"usage": true}}
	d := languageDB(map[string]map[string][]string{"plperl": {"app_dev": {"usage"}}})
	sql, _ := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`revoke usage on language "plperl" from "later";`,
		`grant usage on language "plperl" to "app_dev";`,
	)
	d.Languages["plperl"].Grants["ext_role"] = []string{}
	if sql, _ := planSQL(t, live, d, Options{}); !strings.Contains(sql, `revoke usage on language "plperl" from "ext_role";`) {
		t.Errorf("naming a grantee of the extension makes it exact, got:\n%s", sql)
	}
}

func TestLanguageGrantsBeforeRoutines(t *testing.T) {
	d := languageDB(map[string]map[string][]string{"plperl": {"public": {}, "app_dev": {"usage"}, "nobody": {}}})
	d.Extensions = []*schema.Extension{{Name: "plperl"}}
	d.Roles = map[string]*schema.Role{"app_dev": {Name: "app_dev", ConnectionLimit: -1}}
	d.Functions = map[string]*schema.Function{"public.f()": {Schema: "public", Name: "f", ArgsSig: "()", Returns: "int", Language: "plperl", Body: "return 1;"}}
	sql, warns := planSQL(t, languageLive(), d, Options{})
	wantSQL(t, sql,
		`do $$ begin if not exists (select from pg_catalog.pg_roles where rolname = 'app_dev') then create role "app_dev"; end if; end $$;`,
		`create extension "plperl";`,
		`revoke all on language "plperl" from "nobody";`,
		`revoke all on language "plperl" from public;`,
		`grant usage on language "plperl" to "app_dev";`,
		`set check_function_bodies = off;`,
		`create function "public"."f"() returns int language plperl as $$return 1;$$;`,
		`reset check_function_bodies;`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestLanguageAbsentWarns(t *testing.T) {
	d := languageDB(map[string]map[string][]string{"plperl": {"app_dev": {"usage"}}})
	sql, warns := planSQL(t, languageLive(), d, Options{})
	if sql != "" {
		t.Errorf("nothing can be planned for a language that does not exist, got:\n%s", sql)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "language plperl is declared under languages") {
		t.Errorf("want one warning naming the declaration, got %v", warns)
	}

	d.Extensions = []*schema.Extension{{Name: "hstore"}}
	sql, warns = planSQL(t, languageLive(), d, Options{})
	wantSQL(t, sql, `create extension "hstore";`)
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "this plan creates no extension plperl") {
		t.Errorf("another extension of the plan does not create the language, got %v", warns)
	}

	live := languageLive()
	live.Extensions["plperl"] = true
	d.Extensions = []*schema.Extension{{Name: "plperl"}}
	if sql, warns := planSQL(t, live, d, Options{}); sql != "" || len(warns) != 1 {
		t.Errorf("an installed extension that brought no such language does not make it pending, got:\n%s%v", sql, warns)
	}
}

func TestLanguageUntrustedWarns(t *testing.T) {
	live := languageLive()
	live.Languages["plpython3u"] = &LiveLanguage{Owner: "owner"}
	d := languageDB(map[string]map[string][]string{"plpython3u": {"app_dev": {"usage"}}, "c": {"public": {}}})
	sql, warns := planSQL(t, live, d, Options{})
	if sql != "" {
		t.Errorf("PostgreSQL rejects GRANT on an untrusted language, got:\n%s", sql)
	}
	if len(warns) != 2 || !strings.Contains(warns[0].Message, "language c is not trusted") || !strings.Contains(warns[1].Message, "language plpython3u is not trusted") {
		t.Errorf("want a warning for each untrusted language, got %v", warns)
	}
}

func TestLanguageGrantsFromEmpty(t *testing.T) {
	live := &Live{Schemas: map[string]bool{}, Tables: map[string]*LiveTable{}, Types: map[string]bool{}, Functions: map[string]bool{}, Extensions: map[string]bool{}, Offline: true}
	d := languageDB(map[string]map[string][]string{
		"plpgsql": {"public": {}, "app_dev": {"usage"}},
		"sql":     {"app_dev": {"usage"}},
		"plperl":  {"app_dev": {"usage"}},
	})
	d.Extensions = []*schema.Extension{{Name: "plperl"}}
	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`create extension "plperl";`,
		`revoke usage on language "plpgsql" from public;`,
		`grant usage on language "plperl" to "app_dev";`,
		`grant usage on language "plpgsql" to "app_dev";`,
		`grant usage on language "sql" to "app_dev";`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestLanguageDeclarationKeepsItsExtension(t *testing.T) {
	live := languageLive()
	live.ExtensionDefs = map[string]*LiveExtension{"plv8": {Schema: "public"}}
	live.Extensions["plv8"] = true
	live.Languages["plv8"] = &LiveLanguage{Owner: "owner", Trusted: true, Grants: map[string]map[string]bool{"public": {"usage": true}}}
	live.ExtensionMembers["language:plv8"] = true
	live.MemberExtension["language:plv8"] = "plv8"
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	live.Tables["public.keep"] = lt
	d := languageDB(map[string]map[string][]string{"plv8": {"public": {"usage"}}})
	d.Extensions = []*schema.Extension{}
	d.Tables = map[string]*schema.Table{"public.keep": {Name: "keep", Columns: map[string]*schema.Column{"id": {Type: "int"}}}}
	p := PlanWith(live, d, pruneAll)
	if len(p.Drops) > 0 {
		t.Errorf("an extension whose language the YAML declares is kept, got %v", p.Drops)
	}
	if len(p.Warnings) != 1 || p.Warnings[0].Kind != "dependents" || !strings.Contains(p.Warnings[0].Message, "language plv8, which the YAML declares") {
		t.Errorf("want a warning naming the declaration, got %v", p.Warnings)
	}
	d.Languages = nil
	wantStatements(t, PlanWith(live, d, pruneAll).Drops, []string{`drop extension "plv8";`})
}

func TestLanguageGrantsSimulated(t *testing.T) {
	d := languageDB(map[string]map[string][]string{
		"plpgsql": {"public": {}, "app_dev": {"usage"}},
		"sql":     {"app_dev": {"usage*"}},
		"plperl":  {"app_dev": {"usage"}, "public": {}},
	})
	d.Extensions = []*schema.Extension{{Name: "plperl"}}
	live := languageLive()
	if sql, _ := planSQL(t, live, d, Options{}); !strings.Contains(sql, `grant usage on language "plperl" to "app_dev";`) {
		t.Fatalf("the plan must have something to simulate:\n%s", sql)
	}
	if sql, warns := planSQL(t, Simulate(live, d), d, pruneAll); sql != "" || len(warns) > 0 {
		t.Errorf("a plan against the state the first one leaves must be empty, got:\n%s%v", sql, warns)
	}
	if len(live.Languages["plpgsql"].Grants["public"]) != 1 || live.Languages["plperl"] != nil {
		t.Errorf("Simulate must not modify its input")
	}
}
