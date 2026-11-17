package diff

import (
	"fmt"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func extensionCreates(p *PlanDiff) []string {
	out := []string{}
	for _, s := range p.Creates {
		if strings.Contains(s, " extension ") {
			out = append(out, s)
		}
	}
	return out
}

func wantStatements(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("want:\n%s\ngot:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
}

func TestExtensionCreateOrderContribRequirements(t *testing.T) {
	d := &schema.Database{Extensions: []*schema.Extension{
		{Name: "bool_plperl"}, {Name: "earthdistance"}, {Name: "cube"}, {Name: "hstore_plperl"}, {Name: "hstore"}, {Name: "plperl"},
	}}
	wantStatements(t, extensionCreates(Plan(emptyLive(), d, false)), []string{
		`create extension "plperl";`,
		`create extension "bool_plperl";`,
		`create extension "cube";`,
		`create extension "earthdistance";`,
		`create extension "hstore";`,
		`create extension "hstore_plperl";`,
	})
}

func TestExtensionCreateOrderDependsOn(t *testing.T) {
	d := &schema.Database{Extensions: []*schema.Extension{
		{Name: "a_stats", DependsOn: []string{"extension z_base", "schema public"}},
		{Name: "m_other"},
		{Name: "z_base"},
	}}
	wantStatements(t, extensionCreates(Plan(emptyLive(), d, false)), []string{
		`create extension "z_base";`,
		`create extension "a_stats";`,
		`create extension "m_other";`,
	})
}

func TestExtensionCreateOrderDependsOnCycle(t *testing.T) {
	d := &schema.Database{Extensions: []*schema.Extension{
		{Name: "a", DependsOn: []string{"extension b"}},
		{Name: "b", DependsOn: []string{"extension a"}},
	}}
	if got := extensionCreates(Plan(emptyLive(), d, false)); len(got) != 2 {
		t.Errorf("both extensions must still be created, got %v", got)
	}
}

func TestExtensionCreateOrderServerRequirements(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 170000
	live.AvailableExtensions = map[string]*AvailableExtension{
		"a_kcache":      {DefaultVersion: "2.3", Requires: map[string][]string{"2.2": {}, "2.3": {"z_statements"}}},
		"z_statements":  {DefaultVersion: "1.11", Requires: map[string][]string{"1.11": {}}},
		"earthdistance": {DefaultVersion: "1.2", Requires: map[string][]string{"1.2": {}}},
		"cube":          {DefaultVersion: "1.5", Requires: map[string][]string{"1.5": {}}},
	}
	d := &schema.Database{Extensions: []*schema.Extension{{Name: "a_kcache"}, {Name: "z_statements"}, {Name: "earthdistance"}, {Name: "cube"}}}
	p := Plan(live, d, false)
	wantStatements(t, extensionCreates(p), []string{
		`create extension "z_statements";`,
		`create extension "a_kcache";`,
		`create extension "cube";`,
		`create extension "earthdistance";`,
	})
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.Warnings)
	}
	d.Extensions[0].Version = "2.2"
	wantStatements(t, extensionCreates(Plan(live, d, false))[:2], []string{
		`create extension "a_kcache" version '2.2';`,
		`create extension "cube";`,
	})
}

func TestExtensionCascadeMakesLaterCreatesIdempotent(t *testing.T) {
	d := &schema.Database{Extensions: []*schema.Extension{{Name: "a_needs_b", Cascade: true}, {Name: "b_required"}}}
	wantStatements(t, extensionCreates(Plan(emptyLive(), d, false)), []string{
		`create extension "a_needs_b" cascade;`,
		`create extension if not exists "b_required";`,
	})
}

func TestExtensionSystemSchemaNotCreated(t *testing.T) {
	d := &schema.Database{Extensions: []*schema.Extension{{Name: "plperl", IfNotExists: true, Schema: "pg_catalog"}}}
	wantStatements(t, Plan(emptyLive(), d, false).Creates, []string{`create extension if not exists "plperl" schema "pg_catalog";`})
	live := emptyLive()
	live.Extensions["plperl"] = true
	live.ExtensionDefs = map[string]*LiveExtension{"plperl": {Schema: "pg_catalog", Version: "1.0"}}
	if sql := Render(Plan(live, d, true)); sql != "" {
		t.Errorf("an extension living in pg_catalog must diff clean, got:\n%s", sql)
	}
}

func TestExtensionPlpgsqlNotCreatedWithoutServer(t *testing.T) {
	d := &schema.Database{Extensions: []*schema.Extension{{Name: "plpgsql"}}}
	if sql := Render(Plan(emptyLive(), d, false)); sql != "" {
		t.Errorf("plpgsql exists in every new database, got:\n%s", sql)
	}
	live := emptyLive()
	live.ServerVersion = 170000
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, `create extension "plpgsql";`) {
		t.Errorf("a server that lacks plpgsql must get it, got:\n%s", sql)
	}
}

func TestExtensionAltersPrecedeDependents(t *testing.T) {
	live := emptyLive()
	live.Schemas["public"] = true
	live.Extensions["citext"], live.Extensions["pg_stat_statements"] = true, true
	live.ExtensionDefs = map[string]*LiveExtension{
		"citext":             {Schema: "public", Version: "1.6", Relocatable: true},
		"pg_stat_statements": {Schema: "public", Version: "1.6", Relocatable: true},
	}
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "citext", Schema: "ext"}, {Name: "pg_stat_statements", Version: "1.10"}},
		Tables:     map[string]*schema.Table{"public.invites": {Columns: map[string]*schema.Column{"email": {Type: "ext.citext"}}}},
		Views:      map[string]*schema.View{"public.info": {Name: "info", Query: "select dealloc from pg_stat_statements_info"}},
	}
	p := Plan(live, d, false)
	if len(p.Creates) < 5 {
		t.Fatalf("creates: %v", p.Creates)
	}
	wantStatements(t, p.Creates[:3], []string{
		`create schema if not exists "ext";`,
		`alter extension "citext" set schema "ext";`,
		`alter extension "pg_stat_statements" update to '1.10';`,
	})
	if !findCreate(p, `"public"."invites"`) || !findCreate(p, `"public"."info"`) {
		t.Errorf("dependents missing: %v", p.Creates)
	}
}

func TestDatabaseSettingsFollowExtensions(t *testing.T) {
	d := &schema.Database{
		Extensions:       []*schema.Extension{{Name: "pg_trgm", IfNotExists: true}},
		DatabaseSettings: map[string]string{"pg_trgm.similarity_threshold": "0.4"},
	}
	p := Plan(emptyLive(), d, false)
	ext, set := -1, -1
	for i, s := range p.Creates {
		if strings.Contains(s, "create extension") {
			ext = i
		}
		if strings.Contains(s, "pg_trgm.similarity_threshold") {
			set = i
		}
	}
	if ext < 0 || set < 0 || set < ext {
		t.Errorf("the database setting must follow CREATE EXTENSION: %v", p.Creates)
	}
}

func TestExtensionVersionWithoutUpdatePath(t *testing.T) {
	live := emptyLive()
	live.Extensions["pg_stat_statements"] = true
	live.ExtensionDefs = map[string]*LiveExtension{
		"pg_stat_statements": {Schema: "public", Version: "1.10", Relocatable: true, UpdateTargets: map[string]bool{"1.11": true}},
	}
	d := &schema.Database{Extensions: []*schema.Extension{{Name: "pg_stat_statements", Version: "1.4"}}}
	p := Plan(live, d, true)
	if sql := Render(p); sql != "" {
		t.Errorf("a downgrade must not be planned, got:\n%s", sql)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "no update path from the installed version 1.10 to 1.4") {
		t.Errorf("warnings: %v", p.Warnings)
	}
	d.Extensions[0].Version = "1.11"
	p = Plan(live, d, true)
	wantStatements(t, p.Creates, []string{`alter extension "pg_stat_statements" update to '1.11';`})
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.Warnings)
	}
	live.ExtensionDefs["pg_stat_statements"].UpdateTargets = nil
	d.Extensions[0].Version = "1.4"
	wantStatements(t, Plan(live, d, true).Creates, []string{`alter extension "pg_stat_statements" update to '1.4';`})
}

func TestExtensionVersionWithoutUpdatePathReason(t *testing.T) {
	live := emptyLive()
	live.Extensions["postgres_fdw"] = true
	live.ExtensionDefs = map[string]*LiveExtension{
		"postgres_fdw": {Schema: "public", Version: "1.2", Relocatable: true, UpdateTargets: map[string]bool{}},
	}
	live.AvailableExtensions = map[string]*AvailableExtension{
		"postgres_fdw": {DefaultVersion: "1.2", Requires: map[string][]string{"1.0": {}, "1.1": {}, "1.2": {}, "1.10": {}}},
	}
	for version, want := range map[string]string{
		"1.0":  "extension postgres_fdw: PostgreSQL has no update path from the installed version 1.2 to 1.0 (an extension cannot be downgraded), so its version is left as it is",
		"1.9":  "extension postgres_fdw: version 1.9 is not available on this server (available: 1.0, 1.1, 1.2, 1.10), so the installed version 1.2 is left as it is",
		"1.10": "extension postgres_fdw: PostgreSQL has no update path from the installed version 1.2 to 1.10, so its version is left as it is",
	} {
		p := Plan(live, &schema.Database{Extensions: []*schema.Extension{{Name: "postgres_fdw", Version: version}}}, true)
		if sql := Render(p); sql != "" {
			t.Errorf("%s: nothing is planned, got:\n%s", version, sql)
		}
		if len(p.Warnings) != 1 || p.Warnings[0].Message != want {
			t.Errorf("%s: want %q, got %v", version, want, p.Warnings)
		}
	}
	live.AvailableExtensions = nil
	p := Plan(live, &schema.Database{Extensions: []*schema.Extension{{Name: "postgres_fdw", Version: "1.9"}}}, true)
	if len(p.Warnings) != 1 || p.Warnings[0].Message != "extension postgres_fdw: PostgreSQL has no update path from the installed version 1.2 to 1.9, so its version is left as it is" {
		t.Errorf("without the available versions: %v", p.Warnings)
	}
}

func TestExtensionVersionLess(t *testing.T) {
	for _, c := range [][2]string{{"1.9", "1.10"}, {"1.1", "1.1.1"}, {"1.0", "2"}, {"1.4beta", "1.4rc"}, {"0.9", "1.0"}} {
		if !versionLess(c[0], c[1]) || versionLess(c[1], c[0]) {
			t.Errorf("%s must come before %s", c[0], c[1])
		}
	}
	if versionLess("1.2", "1.2") {
		t.Error("a version does not come before itself")
	}
}

func TestExtensionUnavailableWarns(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 170000
	live.AvailableExtensions = map[string]*AvailableExtension{
		"citext": {DefaultVersion: "1.6", Requires: map[string][]string{"1.4": {}, "1.5": {}, "1.6": {}, "1.10": {}}},
	}
	d := &schema.Database{Extensions: []*schema.Extension{{Name: "citext", Version: "9.9"}, {Name: "pg_cron"}}}
	p := Plan(live, d, false)
	if len(extensionCreates(p)) != 2 {
		t.Errorf("the statements stay in the plan: %v", p.Creates)
	}
	if len(p.Warnings) != 2 ||
		!strings.Contains(p.Warnings[0].Message, "extension citext: version 9.9 is not available on this server (available: 1.4, 1.5, 1.6, 1.10)") ||
		!strings.Contains(p.Warnings[1].Message, "extension pg_cron is not available on this server") {
		t.Errorf("warnings: %v", p.Warnings)
	}
}

func movedCitextLive() *Live {
	live := emptyLive()
	live.Schemas["public"], live.Schemas["app"] = true, true
	live.Extensions["citext"] = true
	live.ExtensionDefs = map[string]*LiveExtension{"citext": {
		Schema: "public", Version: "1.6", Relocatable: true,
		Members:   map[string]bool{"citext": true, "citext_pattern_ops": true, "regexp_replace": true},
		Operators: map[string]bool{"<>": true, "=": true},
	}}
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "bigint"}
	lt.Columns["c"] = &LiveColumn{Type: "citext", Default: "''::citext"}
	lt.Columns["n"] = &LiveColumn{Type: "text"}
	lt.Constraints["t_c_check"] = true
	lt.ConstraintDefs["t_c_check"] = "CHECK ((c <> ''::citext))"
	live.Tables["app.t"] = lt
	return live
}

func movedCitextDesired() *schema.Database {
	return &schema.Database{
		Extensions: []*schema.Extension{{Name: "citext", Schema: "ext"}},
		Tables: map[string]*schema.Table{"app.t": {
			Columns: map[string]*schema.Column{
				"id": {Type: "bigint"},
				"c":  {Type: "ext.citext", Default: "''::ext.citext"},
				"n":  {Type: "text"},
			},
			Constraints: []*schema.Constraint{{Name: "t_c_check", Type: "check", Expression: "c operator(ext.<>) ''"}},
		}},
	}
}

func TestExtensionMoveLeavesFollowersAlone(t *testing.T) {
	p := Plan(movedCitextLive(), movedCitextDesired(), true)
	wantStatements(t, append(append(append([]string{}, p.Creates...), p.Alters...), p.Drops...), []string{
		`create schema if not exists "ext";`,
		`alter extension "citext" set schema "ext";`,
	})
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.Warnings)
	}
}

func TestExtensionMoveKeepsRealChanges(t *testing.T) {
	d := movedCitextDesired()
	d.Tables["app.t"].Columns["n"].Type = "ext.citext"
	d.Tables["app.t"].Columns["extra"] = &schema.Column{Type: "ext.citext", Nullable: true}
	sql := Render(Plan(movedCitextLive(), d, true))
	for _, want := range []string{
		`alter extension "citext" set schema "ext";`,
		`alter table "app"."t" alter column "n" type ext.citext;`,
		`alter table "app"."t" add column "extra" ext.citext;`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, `alter column "c"`) || strings.Contains(sql, "t_c_check") {
		t.Errorf("column c and its check only follow the extension:\n%s", sql)
	}
	if strings.Index(sql, "alter extension") > strings.Index(sql, `alter column "n"`) {
		t.Errorf("the extension must move first:\n%s", sql)
	}
}

func movedCitextFunctionLive() *Live {
	live := movedCitextLive()
	live.Functions["app.by_c(p citext)"] = true
	live.FunctionDefs["app.by_c(p citext)"] = &LiveFunction{
		Signature: "app.by_c(p citext)", Args: "p citext DEFAULT ''::citext", Returns: "citext", Language: "sql",
		Body: "select p", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100,
	}
	live.FunctionGrants["app.by_c(p citext)"] = map[string]map[string]bool{"app_user": {"execute": true}}
	live.Procedures["app.touch(p citext)"] = true
	live.ProcedureDefs["app.touch(p citext)"] = &LiveProcedure{Signature: "app.touch(p citext)", Args: "p citext", Body: "select 1", Language: "sql", Security: "invoker"}
	return live
}

func movedCitextFunctionDesired(body string) *schema.Database {
	d := movedCitextDesired()
	d.Functions = map[string]*schema.Function{"app.by_c(p ext.citext default ''::ext.citext)": {
		Schema: "app", Name: "by_c", ArgsSig: "(p ext.citext default ''::ext.citext)", Returns: "ext.citext", Language: "sql", Body: body,
		Grants: map[string][]string{"app_user": {"execute"}},
	}}
	d.Procedures = map[string]*schema.Procedure{"app.touch(p ext.citext)": {
		Schema: "app", Name: "touch", ArgsSig: "(p ext.citext)", Language: "sql", Body: "select 1",
	}}
	return d
}

func TestExtensionMoveRequalifiedRoutineUnchanged(t *testing.T) {
	p := Plan(movedCitextFunctionLive(), movedCitextFunctionDesired("select p"), true)
	wantStatements(t, append(append(append([]string{}, p.Creates...), p.Alters...), p.Drops...), []string{
		`create schema if not exists "ext";`,
		`alter extension "citext" set schema "ext";`,
	})
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.Warnings)
	}
}

func TestExtensionMoveReplacesRequalifiedRoutine(t *testing.T) {
	d := movedCitextFunctionDesired("select p limit 1")
	d.Procedures["app.touch(p ext.citext)"].Body = "select 2"
	for _, opts := range []Options{{}, {Unsafe: true}, {Unsafe: true, Prune: true}} {
		p := PlanWith(movedCitextFunctionLive(), d, opts)
		wantStatements(t, append(append(append([]string{}, p.Creates...), p.Alters...), p.Drops...), []string{
			`create schema if not exists "ext";`,
			`alter extension "citext" set schema "ext";`,
			`create or replace function "app"."by_c"(p ext.citext default ''::ext.citext) returns ext.citext language sql as $$select p limit 1$$;`,
			`create or replace procedure "app"."touch"(p ext.citext) language sql as $$select 2$$;`,
		})
		if len(p.Warnings) != 0 {
			t.Errorf("%+v: unexpected warnings: %v", opts, p.Warnings)
		}
	}
}

func TestExtensionMoveAltersRequalifiedRoutine(t *testing.T) {
	d := movedCitextFunctionDesired("select p")
	d.Functions["app.by_c(p ext.citext default ''::ext.citext)"].Volatility = "stable"
	p := Plan(movedCitextFunctionLive(), d, true)
	wantStatements(t, append(append(append([]string{}, p.Creates...), p.Alters...), p.Drops...), []string{
		`create schema if not exists "ext";`,
		`alter extension "citext" set schema "ext";`,
		`alter function "app"."by_c"(p ext.citext) stable;`,
	})
}

func unqualifiedCitextDesired() *schema.Database {
	d := movedCitextDesired()
	d.Tables["app.t"].Columns["c"] = &schema.Column{Type: "citext", Default: "''::citext"}
	d.Tables["app.t"].Columns["n"] = &schema.Column{Type: "text", Default: `regexp_replace('a', 'b', 'c')`}
	d.Tables["app.t"].Columns["l"] = &schema.Column{Type: "text", Default: `'citext(x)'`}
	d.Tables["app.t"].Columns["m"] = &schema.Column{Type: "text", Generated: `upper(citext(n)::text)`}
	d.Tables["app.t"].Columns["q"] = &schema.Column{Type: "text", Generated: `"CITEXT"(n)::text`}
	d.Domains = map[string]*schema.Domain{"app.email": {Type: "citext[]"}, "app.code": {Type: "ext.citext"}}
	d.Functions = map[string]*schema.Function{
		"app.norm(e citext)":          {Schema: "app", Name: "norm", ArgsSig: "(e citext)", Returns: "ext.citext", Language: "sql", Body: "select e::text::citext"},
		"app.size(e ext.citext)":      {Schema: "app", Name: "size", ArgsSig: "(e ext.citext)", Returns: "setof citext", Language: "sql", Body: "select e"},
		"app.ok(e ext.citext, n int)": {Schema: "app", Name: "ok", ArgsSig: "(e ext.citext, n int)", Returns: "boolean", Language: "sql", Body: "select true"},
	}
	return d
}

func searchPathWarnings(p *PlanDiff) []string {
	out := []string{}
	for _, w := range p.Warnings {
		if strings.Contains(w.Message, "not on the search_path") {
			out = append(out, w.Message)
		}
	}
	return out
}

func TestExtensionMoveOffSearchPathWarns(t *testing.T) {
	live := movedCitextLive()
	live.ExtensionDefs["citext"].Builtins = map[string]bool{"regexp_replace": true}
	p := Plan(live, unqualifiedCitextDesired(), true)
	if !findCreate(p, `alter extension "citext" set schema "ext";`) {
		t.Fatalf("the extension still moves:\n%s", Render(p))
	}
	want := "extension citext moves to schema ext, which is not on the search_path, while the YAML still names its objects without a schema in " +
		"column app.t.c, column app.t.m, domain app.email, function app.norm(e citext), function app.size(e ext.citext); " +
		"those names will not resolve once it has moved, so qualify them with the schema"
	wantStatements(t, searchPathWarnings(p), []string{want})
	if p := Plan(live, movedCitextDesired(), true); len(p.Warnings) != 0 {
		t.Errorf("qualified names need no warning: %v", p.Warnings)
	}
}

func TestExtensionMoveOnSearchPathQuiet(t *testing.T) {
	live := movedCitextLive()
	d := unqualifiedCitextDesired()
	d.DatabaseSettings = map[string]string{"search_path": `"$user", public, "ext"`}
	if w := searchPathWarnings(Plan(live, d, true)); len(w) != 0 {
		t.Errorf("a database search_path with the schema resolves the names: %v", w)
	}
	live.SearchPath = []string{"public", "ext"}
	if w := searchPathWarnings(Plan(live, unqualifiedCitextDesired(), true)); len(w) != 0 {
		t.Errorf("the session's search_path has the schema: %v", w)
	}
	live.SearchPath = nil
	live.ExtensionDefs["citext"].Members = nil
	if w := searchPathWarnings(Plan(live, unqualifiedCitextDesired(), true)); len(w) != 0 {
		t.Errorf("without member names nothing can be named: %v", w)
	}
}

func TestExtensionMoveOffSearchPathListsTen(t *testing.T) {
	d := movedCitextDesired()
	for i := 0; i < 12; i++ {
		d.Tables["app.t"].Columns[fmt.Sprintf("c%02d", i)] = &schema.Column{Type: "citext", Nullable: true}
	}
	w := searchPathWarnings(Plan(movedCitextLive(), d, true))
	if len(w) != 1 || !strings.Contains(w[0], "column app.t.c09, 2 more;") || strings.Contains(w[0], "c10") {
		t.Errorf("ten objects are named, then a count: %v", w)
	}
}

func TestExtensionMoveWithoutMemberNamesReemits(t *testing.T) {
	live := movedCitextLive()
	live.ExtensionDefs["citext"].Members, live.ExtensionDefs["citext"].Operators = nil, nil
	sql := Render(Plan(live, movedCitextDesired(), true))
	if !strings.Contains(sql, `alter column "c" type ext.citext`) {
		t.Errorf("without member names the old comparison applies:\n%s", sql)
	}
	if strings.Index(sql, "alter extension") > strings.Index(sql, `alter column "c"`) {
		t.Errorf("the extension must still move first:\n%s", sql)
	}
}

func TestExtensionMoveSimulatedStaysQuiet(t *testing.T) {
	live, d := movedCitextLive(), movedCitextDesired()
	after := Simulate(live, d)
	if le := after.ExtensionDefs["citext"]; le.Schema != "ext" || le.MovedFrom != "public" {
		t.Fatalf("simulated extension: %+v", le)
	}
	after.Tables["app.t"] = live.Tables["app.t"]
	if sql := Render(Plan(after, d, true)); sql != "" {
		t.Errorf("a later group must not re-emit what follows the moved extension:\n%s", sql)
	}
}

func TestRequalifierText(t *testing.T) {
	live := emptyLive()
	live.ExtensionDefs = map[string]*LiveExtension{
		"citext":    {Schema: "public", Relocatable: true, Members: map[string]bool{"citext": true, "regexp_replace": true}, Operators: map[string]bool{"<>": true}},
		"uuid-ossp": {Schema: "old", Relocatable: true, Members: map[string]bool{"uuid_generate_v4": true}, Operators: map[string]bool{}},
	}
	rq := newRequalifier(live, &schema.Database{Extensions: []*schema.Extension{{Name: "citext", Schema: "ext"}, {Name: "uuid-ossp", Schema: "ext-s"}}})
	if rq == nil {
		t.Fatal("no requalifier")
	}
	for in, want := range map[string]string{
		"ext.citext":                        "public.citext",
		"EXT.Citext[]":                      "public.Citext[]",
		`"ext"."citext"`:                    `"public"."citext"`,
		"c operator(ext.<>) ''::ext.citext": "c <> ''::public.citext",
		"c OPERATOR(ext.~~) 'x'":            "c OPERATOR(ext.~~) 'x'",
		"ext.my_table":                      "ext.my_table",
		"next.citext":                       "next.citext",
		"app.ext.citext":                    "app.ext.citext",
		"select 'ext.citext', ext.regexp_replace(a)": "select 'ext.citext', public.regexp_replace(a)",
		"as $fn$ select ext.citext $fn$":             "as $fn$ select ext.citext $fn$",
		`"ext-s".uuid_generate_v4()`:                 `"old".uuid_generate_v4()`,
		`"ext-s".other()`:                            `"ext-s".other()`,
	} {
		if got := rq.text(in); got != want {
			t.Errorf("%q: want %q, got %q", in, want, got)
		}
	}
}

func TestRequalifierDatabaseKeepsBodiesAndComments(t *testing.T) {
	live := emptyLive()
	live.ExtensionDefs = map[string]*LiveExtension{"citext": {Schema: "public", Relocatable: true, Members: map[string]bool{"citext": true}, Operators: map[string]bool{}}}
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "citext", Schema: "ext"}},
		Functions: map[string]*schema.Function{"app.norm(e ext.citext)": {
			ArgsSig: "(e ext.citext)", Returns: "ext.citext", Language: "sql", Body: "select lower(e::text)::ext.citext", Comment: "returns ext.citext",
		}},
	}
	out := newRequalifier(live, d).database(d)
	f := out.Functions["app.norm(e public.citext)"]
	if f == nil {
		t.Fatalf("routine keys must be rewritten: %v", sortedKeys(out.Functions))
	}
	if f.ArgsSig != "(e public.citext)" || f.Returns != "public.citext" {
		t.Errorf("signature: %+v", f)
	}
	if f.Body != "select lower(e::text)::ext.citext" || f.Comment != "returns ext.citext" {
		t.Errorf("body and comment must be copied verbatim: %+v", f)
	}
	if d.Functions["app.norm(e ext.citext)"].Returns != "ext.citext" || out.Extensions[0].Schema != "ext" {
		t.Errorf("the source must not change, and the extension keeps its target schema")
	}
}
