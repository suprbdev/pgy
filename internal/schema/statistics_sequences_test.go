package schema

import (
	"slices"
	"testing"
)

func TestStatisticsTargetUnloggedSequenceAndAttributeCommentParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  type point:
    type: composite
    attributes:
      x: {type: int, comment: horizontal}
      y: {type: int}
  table t:
    columns: {a: {type: int}, b: {type: int}}
    statistics:
      t_ab: {columns: [a, b], statisticsTarget: 500}
      t_ba: {columns: [b, a]}
  sequence fast: {unlogged: true}
  sequence plain: {start: 1}
`))
	if err != nil {
		t.Fatal(err)
	}
	stats := map[string]*Statistic{}
	for _, st := range db.Tables["app.t"].Statistics {
		stats[st.Name] = st
	}
	if st := stats["t_ab"]; st == nil || st.StatisticsTarget == nil || *st.StatisticsTarget != 500 {
		t.Errorf("t_ab statisticsTarget: %+v", st)
	}
	if st := stats["t_ba"]; st == nil || st.StatisticsTarget != nil {
		t.Errorf("t_ba statisticsTarget must be unmanaged: %+v", st)
	}
	if sq := db.Sequences["app.fast"]; sq == nil || sq.Unlogged == nil || !*sq.Unlogged {
		t.Errorf("fast unlogged: %+v", sq)
	}
	if sq := db.Sequences["app.plain"]; sq == nil || sq.Unlogged != nil {
		t.Errorf("plain unlogged must be unmanaged: %+v", sq)
	}
	td := db.Types["app.point"]
	if td == nil || td.AttributeComments["x"] != "horizontal" || len(td.AttributeComments) != 1 {
		t.Errorf("attribute comments: %+v", td)
	}
}

func TestIndexStatisticsTargetsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  table t:
    columns: {a: {type: text}}
    indexes:
      t_lower: {columns: [lower(a)], statisticsTargets: {lower(a): 400}}
`))
	if err != nil {
		t.Fatal(err)
	}
	ix := db.Tables["app.t"].Indexes[0]
	if ix.StatisticsTargets["lower(a)"] != 400 || len(ix.StatisticsTargets) != 1 {
		t.Errorf("statisticsTargets: %v", ix.StatisticsTargets)
	}
}

func TestPrimaryKeyCommentParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  table t:
    columns: {id: {type: int}}
    primaryKey: {t_key: {columns: [id], comment: the key}}
  table u:
    columns: {id: {type: int}}
    primaryKey: [id]
`))
	if err != nil {
		t.Fatal(err)
	}
	if tb := db.Tables["app.t"]; tb.PrimaryKeyName != "t_key" || tb.PrimaryKeyComment != "the key" || len(tb.PrimaryKey) != 1 {
		t.Errorf("t: %q %q %v", tb.PrimaryKeyName, tb.PrimaryKeyComment, tb.PrimaryKey)
	}
	if tb := db.Tables["app.u"]; tb.PrimaryKeyComment != "" || tb.PrimaryKeyName != "" {
		t.Errorf("u: %q %q", tb.PrimaryKeyName, tb.PrimaryKeyComment)
	}
}

func TestAggregateOptionsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  aggregate total(int):
    sfunc: app.add2
    stype: int
    sspace: 8
    msfunc: app."Add2"
    mfinalfuncExtra: true
    mfinalfuncModify: SHAREABLE
    minitcond: " 0"
    sortop: ">"
`))
	if err != nil {
		t.Fatal(err)
	}
	var ag *Aggregate
	for _, a := range db.Aggregates {
		ag = a
	}
	want := map[string]string{"sspace": "8", "msfunc": `app."Add2"`, "mfinalfunc_extra": "true", "mfinalfunc_modify": "shareable", "minitcond": " 0", "sortop": ">"}
	if ag == nil || len(ag.Options) != len(want) {
		t.Fatalf("options: %+v", ag)
	}
	for k, v := range want {
		if ag.Options[k] != v {
			t.Errorf("%s = %q, want %q", k, ag.Options[k], v)
		}
	}
}

func TestAggregateSupportParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  aggregate total(int8):
    sfunc: int8pl
    stype: int8
    support: " app.total_support "
  aggregate plain(int8):
    sfunc: int8pl
    stype: int8
    support: ""
`))
	if err != nil {
		t.Fatal(err)
	}
	if ag := db.Aggregates["app.total(int8)"]; ag == nil || ag.Options["support"] != "app.total_support" {
		t.Errorf("total = %+v", ag)
	}
	if ag := db.Aggregates["app.plain(int8)"]; ag == nil {
		t.Error("plain is missing")
	} else if v, ok := ag.Options["support"]; !ok || v != "" {
		t.Errorf("an empty support is a managed option that means none: %+v", ag.Options)
	}
}

func TestTopologicalSortAggregateDependsOnSupport(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
schema app:
  function zz_support(internal):
    returns: internal
    language: internal
    body: int8inc_support
  aggregate total(int8):
    sfunc: int8pl
    stype: int8
    support: app.zz_support
`))
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := TopologicalSort(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range sorted {
		if e.Kind == "aggregate" && !slices.Contains(e.DependsOn, "function app.zz_support") {
			t.Errorf("the aggregate must depend on its support function: %+v", e)
		}
	}
}

func TestKeyIndexParametersAndIdentityNameParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  table t:
    columns:
      id: {type: bigint, identity: {generated: always, sequenceName: custom_seq}}
      code: {type: text}
    primaryKey: {t_pk: {columns: [id], include: [code], with: {fillfactor: 80}}}
    constraints:
      t_uq: {type: unique, columns: [code], include: [id], with: {fillfactor: 70}}
`))
	if err != nil {
		t.Fatal(err)
	}
	tb := db.Tables["app.t"]
	if tb.PrimaryKeyName != "t_pk" || len(tb.PrimaryKeyInclude) != 1 || tb.PrimaryKeyWith["fillfactor"] != "80" {
		t.Errorf("primary key: %q %v %v", tb.PrimaryKeyName, tb.PrimaryKeyInclude, tb.PrimaryKeyWith)
	}
	if ct := tb.Constraints[0]; len(ct.Include) != 1 || ct.Include[0] != "id" || ct.With["fillfactor"] != "70" {
		t.Errorf("unique: %+v", ct)
	}
	if o := tb.Columns["id"].IdentityOptions; o == nil || o.Name != "custom_seq" {
		t.Errorf("identity: %+v", o)
	}
}

func TestRoleValidUntilAndMembershipOptionsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
roles:
  app:
    validUntil: 2030-01-01T00:00:00Z
    inRoles: [readers, {role: admins, admin: true, inherit: false}]
  svc:
    validUntil: infinity
`))
	if err != nil {
		t.Fatal(err)
	}
	app := db.Roles["app"]
	if app.ValidUntil != "2030-01-01T00:00:00Z" || !app.Managed["validUntil"] {
		t.Errorf("validUntil: %q", app.ValidUntil)
	}
	if len(app.InRoles) != 2 || app.InRoles[0] != "readers" || app.InRoles[1] != "admins" {
		t.Errorf("inRoles: %v", app.InRoles)
	}
	o := app.MemberOptions["admins"]
	if o == nil || o.Admin == nil || !*o.Admin || o.Inherit == nil || *o.Inherit || o.Set != nil || app.MemberOptions["readers"] != nil {
		t.Errorf("options: %+v", app.MemberOptions)
	}
	if db.Roles["svc"].ValidUntil != "infinity" {
		t.Errorf("svc: %q", db.Roles["svc"].ValidUntil)
	}
}

func TestRevokePublicAndServerGrantsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
servers:
  remote: {wrapper: postgres_fdw, grants: {app: [usage]}}
schema app:
  type e: {type: enum, labels: [a], revokePublic: true}
  domain pos: {type: int, revokePublic: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	if !db.Types["app.e"].RevokePublic || !db.Domains["app.pos"].RevokePublic {
		t.Error("revokePublic not parsed")
	}
	if g := db.Servers["remote"].Grants["app"]; len(g) != 1 || g[0] != "usage" {
		t.Errorf("server grants: %v", db.Servers["remote"].Grants)
	}
}

func TestExtensionCommentParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte("extensions:\n  - {name: pg_trgm, comment: trigram matching}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Extensions) != 1 || db.Extensions[0].Comment != "trigram matching" {
		t.Errorf("extensions: %+v", db.Extensions)
	}
}

func TestViewColumnDefaultsParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte("schema app:\n  view v:\n    query: select 1 as a\n    columnDefaults: {a: 0, b: \"'x'\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cd := db.Views["app.v"].ColumnDefaults; cd["a"] != "0" || cd["b"] != "'x'" {
		t.Errorf("columnDefaults: %v", cd)
	}
}

func TestTypedTableParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte("schema app:\n  table addresses:\n    of: addr\n    columns: {city: {default: x}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if tb := db.Tables["app.addresses"]; tb.Of != "app.addr" || tb.Columns["city"].Type != "" {
		t.Errorf("of: %q", tb.Of)
	}
}
