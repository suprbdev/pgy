package schema

import "testing"

func TestStatisticsAndReplicaIdentityParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema app:
  table t:
    replicaIdentity: using index t_a
    columns: {a: {type: int}, b: {type: int}}
    statistics:
      t_ab: {kinds: [NDistinct], columns: [a, b], comment: c}
  table u:
    replicaIdentity: FULL
    columns: {a: {type: int}}
`))
	if err != nil {
		t.Fatal(err)
	}
	tt := db.Tables["app.t"]
	if tt.ReplicaIdentity != "index t_a" || len(tt.Statistics) != 1 || tt.Statistics[0].Kinds[0] != "ndistinct" || len(tt.Statistics[0].Columns) != 2 {
		t.Errorf("unexpected table t: %+v %+v", tt.ReplicaIdentity, tt.Statistics)
	}
	if db.Tables["app.u"].ReplicaIdentity != "full" {
		t.Errorf("unexpected replica identity: %q", db.Tables["app.u"].ReplicaIdentity)
	}
}

func TestTableOptionsParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
schema app:
  table t:
    clusterOn: t_a_idx
    columns:
      a: {type: int, statisticsTarget: 500}
    triggers:
      t_trg: {timing: before, events: [insert], level: row, procedure: app.f(), enabled: false}
      t_rep: {timing: before, events: [insert], level: row, procedure: app.f(), enabled: replica}
  view v:
    query: select 1 as one
    columnComments: {one: the number one}
`))
	if err != nil {
		t.Fatal(err)
	}
	tbl := d.Tables["app.t"]
	if tbl.ClusterOn != "t_a_idx" {
		t.Errorf("clusterOn = %q", tbl.ClusterOn)
	}
	if st := tbl.Columns["a"].StatisticsTarget; st == nil || *st != 500 {
		t.Errorf("statisticsTarget = %v", st)
	}
	got := map[string]string{}
	for _, tr := range tbl.Triggers {
		got[tr.Name] = tr.Enabled
	}
	if got["t_trg"] != "disabled" || got["t_rep"] != "replica" {
		t.Errorf("trigger enabled = %v", got)
	}
	if d.Views["app.v"].ColumnComments["one"] != "the number one" {
		t.Errorf("columnComments = %v", d.Views["app.v"].ColumnComments)
	}
}

func TestFunctionParallelCostRowsParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
schema app:
  function f():
    returns: setof int
    language: sql
    parallel: SAFE
    cost: 5
    rows: 2.5
    body: select 1
  function g():
    returns: int
    language: sql
    body: select 1
`))
	if err != nil {
		t.Fatal(err)
	}
	f := d.Functions["app.f()"]
	if f.Parallel != "safe" || f.Cost == nil || *f.Cost != 5 || f.Rows == nil || *f.Rows != 2.5 {
		t.Errorf("unexpected function attributes: %q %v %v", f.Parallel, f.Cost, f.Rows)
	}
	if g := d.Functions["app.g()"]; g.Parallel != "" || g.Cost != nil || g.Rows != nil {
		t.Errorf("unset attributes must stay unmanaged: %+v", g)
	}
}

func TestDomainChecksParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
schema app:
  domain score:
    type: int
    check: VALUE >= 0
    checks: {score_max: VALUE <= 100, score_even: "VALUE % 2 = 0"}
`))
	if err != nil {
		t.Fatal(err)
	}
	dm := d.Domains["app.score"]
	if dm.Check != "VALUE >= 0" || len(dm.Checks) != 2 || dm.Checks["score_max"] != "VALUE <= 100" || dm.Checks["score_even"] != "VALUE % 2 = 0" {
		t.Errorf("unexpected domain: %+v", dm)
	}
}

func TestIdentityOptionsParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
schema app:
  table t:
    columns:
      id: {type: bigint, identity: {generated: by default, start: 1000, cycle: true}}
      a: {type: int, identity: {start: 5}}
      b: {type: int, identity: always}
`))
	if err != nil {
		t.Fatal(err)
	}
	cols := d.Tables["app.t"].Columns
	if id := cols["id"]; id.Identity != "by default" || id.IdentityOptions == nil || id.IdentityOptions.Start != "1000" || id.IdentityOptions.Cycle == nil || !*id.IdentityOptions.Cycle {
		t.Errorf("unexpected id: %+v %+v", id, id.IdentityOptions)
	}
	if a := cols["a"]; a.Identity != "always" || a.IdentityOptions.Start != "5" {
		t.Errorf("an options map defaults to always: %+v", a)
	}
	if b := cols["b"]; b.Identity != "always" || b.IdentityOptions != nil {
		t.Errorf("scalar identity has no options: %+v", b)
	}
}
