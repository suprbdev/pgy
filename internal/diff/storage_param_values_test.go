package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestStorageParamValue(t *testing.T) {
	cases := map[string]string{
		"70":                                  "70",
		"0.05":                                "0.05",
		"-1":                                  "-1",
		"off":                                 "off",
		"true":                                "true",
		"auto":                                "auto",
		"AUTO":                                "'AUTO'",
		"TokenBigram":                         "'TokenBigram'",
		`TokenNgram("unify_alphabet", false)`: `'TokenNgram("unify_alphabet", false)'`,
		"'NormalizerNFKC150'":                 "'NormalizerNFKC150'",
		`"Quoted"`:                            `"Quoted"`,
		"it's":                                "'it''s'",
		"two words":                           "'two words'",
		"default":                             "'default'",
		"":                                    "''",
	}
	for in, want := range cases {
		if got := storageParamValue(in); got != want {
			t.Errorf("storageParamValue(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestStorageParamStringValuesQuoted(t *testing.T) {
	with := map[string]string{"tokenizer": "TokenBigram", "normalizers": "NormalizerNFKC150"}
	d := &schema.Database{Tables: map[string]*schema.Table{"public.memos": {
		Name:    "memos",
		Columns: map[string]*schema.Column{"content": {Type: "text"}},
		With:    map[string]string{"vacuum_index_cleanup": "AUTO"},
		Indexes: []*schema.Index{{Name: "memos_v2", Columns: []string{"content"}, Using: "pgroonga", With: with}},
	}}}
	p := Plan(emptyLive(), d, false)
	if !findCreate(p, `with (vacuum_index_cleanup = 'AUTO');`) {
		t.Errorf("table storage parameter not quoted: %v", p.Creates)
	}
	if !findCreate(p, `using pgroonga("content") with (normalizers = 'NormalizerNFKC150', tokenizer = 'TokenBigram');`) {
		t.Errorf("index storage parameters not quoted: %v", p.Creates)
	}

	live := liveWithTable("public.memos", map[string]*LiveColumn{"content": {Type: "text"}})
	lt := live.Tables["public.memos"]
	lt.Options = map[string]string{"vacuum_index_cleanup": "AUTO"}
	lt.Indexes["memos_v2"] = true
	lt.IndexDefs = map[string]*LiveIndex{"memos_v2": {Method: "pgroonga", Keys: []string{"content"}, Descending: []bool{false}, NullsFirst: []bool{false},
		Options: map[string]string{"tokenizer": "TokenBigram", "normalizers": "NormalizerNFKC150"}}}
	if sql := Render(Plan(live, d, true)); sql != "" {
		t.Errorf("the stored values match their declarations, got:\n%s", sql)
	}
	d.Tables["public.memos"].With["vacuum_index_cleanup"] = "On"
	if p := Plan(live, d, false); !findAlter(p, `alter table "public"."memos" set (vacuum_index_cleanup = 'On');`) {
		t.Errorf("changed table storage parameter not quoted: %v", p.Alters)
	}
}

func TestStorageParamsInDeclarationOrder(t *testing.T) {
	d := &schema.Database{
		Tables: map[string]*schema.Table{"public.t": {
			Name:                "t",
			Columns:             map[string]*schema.Column{"id": {Type: "int"}, "k": {Type: "int"}},
			With:                map[string]string{"toast_tuple_target": "4000", "fillfactor": "90", "autovacuum_enabled": "false"},
			WithOrder:           []string{"toast_tuple_target", "fillfactor", "autovacuum_enabled"},
			PrimaryKey:          []string{"id"},
			PrimaryKeyName:      "t_pkey",
			PrimaryKeyWith:      map[string]string{"fillfactor": "80", "deduplicate_items": "off"},
			PrimaryKeyWithOrder: []string{"fillfactor", "deduplicate_items"},
			Constraints: []*schema.Constraint{{Name: "t_k_key", Type: "unique", Columns: []string{"k"},
				With: map[string]string{"fillfactor": "70", "deduplicate_items": "off"}, WithOrder: []string{"fillfactor", "deduplicate_items"}}},
			Indexes: []*schema.Index{{Name: "t_k", Columns: []string{"k"},
				With: map[string]string{"fillfactor": "50", "deduplicate_items": "off", "a_not_in_order": "1"}, WithOrder: []string{"fillfactor", "deduplicate_items"}}},
		}},
		Views: map[string]*schema.View{"public.mv": {Name: "mv", Schema: "public", Materialized: true, Query: "select 1",
			With: map[string]string{"fillfactor": "60", "autovacuum_enabled": "false"}, WithOrder: []string{"fillfactor", "autovacuum_enabled"}}},
	}
	p := Plan(emptyLive(), d, false)
	for _, want := range []string{
		`with (toast_tuple_target = 4000, fillfactor = 90, autovacuum_enabled = false);`,
		`primary key ("id") with (fillfactor = 80, deduplicate_items = off);`,
		`unique ("k") with (fillfactor = 70, deduplicate_items = off);`,
		`("k") with (fillfactor = 50, deduplicate_items = off, a_not_in_order = 1);`,
		`create materialized view if not exists "public"."mv" with (fillfactor = 60, autovacuum_enabled = false)`,
	} {
		if !findCreate(p, want) && !findAlter(p, want) {
			t.Errorf("missing %q in:\n%s", want, Render(p))
		}
	}

	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "integer"}, "k": {Type: "integer"}})
	live.Tables["public.t"].Options = map[string]string{"fillfactor": "10"}
	if got := storageParamStmts("table", "public.t", d.Tables["public.t"].With, d.Tables["public.t"].WithOrder, live.Tables["public.t"]); len(got) != 1 || got[0] != `alter table "public"."t" set (toast_tuple_target = 4000, fillfactor = 90, autovacuum_enabled = false);` {
		t.Errorf("SET follows the declaration order, got %v", got)
	}
}
