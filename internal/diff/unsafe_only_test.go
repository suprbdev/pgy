package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestUnsafeOnlyReportsLeftOutChanges(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"id": {Type: "integer"}, "n": {Type: "integer", Nullable: true}, "old": {Type: "integer", Nullable: true}})
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{
		"id": {Type: "int"}, "n": {Type: "bigint", Nullable: true}, "x": {Type: "text", Nullable: true},
	}}}}
	ws := UnsafeOnly(live, d, Options{})
	got := []string{}
	for _, w := range ws {
		if w.Kind != "unsafe" || !strings.HasPrefix(w.Message, "needs --unsafe, so it was left out: ") {
			t.Errorf("unexpected warning %+v", w)
		}
		got = append(got, w.Statement)
	}
	sql := strings.Join(got, "\n")
	for _, want := range []string{`alter column "n" type bigint`, `drop column "old"`} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, `add column "x"`) {
		t.Errorf("statements the safe plan has must not be reported:\n%s", sql)
	}
	if ws := UnsafeOnly(live, d, Options{Unsafe: true}); ws != nil {
		t.Errorf("with --unsafe nothing is left out: %v", ws)
	}
}

func TestDomainBaseTypeChangeWarns(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Domains = map[string]bool{"app.code": true, "app.amount": true}
	live.DomainDefs = map[string]*LiveDomain{
		"app.code":   {Type: "character varying(10)", Checks: map[string]string{}},
		"app.amount": {Type: "numeric(12,2)", Checks: map[string]string{}},
	}
	d := &schema.Database{Domains: map[string]*schema.Domain{
		"app.code":   {Name: "code", Type: "varchar(20)"},
		"app.amount": {Name: "amount", Type: "NUMERIC(12, 2)"},
	}}
	p := Plan(live, d, true)
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "domain app.code: its base type changed from character varying(10) to varchar(20)") {
		t.Errorf("want one warning, for app.code: %+v", p.Warnings)
	}
}

func TestPlanReportingUnsafe(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"id": {Type: "integer"}, "n": {Type: "integer", Nullable: true}})
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{
		"id": {Type: "int"}, "n": {Type: "bigint", Nullable: true}, "x": {Type: "text", Nullable: true},
	}}}}
	p := PlanReportingUnsafe(live, d, Options{})
	if sql := Render(p); !strings.Contains(sql, `add column "x"`) || strings.Contains(sql, "type bigint") {
		t.Errorf("the plan itself must stay safe:\n%s", sql)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, `alter column "n" type bigint`) {
		t.Errorf("want the left-out type change reported: %+v", p.Warnings)
	}
}
