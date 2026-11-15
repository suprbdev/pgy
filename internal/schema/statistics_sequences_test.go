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
