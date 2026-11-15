package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestStatisticsTargetCreate(t *testing.T) {
	d := statsTable([]*schema.Statistic{{Name: "t_ab", Columns: []string{"a", "b"}, StatisticsTarget: intPtr(500)}}, "")
	p := Plan(emptyLive(), d, false)
	if !findAlter(p, `alter statistics "app"."t_ab" set statistics 500;`) {
		t.Errorf("missing statistics target in %v", p.Alters)
	}
}

func TestStatisticsTargetChangedInPlace(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"a": {Type: "integer"}, "b": {Type: "integer"}, "c": {Type: "text"}})
	live.Schemas["app"] = true
	live.Tables["app.t"].Statistics = map[string]*LiveStatistic{"t_ab": {Items: []string{"a", "b"}, Target: 500}}
	d := statsTable([]*schema.Statistic{{Name: "t_ab", Columns: []string{"a", "b"}, StatisticsTarget: intPtr(500)}}, "")
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("matching target must not diff, got:\n%s", sql)
	}
	d.Tables["app.t"].Statistics[0].StatisticsTarget = intPtr(-1)
	sql := Render(Plan(live, d, false))
	if !strings.Contains(sql, `alter statistics "app"."t_ab" set statistics -1;`) || strings.Contains(sql, "drop statistics") {
		t.Errorf("want an in-place reset, got:\n%s", sql)
	}
	d.Tables["app.t"].Statistics[0].StatisticsTarget = nil
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("unset target is unmanaged, got:\n%s", sql)
	}
}

func TestSequenceUnloggedCreate(t *testing.T) {
	yes, no := true, false
	d := &schema.Database{Sequences: map[string]*schema.Sequence{
		"app.fast": {Schema: "app", Name: "fast", Unlogged: &yes},
		"app.slow": {Schema: "app", Name: "slow", Unlogged: &no},
	}}
	p := Plan(emptyLive(), d, false)
	if !findCreate(p, `create unlogged sequence if not exists "app"."fast";`) {
		t.Errorf("missing unlogged sequence in %v", p.Creates)
	}
	if !findCreate(p, `create sequence if not exists "app"."slow";`) {
		t.Errorf("missing logged sequence in %v", p.Creates)
	}
}

func TestSequenceUnloggedAltered(t *testing.T) {
	yes, no := true, false
	sq := &schema.Sequence{Schema: "app", Name: "s", Increment: "2", Unlogged: &yes}
	got := sequenceAlterStmts("app.s", sq, &LiveSequence{Increment: "1"})
	want := []string{`alter sequence "app"."s" increment by 2;`, `alter sequence "app"."s" set unlogged;`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %v, want %v", got, want)
	}
	sq.Increment, sq.Unlogged = "", &no
	if got := sequenceAlterStmts("app.s", sq, &LiveSequence{Unlogged: true}); len(got) != 1 || got[0] != `alter sequence "app"."s" set logged;` {
		t.Errorf("got %v", got)
	}
	sq.Unlogged = nil
	if got := sequenceAlterStmts("app.s", sq, &LiveSequence{Unlogged: true}); len(got) != 0 {
		t.Errorf("unset unlogged is unmanaged, got %v", got)
	}
}

func TestCompositeAttributeComments(t *testing.T) {
	d := &schema.Database{Types: map[string]*schema.TypeDef{"app.point": {
		Name: "point", Kind: "composite", Attributes: map[string]string{"x": "int", "y": "int"},
		AttributeComments: map[string]string{"x": "it's x"},
	}}}
	live := emptyLive()
	sql := Render(Plan(live, d, false))
	if !strings.Contains(sql, `comment on column "app"."point"."x" is 'it''s x';`) || strings.Contains(sql, `"y" is`) {
		t.Errorf("want a comment on x only, got:\n%s", sql)
	}
	live.Types = map[string]bool{"app.point": true}
	live.CompositeComments = map[string]map[string]string{"app.point": {"x": "it's x"}}
	if strings.Contains(Render(Plan(live, d, false)), "comment on") {
		t.Error("matching attribute comment must not diff")
	}
}
