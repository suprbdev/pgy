package diff

import (
	"reflect"
	"strings"
	"testing"
)

func TestViewProbeDeniedIsLeftAlone(t *testing.T) {
	for _, materialized := range []bool{false, true} {
		live, k := viewLive(), "app.v"
		if materialized {
			k = "app.mv"
		}
		live.ViewProbes[k] = &ViewProbe{Err: `permission denied for schema hidden`, Denied: true, LiveDef: " SELECT 3"}
		for _, unsafe := range []bool{false, true} {
			p := Plan(live, probedViews(), unsafe)
			if sql := Render(p); strings.Contains(sql, "view") {
				t.Errorf("%s: a query the role may not parse is never replaced (unsafe %v), got:\n%s", k, unsafe, sql)
			}
			if len(p.Warnings) > 0 {
				t.Errorf("%s: ProbeViews reports it, the plan doesn't, got %+v", k, p.Warnings)
			}
		}
	}
}

func TestViewProbeChanged(t *testing.T) {
	for _, c := range []struct {
		p    ViewProbe
		want bool
	}{
		{ViewProbe{DesiredDef: " SELECT 1", LiveDef: " SELECT 1"}, false},
		{ViewProbe{DesiredDef: " SELECT 2", LiveDef: " SELECT 1"}, true},
		{ViewProbe{Err: `column "a" does not exist`, LiveDef: " SELECT 1"}, true},
		{ViewProbe{Err: `permission denied for schema hidden`, Denied: true, LiveDef: " SELECT 1"}, false},
	} {
		if got := c.p.Changed(); got != c.want {
			t.Errorf("%+v: Changed() = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestSameViewText(t *testing.T) {
	live := " SELECT max(beat.ts) AS ts\n   FROM platform_internal.beat;"
	for declared, want := range map[string]bool{
		"SELECT max(beat.ts) AS ts\n   FROM platform_internal.beat": true,
		"SELECT max(beat.ts) AS ts FROM platform_internal.beat;":    true,
		"select max(beat.ts) as ts from platform_internal.beat":     false,
		"SELECT max(ts) AS ts FROM platform_internal.beat":          false,
	} {
		if got := sameViewText(declared, live); got != want {
			t.Errorf("%q: got %v, want %v", declared, got, want)
		}
	}
}

func TestDeniedExprProbes(t *testing.T) {
	probes := map[ExprKey]*ExprProbe{
		{ExprDefault, "hidden.p", "ttl"}:                            {Err: "permission denied for schema hidden", Denied: true},
		{ExprConstraint, "hidden.p", "p_ttl"}:                       {Err: "permission denied for schema hidden", Denied: true},
		{ExprFunctionArgs, "hidden.f(i interval default '1h')", ""}: {Err: "permission denied for schema hidden", Denied: true},
		{ExprDefault, "app.q", "ttl"}:                               {Parts: []string{"'01:00:00'::interval"}},
		{ExprConstraint, "app.q", "q_nope"}:                         {Err: "function nope(interval) does not exist"},
	}
	reply := &probeReply{Types: []probeType{
		{Type: "hidden.money(2)", Err: "permission denied for schema hidden", Code: insufficientPrivilege},
		{Type: "nosuch(1)", Err: `type "nosuch(1)" does not exist`, Code: "42704"},
	}}
	err := deniedExprProbes(probes, reply).err()
	d, ok := err.(*DeniedError)
	if !ok {
		t.Fatalf("want a *DeniedError, got %v", err)
	}
	if want := []string{"hidden.f", "hidden.money(2)", "hidden.p"}; !reflect.DeepEqual(d.Objects, want) || d.Reason != "permission denied for schema hidden" {
		t.Errorf("want each object denied once, sorted, with its reason, got %+v", d)
	}
	if err := deniedExprProbes(map[ExprKey]*ExprProbe{{ExprDefault, "app.q", "ttl"}: {}}, &probeReply{}).err(); err != nil {
		t.Errorf("nothing denied is no error, got %v", err)
	}
}

func TestJoinDenied(t *testing.T) {
	if JoinDenied() != nil || JoinDenied(nil, &DeniedError{}) != nil {
		t.Error("no objects join to nil")
	}
	got := JoinDenied(&DeniedError{Objects: []string{"hidden.v"}, Reason: "permission denied for schema hidden"}, nil, &DeniedError{Objects: []string{"hidden.p", "hidden.v"}, Reason: "permission denied for table p"})
	if want := (&DeniedError{Objects: []string{"hidden.p", "hidden.v"}, Reason: "permission denied for schema hidden"}); !reflect.DeepEqual(got, want) {
		t.Errorf("want %+v, got %+v", want, got)
	}
	if msg := got.Error(); msg != "hidden.p, hidden.v: permission denied for schema hidden" {
		t.Errorf("unexpected message %q", msg)
	}
}
