package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestPgCatalogTypeNormalized(t *testing.T) {
	for a, b := range map[string]string{
		"pg_catalog.int4":          "integer",
		"PG_CATALOG.text":          "text",
		`"pg_catalog".jsonb[]`:     "jsonb[]",
		`pg_catalog."varchar"(10)`: "character varying(10)",
		"pg_catalog.timestamptz":   "timestamp with time zone",
		"pg_catalog.bool":          "boolean",
		"pg_catalog.float8":        "double precision",
	} {
		if normalizeColumnType(a) != normalizeColumnType(b) {
			t.Errorf("%q and %q must be the same type: %q vs %q", a, b, normalizeColumnType(a), normalizeColumnType(b))
		}
	}
	for a, b := range map[string]string{
		"pg_catalog.int4":    "bigint",
		"other.int4":         "integer",
		"my_pg_catalog.text": "text",
	} {
		if normalizeColumnType(a) == normalizeColumnType(b) {
			t.Errorf("%q and %q must differ", a, b)
		}
	}
	if got, want := normalizeFunctionSignature(`public.f(a pg_catalog.int4, pg_catalog."varchar", c pg_catalog.jsonb[])`), normalizeFunctionSignature("public.f(a integer, character varying, c jsonb[])"); got != want {
		t.Errorf("signature %q, want %q", got, want)
	}
}

func pgCatalogLive() *Live {
	live := emptyLive()
	live.Schemas["public"] = true
	for _, sig := range []string{"public.f(a integer)", "public.g(a text, b character varying)"} {
		live.Functions[sig] = true
		live.FunctionDefs[normalizeFunctionSignature(sig)] = &LiveFunction{
			Signature: sig, Kind: "f", Body: "select 1", Returns: "integer", Language: "sql", Args: sig[len("public.f(") : len(sig)-1],
			Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100,
		}
	}
	live.FunctionDefs["public.f(a int)"].Language, live.FunctionDefs["public.f(a int)"].Transform = "plpython3u", []string{"jsonb", "boolean"}
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	lt.Columns["doc"] = &LiveColumn{Type: "jsonb", Nullable: true}
	lt.Columns["name"] = &LiveColumn{Type: "character varying(20)", Nullable: true}
	live.Tables["public.t"] = lt
	return live
}

func pgCatalogDesired(language string, transform []string) *schema.Database {
	return &schema.Database{
		Functions: map[string]*schema.Function{
			"public.f(a pg_catalog.int4)":                         {Schema: "public", Name: "f", ArgsSig: "(a pg_catalog.int4)", Returns: "pg_catalog.int4", Language: language, Transform: transform, Body: "select 1"},
			`public.g(a pg_catalog.text, b pg_catalog."varchar")`: {Schema: "public", Name: "g", ArgsSig: `(a pg_catalog.text, b pg_catalog."varchar")`, Returns: "integer", Language: "sql", Body: "select 1"},
		},
		Tables: map[string]*schema.Table{"public.t": {Columns: map[string]*schema.Column{
			"id":   {Type: "pg_catalog.int4"},
			"doc":  {Type: "pg_catalog.jsonb", Nullable: true},
			"name": {Type: `pg_catalog."varchar"(20)`, Nullable: true},
		}}},
	}
}

func TestPgCatalogTypeSkippedIfExists(t *testing.T) {
	for _, d := range []*schema.Database{
		pgCatalogDesired("plpython3u", []string{"pg_catalog.jsonb", "pg_catalog.bool"}),
		pgCatalogDesired("plpython3u transform for type pg_catalog.jsonb, for type pg_catalog.bool", nil),
	} {
		for _, opts := range []Options{{}, {Unsafe: true, Prune: true}} {
			p := PlanWith(pgCatalogLive(), d, opts)
			if sql := Render(p); sql != "" {
				t.Errorf("%+v: pg_catalog-qualified types match the live ones:\n%s", opts, sql)
			}
			if len(p.Warnings) != 0 {
				t.Errorf("%+v: unexpected warnings: %v", opts, p.Warnings)
			}
		}
	}
}

func TestPgCatalogTypeChangeDetected(t *testing.T) {
	d := pgCatalogDesired("plpython3u", []string{"pg_catalog.jsonb"})
	d.Tables["public.t"].Columns["id"].Type = "pg_catalog.int8"
	p := PlanWith(pgCatalogLive(), d, Options{Unsafe: true})
	for _, want := range []string{
		`alter table "public"."t" alter column "id" type pg_catalog.int8;`,
		`create or replace function "public"."f"(a pg_catalog.int4) returns pg_catalog.int4 language plpython3u transform for type pg_catalog.jsonb as $$select 1$$;`,
	} {
		if !findCreate(p, want) && !findAlter(p, want) {
			t.Errorf("missing %q in:\n%s", want, Render(p))
		}
	}
}
