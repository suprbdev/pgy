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
