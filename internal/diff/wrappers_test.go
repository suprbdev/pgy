package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func wrapperLive() *Live {
	live := memberLive()
	live.Wrappers = map[string]*LiveWrapper{}
	live.Servers = map[string]*LiveServer{}
	live.MemberExtension = map[string]string{}
	live.ExtensionDefs = map[string]*LiveExtension{}
	return live
}

// addMemberWrapper adds a foreign-data wrapper that extension owns.
func addMemberWrapper(live *Live, name, extension string) *LiveWrapper {
	lw := &LiveWrapper{Handler: "public." + name + "_handler", Validator: "public." + name + "_validator", Options: map[string]string{}, Owner: live.CurrentUser, ExtensionOwner: live.CurrentUser}
	live.Wrappers[name] = lw
	live.Extensions[extension] = true
	live.ExtensionDefs[extension] = &LiveExtension{Schema: "public", Version: "1.0"}
	live.ExtensionMembers["fdw:"+name] = true
	live.MemberExtension["fdw:"+name] = extension
	return lw
}

func wrapperDB(wrappers map[string]*schema.ForeignDataWrapper) *schema.Database {
	return &schema.Database{ForeignDataWrappers: wrappers}
}

func TestForeignDataWrapperCreate(t *testing.T) {
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{
		"pg2": {Name: "pg2", Handler: "public.postgres_fdw_handler", Validator: "postgres_fdw_validator", Owner: "admin", Comment: "second wrapper",
			Grants: map[string][]string{"app": {"usage"}}},
		"legacy": {Name: "legacy", Options: map[string]string{"debug": "true", "level": "3"}},
		"bare":   {Name: "bare"},
	})
	d.Servers = map[string]*schema.Server{"on_pg2": {Name: "on_pg2", Wrapper: "pg2"}}
	sql, warns := planSQL(t, wrapperLive(), d, Options{})
	wantSQL(t, sql,
		`create foreign data wrapper "bare";`,
		`create foreign data wrapper "legacy" options ("debug" 'true', "level" '3');`,
		`create foreign data wrapper "pg2" handler "public"."postgres_fdw_handler" validator "postgres_fdw_validator";`,
		`alter foreign data wrapper "pg2" owner to "admin";`,
		`grant usage on foreign data wrapper "pg2" to "app";`,
		`create server "on_pg2" foreign data wrapper "pg2";`,
		`comment on foreign data wrapper "pg2" is 'second wrapper';`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestForeignDataWrapperCreatedAfterExtensions(t *testing.T) {
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{"pg2": {Name: "pg2", Handler: "postgres_fdw_handler"}})
	d.Extensions = []*schema.Extension{{Name: "postgres_fdw"}}
	d.Functions = map[string]*schema.Function{"public.f()": {Schema: "public", Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select 1"}}
	sql, _ := planSQL(t, wrapperLive(), d, Options{})
	ext, fdw, fn := strings.Index(sql, `create extension "postgres_fdw";`), strings.Index(sql, `create foreign data wrapper "pg2" handler "postgres_fdw_handler";`), strings.Index(sql, "create function")
	if ext < 0 || fdw < ext || fn < fdw {
		t.Errorf("want the extension, then the wrapper, then everything else:\n%s", sql)
	}
}

func TestForeignDataWrapperSkippedIfExists(t *testing.T) {
	live := wrapperLive()
	live.Wrappers["pg2"] = &LiveWrapper{Handler: "public.postgres_fdw_handler", Validator: "ext.postgres_fdw_validator", Options: map[string]string{}, Owner: "admin", Comment: "second wrapper",
		Grants: map[string]map[string]bool{"app": {"usage": true}}}
	live.Wrappers["legacy"] = &LiveWrapper{Options: map[string]string{"debug": "true"}, Owner: "owner"}
	live.Wrappers["bare"] = &LiveWrapper{Options: map[string]string{"left": "alone"}, Owner: "owner", Comment: "kept"}
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{
		"pg2": {Name: "pg2", Handler: "public.postgres_fdw_handler", Validator: "postgres_fdw_validator", Owner: "admin", Comment: "second wrapper",
			Options: map[string]string{}, Grants: map[string][]string{"app": {"usage"}, "admin": {"usage"}}},
		"legacy": {Name: "legacy", Options: map[string]string{"debug": "true"}, Owner: "current_user"},
		"bare":   {Name: "bare"},
	})
	sql, warns := planSQL(t, live, d, pruneAll)
	if sql != "" || len(warns) > 0 {
		t.Errorf("wrappers in the declared state must be skipped, got:\n%s%v", sql, warns)
	}
}

func TestForeignDataWrapperAltered(t *testing.T) {
	base := func() *LiveWrapper {
		return &LiveWrapper{Handler: "public.h", Validator: "public.v", Options: map[string]string{"keep": "1", "change": "old", "gone": "x"}, Owner: "owner", Comment: "was"}
	}
	same := func() *schema.ForeignDataWrapper {
		return &schema.ForeignDataWrapper{Name: "w", Handler: "public.h", Validator: "public.v"}
	}
	for name, c := range map[string]struct {
		change func(w *schema.ForeignDataWrapper)
		want   string
	}{
		"another handler":       {func(w *schema.ForeignDataWrapper) { w.Handler = "ext.h2" }, `alter foreign data wrapper "w" handler "ext"."h2";`},
		"the handler removed":   {func(w *schema.ForeignDataWrapper) { w.Handler = "" }, `alter foreign data wrapper "w" no handler;`},
		"another validator":     {func(w *schema.ForeignDataWrapper) { w.Validator = "v2" }, `alter foreign data wrapper "w" validator "v2";`},
		"the validator removed": {func(w *schema.ForeignDataWrapper) { w.Validator = "" }, `alter foreign data wrapper "w" no validator;`},
		"options": {func(w *schema.ForeignDataWrapper) {
			w.Options = map[string]string{"keep": "1", "change": "new", "more": "2"}
		}, `alter foreign data wrapper "w" options (set "change" 'new', add "more" '2', drop "gone");`},
		"no options":              {func(w *schema.ForeignDataWrapper) { w.Options = map[string]string{} }, `alter foreign data wrapper "w" options (drop "change", drop "gone", drop "keep");`},
		"the owner":               {func(w *schema.ForeignDataWrapper) { w.Owner = "admin" }, `alter foreign data wrapper "w" owner to "admin";`},
		"the comment":             {func(w *schema.ForeignDataWrapper) { w.Comment = "is" }, `comment on foreign data wrapper "w" is 'is';`},
		"the definition together": {func(w *schema.ForeignDataWrapper) { w.Handler, w.Validator, w.Options = "", "v2", map[string]string{} }, `alter foreign data wrapper "w" no handler validator "v2" options (drop "change", drop "gone", drop "keep");`},
	} {
		live := wrapperLive()
		live.Wrappers["w"] = base()
		w := same()
		c.change(w)
		sql, warns := planSQL(t, live, wrapperDB(map[string]*schema.ForeignDataWrapper{"w": w}), Options{})
		if strings.TrimSpace(sql) != c.want || len(warns) > 0 {
			t.Errorf("%s: got\n%s%v\nwant\n%s", name, sql, warns, c.want)
		}
	}
}

func TestForeignDataWrapperFunctionWithoutSchemaMatchesAnySchema(t *testing.T) {
	live := wrapperLive()
	live.Wrappers["w"] = &LiveWrapper{Handler: "ext.h", Validator: "pg_catalog.postgresql_fdw_validator", Owner: "owner"}
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{"w": {Name: "w", Handler: "h", Validator: "postgresql_fdw_validator"}})
	if sql, _ := planSQL(t, live, d, Options{}); sql != "" {
		t.Errorf("a function named without a schema is the live one, got:\n%s", sql)
	}
	d.ForeignDataWrappers["w"].Handler = "public.h"
	sql, _ := planSQL(t, live, d, Options{})
	wantSQL(t, sql, `alter foreign data wrapper "w" handler "public"."h";`)
}

func TestForeignDataWrapperGrants(t *testing.T) {
	live := wrapperLive()
	live.Wrappers["w"] = &LiveWrapper{Owner: "boss", Grants: map[string]map[string]bool{
		"keeper": {"usage*": true}, "loses_option": {"usage*": true}, "gone": {"usage": true}, "public": {"usage": true},
	}}
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{"w": {Name: "w", Grants: map[string][]string{
		"keeper": {"usage*"}, "loses_option": {"usage"}, "added": {"usage"}, "with_option": {"usage*"}, "boss": {"usage"},
	}}})
	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`revoke usage on foreign data wrapper "w" from "gone";`,
		`revoke grant option for usage on foreign data wrapper "w" from "loses_option";`,
		`revoke usage on foreign data wrapper "w" from public;`,
		`grant usage on foreign data wrapper "w" to "added";`,
		`grant usage on foreign data wrapper "w" to "with_option" with grant option;`,
	)
	if len(warns) != 1 || warns[0].Kind != "public_revoke" || !strings.Contains(warns[0].Message, "a grants block on a foreign-data wrapper covers PUBLIC") {
		t.Errorf("want a warning about the PUBLIC revoke, got %v", warns)
	}

	d.ForeignDataWrappers["w"].Grants["public"] = []string{"usage"}
	if sql, warns := planSQL(t, live, d, Options{}); strings.Contains(sql, "public") || len(warns) > 0 {
		t.Errorf("a block that lists public keeps its privileges, got:\n%s%v", sql, warns)
	}
	delete(d.ForeignDataWrappers["w"].Grants, "public")
	if sql, _ := planSQL(t, live, d, Options{PublicGrants: PublicExplicit}); strings.Contains(sql, "public") {
		t.Errorf("policy.public_grants: explicit leaves PUBLIC alone unless listed, got:\n%s", sql)
	}
	d.ForeignDataWrappers["w"].Grants = nil
	if sql, _ := planSQL(t, live, d, Options{}); sql != "" {
		t.Errorf("without a grants block the privileges are unmanaged, got:\n%s", sql)
	}
}

func TestForeignDataWrapperOfExtension(t *testing.T) {
	live := wrapperLive()
	lw := addMemberWrapper(live, "postgres_fdw", "postgres_fdw")
	lw.Grants = map[string]map[string]bool{"extension_granted": {"usage": true}, "stale": {"usage": true}}
	live.InitPrivs["fdw:postgres_fdw"] = map[string]map[string]bool{"extension_granted": {"usage": true}}
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{"postgres_fdw": {Name: "postgres_fdw",
		Options: map[string]string{"use_remote_estimate": "true"}, Owner: "admin", Comment: "remote PostgreSQL",
		Grants: map[string][]string{"app_owner": {"usage"}},
	}})
	sql, warns := planSQL(t, live, d, pruneAll)
	wantSQL(t, sql,
		`alter foreign data wrapper "postgres_fdw" options (add "use_remote_estimate" 'true');`,
		`alter foreign data wrapper "postgres_fdw" owner to "admin";`,
		`revoke usage on foreign data wrapper "postgres_fdw" from "stale";`,
		`grant usage on foreign data wrapper "postgres_fdw" to "app_owner";`,
		`comment on foreign data wrapper "postgres_fdw" is 'remote PostgreSQL';`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}

	d.ForeignDataWrappers["postgres_fdw"].Grants["extension_granted"] = []string{}
	if sql, _ := planSQL(t, live, d, Options{}); !strings.Contains(sql, `revoke usage on foreign data wrapper "postgres_fdw" from "extension_granted";`) {
		t.Errorf("naming a grantee of the extension makes it exact, got:\n%s", sql)
	}

	lw.Options, lw.Owner, lw.Comment = map[string]string{"use_remote_estimate": "true"}, "admin", "remote PostgreSQL"
	lw.Grants = map[string]map[string]bool{"extension_granted": {"usage": true}, "app_owner": {"usage": true}}
	delete(d.ForeignDataWrappers["postgres_fdw"].Grants, "extension_granted")
	if sql, _ := planSQL(t, live, d, pruneAll); sql != "" {
		t.Errorf("an extension's wrapper in the declared state must be skipped, got:\n%s", sql)
	}
}

func TestForeignDataWrapperOfExtensionIgnoresHandler(t *testing.T) {
	live := wrapperLive()
	addMemberWrapper(live, "file_fdw", "file_fdw")
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{"file_fdw": {Name: "file_fdw", Handler: "other_handler", Comment: "files"}})
	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql, `comment on foreign data wrapper "file_fdw" is 'files';`)
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "foreign-data wrapper file_fdw belongs to extension file_fdw") {
		t.Errorf("want a warning about the ignored handler, got %v", warns)
	}
}

func TestForeignDataWrapperOfExtensionCreatedByPlan(t *testing.T) {
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{
		"postgres_fdw": {Name: "postgres_fdw", Owner: "admin", Grants: map[string][]string{"app_owner": {"usage"}, "nobody": {}}},
		"dblink_fdw":   {Name: "dblink_fdw", Options: map[string]string{"a": "1"}, Comment: "links"},
		"mysql_fdw":    {Name: "mysql_fdw", Grants: map[string][]string{"app_owner": {"usage"}}},
		"legacy":       {Name: "legacy"},
	})
	d.Extensions = []*schema.Extension{{Name: "dblink"}, {Name: "mysql_fdw"}, {Name: "postgres_fdw"}}
	sql, warns := planSQL(t, wrapperLive(), d, Options{})
	wantSQL(t, sql,
		`create extension "dblink";`,
		`create extension "mysql_fdw";`,
		`create extension "postgres_fdw";`,
		`alter foreign data wrapper "dblink_fdw" options (add "a" '1');`,
		`create foreign data wrapper "legacy";`,
		`grant usage on foreign data wrapper "mysql_fdw" to "app_owner";`,
		`alter foreign data wrapper "postgres_fdw" owner to "admin";`,
		`revoke all on foreign data wrapper "postgres_fdw" from "nobody";`,
		`grant usage on foreign data wrapper "postgres_fdw" to "app_owner";`,
		`comment on foreign data wrapper "dblink_fdw" is 'links';`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestForeignDataWrapperOfInstalledExtensionWithoutOneIsCreated(t *testing.T) {
	live := wrapperLive()
	live.Extensions["hstore"] = true
	live.ExtensionDefs["hstore"] = &LiveExtension{Schema: "public"}
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{"hstore": {Name: "hstore"}})
	d.Extensions = []*schema.Extension{{Name: "hstore"}}
	sql, _ := planSQL(t, live, d, Options{})
	wantSQL(t, sql, `create foreign data wrapper "hstore";`)
}

func TestForeignDataWrapperOfMissingExtensionWarns(t *testing.T) {
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{"postgres_fdw": {Name: "postgres_fdw", Grants: map[string][]string{"app_owner": {"usage"}}}})
	sql, warns := planSQL(t, wrapperLive(), d, Options{})
	if sql != "" {
		t.Errorf("nothing can be planned for the wrapper of an extension that is not there, got:\n%s", sql)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "foreign-data wrapper postgres_fdw comes with extension postgres_fdw") {
		t.Errorf("want a warning naming the declaration, got %v", warns)
	}
}

func TestForeignDataWrapperRenamed(t *testing.T) {
	live := wrapperLive()
	live.Wrappers["pg_two"] = &LiveWrapper{Handler: "public.h", Owner: "owner"}
	live.Servers["s"] = &LiveServer{Wrapper: "pg_two", Owner: "owner", UserMapping: true}
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{"pg2": {Name: "pg2", Handler: "h", RenamedFrom: "pg_two"}})
	d.Servers = map[string]*schema.Server{"s": {Name: "s", Wrapper: "pg2"}}
	sql, warns := planSQL(t, live, d, pruneAll)
	wantSQL(t, sql, `alter foreign data wrapper "pg_two" rename to "pg2";`)
	if len(warns) > 0 {
		t.Errorf("the servers of a renamed wrapper follow it, got %v", warns)
	}
	if live.Wrappers["pg_two"] == nil || live.Servers["s"].Wrapper != "pg_two" {
		t.Errorf("the live snapshot must not be modified")
	}

	live.Wrappers["pg2"] = live.Wrappers["pg_two"]
	delete(live.Wrappers, "pg_two")
	live.Servers["s"].Wrapper = "pg2"
	if sql, _ := planSQL(t, live, d, pruneAll); sql != "" {
		t.Errorf("an applied rename is a no-op, got:\n%s", sql)
	}
}

func TestForeignDataWrapperRenameOfExtensionWrapperRefused(t *testing.T) {
	live := wrapperLive()
	addMemberWrapper(live, "postgres_fdw", "postgres_fdw")
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{"pg": {Name: "pg", RenamedFrom: "postgres_fdw", Comment: "x"}})
	sql, warns := planSQL(t, live, d, Options{})
	if sql != "" {
		t.Errorf("an extension's wrapper is not renamed, got:\n%s", sql)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "renamedFrom names postgres_fdw, which an extension owns") {
		t.Errorf("want a warning, got %v", warns)
	}
}

func TestForeignDataWrapperPruned(t *testing.T) {
	live := wrapperLive()
	addMemberWrapper(live, "postgres_fdw", "postgres_fdw")
	live.Wrappers["declared"] = &LiveWrapper{Owner: "owner"}
	live.Wrappers["unused"] = &LiveWrapper{Owner: "owner"}
	live.Wrappers["with_dropped_server"] = &LiveWrapper{Owner: "owner"}
	live.Wrappers["with_kept_server"] = &LiveWrapper{Owner: "owner"}
	live.Wrappers["with_new_server"] = &LiveWrapper{Owner: "owner"}
	live.Servers["dropped"] = &LiveServer{Wrapper: "with_dropped_server", Owner: "owner", UserMappings: []string{"public"}}
	live.Servers["kept"] = &LiveServer{Wrapper: "with_kept_server", Owner: "owner"}
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{"declared": {Name: "declared"}})
	d.Servers = map[string]*schema.Server{
		"kept": {Name: "kept", Wrapper: "with_kept_server"},
		"new":  {Name: "new", Wrapper: "with_new_server"},
	}
	p := PlanWith(live, d, pruneAll)
	wantStatements(t, p.Drops, []string{
		`drop user mapping for public server "dropped";`,
		`drop server "dropped";`,
		`drop foreign data wrapper "unused";`,
		`drop foreign data wrapper "with_dropped_server";`,
	})
	got := []string{}
	for _, w := range p.Warnings {
		got = append(got, w.Message)
	}
	for _, want := range []string{
		"foreign-data wrapper with_kept_server is not in the foreignDataWrappers block but servers this plan keeps still use it (kept)",
		"foreign-data wrapper with_new_server is not in the foreignDataWrappers block but servers this plan keeps still use it (new)",
	} {
		if !strings.Contains(strings.Join(got, "\n"), want) {
			t.Errorf("missing warning %q in %v", want, got)
		}
	}

	if p := PlanWith(live, d, Options{Unsafe: true}); len(p.Drops) > 0 {
		t.Errorf("wrappers are only dropped with --prune, got %v", p.Drops)
	}
	d.ForeignDataWrappers = nil
	for _, s := range PlanWith(live, d, pruneAll).Drops {
		if strings.Contains(s, "foreign data wrapper") {
			t.Errorf("without a foreignDataWrappers block, wrappers are unmanaged: %s", s)
		}
	}
	d.ForeignDataWrappers, d.Servers = map[string]*schema.ForeignDataWrapper{}, nil
	p = PlanWith(live, d, pruneAll)
	wantStatements(t, p.Drops, []string{`drop foreign data wrapper "declared";`, `drop foreign data wrapper "unused";`, `drop foreign data wrapper "with_new_server";`})
	if len(p.Warnings) != 2 {
		t.Errorf("servers that are unmanaged keep their wrappers, got %v", p.Warnings)
	}
}

func TestForeignDataWrapperDeclarationKeepsItsExtension(t *testing.T) {
	live := wrapperLive()
	live.Schemas["public"] = true
	addMemberWrapper(live, "postgres_fdw", "postgres_fdw")
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	live.Tables["public.keep"] = lt
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{"postgres_fdw": {Name: "postgres_fdw", Grants: map[string][]string{}}})
	d.Extensions = []*schema.Extension{}
	d.Tables = map[string]*schema.Table{"public.keep": {Name: "keep", Columns: map[string]*schema.Column{"id": {Type: "int"}}}}
	p := PlanWith(live, d, pruneAll)
	if len(p.Drops) > 0 {
		t.Errorf("an extension whose wrapper the YAML declares is kept, got %v", p.Drops)
	}
	if len(p.Warnings) != 1 || p.Warnings[0].Kind != "dependents" || !strings.Contains(p.Warnings[0].Message, "foreign-data wrapper postgres_fdw, which the YAML declares") {
		t.Errorf("want a warning naming the declaration, got %v", p.Warnings)
	}
	d.ForeignDataWrappers = nil
	wantStatements(t, PlanWith(live, d, pruneAll).Drops, []string{`drop extension "postgres_fdw";`})
}

func TestForeignDataWrapperOnExtensionHandlerDroppedBeforeExtension(t *testing.T) {
	live := wrapperLive()
	live.Schemas["public"] = true
	addMemberWrapper(live, "postgres_fdw", "postgres_fdw")
	live.Wrappers["pg2"] = &LiveWrapper{Handler: "public.postgres_fdw_handler", Owner: "owner"}
	live.ExtensionDefs["postgres_fdw"].Dependents = []ExtensionDependent{
		newExtensionDependent(dependentAddress{kind: "foreign-data wrapper", names: []string{"pg2"}}, "foreign-data wrapper pg2"),
	}
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	live.Tables["public.keep"] = lt
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{})
	d.Extensions = []*schema.Extension{}
	d.Tables = map[string]*schema.Table{"public.keep": {Name: "keep", Columns: map[string]*schema.Column{"id": {Type: "int"}}}}
	wantStatements(t, PlanWith(live, d, pruneAll).Drops, []string{`drop foreign data wrapper "pg2";`, `drop extension "postgres_fdw";`})

	d.ForeignDataWrappers = nil
	p := PlanWith(live, d, pruneAll)
	if len(p.Drops) > 0 || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "foreign-data wrapper pg2") {
		t.Errorf("a wrapper that stays keeps the extension of its handler, got %v %v", p.Drops, p.Warnings)
	}
}

func TestForeignDataWrapperSimulated(t *testing.T) {
	live := wrapperLive()
	live.Wrappers["altered"] = &LiveWrapper{Handler: "public.h", Options: map[string]string{"a": "1"}, Owner: "owner", Grants: map[string]map[string]bool{"old": {"usage": true}}}
	d := wrapperDB(map[string]*schema.ForeignDataWrapper{
		"altered":      {Name: "altered", Validator: "v", Options: map[string]string{"b": "2"}, Owner: "admin", Comment: "c", Grants: map[string][]string{"app": {"usage*"}, "none": {}}},
		"created":      {Name: "created", Handler: "ext.h", Grants: map[string][]string{"app": {"usage"}}},
		"renamed":      {Name: "renamed", RenamedFrom: "altered_too"},
		"postgres_fdw": {Name: "postgres_fdw", Options: map[string]string{"x": "y"}, Grants: map[string][]string{"app": {"usage"}, "none": {}}},
	})
	live.Wrappers["altered_too"] = &LiveWrapper{Owner: "owner"}
	d.Extensions = []*schema.Extension{{Name: "postgres_fdw"}}
	d.Servers = map[string]*schema.Server{"s": {Name: "s", Wrapper: "created"}}
	if sql, _ := planSQL(t, live, d, Options{}); !strings.Contains(sql, `create foreign data wrapper "created"`) {
		t.Fatalf("the plan must have something to simulate:\n%s", sql)
	}
	after := Simulate(live, d)
	if sql, warns := planSQL(t, after, d, pruneAll); sql != "" || len(warns) > 0 {
		t.Errorf("a plan against the state the first one leaves must be empty, got:\n%s%v", sql, warns)
	}
	if live.Wrappers["created"] != nil || live.Wrappers["altered"].Validator != "" || live.ExtensionMembers["fdw:postgres_fdw"] {
		t.Errorf("Simulate must not modify its input")
	}
}
