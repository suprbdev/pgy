package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func statsTable(stats []*schema.Statistic, ri string) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name: "t", Columns: map[string]*schema.Column{"a": {Type: "int"}, "b": {Type: "int"}, "c": {Type: "text"}},
		Statistics: stats, ReplicaIdentity: ri,
	}}}
}

func TestStatisticsAndReplicaIdentityCreate(t *testing.T) {
	d := statsTable([]*schema.Statistic{
		{Name: "t_ab", Kinds: []string{"dependencies", "ndistinct"}, Columns: []string{"a", "b"}, Comment: "correlated"},
		{Name: "t_expr", Columns: []string{"lower(c)", "b"}},
	}, "full")
	p := Plan(emptyLive(), d, false)
	for _, want := range []string{
		`create statistics "app"."t_ab" (ndistinct, dependencies) on "a", "b" from "app"."t";`,
		`create statistics "app"."t_expr" on (lower(c)), "b" from "app"."t";`,
		`alter table "app"."t" replica identity full;`,
		`comment on statistics "app"."t_ab" is 'correlated';`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
}

func TestStatisticsAndReplicaIdentityReconciled(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"a": {Type: "integer"}, "b": {Type: "integer"}, "c": {Type: "text"}})
	live.Schemas["app"] = true
	lt := live.Tables["app.t"]
	lt.ReplicaIdentity = "index t_a"
	lt.Statistics = map[string]*LiveStatistic{
		"t_ab":   {Kinds: []string{"ndistinct", "dependencies"}, Items: []string{"a", "b"}, Comment: "correlated"},
		"t_expr": {Items: []string{"b", "lower(c)"}},
		"t_old":  {Items: []string{"a", "c"}},
	}
	d := statsTable([]*schema.Statistic{
		{Name: "t_ab", Kinds: []string{"ndistinct", "dependencies"}, Columns: []string{"b", "a"}, Comment: "correlated"},
		{Name: "t_expr", Kinds: []string{"ndistinct", "dependencies", "mcv"}, Columns: []string{"(lower(c))", "b"}},
	}, "using index t_a")
	d.Tables["app.t"].ReplicaIdentity = "index t_a"
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("equivalent statistics and replica identity must not diff, got:\n%s", sql)
	}
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if len(p.Drops) != 1 || p.Drops[0] != `drop statistics "app"."t_old";` {
		t.Errorf("prune should drop the undeclared statistics, got %v", p.Drops)
	}
	d.Tables["app.t"].Statistics[0].Kinds = []string{"mcv"}
	d.Tables["app.t"].ReplicaIdentity = "nothing"
	p = Plan(live, d, false)
	for _, want := range []string{
		`drop statistics "app"."t_ab";`,
		`create statistics "app"."t_ab" (mcv) on "b", "a" from "app"."t";`,
		`alter table "app"."t" replica identity nothing;`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
}
