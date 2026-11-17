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
