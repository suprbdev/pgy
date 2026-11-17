package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func ownerPruneLive() *Live {
	live := pruneLive()
	live.CurrentUser = "app"
	live.OwnerRoles = map[string]bool{"app": true}
	live.EventTriggers = map[string]*LiveEventTrigger{
		"platform_watch": {Event: "ddl_command_end", Function: "app.platform_fn", Owner: "platform"},
		"app_watch":      {Event: "ddl_command_end", Function: "app.app_fn", Owner: "app"},
	}
	live.Publications = map[string]*LivePublication{"platform_pub": {Owner: "platform"}, "app_pub": {Owner: "app"}}
	live.Extensions["pg_stat_statements"], live.Extensions["hstore"] = true, true
	live.ExtensionDefs = map[string]*LiveExtension{
		"pg_stat_statements": {Schema: "app", Owner: "platform", Dependents: []ExtensionDependent{}},
		"hstore":             {Schema: "app", Owner: "app", Dependents: []ExtensionDependent{}},
	}
	live.Owners = map[string]string{}
	live.Servers = map[string]*LiveServer{"platform_srv": {Wrapper: "w", Owner: "platform", UserMappings: []string{"app"}}, "app_srv": {Wrapper: "w", Owner: "app"}}
	for _, k := range []string{"rel:app.gone", "rel:app.v_gone", "rel:app.seq_gone", "type:app.t_gone", "proc:" + normalizeFunctionSignature("app.f_gone(a integer, b text)")} {
		live.Owners[k] = "platform"
	}
	live.Owners["rel:app.keep"] = "platform"
	live.Owners["type:app.dom_gone"], live.Owners["proc:app.p_gone()"] = "app", "app"
	return live
}

func ownerPruneDesired() *schema.Database {
	d := pruneDesired()
	d.EventTriggers, d.Publications, d.Servers = map[string]*schema.EventTrigger{}, map[string]*schema.Publication{}, map[string]*schema.Server{}
	d.Extensions = []*schema.Extension{}
	return d
}

func TestPruneLeavesObjectsOfOtherOwners(t *testing.T) {
	p := PlanWith(ownerPruneLive(), ownerPruneDesired(), Options{Unsafe: true, Prune: true})
	want := []string{
		`drop index "app"."mv_old_idx";`,
		`drop materialized view "app"."mv_gone";`,
		`drop event trigger "app_watch";`,
		`drop publication "app_pub";`,
		`drop procedure "app"."p_gone"();`,
		`drop domain "app"."dom_gone";`,
		`drop server "app_srv";`,
		`drop extension "hstore";`,
	}
	if got := strings.Join(p.Drops, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("drops:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	wantWarnings := []string{
		"event trigger platform_watch is owned by platform",
		"publication platform_pub is owned by platform",
		"table app.keep is owned by platform",
		"view app.v_gone is owned by platform",
		"table app.gone is owned by platform",
		"sequence app.seq_gone is owned by platform",
		"type app.t_gone is owned by platform",
		"function app.f_gone(a integer, b text) is owned by platform",
		"server platform_srv is owned by platform",
		"extension pg_stat_statements is owned by platform",
	}
	got := []string{}
	for _, w := range p.Warnings {
		if w.Kind == "not_owner" {
			got = append(got, w.Message)
		}
	}
	if len(got) != len(wantWarnings) {
		t.Fatalf("want %d not_owner warnings, got %d: %v", len(wantWarnings), len(got), got)
	}
	for i, w := range wantWarnings {
		if !strings.HasPrefix(got[i], w) || !strings.Contains(got[i], "app, the connecting role, does not have that role's privileges") {
			t.Errorf("warning %d: want %q..., got %q", i, w, got[i])
		}
	}
}

func TestPruneOwnersUnknownOrHeld(t *testing.T) {
	live := ownerPruneLive()
	live.OwnerRoles["platform"] = true
	held := PlanWith(live, ownerPruneDesired(), Options{Unsafe: true, Prune: true})
	live.OwnerRoles = nil
	unknown := PlanWith(live, ownerPruneDesired(), Options{Unsafe: true, Prune: true})
	for _, p := range []*PlanDiff{held, unknown} {
		got := strings.Join(p.Drops, "\n")
		for _, stmt := range []string{`drop event trigger "platform_watch";`, `drop table "app"."gone";`, `drop extension "hstore", "pg_stat_statements";`, `drop user mapping for "app" server "platform_srv";`} {
			if !strings.Contains(got, stmt) {
				t.Errorf("a role with the owner's privileges, or an unknown role set, drops everything; missing %s in:\n%s", stmt, got)
			}
		}
		for _, w := range p.Warnings {
			if w.Kind == "not_owner" {
				t.Errorf("unexpected warning: %s", w.Message)
			}
		}
	}
}

func TestPruneKeepsRoutineAnEventTriggerUses(t *testing.T) {
	live := emptyLive()
	live.Schemas["public"] = true
	live.Tables["public.t"] = newLiveTable()
	live.Tables["public.t"].Columns["id"] = &LiveColumn{Type: "integer"}
	sig := "public.ddl_watch()"
	live.Functions[sig] = true
	live.FunctionDefs[normalizeFunctionSignature(sig)] = &LiveFunction{Signature: sig, Kind: "f", Language: "plpgsql", Dependents: []ExtensionDependent{
		newExtensionDependent(dependentAddress{kind: "event trigger", names: []string{"ddl_watch"}}, "event trigger ddl_watch"),
	}}
	live.EventTriggers = map[string]*LiveEventTrigger{"ddl_watch": {Event: "ddl_command_end", Function: "public.ddl_watch"}}
	d := &schema.Database{Tables: map[string]*schema.Table{"public.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "integer"}}}}}
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if len(p.Drops) != 0 {
		t.Errorf("a routine an unmanaged event trigger uses is kept, got %v", p.Drops)
	}
	if len(p.Warnings) != 1 || p.Warnings[0].Kind != "dependents" || !strings.Contains(p.Warnings[0].Message, "function public.ddl_watch() is not declared but still in use (event trigger ddl_watch), so --prune keeps it") {
		t.Errorf("want one dependents warning naming the event trigger, got %+v", p.Warnings)
	}

	d.EventTriggers = map[string]*schema.EventTrigger{}
	p = PlanWith(live, d, Options{Unsafe: true, Prune: true})
	want := []string{`drop event trigger "ddl_watch";`, `drop function "public"."ddl_watch"();`}
	if strings.Join(p.Drops, "\n") != strings.Join(want, "\n") || len(p.Warnings) != 0 {
		t.Errorf("an event trigger the plan drops no longer keeps its function: %v %+v", p.Drops, p.Warnings)
	}
}

func TestPruneKeepsRoutinesKeptRoutinesUse(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	add := func(sig string, deps ...ExtensionDependent) {
		live.Functions[sig] = true
		live.FunctionDefs[normalizeFunctionSignature(sig)] = &LiveFunction{Signature: sig, Kind: "f", Language: "sql", Dependents: append([]ExtensionDependent{}, deps...)}
	}
	add("app.inner(a integer)", newExtensionDependent(dependentAddress{kind: "function", names: []string{"app", "outer"}, args: "a integer"}, "function app.outer(integer)"))
	add("app.outer(a integer)", newExtensionDependent(dependentAddress{kind: "table column", names: []string{"other", "t", "x"}}, "default value for column x of table other.t"))
	add("app.alone()")
	add("app.unknown()")
	live.FunctionDefs[normalizeFunctionSignature("app.unknown()")].Dependents = nil
	d := &schema.Database{Types: map[string]*schema.TypeDef{"app.declared": {Name: "declared", Kind: "enum", Labels: []string{"a"}}}}
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if want := `drop function "app"."alone"(), "app"."unknown"();`; len(p.Drops) != 1 || p.Drops[0] != want {
		t.Errorf("only the routines nothing kept uses, or whose dependents are unknown, are dropped: %v", p.Drops)
	}
	kept := []string{}
	for _, w := range p.Warnings {
		if w.Kind == "dependents" {
			kept = append(kept, w.Message)
		}
	}
	if len(kept) != 2 || !strings.HasPrefix(kept[0], "function app.inner(a integer) is not declared but still in use (function app.outer(integer))") || !strings.HasPrefix(kept[1], "function app.outer(a integer) is not declared but still in use (default value for column x of table other.t)") {
		t.Errorf("both kept routines are reported, the inner one for the outer one: %v", kept)
	}
}
