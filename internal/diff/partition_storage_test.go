package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestPartitionStorageCreate(t *testing.T) {
	yes := true
	d := &schema.Database{Tables: map[string]*schema.Table{
		"app.m": {Name: "m", Columns: map[string]*schema.Column{"a": {Type: "int"}}, PartitionBy: &schema.PartitionBy{Type: "list", Columns: []string{"a"}}},
		"app.m1": {Name: "m1", PartitionOf: "app.m", Partition: &schema.PartitionSpec{In: []string{"1"}, Modulus: -1, Remainder: -1},
			Unlogged: &yes, With: map[string]string{"fillfactor": "90"}},
	}}
	sql := Render(Plan(emptyLive(), d, false))
	if !strings.Contains(sql, `create unlogged table if not exists "app"."m1" partition of "app"."m" for values in (1) with (fillfactor = 90);`) {
		t.Errorf("missing unlogged partition with storage parameters in:\n%s", sql)
	}
}
