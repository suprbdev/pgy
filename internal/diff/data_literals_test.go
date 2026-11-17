package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestDataArrayValuesUseArrayConstructor(t *testing.T) {
	dt := &schema.Table{Columns: map[string]*schema.Column{
		"tags":   {Type: "text[]"},
		"boxes":  {Type: "box[]"},
		"grid":   {Type: "int[][]"},
		"codes":  {Type: "varchar(3)[]"},
		"fixed":  {Type: "character(2) array"},
		"bits":   {Type: "pg_catalog.bit(3)[2]"},
		"prices": {Type: "numeric(6,2)[]"},
		"ts":     {Type: "timestamp(0) with time zone[]"},
		"pts":    {Type: "public.geometry(Point,4326)[]"},
		"plain":  {},
	}}
	for _, c := range []struct {
		col  string
		v    any
		want string
	}{
		{"tags", []any{"a", "it's", nil, 1, true}, `array['a', 'it''s', null, '1', 'true']::text[]`},
		{"boxes", []any{"(1,1),(0,0)", "(3,3),(2,2)"}, `array['(1,1),(0,0)', '(3,3),(2,2)']::box[]`},
		{"grid", []any{[]any{1, nil}, []any{3, 4}}, `array[array['1', null], array['3', '4']]::int[][]`},
		{"codes", []any{"abc"}, `array['abc']::varchar[]`},
		{"fixed", []any{"ab"}, `array['ab']::bpchar array`},
		{"bits", []any{"101"}, `array['101']::pg_catalog."bit"[2]`},
		{"prices", []any{1.5, "1.005"}, `array['1.5', '1.005']::numeric[]`},
		{"ts", []any{"2026-01-02 03:04:05.6+00"}, `array['2026-01-02 03:04:05.6+00']::timestamp with time zone[]`},
		{"pts", []any{"SRID=4326;POINT(1 1)"}, `array['SRID=4326;POINT(1 1)']::public.geometry[]`},
		{"tags", []any{}, `'{}'`},
		{"tags", nil, `null`},
		{"plain", []any{"a", "b"}, `'{"a","b"}'`},
	} {
		if got := dataSQL("app.t", dt, c.col, c.v); got != c.want {
			t.Errorf("dataSQL(%s, %v) = %s, want %s", c.col, c.v, got, c.want)
		}
	}
}

func TestIsArrayTypeName(t *testing.T) {
	for typ, want := range map[string]bool{
		"text[]": true, "int [ ]": true, "integer ARRAY": true, "integer array[3]": true, `"My Type"[]`: true, "box[3][3]": true,
		"text": false, "vector(3)": false, "app.array": false, "myarray": false,
	} {
		if got := isArrayTypeName(typ); got != want {
			t.Errorf("isArrayTypeName(%q) = %v, want %v", typ, got, want)
		}
	}
}

func TestDataListForNonArrayColumn(t *testing.T) {
	dt := &schema.Table{Columns: map[string]*schema.Column{"r": {Type: "int4range"}, "emb": {Type: "vector(3)"}, "tags": {Type: "app.tag_list"}, "inherited": {}}}
	for _, c := range []struct {
		col  string
		v    any
		want string
	}{
		{"r", []any{1, 3}, `(select v from jsonb_to_record('{"v":[1,3]}'::jsonb) as x(v int4range))`},
		{"emb", []any{1, 2.5, 3}, `(select v from jsonb_to_record('{"v":[1,2.5,3]}'::jsonb) as x(v vector(3)))`},
		{"tags", []any{}, `(select v from jsonb_to_record('{"v":[]}'::jsonb) as x(v app.tag_list))`},
		{"emb", "[1,2,3]", `'[1,2,3]'`},
		{"inherited", []any{"a"}, `'{"a"}'`},
	} {
		if got := dataSQL("app.t", dt, c.col, c.v); got != c.want {
			t.Errorf("dataSQL(%s, %v) = %s, want %s", c.col, c.v, got, c.want)
		}
	}
}
