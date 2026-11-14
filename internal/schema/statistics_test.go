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
