package schema

import "testing"

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
