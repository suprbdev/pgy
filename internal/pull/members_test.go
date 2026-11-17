package pull

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func memberLive() *diff.Live {
	live := routineLive()
	live.Schemas["public"] = true
	live.Extensions = map[string]bool{"pgcrypto": true}
	live.ExtensionMembers = map[string]bool{}
	live.InitPrivs = map[string]map[string]map[string]bool{}
	live.FunctionGrants = map[string]map[string]map[string]bool{}
	live.TableGrants = map[string]map[string]map[string]bool{}
	live.TypeGrants = map[string]map[string]map[string]bool{}
	live.TypePublicRevoked = map[string]bool{}
	live.Views = map[string]bool{}
	live.MatViews = map[string]bool{}
	live.Types = map[string]bool{}
	live.BaseTypes = map[string]bool{}
	live.Tables = map[string]*diff.LiveTable{}
	live.Details.ViewQueries = map[string]string{}
	return live
}

func addMemberFunction(live *diff.Live, sig string) {
	name, args, _ := strings.Cut(strings.TrimPrefix(sig, "public."), "(")
	live.Functions[sig] = true
	live.FunctionDefs[sig] = &diff.LiveFunction{Signature: sig, Kind: "f", Language: "c", Volatility: "volatile", Security: "invoker", Parallel: "unsafe"}
	live.FunctionPublicExec[sig] = true
	live.Details.Routines[sig] = &diff.LiveRoutine{Schema: "public", Name: name, Arguments: strings.TrimSuffix(args, ")"), Returns: "integer", Language: "c"}
	live.ExtensionMembers["proc:"+sig] = true
}

func TestPullExtensionMemberPrivileges(t *testing.T) {
	live := memberLive()

	addMemberFunction(live, "public.untouched(text)")
	addMemberFunction(live, "public.digest(text, text)")
	live.FunctionPublicExec["public.digest(text, text)"] = false
	live.FunctionGrants["public.digest(text, text)"] = map[string]map[string]bool{"app": {"execute": true}}
	addMemberFunction(live, "public.pg_buffercache_pages()")
	live.InitPrivs["proc:public.pg_buffercache_pages()"] = map[string]map[string]bool{"pg_monitor": {"execute": true}}
	live.FunctionPublicExec["public.pg_buffercache_pages()"] = false
	live.FunctionGrants["public.pg_buffercache_pages()"] = map[string]map[string]bool{"mon": {"execute*": true}}
	addMemberFunction(live, "public.pg_buffercache_summary()")
	live.InitPrivs["proc:public.pg_buffercache_summary()"] = map[string]map[string]bool{"pg_monitor": {"execute": true}}
	live.FunctionPublicExec["public.pg_buffercache_summary()"] = false
	live.FunctionGrants["public.pg_buffercache_summary()"] = map[string]map[string]bool{"pg_monitor": {"execute": true}}

	for _, v := range []string{"public.pg_stat_statements", "public.pg_stat_statements_info"} {
		live.Views[v] = true
		live.Details.ViewQueries[v] = "SELECT 1"
		live.ExtensionMembers["rel:"+v] = true
		live.InitPrivs["rel:"+v] = map[string]map[string]bool{"public": {"select": true}}
	}
	live.TableGrants["public.pg_stat_statements"] = map[string]map[string]bool{"mon": {"select": true}}
	live.TableGrants["public.pg_stat_statements_info"] = map[string]map[string]bool{"public": {"select": true}}

	live.BaseTypes["public.hstore"] = true
	live.ExtensionMembers["type:public.hstore"] = true
	live.TypePublicRevoked["public.hstore"] = true
	live.TypeGrants["public.hstore"] = map[string]map[string]bool{"app": {"usage": true}}
	live.BaseTypes["public.citext"] = true
	live.ExtensionMembers["type:public.citext"] = true

	live.Tables["public.spatial_ref_sys"] = &diff.LiveTable{}
	live.ExtensionMembers["rel:public.spatial_ref_sys"] = true
	live.TableGrants["public.spatial_ref_sys"] = map[string]map[string]bool{"app": {"insert": true}}

	out, d, warns := buildSingle(t, live)
	f := d.Functions["public.digest(text, text)"]
	if f == nil || f.HasBody() || !f.RevokePublic || !reflect.DeepEqual(f.Grants, map[string][]string{"app": {"execute"}}) {
		t.Errorf("digest must be pulled without a body, with the privileges that changed: %+v\n%s", f, out)
	}
	f = d.Functions["public.pg_buffercache_pages()"]
	if f == nil || f.RevokePublic || !reflect.DeepEqual(f.Grants, map[string][]string{"mon": {"execute*"}, "pg_monitor": {}}) {
		t.Errorf("a grantee of the extension that lost its privilege is written with an empty list: %+v\n%s", f, out)
	}
	if len(d.Functions) != 2 {
		t.Errorf("functions with the privileges the extension set are left out:\n%s", out)
	}
	v := d.Views["public.pg_stat_statements"]
	if v == nil || v.Query != "" || !reflect.DeepEqual(v.Grants, map[string][]string{"mon": {"select"}, "public": {}}) {
		t.Errorf("the view must be pulled without a query: %+v\n%s", v, out)
	}
	if len(d.Views) != 1 {
		t.Errorf("views with the privileges the extension set are left out:\n%s", out)
	}
	td := d.Types["public.hstore"]
	if td == nil || td.Kind != "" || !td.RevokePublic || !reflect.DeepEqual(td.Grants, map[string][]string{"app": {"usage"}}) {
		t.Errorf("the type must be pulled without a kind: %+v\n%s", td, out)
	}
	if len(d.Types) != 1 {
		t.Errorf("types with the privileges the extension set are left out:\n%s", out)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "table public.spatial_ref_sys belongs to an extension") {
		t.Errorf("want one warning about the extension's table, got %v", warns)
	}
}

func TestPullExtensionMembersUnchanged(t *testing.T) {
	live := memberLive()
	addMemberFunction(live, "public.digest(text, text)")
	live.Views["public.pg_stat_statements"] = true
	live.Details.ViewQueries["public.pg_stat_statements"] = "SELECT 1"
	live.ExtensionMembers["rel:public.pg_stat_statements"] = true
	live.BaseTypes["public.hstore"] = true
	live.ExtensionMembers["type:public.hstore"] = true
	out, d, warns := buildSingle(t, live)
	if len(d.Functions)+len(d.Views)+len(d.Types) != 0 || len(warns) != 0 {
		t.Errorf("an extension's objects are not declared while their privileges are untouched:\n%s\nwarnings: %v", out, warns)
	}
}
