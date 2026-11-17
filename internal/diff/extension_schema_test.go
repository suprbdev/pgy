package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func unmovableGisLive(members bool) *Live {
	live := emptyLive()
	live.Schemas["public"], live.Schemas["app"] = true, true
	live.Extensions["gis"] = true
	le := &LiveExtension{Schema: "public", Version: "3.6", Relocatable: false}
	if members {
		le.Members, le.Operators = map[string]bool{"geometry": true, "st_x": true}, map[string]bool{"&&": true}
	}
	live.ExtensionDefs = map[string]*LiveExtension{"gis": le}
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	lt.Columns["g"] = &LiveColumn{Type: "geometry(Point,4326)"}
	lt.Columns["x"] = &LiveColumn{Type: "double precision", Generated: "st_x(g)"}
	live.Tables["app.t"] = lt
	return live
}

func unmovableGisDesired() *schema.Database {
	return &schema.Database{
		Extensions: []*schema.Extension{{Name: "gis", Schema: "geo"}},
		Tables: map[string]*schema.Table{"app.t": {
			ColumnOrder: []string{"id", "g", "x"},
			Columns: map[string]*schema.Column{
				"id": {Type: "integer"},
				"g":  {Type: "geo.geometry(Point,4326)"},
				"x":  {Type: "double precision", Generated: "geo.st_x(g)"},
			},
		}},
	}
}

func TestExtensionNotRelocatableStaysWithWarning(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		p := PlanWith(unmovableGisLive(true), unmovableGisDesired(), Options{Unsafe: unsafe, Prune: unsafe})
		if sql := Render(p); strings.Contains(sql, "alter extension") || strings.Contains(sql, `alter column`) || strings.Contains(sql, "geo.") {
			t.Errorf("unsafe=%v: the extension cannot move, so nothing that would need the move is planned, got:\n%s", unsafe, sql)
		}
		if len(p.Warnings) != 1 || p.Warnings[0].Message != "extension gis is not relocatable, so it stays in schema public with its objects instead of moving to schema geo, and references to them qualified with geo are planned as references to public; declare schema: public, or drop the extension and create it in geo by hand" {
			t.Errorf("unsafe=%v: want one warning, got %+v", unsafe, p.Warnings)
		}
	}
	d := unmovableGisDesired()
	d.Tables["app.t"].Columns["extra"] = &schema.Column{Type: "geo.geometry", Nullable: true}
	if sql := Render(Plan(unmovableGisLive(true), d, true)); !strings.Contains(sql, `alter table "app"."t" add column "extra" public.geometry;`) {
		t.Errorf("a real change uses the schema the extension stays in, got:\n%s", sql)
	}
	p := Plan(unmovableGisLive(false), unmovableGisDesired(), false)
	if len(p.Warnings) == 0 || !strings.HasPrefix(p.Warnings[0].Message, "extension gis is not relocatable, so it stays in schema public with its objects instead of moving to schema geo; declare schema: public") {
		t.Errorf("without the member names the warning still comes, got %+v", p.Warnings)
	}
}

func TestExtensionNotRelocatableSimulatedStays(t *testing.T) {
	live := unmovableGisLive(true)
	after := Simulate(live, unmovableGisDesired())
	if le := after.ExtensionDefs["gis"]; le.Schema != "public" || le.MovedFrom != "" {
		t.Errorf("a simulated plan does not move it either, got %+v", le)
	}
	if sql := Render(Plan(after, unmovableGisDesired(), true)); sql != "" {
		t.Errorf("a later step plans nothing for the extension's users, got:\n%s", sql)
	}
}

func TestExtensionFixedSchemaWarns(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	live.AvailableExtensions = map[string]*AvailableExtension{
		"topo": {DefaultVersion: "3.6", Requires: map[string][]string{"3.5": {}, "3.6": {}}, Schemas: map[string]string{"3.5": "topology", "3.6": "topology"}},
		"free": {DefaultVersion: "1.0", Requires: map[string][]string{"1.0": {}}, Schemas: map[string]string{}},
	}
	for _, c := range []struct {
		ext  schema.Extension
		warn bool
	}{
		{schema.Extension{Name: "topo", Schema: "public"}, true},
		{schema.Extension{Name: "topo", Schema: "public", Version: "3.5"}, true},
		{schema.Extension{Name: "topo", Schema: "topology"}, false},
		{schema.Extension{Name: "topo"}, false},
		{schema.Extension{Name: "free", Schema: "public"}, false},
	} {
		ext := c.ext
		p := Plan(live, &schema.Database{Extensions: []*schema.Extension{&ext}}, false)
		want := "extension topo can only be installed in schema topology, which its control file names, so CREATE EXTENSION with schema public will fail; declare schema: topology, or leave schema out"
		if got := len(p.Warnings) == 1 && p.Warnings[0].Message == want; got != c.warn {
			t.Errorf("%+v: want warning %v, got %+v", c.ext, c.warn, p.Warnings)
		}
	}
}
