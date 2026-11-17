package pull

import (
	"encoding/json"
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
	tb := d.Tables["public.spatial_ref_sys"]
	if tb == nil || !tb.AttributeOnly() || !reflect.DeepEqual(tb.Grants, map[string][]string{"app": {"insert"}}) {
		t.Errorf("the extension's table must be pulled without columns: %+v\n%s", tb, out)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestPullExtensionTablesSequencesAggregates(t *testing.T) {
	live := memberLive()
	live.Sequences = map[string]bool{"public.topology_seq": true, "public.untouched_seq": true}
	live.SequenceDefs = map[string]*diff.LiveSequence{
		"public.topology_seq":  {Type: "bigint", Increment: "1", MinValue: "1", MaxValue: "9223372036854775807", Start: "1", Cache: "1"},
		"public.untouched_seq": {Type: "integer", Increment: "1", MinValue: "1", MaxValue: "2147483647", Start: "1", Cache: "1"},
	}
	live.ExtensionMembers["rel:public.topology_seq"] = true
	live.ExtensionMembers["rel:public.untouched_seq"] = true
	live.TableGrants["public.topology_seq"] = map[string]map[string]bool{"app": {"usage": true}}

	live.Tables["public.layer"] = &diff.LiveTable{RLSEnabled: true, Policies: map[string]*diff.LivePolicy{"own": {Cmd: "select", Using: "(owner = CURRENT_USER)"}}}
	live.ExtensionMembers["rel:public.layer"] = true
	live.InitPrivs["rel:public.layer"] = map[string]map[string]bool{"public": {"select": true}}
	live.TableGrants["public.layer"] = map[string]map[string]bool{"public": {"select": true}}
	live.Tables["public.config"] = &diff.LiveTable{}
	live.ExtensionMembers["rel:public.config"] = true
	live.ColumnGrants = map[string]map[string]map[string]map[string]bool{"public.config": {"name": {"app": {"select": true}}}}

	live.AggregateDefs = map[string]*diff.LiveAggregate{}
	for _, sig := range []string{"public.st_union(geometry)", "public.st_extent(geometry)", "public.percentile(double precision ORDER BY anyelement)"} {
		norm := strings.Replace(sig, "ORDER BY", "order by", 1)
		live.AggregateDefs[norm] = &diff.LiveAggregate{SFunc: "f", SType: "internal", Parallel: "unsafe"}
		live.FunctionDefs[norm] = &diff.LiveFunction{Signature: sig, Kind: "a"}
		live.FunctionPublicExec[norm] = true
		live.ExtensionMembers["proc:"+norm] = true
	}
	union := "public.st_union(geometry)"
	live.FunctionPublicExec[union] = false
	live.FunctionGrants[union] = map[string]map[string]bool{"app": {"execute": true}}
	pct := "public.percentile(double precision order by anyelement)"
	live.FunctionGrants[pct] = map[string]map[string]bool{"app": {"execute": true}}

	out, d, warns := buildSingle(t, live)
	if sq := d.Sequences["public.topology_seq"]; sq == nil || sq.DeclaresOptions() || !reflect.DeepEqual(sq.Grants, map[string][]string{"app": {"usage"}}) {
		t.Errorf("the extension's sequence must be pulled without options: %+v\n%s", sq, out)
	}
	if len(d.Sequences) != 1 {
		t.Errorf("sequences with the privileges the extension set are left out:\n%s", out)
	}
	tb := d.Tables["public.layer"]
	if tb == nil || !tb.AttributeOnly() || tb.Grants != nil || tb.RowLevelSecurity == nil || !*tb.RowLevelSecurity || len(tb.Policies) != 1 {
		t.Errorf("row level security and policies of an extension's table are pulled: %+v\n%s", tb, out)
	}
	if d.Tables["public.config"] != nil {
		t.Errorf("a table without changes is left out:\n%s", out)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "table public.config belongs to an extension and has column privileges") {
		t.Errorf("want a warning about the column privileges, got %v", warns)
	}
	ag := d.Aggregates["public.st_union(geometry)"]
	if ag == nil || !ag.AttributeOnly() || !ag.RevokePublic || !reflect.DeepEqual(ag.Grants, map[string][]string{"app": {"execute"}}) {
		t.Errorf("the extension's aggregate must be pulled without sfunc and stype: %+v\n%s", ag, out)
	}
	if ag := d.Aggregates["public.percentile(double precision order by anyelement)"]; ag == nil || !ag.AttributeOnly() || ag.RevokePublic {
		t.Errorf("an ordered-set aggregate is pulled with its arguments: %v\n%s", d.Aggregates, out)
	}
	if len(d.Aggregates) != 2 {
		t.Errorf("aggregates with the privileges the extension set are left out:\n%s", out)
	}
}

func TestPullExtensionSchemas(t *testing.T) {
	live := memberLive()
	live.Schemas = map[string]bool{"public": true, "pdb": true, "pgivm": true}
	live.SchemaComments = map[string]string{"pdb": "pg_search objects"}
	live.SchemaGrants = map[string]map[string]map[string]bool{
		"pdb":   {"public": {"usage": true}, "app": {"usage": true}},
		"pgivm": {"public": {"usage": true}},
	}
	live.Owners = map[string]string{"schema:pdb": "someone"}
	for _, s := range []string{"pdb", "pgivm"} {
		live.ExtensionMembers["schema:"+s] = true
		live.InitPrivs["schema:"+s] = map[string]map[string]bool{"public": {"usage": true}}
	}
	res, err := Build(live, Options{Single: true, Owners: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Files[0].Content)
	if !strings.Contains(out, "schema pdb:\n  grants: {app: [usage]}\n") {
		t.Errorf("an extension's schema is written with the privileges that changed only:\n%s", out)
	}
	if strings.Contains(out, "pgivm") || strings.Contains(out, "pg_search objects") || strings.Contains(out, "someone") {
		t.Errorf("an extension's schema is not declared as the application's:\n%s", out)
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

func TestPullExtensionConfigTableData(t *testing.T) {
	live := memberLive()
	live.Details.ColumnOrder = map[string][]string{}
	member := func(fq string, cols ...string) *diff.LiveTable {
		lt := &diff.LiveTable{Columns: map[string]*diff.LiveColumn{}, HasPK: true, ConstraintDefs: map[string]string{}, Constraints: map[string]bool{}}
		for _, c := range cols {
			lt.Columns[c] = &diff.LiveColumn{Type: "integer"}
		}
		live.Tables[fq] = lt
		live.Details.ColumnOrder[fq] = cols
		live.ExtensionMembers["rel:"+fq] = true
		return lt
	}
	topo := member("public.topology", "id", "srid")
	topo.ConstraintDefs["topology_pkey"] = "PRIMARY KEY (id)"
	layer := member("public.layer", "topology_id", "layer_id")
	layer.ConstraintDefs["layer_pkey"] = "PRIMARY KEY (topology_id, layer_id)"
	layer.ConstraintDefs["layer_topology_id_fkey"] = "FOREIGN KEY (topology_id) REFERENCES topology(id)"
	other := member("public.lookup", "id")
	other.ConstraintDefs["lookup_pkey"] = "PRIMARY KEY (id)"
	live.ExtensionConfig = map[string]string{"public.topology": "", "public.layer": ""}

	res, err := Build(live, Options{Single: true, Data: map[string][]map[string]any{
		"public.topology": {{"id": json.Number("1"), "srid": json.Number("4326")}},
		"public.layer":    {},
		"public.lookup":   {{"id": json.Number("1")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Files[0].Content)
	for _, want := range []string{
		"table topology:\n    data:\n      key: [id]\n      rows:\n        - {id: 1, srid: 4326}\n",
		"table layer:\n    data:\n      key: [topology_id, layer_id]\n      rows: []\n    dependsOn: [table public.topology]\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "lookup") {
		t.Errorf("the rows of a table that is not a configuration table are the extension's:\n%s", out)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "table public.lookup belongs to an extension and is not one of its configuration tables") {
		t.Errorf("want a warning about public.lookup, got %v", res.Warnings)
	}
}
