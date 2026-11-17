package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// addMemberTable adds a table an extension owns, with a primary key, an
// index, a check constraint and a rule, and returns it.
func addMemberTable(live *Live, fq, extension string) *LiveTable {
	lt := newLiveTable()
	lt.Columns = map[string]*LiveColumn{"srid": {Type: "integer"}, "auth_name": {Type: "character varying(256)", Nullable: true}}
	lt.HasPK = true
	lt.Constraints = map[string]bool{"srid_pkey": true, "srid_check": true}
	lt.ConstraintDefs = map[string]string{"srid_pkey": "PRIMARY KEY (srid)", "srid_check": "CHECK (srid > 0)"}
	lt.Indexes = map[string]bool{"srid_pkey": true, "auth_idx": true}
	lt.IndexDefs = map[string]*LiveIndex{"srid_pkey": {Constraint: true}, "auth_idx": {}}
	lt.Rules = map[string]*LiveRule{"protect": {Event: "delete", Instead: true}}
	live.Tables[fq] = lt
	live.ExtensionMembers["rel:"+fq] = true
	if live.MemberExtension == nil {
		live.MemberExtension = map[string]string{}
	}
	live.MemberExtension["rel:"+fq] = extension
	return lt
}

func TestTableAttributeOnlyGrants(t *testing.T) {
	live := memberLive()
	addMemberTable(live, "public.spatial_ref_sys", "postgis")
	live.InitPrivs["rel:public.spatial_ref_sys"] = map[string]map[string]bool{"public": {"select": true}}
	live.TableGrants["public.spatial_ref_sys"] = map[string]map[string]bool{"public": {"select": true}}
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.spatial_ref_sys": {Name: "public.spatial_ref_sys", Grants: map[string][]string{"app": {"select", "insert"}, "public": {}}, Comment: "srs"},
	}}
	sql, warns := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, sql,
		`revoke select on table "public"."spatial_ref_sys" from public;`,
		`grant insert, select on table "public"."spatial_ref_sys" to "app";`,
		`comment on table "public"."spatial_ref_sys" is 'srs';`,
	)
	for _, w := range warns {
		if w.Kind != "public_revoke" {
			t.Errorf("unexpected warning: %v", w)
		}
	}

	live.TableGrants["public.spatial_ref_sys"] = map[string]map[string]bool{"app": {"select": true, "insert": true}}
	live.RelComments = map[string]string{"public.spatial_ref_sys": "srs"}
	if sql, _ := planSQL(t, live, d, Options{Unsafe: true, Prune: true}); sql != "" {
		t.Errorf("a table in the declared state, its columns, keys, indexes and rules are left alone, got:\n%s", sql)
	}
}

func TestTableAttributeOnlyLeavesExtensionGrants(t *testing.T) {
	live := memberLive()
	addMemberTable(live, "public.layer", "postgis_topology")
	live.InitPrivs["rel:public.layer"] = map[string]map[string]bool{"public": {"select": true}}
	live.TableGrants["public.layer"] = map[string]map[string]bool{"public": {"select": true}, "old": {"select": true}}
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.layer": {Name: "public.layer", Grants: map[string][]string{"app": {"select"}}},
	}}
	sql, _ := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`revoke select on table "public"."layer" from "old";`,
		`grant select on table "public"."layer" to "app";`,
	)
}

func TestTableAttributeOnlyPending(t *testing.T) {
	live := memberLive()
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "postgis", Schema: "public"}},
		Tables: map[string]*schema.Table{
			"public.spatial_ref_sys": {Name: "public.spatial_ref_sys", Grants: map[string][]string{"app": {"select"}, "public": {}}, Owner: "gis", Comment: "srs"},
		},
	}
	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`create extension "postgis" schema "public";`,
		`alter table "public"."spatial_ref_sys" owner to "gis";`,
		`revoke all on table "public"."spatial_ref_sys" from public;`,
		`grant select on table "public"."spatial_ref_sys" to "app";`,
		`comment on table "public"."spatial_ref_sys" is 'srs';`,
	)
	for _, w := range warns {
		if w.Kind != "public_revoke" {
			t.Errorf("unexpected warning: %v", w)
		}
	}
}

func TestTableAttributeOnlyMissing(t *testing.T) {
	live := memberLive()
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.spatial_ref_sys": {Name: "public.spatial_ref_sys", Grants: map[string][]string{"app": {"select"}}, Comment: "srs"},
	}}
	sql, warns := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	if sql != "" {
		t.Errorf("a table without columns is never created, got:\n%s", sql)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "table public.spatial_ref_sys has no columns, so it only manages a table that already exists") {
		t.Errorf("want a warning naming the declaration, got %v", warns)
	}
}

func TestTableOfExtensionDeclaredWithColumns(t *testing.T) {
	live := memberLive()
	addMemberTable(live, "public.spatial_ref_sys", "postgis")
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.spatial_ref_sys": {Name: "public.spatial_ref_sys", ColumnsSet: true,
			Columns:    map[string]*schema.Column{"srid": {Type: "bigint"}, "extra": {Type: "text", Nullable: true}},
			PrimaryKey: []string{"srid"}, Grants: map[string][]string{"app": {"select"}}},
	}}
	sql, warns := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, sql, `grant select on table "public"."spatial_ref_sys" to "app";`)
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "table public.spatial_ref_sys belongs to extension postgis") {
		t.Errorf("want a warning about the definition left alone, got %v", warns)
	}
	if len(d.Tables["public.spatial_ref_sys"].Columns) != 2 {
		t.Error("the declared schema must not be modified")
	}
}

func TestTableZeroColumnsStillCreated(t *testing.T) {
	live := memberLive()
	d := &schema.Database{Tables: map[string]*schema.Table{"public.marker": {Name: "public.marker", ColumnsSet: true}}}
	sql, _ := planSQL(t, live, d, Options{})
	wantSQL(t, sql, `create table if not exists "public"."marker" ();`)
}

func TestSequenceWithoutOptionsPending(t *testing.T) {
	live := memberLive()
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "postgis_topology"}},
		Sequences: map[string]*schema.Sequence{
			"public.topology_id_seq": {Schema: "public", Name: "topology_id_seq", Grants: map[string][]string{"app": {"usage"}, "public": {}}},
		},
	}
	sql, _ := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`create extension "postgis_topology";`,
		`create sequence if not exists "public"."topology_id_seq";`,
		`revoke all on sequence "public"."topology_id_seq" from public;`,
		`grant usage on sequence "public"."topology_id_seq" to "app";`,
	)
}

func TestSequenceOfExtensionGrants(t *testing.T) {
	live := memberLive()
	live.Sequences["public.topology_id_seq"] = true
	live.SequenceDefs = map[string]*LiveSequence{"public.topology_id_seq": {Type: "integer", Increment: "1", MinValue: "1", MaxValue: "2147483647", Start: "1", Cache: "1"}}
	live.ExtensionMembers["rel:public.topology_id_seq"] = true
	live.InitPrivs["rel:public.topology_id_seq"] = map[string]map[string]bool{"public": {"usage": true}}
	live.TableGrants["public.topology_id_seq"] = map[string]map[string]bool{"public": {"usage": true}}
	d := &schema.Database{Sequences: map[string]*schema.Sequence{
		"public.topology_id_seq": {Schema: "public", Name: "topology_id_seq", Grants: map[string][]string{"app": {"usage"}}},
	}}
	sql, _ := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, sql, `grant usage on sequence "public"."topology_id_seq" to "app";`)
}

// addMemberAggregate adds an aggregate an extension owns, with PostgreSQL's
// default privileges, and returns its normalized signature.
func addMemberAggregate(live *Live, sig string) string {
	norm := normalizeFunctionSignature(sig)
	if live.AggregateDefs == nil {
		live.AggregateDefs = map[string]*LiveAggregate{}
	}
	live.AggregateDefs[norm] = &LiveAggregate{SFunc: "agg_sfunc", SType: "internal", Parallel: "unsafe", Options: map[string]string{}}
	live.FunctionDefs[norm] = &LiveFunction{Signature: sig, Kind: "a"}
	live.FunctionPublicExec[norm] = true
	live.ExtensionMembers["proc:"+norm] = true
	return norm
}

func TestAggregateAttributeOnlyGrants(t *testing.T) {
	live := memberLive()
	norm := addMemberAggregate(live, "public.st_union(geometry)")
	pct := addMemberAggregate(live, "public.percentile(double precision ORDER BY anyelement)")
	d := &schema.Database{Aggregates: map[string]*schema.Aggregate{
		"public.st_union(geometry)":                     {Schema: "public", Name: "st_union", ArgsSig: "(geometry)", RevokePublic: true, Grants: map[string][]string{"app": {"execute"}}, Comment: "union", Owner: "gis"},
		"public.percentile(float8 order by anyelement)": {Schema: "public", Name: "percentile", ArgsSig: "(double precision order by anyelement)", Grants: map[string][]string{"app": {"execute"}}},
	}}
	sql, warns := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, sql,
		`alter aggregate "public"."st_union"(geometry) owner to "gis";`,
		`revoke all on function "public"."st_union"(geometry) from public;`,
		`grant execute on function "public"."percentile"(double precision, anyelement) to "app";`,
		`grant execute on function "public"."st_union"(geometry) to "app";`,
		`comment on aggregate "public"."st_union"(geometry) is 'union';`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}

	live.FunctionPublicExec[norm] = false
	live.FunctionGrants[norm] = map[string]map[string]bool{"app": {"execute": true}}
	live.FunctionGrants[pct] = map[string]map[string]bool{"app": {"execute": true}}
	live.FunctionComments[norm] = "union"
	live.Owners = map[string]string{"proc:" + norm: "gis"}
	if sql, _ := planSQL(t, live, d, Options{Unsafe: true, Prune: true}); sql != "" {
		t.Errorf("aggregates in the declared state are left alone, got:\n%s", sql)
	}
}

func TestAggregateAttributeOnlyPendingAndMissing(t *testing.T) {
	live := memberLive()
	live.AggregateDefs = map[string]*LiveAggregate{}
	ag := map[string]*schema.Aggregate{
		"public.st_union(geometry)": {Schema: "public", Name: "st_union", ArgsSig: "(geometry)", Grants: map[string][]string{"app": {"execute"}, "public": {}}},
	}
	sql, warns := planSQL(t, live, &schema.Database{Aggregates: ag}, Options{})
	if sql != "" || len(warns) != 1 || !strings.Contains(warns[0].Message, "aggregate public.st_union(geometry) has no sfunc and stype, so it only manages an aggregate that already exists") {
		t.Errorf("an aggregate without sfunc that doesn't exist gets a warning and no SQL, got:\n%s\n%v", sql, warns)
	}
	sql, _ = planSQL(t, live, &schema.Database{Extensions: []*schema.Extension{{Name: "postgis"}}, Aggregates: ag}, Options{})
	wantSQL(t, sql,
		`create extension "postgis";`,
		`revoke all on function "public"."st_union"(geometry) from public;`,
		`grant execute on function "public"."st_union"(geometry) to "app";`,
	)
}

func TestAggregateGrantsOnCreatedAggregate(t *testing.T) {
	live := memberLive()
	live.AggregateDefs = map[string]*LiveAggregate{}
	d := &schema.Database{Aggregates: map[string]*schema.Aggregate{
		"public.total(integer)": {Schema: "public", Name: "total", ArgsSig: "(integer)", SFunc: "int4pl", SType: "integer", RevokePublic: true, Grants: map[string][]string{"app": {"execute"}}},
	}}
	sql, _ := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`create or replace aggregate "public"."total"(integer) (sfunc = int4pl, stype = integer);`,
		`revoke all on function "public"."total"(integer) from public;`,
		`grant execute on function "public"."total"(integer) to "app";`,
	)
}

func TestAggregateGrantArgs(t *testing.T) {
	for in, want := range map[string]string{
		"(*)":                          "()",
		"(integer)":                    "(integer)",
		"(float8 order by anyelement)": "(float8, anyelement)",
		"(ORDER BY anyelement)":        "(anyelement)",
		"(a int, b text)":              "(a int, b text)",
	} {
		if got := aggregateGrantArgs(in); got != want {
			t.Errorf("aggregateGrantArgs(%q) = %q, want %q", in, got, want)
		}
	}
}
