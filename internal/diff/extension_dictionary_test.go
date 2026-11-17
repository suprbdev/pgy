package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func dictionaryLive() *Live {
	live := memberLive()
	live.TSDictionaries = map[string]bool{"public.unaccent": true}
	live.TSDictionaryDefs = map[string]*schema.TSDictionary{"public.unaccent": {Template: "public.unaccent", Options: map[string]string{"rules": "'unaccent'"}}}
	live.TSComments = map[string]string{}
	live.ExtensionMembers["tsdict:public.unaccent"] = true
	return live
}

func TestDictionaryAttributeOnlyAltered(t *testing.T) {
	live := dictionaryLive()
	d := &schema.Database{TSDictionaries: map[string]*schema.TSDictionary{
		"public.unaccent": {Schema: "public", Name: "unaccent", Options: map[string]string{"rules": "xsyn_sample"}, Comment: "sample rules", Owner: "dict_owner"},
	}}
	sql, warns := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, sql,
		`alter text search dictionary "public"."unaccent" (rules = xsyn_sample);`,
		`alter text search dictionary "public"."unaccent" owner to "dict_owner";`,
		`comment on text search dictionary "public"."unaccent" is 'sample rules';`,
	)
	if len(warns) > 0 {
		t.Errorf("a dictionary declared without a template has no template to compare: %v", warns)
	}

	live.TSDictionaryDefs["public.unaccent"].Options = map[string]string{"rules": "'xsyn_sample'"}
	live.TSComments["dict:public.unaccent"] = "sample rules"
	live.Owners = map[string]string{"tsdict:public.unaccent": "dict_owner"}
	if sql, _ := planSQL(t, live, d, Options{Unsafe: true, Prune: true}); sql != "" {
		t.Errorf("a dictionary in the declared state is left alone, got:\n%s", sql)
	}
}

func TestDictionaryAttributeOnlyPending(t *testing.T) {
	live := memberLive()
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "dict_int", Schema: "public"}},
		TSDictionaries: map[string]*schema.TSDictionary{
			"public.intdict": {Schema: "public", Name: "intdict", Options: map[string]string{"maxlen": "4", "rejectlong": "true"}},
		},
	}
	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`create extension "dict_int" schema "public";`,
		`alter text search dictionary "public"."intdict" (maxlen = 4, rejectlong = true);`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestDictionaryAttributeOnlyMissing(t *testing.T) {
	live := memberLive()
	d := &schema.Database{TSDictionaries: map[string]*schema.TSDictionary{
		"public.intdict": {Schema: "public", Name: "intdict", Options: map[string]string{"maxlen": "4"}, Comment: "ints"},
	}}
	sql, warns := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	if sql != "" {
		t.Errorf("a dictionary without a template is never created, got:\n%s", sql)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "text search dictionary public.intdict has no template, so it only manages a text search dictionary that already exists") {
		t.Errorf("want a warning naming the declaration, got %v", warns)
	}
}

func TestDictionaryOwner(t *testing.T) {
	live := memberLive()
	d := &schema.Database{TSDictionaries: map[string]*schema.TSDictionary{
		"public.english_nostop": {Schema: "public", Name: "english_nostop", Template: "snowball", Options: map[string]string{"language": "english"}, Owner: "dict_owner"},
	}}
	sql, _ := planSQL(t, live, d, Options{})
	wantSQL(t, sql,
		`create text search dictionary "public"."english_nostop" (template = snowball, language = english);`,
		`alter text search dictionary "public"."english_nostop" owner to "dict_owner";`,
	)
}
