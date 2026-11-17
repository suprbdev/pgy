package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func otherSchemaStatsLive() *Live {
	live := liveWithTable("app.t", map[string]*LiveColumn{"a": {Type: "integer"}, "b": {Type: "integer"}, "c": {Type: "text"}})
	live.Schemas["app"] = true
	live.Schemas["public"] = true
	live.Tables["app.t"].Statistics = map[string]*LiveStatistic{
		`"public"."s"`: {Kinds: []string{"dependencies"}, Items: []string{"a", "b"}, Comment: "elsewhere", Target: -1, Schema: "public", Name: "s"},
	}
	return live
}

func TestStatisticKey(t *testing.T) {
	for _, c := range []struct{ fq, nsp, name, want string }{
		{"app.t", "", "s", "s"},
		{"app.t", "app", "s", "s"},
		{"app.t", "public", "s", `"public"."s"`},
		{"app.t", "public", "a.b", `"public"."a.b"`},
	} {
		if got := StatisticKey(c.fq, c.nsp, c.name); got != c.want {
			t.Errorf("StatisticKey(%q, %q, %q) = %q, want %q", c.fq, c.nsp, c.name, got, c.want)
		}
	}
}

func TestStatisticsInOtherSchemaCreate(t *testing.T) {
	target := 50
	d := statsTable([]*schema.Statistic{{Name: "s", Schema: "public", Kinds: []string{"dependencies"}, Columns: []string{"a", "b"}, Comment: "elsewhere", StatisticsTarget: &target}}, "")
	p := Plan(emptyLive(), d, false)
	for _, want := range []string{
		`create statistics "public"."s" (dependencies) on "a", "b" from "app"."t";`,
		`alter statistics "public"."s" set statistics 50;`,
		`comment on statistics "public"."s" is 'elsewhere';`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in:\n%s", want, Render(p))
		}
	}
}

func TestStatisticsInOtherSchemaCreatesSchema(t *testing.T) {
	d := statsTable([]*schema.Statistic{{Name: "s", Schema: "stats", Columns: []string{"a", "b"}}}, "")
	statementOrder(t, Render(Plan(emptyLive(), d, false)), `create schema if not exists "stats";`, `create statistics "stats"."s" on "a", "b" from "app"."t";`)
	if p := Plan(Simulate(emptyLive(), d), d, false); planSize(p) != 0 {
		t.Errorf("a plan after the simulated one must be empty, got:\n%s", Render(p))
	}
}

func TestStatisticsInOtherSchemaSkippedIfExists(t *testing.T) {
	d := statsTable([]*schema.Statistic{{Name: "s", Schema: "public", Kinds: []string{"dependencies"}, Columns: []string{"b", "a"}, Comment: "elsewhere"}}, "")
	if sql := Render(PlanWith(otherSchemaStatsLive(), d, Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("statistics in another schema match their declaration by schema and name, got:\n%s", sql)
	}
	if p := Plan(Simulate(emptyLive(), d), d, true); planSize(p) != 0 {
		t.Errorf("a plan after the simulated one must be empty, got:\n%s", Render(p))
	}
	d.Tables["app.t"].Statistics[0].Schema = "app"
	sql := Render(PlanWith(otherSchemaStatsLive(), d, Options{Unsafe: true, Prune: true}))
	for _, want := range []string{`create statistics "app"."s" (dependencies) on "b", "a" from "app"."t";`, `drop statistics "public"."s";`} {
		if !strings.Contains(sql, want) {
			t.Errorf("statistics declared in the table's schema are another object; missing %q in:\n%s", want, sql)
		}
	}
}

func TestStatisticsInOtherSchemaPruned(t *testing.T) {
	p := PlanWith(otherSchemaStatsLive(), statsTable(nil, ""), Options{Unsafe: true, Prune: true})
	if len(p.Drops) != 1 || p.Drops[0] != `drop statistics "public"."s";` {
		t.Errorf("prune drops statistics under their own schema, got %v", p.Drops)
	}
}

func TestStatisticsInOtherSchemaChanged(t *testing.T) {
	d := statsTable([]*schema.Statistic{{Name: "s", Schema: "public", Kinds: []string{"mcv"}, Columns: []string{"a", "b"}}}, "")
	p := Plan(otherSchemaStatsLive(), d, false)
	statementOrder(t, Render(p), `drop statistics "public"."s";`, `create statistics "public"."s" (mcv) on "a", "b" from "app"."t";`)
}
