package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestIndexAndStatisticsQuoteDeclaredColumnsWithSpaces(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name:       "t",
		Columns:    map[string]*schema.Column{"Weird Col": {Type: "int"}, "b": {Type: "int"}},
		Indexes:    []*schema.Index{{Name: "t_weird", Columns: []string{"Weird Col desc", "lower(b::text)"}}},
		Statistics: []*schema.Statistic{{Name: "t_st", Columns: []string{"Weird Col", "b"}}},
	}}}
	sql := Render(Plan(emptyLive(), d, false))
	for _, want := range []string{
		`on "app"."t"("Weird Col" desc, (lower(b::text)))`,
		`on "Weird Col", "b" from "app"."t"`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
}
