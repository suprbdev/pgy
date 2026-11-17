package diff

import (
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestSQLValue(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{
		{nil, "null"}, {true, "true"}, {42, "42"}, {9.5, "9.5"}, {"it's", "'it''s'"},
		{time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "'2026-01-02'"},
		{[]any{"a", "b c", nil, 1}, `'{"a","b c",NULL,1}'`},
		{[]any{[]any{1, 2}, []any{3, 4}}, `'{{1,2},{3,4}}'`},
		{map[string]any{"x": 1}, `'{"x":1}'`},
	} {
		if got := sqlValue(c.in); got != c.want {
			t.Errorf("sqlValue(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func dataDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{
		"app.feature": {Name: "feature", ColumnOrder: []string{"id", "plan", "label"},
			Columns:     map[string]*schema.Column{"id": {Type: "int", Identity: "always"}, "plan": {Type: "text"}, "label": {Type: "text"}},
			PrimaryKey:  []string{"id"},
			ForeignKeys: []*schema.ForeignKey{{Name: "f_plan", Columns: []string{"plan"}, RefTable: "app.plan", RefColumns: []string{"code"}}},
			Data:        &schema.TableData{Rows: []map[string]any{{"id": 1, "plan": "free", "label": "basics"}}}},
		"app.plan": {Name: "plan", ColumnOrder: []string{"code", "name"},
			Columns:    map[string]*schema.Column{"code": {Type: "text"}, "name": {Type: "text"}},
			PrimaryKey: []string{"code"},
			Data:       &schema.TableData{Rows: []map[string]any{{"code": "free", "name": "Free"}, {"code": "pro", "name": "Pro"}}}},
	}}
}

func TestDataNewTablesInsertInForeignKeyOrder(t *testing.T) {
	p := Plan(emptyLive(), dataDB(), false)
	sql := Render(p)
	plan := strings.Index(sql, `insert into "app"."plan" ("code", "name") values ('free', 'Free');`)
	feature := strings.Index(sql, `insert into "app"."feature" ("id", "plan", "label") overriding system value values (1, 'free', 'basics');`)
	fk := strings.Index(sql, `add constraint "f_plan"`)
	if plan < 0 || feature < 0 || plan > feature || fk < feature {
		t.Errorf("expected plan rows, then feature rows, then the foreign key:\n%s", sql)
	}
}

func TestDataFromSnapshot(t *testing.T) {
	live := Simulate(emptyLive(), dataDB())
	if sql := Render(PlanWith(live, dataDB(), Options{Unsafe: true, Prune: true})); sql != "" {
		t.Fatalf("simulated data must be in sync, got:\n%s", sql)
	}
	live.TableData["app.plan"] = &LiveTableData{Missing: []int{1}, Changed: map[int][]string{0: {"name"}}, Extra: []map[string]string{{"code": "old"}}}
	p := PlanWith(live, dataDB(), Options{Unsafe: true})
	for _, want := range []string{
		`insert into "app"."plan" ("code", "name") values ('pro', 'Pro');`,
		`update only "app"."plan" set "name" = 'Free' where "code" = 'free';`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in\n%s", want, Render(p))
		}
	}
	if strings.Contains(Render(p), "delete") {
		t.Error("rows are only deleted with --prune")
	}
	if p := PlanWith(live, dataDB(), Options{Unsafe: true, Prune: true}); len(p.Drops) == 0 || p.Drops[0] != `delete from only "app"."plan" where "code" = 'old';` {
		t.Errorf("expected a delete, got %v", p.Drops)
	}
	delete(live.TableData, "app.plan")
	if !findAlter(PlanWith(live, dataDB(), Options{}), `insert into "app"."plan" ("code", "name") values ('free', 'Free') on conflict ("code") do update set "name" = excluded."name";`) {
		t.Error("without introspected data an existing table gets upserts")
	}
}

func TestDataSQLConvertsMapsToTheColumnType(t *testing.T) {
	dt := &schema.Table{Columns: map[string]*schema.Column{"p": {Type: "app.pair"}, "ps": {Type: "app.pair[]"}, "inherited": {}}}
	for _, c := range []struct {
		col  string
		v    any
		want string
	}{
		{"p", map[string]any{"a": 1}, `(select v from jsonb_to_record('{"v":{"a":1}}'::jsonb) as x(v app.pair))`},
		{"ps", []any{map[string]any{"a": 1}}, `(select v from jsonb_to_record('{"v":[{"a":1}]}'::jsonb) as x(v app.pair[]))`},
		{"inherited", map[string]any{"a": 1}, `(jsonb_populate_record((select row(x.*)::"app"."t" from (select (null::"app"."t").*) x), '{"inherited":{"a":1}}'::jsonb))."inherited"`},
		{"p", "(1,x)", `'(1,x)'`},
		{"n", 5, "5"},
	} {
		if got := dataSQL("app.t", dt, c.col, c.v); got != c.want {
			t.Errorf("dataSQL(%s, %v) = %s, want %s", c.col, c.v, got, c.want)
		}
	}
	if got := dataSQL("app.t", nil, "p", map[string]any{"a": 1}); !strings.HasPrefix(got, "(jsonb_populate_record((select row(x.*)") {
		t.Errorf("an undeclared column goes through the table's row type, got %s", got)
	}
}

func TestDataChangedKeyIsRewritten(t *testing.T) {
	live := Simulate(emptyLive(), dataDB())
	live.TableData["app.plan"] = &LiveTableData{Changed: map[int][]string{0: {"code"}, 1: {"code", "name"}}}
	p := PlanWith(live, dataDB(), Options{})
	for _, want := range []string{
		`update only "app"."plan" set "code" = 'free' where "code" = 'free';`,
		`update only "app"."plan" set "code" = 'pro', "name" = 'Pro' where "code" = 'pro';`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in\n%s", want, Render(p))
		}
	}
}

func TestDataKeyOperatorOffSearchPath(t *testing.T) {
	live := Simulate(emptyLive(), dataDB())
	live.TableData["app.plan"] = &LiveTableData{
		Changed:   map[int][]string{0: {"code"}, 1: {"name"}},
		Extra:     []map[string]string{{"code": "old"}},
		KeyEquals: map[string]string{"code": `operator("ext".=)`},
	}
	p := PlanWith(live, dataDB(), Options{Unsafe: true, Prune: true})
	for _, want := range []string{
		`update only "app"."plan" set "code" = 'free' where "code" operator("ext".=) 'free';`,
		`update only "app"."plan" set "name" = 'Pro' where "code" operator("ext".=) 'pro';`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in\n%s", want, Render(p))
		}
	}
	if len(p.Drops) == 0 || p.Drops[0] != `delete from only "app"."plan" where "code" operator("ext".=) 'old';` {
		t.Errorf("expected a delete with the key's operator, got %v", p.Drops)
	}
}

func TestDataKeyTypeError(t *testing.T) {
	types := map[string]dataColumn{
		"id":    {typ: "integer", cmp: "integer", eq: "=", eqSchema: "pg_catalog"},
		"code":  {typ: "ext.citext", cmp: "ext.citext", eq: `operator("ext".=)`, eqSchema: "ext"},
		"shape": {typ: "box", cmp: "box"},
	}
	if err := dataKeyTypeError("app.t", []string{"id", "code", "later"}, types); err != nil {
		t.Errorf("keys with an equality operator, and columns the database lacks, pass: %v", err)
	}
	err := dataKeyTypeError("app.t", []string{"id", "shape"}, types)
	if err == nil || !strings.Contains(err.Error(), "data for app.t: key column shape has type box, which has no default btree operator class") {
		t.Errorf("expected an error naming the table, the column and its type, got %v", err)
	}
}

func TestDataMapHint(t *testing.T) {
	types := map[string]dataColumn{"attrs": {typ: "hstore"}, "meta": {typ: "jsonb", json: true}, "pairs": {typ: "app.pair[]", json: true}}
	ok := []map[string]any{{"attrs": `"a"=>"1"`, "meta": map[string]any{"a": 1}, "pairs": []any{map[string]any{"a": 1}}, "unknown": map[string]any{}}}
	if hint := dataMapHint(ok, types); hint != "" {
		t.Errorf("no hint expected, got %q", hint)
	}
	bad := append(ok, map[string]any{"attrs": map[string]any{"a": "1"}})
	if hint := dataMapHint(bad, types); !strings.Contains(hint, "row 2 gives column attrs a map") || !strings.Contains(hint, "hstore") {
		t.Errorf("hint = %q", hint)
	}
	if hint := dataMapHint([]map[string]any{{"attrs": []any{map[string]any{"a": "1"}}}}, types); !strings.Contains(hint, "row 1 gives column attrs a map") {
		t.Errorf("a map inside a list counts, got %q", hint)
	}
}

func TestDollarTag(t *testing.T) {
	for body, want := range map[string]string{
		"select 1":                        "$$",
		"execute $$x$$":                   "$body$",
		"execute $$x$$ and $body$y$body$": "$body1$",
	} {
		if got := dollarTag(body); got != want {
			t.Errorf("dollarTag(%q) = %s, want %s", body, got, want)
		}
	}
}

func TestDataJSONColumnValues(t *testing.T) {
	dt := &schema.Table{Columns: map[string]*schema.Column{"j": {Type: "jsonb"}, "js": {Type: "json"}, "arr": {Type: "text[]"}}}
	for _, c := range []struct {
		col  string
		v    any
		want string
	}{
		{"j", []any{}, `'[]'::jsonb`},
		{"j", "just a string", `'"just a string"'::jsonb`},
		{"j", map[string]any{"it's": []any{1, nil}}, `'{"it''s":[1,null]}'::jsonb`},
		{"j", nil, `null`},
		{"js", 123, `'123'::jsonb::json`},
		{"js", nil, `null`},
		{"arr", []any{}, `'{}'`},
	} {
		if got := dataSQL("app.t", dt, c.col, c.v); got != c.want {
			t.Errorf("dataSQL(%s, %v) = %s, want %s", c.col, c.v, got, c.want)
		}
	}
}
