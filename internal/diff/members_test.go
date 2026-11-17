package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func memberLive() *Live {
	live := emptyLive()
	live.CurrentUser = "owner"
	live.Schemas["public"] = true
	live.ExtensionMembers = map[string]bool{}
	live.InitPrivs = map[string]map[string]map[string]bool{}
	live.TypeGrants = map[string]map[string]map[string]bool{}
	live.TypePublicRevoked = map[string]bool{}
	live.BaseTypes = map[string]bool{}
	return live
}

// addMemberFunction adds a function an extension owns, with PostgreSQL's
// default privileges, and returns its normalized signature.
func addMemberFunction(live *Live, sig string) string {
	norm := normalizeFunctionSignature(sig)
	live.Functions[sig] = true
	live.FunctionDefs[norm] = &LiveFunction{Signature: sig, Kind: "f", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Language: "c"}
	live.FunctionPublicExec[norm] = true
	live.ExtensionMembers["proc:"+norm] = true
	return norm
}

func planSQL(t *testing.T, live *Live, d *schema.Database, opts Options) (string, []Warning) {
	t.Helper()
	p := PlanWith(live, d, opts)
	return Render(p), p.Warnings
}

func wantSQL(t *testing.T, sql string, want ...string) {
	t.Helper()
	if got := strings.TrimSpace(sql); got != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestAttributeOnlyFunctionMatchesArgumentTypes(t *testing.T) {
	live := memberLive()
	norm := addMemberFunction(live, "public.each(hs hstore, OUT key text, OUT value text)")
	live.FunctionDefs[norm].Rows = 1000
	d := &schema.Database{Functions: map[string]*schema.Function{
		"public.each(hstore)": {Schema: "public", Name: "each", ArgsSig: "(hstore)", Rows: fptr(50), Comment: "pairs", Grants: map[string][]string{"reader": {"execute"}}},
	}}
	sql, warns := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, sql,
		`alter function "public"."each"(hs hstore, OUT key text, OUT value text) rows 50;`,
		`grant execute on function "public"."each"(hs hstore, OUT key text, OUT value text) to "reader";`,
		`comment on function "public"."each"(hs hstore, OUT key text, OUT value text) is 'pairs';`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if _, ok := d.Functions["public.each(hstore)"]; !ok || len(d.Functions) != 1 {
		t.Errorf("the declared schema must not be modified: %v", d.Functions)
	}

	live.FunctionDefs[norm].Rows = 50
	live.FunctionComments[norm] = "pairs"
	live.FunctionGrants[norm] = map[string]map[string]bool{"reader": {"execute": true}}
	if sql, _ := planSQL(t, live, d, Options{Unsafe: true, Prune: true}); sql != "" {
		t.Errorf("a function in the declared state must be skipped, got:\n%s", sql)
	}
}

func TestAttributeOnlyFunctionOtherArgumentNames(t *testing.T) {
	live := memberLive()
	addMemberFunction(live, "public.crosstab(source_sql text, n integer)")
	d := &schema.Database{Functions: map[string]*schema.Function{
		"public.crosstab(q text, cols int)": {Schema: "public", Name: "crosstab", ArgsSig: "(q text, cols int)", Volatility: "stable"},
	}}
	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql, `alter function "public"."crosstab"(source_sql text, n integer) stable;`)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestAttributeOnlyFunctionAbsentWarns(t *testing.T) {
	d := &schema.Database{Functions: map[string]*schema.Function{
		"public.digest(text, text)": {Schema: "public", Name: "digest", ArgsSig: "(text, text)", Leakproof: bptr(true), RevokePublic: true, Owner: "app", Comment: "hash", Grants: map[string][]string{"app": {"execute"}}},
	}}
	sql, warns := planSQL(t, memberLive(), d, Options{})
	if sql != "" {
		t.Errorf("nothing can be done for a function that does not exist, got:\n%s", sql)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "function public.digest(text, text) has no body") {
		t.Errorf("want one warning naming the declaration, got %v", warns)
	}
}

func TestAttributeOnlyFunctionAfterExtensionCreate(t *testing.T) {
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "citext", Schema: "public"}},
		Functions: map[string]*schema.Function{
			"public.citext_eq(citext, citext)": {Schema: "public", Name: "citext_eq", ArgsSig: "(citext, citext)", Leakproof: bptr(true), Security: "definer",
				Set: map[string]string{"search_path": "public"}, Grants: map[string][]string{"app": {"execute"}, "other": {}, "public": {}}},
		},
	}
	sql, warns := planSQL(t, memberLive(), d, Options{})
	wantSQL(t, sql,
		`create extension "citext" schema "public";`,
		`alter function "public"."citext_eq"(citext, citext) security definer leakproof set search_path = public;`,
		`revoke all on function "public"."citext_eq"(citext, citext) from "other";`,
		`revoke all on function "public"."citext_eq"(citext, citext) from public;`,
		`grant execute on function "public"."citext_eq"(citext, citext) to "app";`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestAttributeOnlyFunctionAfterExtensionUpdate(t *testing.T) {
	live := memberLive()
	live.Extensions["hstore"] = true
	live.ExtensionDefs = map[string]*LiveExtension{"hstore": {Schema: "public", Version: "1.7"}}
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "hstore", Version: "1.8"}},
		Functions: map[string]*schema.Function{
			"public.hstore_hash_extended(hstore, bigint)": {Schema: "public", Name: "hstore_hash_extended", ArgsSig: "(hstore, bigint)", Parallel: "safe"},
		},
	}
	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`alter extension "hstore" update to '1.8';`,
		`alter function "public"."hstore_hash_extended"(hstore, bigint) parallel safe;`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestAttributeOnlyAlterPrecedesDependentCreates(t *testing.T) {
	live := memberLive()
	addMemberFunction(live, "public.unaccent(text)")
	live.Tables["public.other"] = newLiveTable()
	d := &schema.Database{
		Functions: map[string]*schema.Function{
			"public.unaccent(text)": {Schema: "public", Name: "unaccent", ArgsSig: "(text)", Volatility: "immutable"},
		},
		Tables: map[string]*schema.Table{
			"public.doc": {Name: "doc", Columns: map[string]*schema.Column{"title": {Type: "text"}},
				Indexes: []*schema.Index{{Name: "doc_unaccent", Columns: []string{"unaccent(title)"}}}},
		},
	}
	p := PlanWith(live, d, Options{})
	alter, table := -1, -1
	for i, s := range p.Creates {
		switch {
		case strings.HasPrefix(s, `alter function "public"."unaccent"(text) immutable`):
			alter = i
		case strings.HasPrefix(s, "create table"):
			table = i
		}
	}
	if alter < 0 || table < 0 || alter > table {
		t.Errorf("the function has to be immutable before the index that needs it is built; creates: %v alters: %v", p.Creates, p.Alters)
	}
}

func TestAttributeOnlyProcedure(t *testing.T) {
	live := memberLive()
	sig := "public.tidy(a integer, OUT b integer)"
	norm := normalizeFunctionSignature(sig)
	live.Procedures[norm] = true
	live.ProcedureDefs[norm] = &LiveProcedure{Signature: sig, Security: "invoker", Language: "plpgsql", Body: "begin b := a; end"}
	live.FunctionPublicExec[norm] = true
	d := &schema.Database{Procedures: map[string]*schema.Procedure{
		"public.tidy(integer, out integer)": {Schema: "public", Name: "tidy", ArgsSig: "(integer, out integer)", Security: "definer",
			Set: map[string]string{"search_path": "public"}, RevokePublic: true, Grants: map[string][]string{"app": {"execute"}}},
	}}
	sql, warns := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, sql,
		`alter procedure "public"."tidy"(a integer, OUT b integer) security definer set search_path = public;`,
		`revoke all on procedure "public"."tidy"(a integer, OUT b integer) from public;`,
		`grant execute on procedure "public"."tidy"(a integer, OUT b integer) to "app";`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}

	live.ProcedureDefs[norm].Security = "definer"
	live.ProcedureDefs[norm].Config = map[string]string{"search_path": "public"}
	live.FunctionPublicExec[norm] = false
	live.FunctionGrants[norm] = map[string]map[string]bool{"app": {"execute": true}}
	if sql, _ := planSQL(t, live, d, Options{Unsafe: true, Prune: true}); sql != "" {
		t.Errorf("a procedure in the declared state must be skipped, got:\n%s", sql)
	}

	sql, warns = planSQL(t, memberLive(), d, Options{Unsafe: true})
	if sql != "" || len(warns) != 1 || !strings.Contains(warns[0].Message, "procedure public.tidy(integer, out integer) has no body") {
		t.Errorf("a procedure that does not exist is never created; sql:\n%s\nwarnings: %v", sql, warns)
	}
}

func TestExtensionMemberGrantsKeepInitialGrantees(t *testing.T) {
	live := memberLive()
	norm := addMemberFunction(live, "public.pg_buffercache_pages()")
	live.FunctionPublicExec[norm] = false
	live.InitPrivs["proc:"+norm] = map[string]map[string]bool{"pg_monitor": {"execute": true}}
	live.FunctionGrants[norm] = map[string]map[string]bool{"pg_monitor": {"execute": true}, "stray": {"execute": true}}
	fn := &schema.Function{Schema: "public", Name: "pg_buffercache_pages", ArgsSig: "()", Grants: map[string][]string{"mon": {"execute"}}}
	d := &schema.Database{Functions: map[string]*schema.Function{"public.pg_buffercache_pages()": fn}}

	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`revoke execute on function "public"."pg_buffercache_pages"() from "stray";`,
		`grant execute on function "public"."pg_buffercache_pages"() to "mon";`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}

	live.FunctionGrants[norm] = map[string]map[string]bool{"pg_monitor": {"execute": true}, "mon": {"execute": true}}
	if sql, _ := planSQL(t, live, d, Options{}); sql != "" {
		t.Errorf("what the extension granted must be left alone, got:\n%s", sql)
	}

	fn.Grants["pg_monitor"] = []string{}
	sql, _ = planSQL(t, live, d, Options{})
	wantSQL(t, sql, `revoke execute on function "public"."pg_buffercache_pages"() from "pg_monitor";`)

	delete(live.ExtensionMembers, "proc:"+norm)
	delete(fn.Grants, "pg_monitor")
	sql, _ = planSQL(t, live, d, Options{})
	wantSQL(t, sql, `revoke execute on function "public"."pg_buffercache_pages"() from "pg_monitor";`)
}

func TestExtensionMemberViewGrantsKeepInitialPublic(t *testing.T) {
	live := memberLive()
	live.Views["public.pg_stat_statements"] = true
	live.ExtensionMembers["rel:public.pg_stat_statements"] = true
	live.InitPrivs["rel:public.pg_stat_statements"] = map[string]map[string]bool{"public": {"select": true}}
	live.TableGrants["public.pg_stat_statements"] = map[string]map[string]bool{"public": {"select": true}}
	vw := &schema.View{Schema: "public", Name: "pg_stat_statements", Grants: map[string][]string{"mon": {"select"}}}
	d := &schema.Database{Views: map[string]*schema.View{"public.pg_stat_statements": vw}}

	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql, `grant select on table "public"."pg_stat_statements" to "mon";`)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}

	live.TableGrants["public.pg_stat_statements"]["public"]["insert"] = true
	sql, warns = planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`revoke insert on table "public"."pg_stat_statements" from public;`,
		`grant select on table "public"."pg_stat_statements" to "mon";`,
	)
	if len(warns) != 1 || warns[0].Kind != "public_revoke" {
		t.Errorf("want one public_revoke warning, got %v", warns)
	}

	sql, _ = planSQL(t, live, d, Options{PublicGrants: PublicAuthoritative})
	if !strings.Contains(sql, `revoke insert, select on table "public"."pg_stat_statements" from public;`) {
		t.Errorf("public_grants: authoritative covers PUBLIC on extension members too, got:\n%s", sql)
	}

	vw.Grants["public"] = []string{}
	sql, warns = planSQL(t, live, d, Options{})
	if !strings.Contains(sql, `revoke insert, select on table "public"."pg_stat_statements" from public;`) || len(warns) > 0 {
		t.Errorf("a named grantee is made exact; sql:\n%s\nwarnings: %v", sql, warns)
	}
}

func TestAttributeOnlyViewAbsentOrPending(t *testing.T) {
	d := &schema.Database{Views: map[string]*schema.View{
		"public.pg_stat_statements": {Schema: "public", Name: "pg_stat_statements", Comment: "stats", Grants: map[string][]string{"mon": {"select"}, "public": {}}},
	}}
	sql, warns := planSQL(t, memberLive(), d, Options{})
	if sql != "" || len(warns) != 1 || !strings.Contains(warns[0].Message, "view public.pg_stat_statements has no query") {
		t.Errorf("a view that does not exist is never created; sql:\n%s\nwarnings: %v", sql, warns)
	}

	d.Extensions = []*schema.Extension{{Name: "pg_stat_statements"}}
	sql, warns = planSQL(t, memberLive(), d, Options{})
	wantSQL(t, sql,
		`create extension "pg_stat_statements";`,
		`revoke all on table "public"."pg_stat_statements" from public;`,
		`grant select on table "public"."pg_stat_statements" to "mon";`,
		`comment on view "public"."pg_stat_statements" is 'stats';`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}

	live := memberLive()
	live.MatViews["public.pg_stat_statements"] = true
	sql, warns = planSQL(t, live, d, Options{})
	if strings.Contains(sql, "pg_stat_statements\" ") || len(warns) != 1 || !strings.Contains(warns[0].Message, "is a materialized view in the database") {
		t.Errorf("a view declaration does not manage a materialized view; sql:\n%s\nwarnings: %v", sql, warns)
	}
}

func TestAttributeOnlyTypeGrants(t *testing.T) {
	live := memberLive()
	live.BaseTypes["public.hstore"] = true
	live.ExtensionMembers["type:public.hstore"] = true
	d := &schema.Database{Types: map[string]*schema.TypeDef{
		"public.hstore": {Schema: "public", Name: "hstore", RevokePublic: true, Owner: "app_owner", Comment: "key/value", Grants: map[string][]string{"app": {"usage"}}},
	}}
	sql, warns := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, sql,
		`alter type "public"."hstore" owner to "app_owner";`,
		`revoke usage on type "public"."hstore" from public;`,
		`grant usage on type "public"."hstore" to "app";`,
		`comment on type "public"."hstore" is 'key/value';`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}

	live.TypePublicRevoked["public.hstore"] = true
	live.TypeGrants["public.hstore"] = map[string]map[string]bool{"app": {"usage": true}}
	live.Owners = map[string]string{"type:public.hstore": "app_owner"}
	live.TypeComments = map[string]string{"public.hstore": "key/value"}
	if sql, _ := planSQL(t, live, d, Options{Unsafe: true, Prune: true}); sql != "" {
		t.Errorf("a type in the declared state must be skipped, got:\n%s", sql)
	}

	sql, warns = planSQL(t, memberLive(), d, Options{})
	if sql != "" || len(warns) != 1 || !strings.Contains(warns[0].Message, "type public.hstore has no type: key") {
		t.Errorf("a type that does not exist is never created; sql:\n%s\nwarnings: %v", sql, warns)
	}

	d.Extensions = []*schema.Extension{{Name: "hstore"}}
	d.Types["public.hstore"].Owner, d.Types["public.hstore"].Comment = "", ""
	sql, _ = planSQL(t, memberLive(), d, Options{})
	wantSQL(t, sql,
		`create extension "hstore";`,
		`revoke usage on type "public"."hstore" from public;`,
		`grant usage on type "public"."hstore" to "app";`,
	)
}

func TestAttributeOnlyDuplicateDeclarationWarns(t *testing.T) {
	live := memberLive()
	addMemberFunction(live, "public.each(hs hstore, OUT key text, OUT value text)")
	d := &schema.Database{Functions: map[string]*schema.Function{
		"public.each(hs hstore, OUT key text, OUT value text)": {Schema: "public", Name: "each", ArgsSig: "(hs hstore, OUT key text, OUT value text)", Rows: fptr(10)},
		"public.each(hstore)": {Schema: "public", Name: "each", ArgsSig: "(hstore)", Rows: fptr(50)},
	}}
	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql, `alter function "public"."each"(hs hstore, OUT key text, OUT value text) rows 10;`)
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "function public.each(hstore) is the function declared as") {
		t.Errorf("want a warning about the second declaration, got %v", warns)
	}
}

func TestAttributeOnlyFunctionNotPruned(t *testing.T) {
	live := memberLive()
	live.AggregateDefs = map[string]*LiveAggregate{}
	sig := "public.score(a integer)"
	norm := normalizeFunctionSignature(sig)
	live.Functions[sig] = true
	live.FunctionDefs[norm] = &LiveFunction{Signature: sig, Kind: "f", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Language: "sql"}
	live.FunctionPublicExec[norm] = true
	d := &schema.Database{Functions: map[string]*schema.Function{
		"public.score(integer)": {Schema: "public", Name: "score", ArgsSig: "(integer)", Parallel: "safe"},
	}}
	sql, _ := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, sql, `alter function "public"."score"(a integer) parallel safe;`)
}

func TestSimulateKeepsMembersOfCreatedExtensionPending(t *testing.T) {
	prev := &schema.Database{Extensions: []*schema.Extension{{Name: "citext", Schema: "public"}}}
	cur := &schema.Database{
		Extensions: prev.Extensions,
		Functions: map[string]*schema.Function{
			"public.citext_eq(citext, citext)": {Schema: "public", Name: "citext_eq", ArgsSig: "(citext, citext)", Leakproof: bptr(true)},
		},
	}
	live := Simulate(memberLive(), prev)
	if le := live.ExtensionDefs["citext"]; le == nil || !le.Created {
		t.Fatalf("an extension created by an earlier step must be marked: %+v", le)
	}
	sql, warns := planSQL(t, live, cur, Options{})
	wantSQL(t, sql, `alter function "public"."citext_eq"(citext, citext) leakproof;`)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if sql, _ := planSQL(t, Simulate(live, cur), cur, Options{}); sql != "" {
		t.Errorf("the following step must not repeat it, got:\n%s", sql)
	}
}

func TestAttributeOnlyPendingPublicIsGranted(t *testing.T) {
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "pg_buffercache"}},
		Functions: map[string]*schema.Function{
			"public.pg_buffercache_pages()": {Schema: "public", Name: "pg_buffercache_pages", ArgsSig: "()", Grants: map[string][]string{"public": {"execute"}}},
		},
		Types: map[string]*schema.TypeDef{
			"public.hstore": {Schema: "public", Name: "hstore", Grants: map[string][]string{"public": {"usage"}}},
		},
	}
	sql, _ := planSQL(t, memberLive(), d, Options{})
	wantSQL(t, sql,
		`create extension "pg_buffercache";`,
		`grant usage on type "public"."hstore" to public;`,
		`grant execute on function "public"."pg_buffercache_pages"() to public;`,
	)
}

func TestDependencyMemberKey(t *testing.T) {
	for dep, want := range map[string]string{
		"function public.digest(text, text)":     "proc:public.digest(text, text)",
		"procedure public.tidy(a integer)":       "proc:public.tidy(a int)",
		"view public.pg_stat_statements":         "rel:public.pg_stat_statements",
		"materialized view public.mv":            "rel:public.mv",
		"table public.spatial_ref_sys":           "rel:public.spatial_ref_sys",
		"sequence public.s":                      "rel:public.s",
		"text search dictionary public.unaccent": "",
	} {
		if got := dependencyMemberKey(dep); got != want {
			t.Errorf("dependencyMemberKey(%q) = %q, want %q", dep, got, want)
		}
	}
}

func TestInitialGrants(t *testing.T) {
	live := memberLive()
	live.ExtensionMembers["proc:public.f()"] = true
	live.ExtensionMembers["proc:public.g()"] = true
	live.ExtensionMembers["rel:public.v"] = true
	live.InitPrivs["proc:public.g()"] = map[string]map[string]bool{}
	for _, c := range []struct {
		key, publicDefault string
		want               map[string]map[string]bool
		ok                 bool
	}{
		{"proc:public.f()", "execute", map[string]map[string]bool{"public": {"execute": true}}, true},
		{"proc:public.g()", "execute", map[string]map[string]bool{}, true},
		{"rel:public.v", "", nil, true},
		{"proc:public.mine()", "execute", nil, false},
	} {
		got, ok := live.InitialGrants(c.key, c.publicDefault)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("InitialGrants(%q, %q) = %v, %v; want %v, %v", c.key, c.publicDefault, got, ok, c.want, c.ok)
		}
	}
	var none *Live
	if _, ok := none.InitialGrants("proc:public.f()", "execute"); ok {
		t.Error("a nil Live has no extension members")
	}
}

func TestGrantChanges(t *testing.T) {
	initial := map[string]map[string]bool{"public": {"select": true}, "pg_monitor": {"select": true}, "same": {"select": true, "insert*": true}}
	current := map[string]map[string]bool{"public": {"select": true, "insert": true}, "app": {"select*": true}, "same": {"insert*": true, "select": true}}
	want := map[string]map[string]bool{"public": {"select": true, "insert": true}, "app": {"select*": true}, "pg_monitor": {}}
	if got := GrantChanges(current, initial); !reflect.DeepEqual(got, want) {
		t.Errorf("GrantChanges = %v, want %v", got, want)
	}
	if got := GrantChanges(initial, initial); got != nil {
		t.Errorf("an unchanged ACL has no changes, got %v", got)
	}
	if got := GrantChanges(nil, nil); got != nil {
		t.Errorf("two empty ACLs have no changes, got %v", got)
	}
}

func TestWithoutInitialGrants(t *testing.T) {
	initial := map[string]map[string]bool{"public": {"select": true}, "pg_monitor": {"select*": true}}
	liveG := map[string]map[string]bool{"public": {"select": true, "insert": true}, "pg_monitor": {"select": true}, "app": {"select": true}}
	got := withoutInitialGrants(liveG, initial, map[string][]string{"app": {"select"}}, false)
	want := map[string]map[string]bool{"public": {"insert": true}, "app": {"select": true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unnamed grantees keep what the extension granted: got %v, want %v", got, want)
	}
	got = withoutInitialGrants(liveG, initial, map[string][]string{"pg_monitor": {}}, true)
	want = map[string]map[string]bool{"public": {"select": true, "insert": true}, "pg_monitor": {"select": true}, "app": {"select": true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a named grantee and a covered PUBLIC are compared in full: got %v, want %v", got, want)
	}
}
