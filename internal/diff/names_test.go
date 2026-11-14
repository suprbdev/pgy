package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

const longTable = "app.a_really_quite_long_table_name_for_testing"
const longCol = "some_really_long_column_name_here"

func TestTruncateIdentRespectsMultibyte(t *testing.T) {
	s := strings.Repeat("a", 62) + "é"
	if got := truncateIdent(s); got != strings.Repeat("a", 62) {
		t.Errorf("must not split a multibyte rune, got %q", got)
	}
	if got := truncateIdent("short"); got != "short" {
		t.Errorf("short identifiers pass through, got %q", got)
	}
}

func TestLongAutoNamesTruncatedWithDistinctSuffixes(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		longTable: {
			Name:    longTable,
			Columns: map[string]*schema.Column{longCol: {Type: "text", Unique: true}},
			Indexes: []*schema.Index{{Columns: []string{longCol}}},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	idx := makeObjectName(uniqueKeyBase(longTable, longCol), "idx")
	key := makeObjectName(uniqueKeyBase(longTable, longCol), "key")
	if len(idx) != 63 || len(key) != 63 || !strings.HasSuffix(idx, "_idx") || !strings.HasSuffix(key, "_key") {
		t.Fatalf("expected 63-byte names keeping their suffix, got %q %q", idx, key)
	}
	if !findCreate(p, `index if not exists "`+idx+`"`) {
		t.Errorf("expected truncated index name, got %+v", p.Creates)
	}
	if !findAlter(p, `add constraint "`+key+`" unique`) {
		t.Errorf("expected truncated unique key name, got %+v", p.Alters)
	}
}

func TestLongAutoNamesAdoptLegacyTruncation(t *testing.T) {
	legacyIdx := truncateIdent(uniqueKeyBase(longTable, longCol) + "_idx")
	legacyKey := truncateIdent(uniqueKeyBase(longTable, longCol) + "_key")
	live := liveWithTable(longTable, map[string]*LiveColumn{longCol: {Type: "text", Nullable: false}})
	live.Schemas["app"] = true
	live.Tables[longTable].Indexes[legacyIdx] = true
	live.Tables[longTable].Constraints[legacyKey] = true
	desired := &schema.Database{Tables: map[string]*schema.Table{
		longTable: {
			Name:    longTable,
			Columns: map[string]*schema.Column{longCol: {Type: "text", Unique: true}},
			Indexes: []*schema.Index{{Columns: []string{longCol}, Comment: "c"}},
		},
	}}
	p := Plan(live, desired, false)
	if len(p.Creates) != 0 || len(p.Alters) != 1 || !findAlter(p, `comment on index "app"."`+legacyIdx+`"`) {
		t.Errorf("objects created under the old PostgreSQL-truncated names must be adopted; creates=%v alters=%v", p.Creates, p.Alters)
	}
}

func TestLongDeclaredNamesComparedTruncated(t *testing.T) {
	long := "constraint_" + strings.Repeat("x", 70)
	live := liveWithTable("public.t", map[string]*LiveColumn{"a": {Type: "integer", Nullable: false}})
	live.Tables["public.t"].Constraints[truncateIdent(long)] = true
	live.Tables["public.t"].Indexes[truncateIdent(long)] = true
	live.Tables["public.t"].Triggers[truncateIdent(long)] = ""
	live.Tables["public.t"].Policies = map[string]*LivePolicy{truncateIdent(long): {Cmd: "all", Using: "true"}}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:        "t",
			Columns:     map[string]*schema.Column{"a": {Type: "int"}},
			Constraints: []*schema.Constraint{{Name: long, Type: "check", Expression: "a > 0"}},
			Indexes:     []*schema.Index{{Name: long, Columns: []string{"a"}}},
			Triggers:    []*schema.Trigger{{Name: long, Timing: "before", Events: []string{"insert"}, Level: "row", Procedure: "f()"}},
			Policies:    []*schema.Policy{{Name: long, Using: "true"}},
		},
	}}
	if sql := Render(Plan(live, desired, false)); sql != "" {
		t.Errorf("declared names longer than 63 bytes must match their truncated live names, got:\n%s", sql)
	}
}

func TestSplitIndexOrdering(t *testing.T) {
	cases := map[string][2]string{
		"created_at":                      {"created_at", ""},
		"created_at DESC":                 {"created_at", "desc"},
		"created_at desc nulls last":      {"created_at", "desc nulls last"},
		"created_at NULLS FIRST":          {"created_at", "nulls first"},
		"lower(email) asc":                {"lower(email)", "asc"},
		"desc":                            {"desc", ""},
		"coalesce(a, b) desc nulls first": {"coalesce(a, b)", "desc nulls first"},
	}
	for in, want := range cases {
		b, o := splitIndexOrdering(in)
		if b != want[0] || o != want[1] {
			t.Errorf("splitIndexOrdering(%q) = (%q, %q), want %v", in, b, o, want)
		}
	}
}

func TestIndexColumnOrdering(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"created": {Type: "timestamptz"}, "name": {Type: "text"}},
			Indexes: []*schema.Index{
				{Name: "t_created", Columns: []string{"created desc nulls last", "lower(name) asc"}},
				{Name: "t_name_trgm", Columns: []string{"name desc"}, Using: "gist", Opclasses: map[string]string{"name": "gist_trgm_ops"}},
				{Columns: []string{"created desc"}},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	for _, want := range []string{
		`create index if not exists "t_created" on "public"."t"("created" desc nulls last, (lower(name)) asc);`,
		`create index if not exists "t_name_trgm" on "public"."t" using gist("name" gist_trgm_ops desc);`,
		`create index if not exists "public_t_created_idx" on "public"."t"("created" desc);`,
	} {
		if !findCreate(p, want) {
			t.Errorf("missing %q in %+v", want, p.Creates)
		}
	}
}
