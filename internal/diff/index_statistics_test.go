package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestIndexStatisticsTargetsCreate(t *testing.T) {
	d := indexCommentTable(&schema.Index{Name: "t_expr", Columns: []string{"a", "(a + 1)", "abs(a)"}, StatisticsTargets: map[string]int{"(a + 1)": 300, "abs(a)": -1}})
	p := Plan(emptyLive(), d, false)
	if !findAlter(p, `alter index "app"."t_expr" alter column 2 set statistics 300;`) {
		t.Errorf("missing statistics target in %v", p.Alters)
	}
	if strings.Contains(Render(p), "alter column 3") {
		t.Errorf("a default target needs no statement on a new index:\n%s", Render(p))
	}
}

func TestIndexStatisticsTargetsChanged(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	live.Schemas["app"] = true
	lt := live.Tables["app.t"]
	lt.Indexes["t_expr"] = true
	lt.IndexOpclasses = map[string][]LiveIndexOpclass{"t_expr": {{Name: "int4_ops", Default: true, StatisticsTarget: -1}, {Name: "int4_ops", Default: true, StatisticsTarget: 300}}}
	ix := &schema.Index{Name: "t_expr", Columns: []string{"a", "(a + 1)"}, StatisticsTargets: map[string]int{"(a + 1)": 300}}
	if sql := Render(Plan(live, indexCommentTable(ix), false)); sql != "" {
		t.Errorf("matching target must not diff, got:\n%s", sql)
	}
	ix.StatisticsTargets["(a + 1)"] = -1
	if sql := Render(Plan(live, indexCommentTable(ix), false)); !strings.Contains(sql, `alter index "app"."t_expr" alter column 2 set statistics -1;`) {
		t.Errorf("want a reset, got:\n%s", sql)
	}
	ix.StatisticsTargets = nil
	if sql := Render(Plan(live, indexCommentTable(ix), false)); sql != "" {
		t.Errorf("unset targets are unmanaged, got:\n%s", sql)
	}
	delete(lt.IndexOpclasses, "t_expr")
	ix.StatisticsTargets = map[string]int{"(a + 1)": 50}
	if sql := Render(Plan(live, indexCommentTable(ix), false)); sql != "" {
		t.Errorf("without live index columns the target is left alone, got:\n%s", sql)
	}
}

func TestIndexStatisticsTargetsFollowConcurrentBuild(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	live.Schemas["app"] = true
	p := Plan(live, indexCommentTable(&schema.Index{Name: "t_expr", Columns: []string{"abs(a)"}, Concurrently: true, StatisticsTargets: map[string]int{"abs(a)": 80}}), false)
	conc := RenderConcurrent(p)
	if strings.Contains(RenderMain(p), "set statistics") || strings.Index(conc, "set statistics 80") < strings.Index(conc, "create index concurrently") {
		t.Errorf("want the target after the concurrent build:\nmain:\n%s\nconcurrent:\n%s", RenderMain(p), conc)
	}
}
