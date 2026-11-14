package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestCompositeTypeAltered(t *testing.T) {
	live := emptyLive()
	live.Types["public.addr"] = true
	live.CompositeAttributes = map[string]map[string]string{"public.addr": {"street": "text", "zip": "integer", "old": "text"}}
	td := &schema.TypeDef{Name: "addr", Kind: "composite", AttributeOrder: []string{"street", "zip", "city"},
		Attributes: map[string]string{"street": "text", "zip": "text", "city": "varchar(80)"}}
	d := &schema.Database{Tables: map[string]*schema.Table{}, Types: map[string]*schema.TypeDef{"public.addr": td}}
	if p := Plan(live, d, false); len(p.Alters) != 1 || p.Alters[0] != `alter type "public"."addr" add attribute "city" varchar(80);` {
		t.Errorf("without --unsafe only the missing attribute is added, got %v", p.Alters)
	}
	p := Plan(live, d, true)
	want := []string{
		`alter type "public"."addr" alter attribute "zip" type text;`,
		`alter type "public"."addr" add attribute "city" varchar(80);`,
		`alter type "public"."addr" drop attribute "old";`,
	}
	if len(p.Alters) != 3 || p.Alters[0] != want[0] || p.Alters[1] != want[1] || p.Alters[2] != want[2] {
		t.Errorf("want %v, got %v", want, p.Alters)
	}
	live.CompositeAttributes["public.addr"] = map[string]string{"street": "text", "zip": "text", "city": "character varying(80)"}
	if sql := Render(Plan(live, d, true)); sql != "" {
		t.Errorf("equivalent composite must not diff, got:\n%s", sql)
	}
}

func TestRangeTypeCreate(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{}, Types: map[string]*schema.TypeDef{
		"public.floatrange": {Name: "floatrange", Kind: "range", Range: &schema.RangeDef{Subtype: "float8", SubtypeDiff: "float8mi", MultirangeTypeName: "floatmultirange"}},
	}}
	p := Plan(emptyLive(), d, false)
	want := `create type "public"."floatrange" as range (subtype = float8, subtype_diff = float8mi, multirange_type_name = floatmultirange);`
	if !findCreate(p, want) {
		t.Errorf("want %q, got %v", want, p.Creates)
	}
	live := emptyLive()
	live.Types["public.floatrange"] = true
	if sql := Render(Plan(live, d, true)); sql != "" {
		t.Errorf("existing range type must not diff, got:\n%s", sql)
	}
}
