package diff

import (
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func triggerSpellingPlan(procedure, liveDef string) string {
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"a": {Type: "int"}}, Triggers: []*schema.Trigger{
			{Name: "trg", Timing: "before", Events: []string{"update"}, Level: "row", Procedure: procedure},
		}},
	}}
	live := liveWithTable("public.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	live.Tables["public.t"].Triggers["trg"] = "CREATE TRIGGER trg BEFORE UPDATE ON public.t FOR EACH ROW EXECUTE FUNCTION " + liveDef
	return Render(Plan(live, d, false))
}

func TestTriggerArgumentSpellingsMatchLive(t *testing.T) {
	cases := []struct{ procedure, live string }{
		{"moddatetime(updated_at)", "moddatetime('updated_at')"},
		{"moddatetime(Updated_At)", "moddatetime('updated_at')"},
		{`moddatetime("UpdatedAt")`, "moddatetime('UpdatedAt')"},
		{"lo_manage ( raster )", "lo_manage('raster')"},
		{"tsvector_update_trigger(tsv, 'pg_catalog.english', title, body)", "tsvector_update_trigger('tsv', 'pg_catalog.english', 'title', 'body')"},
		{"notify($$doc$$)", "notify('doc')"},
		{"notify($tag$it's$tag$)", "notify('it''s')"},
		{`notify(E'doc\tchanges')`, "notify('doc\tchanges')"},
		{`notify(E'a\\b\'c')`, `notify('a\b''c')`},
		{"autoinc(id, 007)", "autoinc('id', '7')"},
		{"autoinc(id, 1.50)", "autoinc('id', '1.50')"},
		{"check_primary_key('parent_id', 'public.parent', 'id')", "check_primary_key('parent_id', 'public.parent', 'id')"},
		{"touch()", "touch()"},
	}
	for _, c := range cases {
		if sql := triggerSpellingPlan(c.procedure, c.live); sql != "" {
			t.Errorf("%s must match live %s, got:\n%s", c.procedure, c.live, sql)
		}
	}
}

func TestTriggerArgumentChangeDetected(t *testing.T) {
	cases := []struct{ procedure, live string }{
		{"moddatetime(updated_at)", "moddatetime('changed_at')"},
		{`moddatetime("UpdatedAt")`, "moddatetime('updatedat')"},
		{"check_primary_key('parent_id', 'public.parent', 'id')", "check_primary_key('parent_id', 'parent', 'id')"},
		{"notify('a', 'b')", "notify('a')"},
		{"notify()", "notify('a')"},
		{"moddatetime(updated_at)", "other_function('updated_at')"},
	}
	for _, c := range cases {
		sql := triggerSpellingPlan(c.procedure, c.live)
		if !strings.Contains(sql, `drop trigger "trg"`) || !strings.Contains(sql, "execute procedure "+c.procedure) {
			t.Errorf("%s differs from live %s and must be recreated, got:\n%s", c.procedure, c.live, sql)
		}
	}
}

func TestTriggerFunctionQualifierIgnored(t *testing.T) {
	cases := []struct{ procedure, live string }{
		{"pg_catalog.suppress_redundant_updates_trigger()", "suppress_redundant_updates_trigger()"},
		{"pg_catalog.tsvector_update_trigger(tsv, 'pg_catalog.simple', body)", "tsvector_update_trigger('tsv', 'pg_catalog.simple', 'body')"},
		{"public.touch()", "touch()"},
		{`"public"."touch"()`, "touch()"},
		{"ext.moddatetime(updated_at)", "ext.moddatetime('updated_at')"},
	}
	for _, c := range cases {
		if sql := triggerSpellingPlan(c.procedure, c.live); sql != "" {
			t.Errorf("%s must match live %s, got:\n%s", c.procedure, c.live, sql)
		}
	}
	if sql := triggerSpellingPlan("ext.moddatetime(updated_at)", "moddatetime('updated_at')"); sql == "" {
		t.Error("a function in another schema is a different function")
	}
}

func TestTriggerWhenClauseStillCompared(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"a": {Type: "int"}}, Triggers: []*schema.Trigger{
			{Name: "trg", Timing: "before", Events: []string{"update"}, Level: "row", When: "old.a is distinct from new.a", Procedure: "moddatetime(updated_at)"},
		}},
	}}
	live := liveWithTable("public.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	live.Tables["public.t"].Triggers["trg"] = "CREATE TRIGGER trg BEFORE UPDATE ON public.t FOR EACH ROW WHEN ((old.a IS DISTINCT FROM new.a)) EXECUTE FUNCTION moddatetime('updated_at')"
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("equivalent trigger must not diff, got:\n%s", sql)
	}
	live.Tables["public.t"].Triggers["trg"] = "CREATE TRIGGER trg BEFORE UPDATE ON public.t FOR EACH ROW WHEN ((old.a = new.a)) EXECUTE FUNCTION moddatetime('updated_at')"
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, `drop trigger "trg"`) {
		t.Errorf("a changed WHEN must recreate the trigger, got:\n%s", sql)
	}
}

func TestParseTriggerArgs(t *testing.T) {
	cases := map[string][]string{
		"":                             {},
		"  a ,B, \"C d\" ":             {"a", "b", "C d"},
		"'x''y', E'\\x41\\101\\u00e9'": {"x'y", "AA\u00e9"},
		"$$a,b$$, 12":                  {"a,b", "12"},
		"'a\\tb'":                      {`a\tb`},
	}
	for in, want := range cases {
		got, ok := parseTriggerArgs(in)
		if !ok || !slices.Equal(got, want) {
			t.Errorf("parseTriggerArgs(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"'unterminated", "a b", "$tag$open", `E'\u12'`} {
		if got, ok := parseTriggerArgs(in); ok {
			t.Errorf("parseTriggerArgs(%q) = %q, want a parse failure", in, got)
		}
	}
}

func excludeLive(def string, opclasses ...LiveIndexOpclass) *LiveTable {
	return &LiveTable{
		ConstraintDefs: map[string]string{"x": def},
		IndexOpclasses: map[string][]LiveIndexOpclass{"x": opclasses},
	}
}

func TestExcludeSpellingsMatchLive(t *testing.T) {
	def := LiveIndexOpclass{Name: "gist_int4_ops", Default: true}
	rng := LiveIndexOpclass{Name: "range_ops", Default: true}
	cases := []struct {
		name string
		ct   schema.Constraint
		live *LiveTable
	}{
		{"no using is btree", schema.Constraint{Expression: "(room_id with =)"},
			excludeLive("EXCLUDE USING btree (room_id WITH =)", LiveIndexOpclass{Name: "int4_ops", Default: true})},
		{"default opclass named", schema.Constraint{Expression: "using gist (room_id gist_int4_ops with =, during with &&)"},
			excludeLive("EXCLUDE USING gist (room_id WITH =, during WITH &&)", def, rng)},
		{"default opclass qualified", schema.Constraint{Expression: "using gist (room_id public.gist_int4_ops with =, during pg_catalog.range_ops with &&)"},
			excludeLive("EXCLUDE USING gist (room_id WITH =, during WITH &&)", def, rng)},
		{"catalog operator", schema.Constraint{Expression: "using gist (room_id with operator(pg_catalog.=), during with &&)"},
			excludeLive("EXCLUDE USING gist (room_id WITH =, during WITH &&)", def, rng)},
		{"qualified operator kept", schema.Constraint{Expression: "using gist (a with operator(ext.&&))"},
			excludeLive("EXCLUDE USING gist (a WITH OPERATOR(ext.&&))", def)},
		{"case, spacing, where, deferral", schema.Constraint{Expression: "USING GIST ( room_id WITH = , during WITH && ) WHERE ( price > 2 )", InitiallyDeferred: true},
			excludeLive("EXCLUDE USING gist (room_id WITH =, during WITH &&) WHERE ((price > (2)::numeric)) DEFERRABLE INITIALLY DEFERRED", def, rng)},
		{"non-default opclass with parameters", schema.Constraint{Expression: "using gist (a gist__intbig_ops(siglen=32) with &&)"},
			excludeLive("EXCLUDE USING gist (a gist__intbig_ops (siglen='32') WITH &&)", LiveIndexOpclass{Name: "gist__intbig_ops"})},
		{"expression key, include, storage", schema.Constraint{Expression: "using gist (lower(name) with =, (a + b) with =) include (c) with (fillfactor = 70)"},
			excludeLive("EXCLUDE USING gist (lower(name) WITH =, ((a + b)) WITH =) INCLUDE (c) WITH (fillfactor='70')", def, def)},
		{"no opclass data", schema.Constraint{Expression: "using gist (room_id with =)"},
			&LiveTable{ConstraintDefs: map[string]string{"x": "EXCLUDE USING gist (room_id WITH =)"}}},
	}
	for _, c := range cases {
		c.ct.Name, c.ct.Type = "x", "exclude"
		if !constraintMatchesLive(c.live, "x", &c.ct) {
			t.Errorf("%s: %q must match %q", c.name, c.ct.Expression, c.live.ConstraintDefs["x"])
		}
	}
}

func TestExcludeChangeDetected(t *testing.T) {
	def := LiveIndexOpclass{Name: "gist_int4_ops", Default: true}
	trgm := LiveIndexOpclass{Name: "gist_trgm_ops"}
	cases := []struct {
		name string
		ct   schema.Constraint
		live *LiveTable
	}{
		{"method", schema.Constraint{Expression: "(room_id with =)"}, excludeLive("EXCLUDE USING gist (room_id WITH =)", def)},
		{"operator", schema.Constraint{Expression: "using gist (room_id with =)"}, excludeLive("EXCLUDE USING gist (room_id WITH <>)", def)},
		{"shadowed catalog operator", schema.Constraint{Expression: "using gist (a with &&)"}, excludeLive("EXCLUDE USING gist (a WITH OPERATOR(pg_catalog.&&))", def)},
		{"opclass dropped", schema.Constraint{Expression: "using gist (name with %)"}, excludeLive("EXCLUDE USING gist (name gist_trgm_ops WITH %)", trgm)},
		{"opclass added", schema.Constraint{Expression: "using gist (name gist_trgm_ops with %)"}, excludeLive("EXCLUDE USING gist (name WITH %)", def)},
		{"opclass parameter", schema.Constraint{Expression: "using gist (name gist_trgm_ops(siglen=64) with %)"}, excludeLive("EXCLUDE USING gist (name gist_trgm_ops (siglen='32') WITH %)", trgm)},
		{"element count", schema.Constraint{Expression: "using gist (room_id with =, during with &&)"}, excludeLive("EXCLUDE USING gist (room_id WITH =)", def)},
		{"predicate", schema.Constraint{Expression: "using gist (room_id with =) where (price > 0)"}, excludeLive("EXCLUDE USING gist (room_id WITH =) WHERE ((price > (2)::numeric))", def)},
		{"deferral", schema.Constraint{Expression: "using gist (room_id with =)", Deferrable: true}, excludeLive("EXCLUDE USING gist (room_id WITH =)", def)},
		{"include", schema.Constraint{Expression: "using gist (room_id with =) include (a)"}, excludeLive("EXCLUDE USING gist (room_id WITH =) INCLUDE (b)", def)},
	}
	for _, c := range cases {
		c.ct.Name, c.ct.Type = "x", "exclude"
		if constraintMatchesLive(c.live, "x", &c.ct) {
			t.Errorf("%s: %q must differ from %q", c.name, c.ct.Expression, c.live.ConstraintDefs["x"])
		}
	}
}

func TestExcludeStorageParameters(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name: "t", Columns: map[string]*schema.Column{"room": {Type: "int"}, "day": {Type: "date"}},
		Constraints: []*schema.Constraint{
			{Name: "t_key", Type: "exclude", Expression: "using gist (room with =, day with <>) where (room > 0)", With: map[string]string{"fillfactor": "80"}},
			{Name: "t_both", Type: "exclude", Expression: "using gist (room with =) with (fillfactor = 70, buffering = off)", With: map[string]string{"fillfactor": "90"}, Tablespace: "fast"},
			{Name: "t_expr", Type: "exclude", Expression: "using gist (room with =) include (day) with (fillfactor = 60)"},
		},
	}}}
	sql := Render(Plan(emptyLive(), d, false))
	for _, want := range []string{
		`add constraint "t_key" exclude using gist (room with =, day with <>) with (fillfactor = 80) where (room > 0);`,
		`add constraint "t_both" exclude using gist (room with =) with (buffering = off, fillfactor = 90) using index tablespace "fast";`,
		`add constraint "t_expr" exclude using gist (room with =) include (day) with (fillfactor = 60);`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}

	live := liveWithTable("app.t", map[string]*LiveColumn{"room": {Type: "integer"}, "day": {Type: "date"}})
	live.Schemas["app"] = true
	lt := live.Tables["app.t"]
	lt.ConstraintDefs = map[string]string{
		"t_key":  "EXCLUDE USING gist (room WITH =, day WITH <>) WITH (fillfactor='50') WHERE ((room > 0))",
		"t_both": "EXCLUDE USING gist (room WITH =) WITH (fillfactor='90', buffering=off)",
		"t_expr": "EXCLUDE USING gist (room WITH =) INCLUDE (day) WITH (fillfactor='60', buffering=auto)",
	}
	lt.IndexDefs = map[string]*LiveIndex{
		"t_key":  {Options: map[string]string{"fillfactor": "50"}},
		"t_both": {Options: map[string]string{"fillfactor": "90", "buffering": "off"}, Tablespace: "fast"},
		"t_expr": {Options: map[string]string{"fillfactor": "60", "buffering": "auto"}},
	}
	for name := range lt.ConstraintDefs {
		lt.Constraints[name] = true
	}
	sql = Render(Plan(live, d, true))
	want := "alter index \"app\".\"t_key\" set (fillfactor = 80);\nalter index \"app\".\"t_expr\" reset (buffering);"
	if !strings.Contains(sql, `alter index "app"."t_key" set (fillfactor = 80);`) || !strings.Contains(sql, `alter index "app"."t_expr" reset (buffering);`) ||
		strings.Contains(sql, "drop constraint") || strings.Contains(sql, "t_both") {
		t.Errorf("storage parameters must change in place; want\n%s\ngot:\n%s", want, sql)
	}
	d.Tables["app.t"].Constraints = []*schema.Constraint{{Name: "t_key", Type: "exclude", Expression: "using gist (room with =, day with <>) where (room > 0)"}}
	if sql := Render(Plan(live, d, true)); strings.Contains(sql, "t_key") {
		t.Errorf("undeclared storage parameters are unmanaged, got:\n%s", sql)
	}
}

func TestExpressionSpellingsMatchLive(t *testing.T) {
	cases := []struct{ desired, live string }{
		{"cast(id as text) = name", "((id)::text = name)"},
		{"CAST( name AS varchar(10) ) <> ''", "(((name)::character varying(10))::text <> ''::text)"},
		{"cast(cast(a as int) as text) = b", "(((a)::integer)::text = b)"},
		{"cast(price as numeric(10, 2)) > 0", "((price)::numeric(10,2) > (0)::numeric)"},
		{"lat between -90 and 90", "((lat >= '-90'::integer) AND (lat <= 90))"},
		{"lat not between -90.5 and -1", "((lat < '-90.5'::numeric) OR (lat > '-1'::integer))"},
		{"pg_catalog.length(name) > 0", "(length(name) > 0)"},
		{`"pg_catalog".lower(name) = name`, "(lower(name) = name)"},
		{"a operator(pg_catalog.&&) b", "(a OPERATOR(pg_catalog.&&) b)"},
	}
	for _, c := range cases {
		if !constraintDefsEqual(c.desired, c.live) {
			t.Errorf("%q must equal %q:\n%s\n%s", c.desired, c.live, normalizeConstraintDef(c.desired), normalizeConstraintDef(c.live))
		}
	}
	for _, c := range []struct{ desired, live string }{
		{"cast(id as text) = name", "((id2)::text = name)"},
		{"lat between -90 and 90", "((lat >= '-80'::integer) AND (lat <= 90))"},
		{"a && b", "(a OPERATOR(pg_catalog.&&) b)"},
		{"broadcast(a as b)", "(a)"},
	} {
		if constraintDefsEqual(c.desired, c.live) {
			t.Errorf("%q must differ from %q", c.desired, c.live)
		}
	}
	for _, c := range []struct{ desired, live string }{
		{"pg_catalog.gen_random_uuid()", "gen_random_uuid()"},
		{"cast(now() as date)", "(now())::date"},
		{"cast('5' as integer)", "5"},
	} {
		if !defaultsEqual(c.desired, c.live) {
			t.Errorf("default %q must equal %q", c.desired, c.live)
		}
	}
}

func TestPublicQualifierInsideLiterals(t *testing.T) {
	for _, c := range []struct{ desired, live string }{
		{"kind <> 'public.x'", "(kind <> 'x'::text)"},
		{"note = 'see public.docs'", "(note = 'see docs'::text)"},
		{"tag = any (array['public.a', 'b'])", "(tag = ANY (ARRAY['a'::text, 'b'::text]))"},
	} {
		if constraintDefsEqual(c.desired, c.live) {
			t.Errorf("%q and %q are different values", c.desired, c.live)
		}
		if defaultsEqual(c.desired, c.live) {
			t.Errorf("defaults %q and %q are different values", c.desired, c.live)
		}
	}
	for _, c := range []struct{ desired, live string }{
		{"to_tsvector('public.cfg', body)", "to_tsvector('cfg'::regconfig, body)"},
		{"to_tsvector('cfg', body)", "to_tsvector('public.cfg'::regconfig, body)"},
		{"id < nextval('public.t_id_seq')", "(id < nextval('t_id_seq'::regclass))"},
		{"rel = 'public.t'::regclass or rel = 'u'::regclass", "((rel = 't'::regclass) OR (rel = 'u'::regclass))"},
		{"public.f(a) = 'x'", "(f(a) = 'x'::text)"},
	} {
		if !constraintDefsEqual(c.desired, c.live) {
			t.Errorf("%q must equal %q", c.desired, c.live)
		}
	}
}

func TestTSDictionaryTemplateSpellings(t *testing.T) {
	for _, c := range []struct{ want, live string }{
		{"unaccent", "public.unaccent"},
		{"unaccent", "extensions.unaccent"},
		{"public.unaccent", "public.unaccent"},
		{`"Ext".intdict_template`, "Ext.intdict_template"},
		{"pg_catalog.simple", "simple"},
		{"Snowball", "snowball"},
	} {
		td := &schema.TSDictionary{Template: c.want}
		if _, warn := tsDictionaryAlterStmts("app.d", td, &schema.TSDictionary{Template: c.live}); warn != "" {
			t.Errorf("template %s names %s, got warning %q", c.want, c.live, warn)
		}
	}
	for _, c := range []struct{ want, live string }{
		{"unaccent", "public.ispell"},
		{"ext.unaccent", "public.unaccent"},
		{"simple", "snowball"},
	} {
		td := &schema.TSDictionary{Template: c.want}
		if _, warn := tsDictionaryAlterStmts("app.d", td, &schema.TSDictionary{Template: c.live}); !strings.Contains(warn, "template changed") {
			t.Errorf("template %s differs from %s, got warning %q", c.want, c.live, warn)
		}
	}
}
