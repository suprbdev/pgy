package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestBuildForeignTablesWriteEveryOptionsBlock(t *testing.T) {
	events := &diff.LiveTable{Partitioned: true, Columns: map[string]*diff.LiveColumn{"id": {Type: "bigint"}, "region": {Type: "text"}}}
	parent := &diff.LiveTable{Columns: map[string]*diff.LiveColumn{"id": {Type: "integer", Storage: "plain", TypeStorage: "plain"}}}
	live := &diff.Live{
		CurrentUser: "admin",
		Schemas:     map[string]bool{"rep": true},
		Servers: map[string]*diff.LiveServer{
			"bare":  {Wrapper: "postgres_fdw", Owner: "admin"},
			"owned": {Wrapper: "postgres_fdw", Owner: "other", Options: map[string]string{"dbname": "x"}},
		},
		Tables: map[string]*diff.LiveTable{
			"rep.events": events,
			"rep.parent": parent,
			"rep.plain": {
				Foreign: &diff.LiveForeignTable{Server: "bare"},
				Columns: map[string]*diff.LiveColumn{"id": {Type: "integer"}, "v": {Type: "text", Nullable: true, FDWOptions: map[string]string{"column_name": "val"}}},
			},
			"rep.events_us": {
				IsPartition: true,
				Foreign:     &diff.LiveForeignTable{Server: "owned", Options: map[string]string{"table_name": "events"}},
				Columns:     map[string]*diff.LiveColumn{"id": {Type: "bigint", Inherited: true, FDWOptions: map[string]string{"column_name": "event_id"}}, "region": {Type: "text", Inherited: true}},
			},
			"rep.child": {
				Inherits: []string{"rep.parent"},
				Foreign:  &diff.LiveForeignTable{Server: "bare"},
				Columns:  map[string]*diff.LiveColumn{"id": {Type: "integer", Inherited: true, Storage: "plain", TypeStorage: "plain"}, "extra": {Type: "integer", Nullable: true}},
			},
		},
		Details: &diff.LiveDetails{
			ColumnOrder:     map[string][]string{"rep.events": {"id", "region"}, "rep.parent": {"id"}, "rep.plain": {"id", "v"}, "rep.events_us": {"id", "region"}, "rep.child": {"id", "extra"}},
			PartitionKeys:   map[string]string{"rep.events": "LIST (region)"},
			PartitionOf:     map[string]string{"rep.events_us": "rep.events"},
			PartitionBounds: map[string]string{"rep.events_us": "FOR VALUES IN ('us')"},
		},
	}
	res, err := Build(live, Options{Single: true, Owners: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{
		"  bare:\n    wrapper: postgres_fdw\n    options: {}\n",
		"    options: {dbname: x}\n    owner: other\n",
		"  foreign table plain:\n    server: bare\n    options: {}\n",
		"id: {type: integer, options: {}}",
		"v: {type: text, options: {column_name: val}, nullable: true}",
		"id: {options: {column_name: event_id}}",
		"region: {options: {}}",
		"inherits: [rep.parent]",
		"id: {options: {}}",
		"extra: {type: integer, options: {}, nullable: true}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "owner: admin") {
		t.Errorf("a server the connecting role owns needs no owner:\n%s", got)
	}
}
