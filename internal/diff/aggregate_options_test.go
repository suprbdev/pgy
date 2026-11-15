package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func movingAggregate() *schema.Aggregate {
	return &schema.Aggregate{Schema: "app", Name: "total", ArgsSig: "(int)", SFunc: "app.add2", SType: "int", Options: map[string]string{
		"msfunc": "app.add2", "minvfunc": "app.sub2", "mstype": "int", "minitcond": "0",
		"finalfunc_extra": "false", "mfinalfunc_extra": "true", "mfinalfunc_modify": "shareable", "sspace": "8", "sortop": "pg_catalog.>",
	}}
}

func TestAggregateOptionsRender(t *testing.T) {
	got := renderAggregate("app.total", movingAggregate())
	want := `create or replace aggregate "app"."total"(int) (sfunc = app.add2, stype = int, sspace = 8, msfunc = app.add2, minvfunc = app.sub2, mstype = int, mfinalfunc_extra, mfinalfunc_modify = shareable, minitcond = '0', sortop = operator(pg_catalog.>));`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestAggregateOptionsMatchLive(t *testing.T) {
	live := &LiveAggregate{SFunc: "app.add2", SType: "integer", Parallel: "unsafe", Options: map[string]string{
		"sspace": "8", "finalfunc_extra": "false", "finalfunc_modify": "read_only", "msfunc": "app.add2", "minvfunc": "app.sub2",
		"mstype": "integer", "msspace": "0", "mfinalfunc_extra": "true", "mfinalfunc_modify": "shareable", "minitcond": "0", "sortop": "pg_catalog.>",
		"serialfunc": "app.ser",
	}}
	ag := movingAggregate()
	if !aggregateMatchesLive(ag, live) {
		t.Error("matching options (serialfunc unmanaged) must match")
	}
	for k, v := range map[string]string{"mfinalfunc_modify": "read_write", "minitcond": "1", "sortop": "<", "mstype": "bigint", "sspace": "16", "mfinalfunc_extra": "false"} {
		changed := movingAggregate()
		changed.Options[k] = v
		if aggregateMatchesLive(changed, live) {
			t.Errorf("changing %s to %s must not match", k, v)
		}
	}
	ag.Options["sortop"] = ">"
	ag.Options["finalfunc_modify"] = ""
	if !aggregateMatchesLive(ag, live) {
		t.Error("an unqualified sortop and a default modify must match")
	}
}

func TestSimulateAggregateOptions(t *testing.T) {
	ag := movingAggregate()
	d := &schema.Database{Aggregates: map[string]*schema.Aggregate{"app.total(int)": ag}}
	if sql := Render(Plan(emptyLive(), d, false)); !strings.Contains(sql, "msfunc = app.add2") {
		t.Fatalf("the aggregate must be planned from empty, got:\n%s", sql)
	}
	if sql := Render(Plan(Simulate(emptyLive(), d), d, false)); strings.Contains(sql, "aggregate") {
		t.Errorf("expected no changes after simulate, got:\n%s", sql)
	}
}
