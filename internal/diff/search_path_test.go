package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// useSearchPath runs the comparisons of test t in the scope of a session
// with the given schemas on its search_path.
func useSearchPath(t *testing.T, schemas ...string) {
	t.Helper()
	currentScope.Store(scopeFor(schemas))
	t.Cleanup(func() { currentScope.Store(publicScope) })
}

func TestSearchPathScopeSchemas(t *testing.T) {
	cases := []struct {
		path []string
		want string
	}{
		{nil, ""},
		{[]string{"public"}, ""},
		{[]string{"pg_catalog", "public"}, ""},
		{[]string{"pgy", "public", "extensions"}, "pgy,extensions"},
		{[]string{"app", "public", "app", "pg_temp_3"}, "app"},
	}
	for _, c := range cases {
		sc := scopeFor(c.path)
		if got := strings.Join(sc.path, ","); got != c.want {
			t.Errorf("scopeFor(%v).path = %q, want %q", c.path, got, c.want)
		}
		if !sc.schemas["public"] {
			t.Errorf("scopeFor(%v): public must be optional", c.path)
		}
	}
	if scopeFor([]string{"public"}) != publicScope {
		t.Error("a search_path of public only must use publicScope")
	}
}

func TestSearchPathColumnTypes(t *testing.T) {
	if normalizeColumnType("extensions.citext") == normalizeColumnType("citext") {
		t.Fatal("a schema that is not on the search_path must stay part of the type")
	}
	useSearchPath(t, "pgy", "public", "extensions", "My Schema")
	for a, b := range map[string]string{
		"extensions.citext":        "citext",
		`"extensions".citext[]`:    "CITEXT[]",
		"Extensions.hstore":        "hstore",
		"public.mood":              "mood",
		"pgy.mood":                 "mood",
		`"My Schema".pair`:         "pair",
		"extensions.ltree":         "public.ltree",
		"character varying(10)":    "varchar(10)",
		"timestamp with time zone": "timestamptz",
	} {
		if normalizeColumnType(a) != normalizeColumnType(b) {
			t.Errorf("%q and %q must be the same type: %q vs %q", a, b, normalizeColumnType(a), normalizeColumnType(b))
		}
	}
	for a, b := range map[string]string{
		"other.citext":       "citext",
		"myextensions.thing": "thing",
		"extensions.citext":  "extensions.text",
	} {
		if normalizeColumnType(a) == normalizeColumnType(b) {
			t.Errorf("%q and %q must differ", a, b)
		}
	}
}

func TestSearchPathExpressions(t *testing.T) {
	qualified := [][2]string{
		{"extensions.uuid_generate_v4()", "extensions.uuid_generate_v4()"},
		{"email OPERATOR(extensions.<>) ''", "(email OPERATOR(extensions.<>) ''::extensions.citext)"},
		{`email operator ( "extensions" . = ) current_setting('app.email', true)::citext`, "(email OPERATOR(extensions.=) (current_setting('app.email'::text, true))::extensions.citext)"},
		{"lower(f_unaccent(name))", "lower(public.f_unaccent(name))"},
		{"nextval('app.ticket_seq')", "nextval('app.ticket_seq'::regclass)"},
		{"note like 'extensions.x'", "(note ~~ 'extensions.x'::text)"},
	}
	unqualified := [][2]string{
		{"uuid_generate_v4()", "extensions.uuid_generate_v4()"},
		{"email <> ''", "(email OPERATOR(extensions.<>) ''::extensions.citext)"},
		{"path is null or nlevel(path) <= 8", "((path IS NULL) OR (extensions.nlevel(path) <= 8))"},
		{"not (attrs ? 'forbidden')", "(NOT (attrs OPERATOR(extensions.?) 'forbidden'::text))"},
		{"nextval('ticket_seq')", "nextval('app.ticket_seq'::regclass)"},
		{"unaccent('unaccent', t)", "extensions.unaccent('extensions.unaccent'::regdictionary, t)"},
		{"is_ok(mood)", "app.is_ok(mood)"},
		{"name ~~ 'a%'", "(name OPERATOR(extensions.~~) 'a%'::extensions.citext)"},
	}
	differ := [][2]string{
		{"email <> ''", "(email OPERATOR(other.<>) ''::other.citext)"},
		{"uuid_generate_v4()", "other.uuid_generate_v4()"},
		{"note = 'x'", "(note = 'extensions.x'::text)"},
		{"a && b", "(a OPERATOR(pg_catalog.&&) b)"},
		{"a ? 'k'", "(a OPERATOR(public.?) 'k'::text)"},
		{"nextval('ticket_seq')", "nextval('other.ticket_seq'::regclass)"},
	}
	for _, c := range qualified {
		if !constraintDefsEqual(c[0], c[1]) {
			t.Errorf("without a search_path %q must equal %q", c[0], c[1])
		}
	}
	for _, c := range unqualified {
		if constraintDefsEqual(c[0], c[1]) {
			t.Errorf("without the schema on the search_path %q must differ from %q", c[0], c[1])
		}
	}
	useSearchPath(t, "app", "public", "extensions")
	for _, c := range append(qualified, unqualified...) {
		if !constraintDefsEqual(c[0], c[1]) || !defaultsEqual(c[0], c[1]) {
			t.Errorf("%q must equal %q: %q vs %q", c[0], c[1], normalizeConstraintDef(c[0]), normalizeConstraintDef(c[1]))
		}
	}
	for _, c := range differ {
		if constraintDefsEqual(c[0], c[1]) || defaultsEqual(c[0], c[1]) {
			t.Errorf("%q must differ from %q", c[0], c[1])
		}
	}
}

func TestVisibleSchemaOperators(t *testing.T) {
	for _, c := range [][2]string{
		{"c operator(public.<>) ''", "(c <> ''::citext)"},
		{"h OPERATOR ( public.? ) 'sku'", "(h ? 'sku'::text)"},
		{"n operator(pg_catalog.>) 0", "(n > 0)"},
		{`n operator("pg_catalog".>) 0`, "(n > 0)"},
		{"h operator(public.?) 'operator(public.?)'", "(h ? 'operator(public.?)'::text)"},
		{"a operator(pg_catalog.&&) b", "(a OPERATOR(pg_catalog.&&) b)"},
		{"a operator(public.&&) b", "(a OPERATOR(public.&&) b)"},
	} {
		if !constraintDefsEqual(c[0], c[1]) {
			t.Errorf("declared %q must equal live %q", c[0], c[1])
		}
	}
	for _, c := range [][2]string{
		{"a && b", "(a OPERATOR(pg_catalog.&&) b)"},
		{"a && b", "(a OPERATOR(public.&&) b)"},
		{"h ? 'sku'", "(h ? 'operator(public.?)'::text)"},
	} {
		if constraintDefsEqual(c[0], c[1]) {
			t.Errorf("declared %q must differ from live %q", c[0], c[1])
		}
	}
}

func TestSearchPathFunctionSignatures(t *testing.T) {
	raw := publicScope.functionSignature("app.by_email(p extensions.citext, q public.mood)")
	if raw != "app.by_email(p extensions.citext, q mood)" {
		t.Fatalf("raw signature = %q", raw)
	}
	useSearchPath(t, "extensions")
	want := "app.by_email(p citext, q mood)"
	for _, sig := range []string{"app.by_email(p extensions.citext, q public.mood)", "app.by_email(p citext, q mood)", `app.by_email(p "extensions".citext, q mood)`, raw} {
		if got := normalizeFunctionSignature(sig); got != want {
			t.Errorf("normalizeFunctionSignature(%q) = %q, want %q", sig, got, want)
		}
	}
	if got := routineIdentity("app.by_email(p extensions.citext, OUT r int)", false); got != "app.by_email(citext)" {
		t.Errorf("routineIdentity = %q", got)
	}
	if got := publicScope.functionSignature("app.by_email(p extensions.citext)"); got != "app.by_email(p extensions.citext)" {
		t.Errorf("publicScope must keep the qualifier: %q", got)
	}
	for _, sig := range []string{
		"app.f(p extensions.citext, q timestamp with time zone[])",
		"app.f(character varying, double precision, extensions.hstore)",
		"app.f(VARIADIC arr extensions.citext[])",
		"app.f(IN x integer, OUT y text, INOUT z extensions.ltree)",
		`app.f(a "Extensions".citext, b character, c bit varying(3))`,
		"public.total(integer ORDER BY extensions.citext)",
		"app.g()",
	} {
		live := publicScope.functionSignature(sig)
		if got, want := activeScope().functionSignature(live), normalizeFunctionSignature(sig); got != want {
			t.Errorf("rekeying %q gives %q, a declared signature gives %q", live, got, want)
		}
	}
}

func TestSearchPathNames(t *testing.T) {
	useSearchPath(t, "ext")
	if !sameType("ext.pair", "pair") || !sameType(`"ext"."pair"`, "public.pair") || sameType("other.pair", "pair") {
		t.Error("sameType must ignore the qualifiers of public and ext only")
	}
	if !triggerDefsEqual(
		`create trigger "t_touch" BEFORE UPDATE on "app"."t" for each row execute procedure touch()`,
		"CREATE TRIGGER t_touch BEFORE UPDATE ON app.t FOR EACH ROW EXECUTE FUNCTION ext.touch()") {
		t.Error("a trigger function on the search_path must match its qualified name")
	}
	if triggerDefsEqual(
		`create trigger "t_touch" BEFORE UPDATE on "app"."t" for each row execute procedure touch()`,
		"CREATE TRIGGER t_touch BEFORE UPDATE ON app.t FOR EACH ROW EXECUTE FUNCTION other.touch()") {
		t.Error("a trigger function in another schema must differ")
	}
	ag := &schema.Aggregate{SFunc: "add2", SType: "int", Options: map[string]string{"sortop": "operator(ext.<)"}}
	la := &LiveAggregate{SFunc: "ext.add2", SType: "integer", Options: map[string]string{"sortop": "ext.<"}}
	if !aggregateMatchesLive(ag, la) {
		t.Error("an aggregate's functions and sort operator on the search_path must match their qualified names")
	}
	la.SFunc = "other.add2"
	if aggregateMatchesLive(ag, la) {
		t.Error("a state function in another schema must differ")
	}
	if !tsTemplatesEqual("ext.unaccent", "unaccent") {
		t.Error("a text search template on the search_path must match its bare name")
	}
}

func TestSearchPathExclusionConstraints(t *testing.T) {
	trgm := LiveIndexOpclass{Name: "ext.gist_trgm_ops"}
	live := excludeLive("EXCLUDE USING gist (room_id ext.gist_int4_ops WITH OPERATOR(ext.=), name ext.gist_trgm_ops WITH OPERATOR(ext.%))",
		LiveIndexOpclass{Name: "ext.gist_int4_ops", Default: true}, trgm)
	unqualified := &schema.Constraint{Name: "x", Type: "exclude", Expression: "using gist (room_id with =, name gist_trgm_ops with %)"}
	qualified := &schema.Constraint{Name: "x", Type: "exclude", Expression: "using gist (room_id ext.gist_int4_ops with operator(ext.=), name ext.gist_trgm_ops with operator(ext.%))"}
	if !constraintMatchesLive(live, "x", qualified) {
		t.Error("the qualified spelling must match without a search_path")
	}
	if constraintMatchesLive(live, "x", unqualified) {
		t.Error("the unqualified spelling must differ while ext is not on the search_path")
	}
	useSearchPath(t, "ext")
	for _, ct := range []*schema.Constraint{qualified, unqualified} {
		if !constraintMatchesLive(live, "x", ct) {
			t.Errorf("%q must match %q with ext on the search_path", ct.Expression, live.ConstraintDefs["x"])
		}
	}
	shadowed := excludeLive("EXCLUDE USING gist (a WITH OPERATOR(pg_catalog.&&))", LiveIndexOpclass{Name: "gist_int4_ops", Default: true})
	if constraintMatchesLive(shadowed, "x", &schema.Constraint{Name: "x", Type: "exclude", Expression: "using gist (a with &&)"}) {
		t.Error("OPERATOR(pg_catalog.op) printed by PostgreSQL must differ from the bare operator")
	}
	if !constraintMatchesLive(excludeLive("EXCLUDE USING gist (a WITH &&)", LiveIndexOpclass{Name: "gist_int4_ops", Default: true}), "x",
		&schema.Constraint{Name: "x", Type: "exclude", Expression: "using gist (a with operator(public.&&))"}) {
		t.Error("a declared OPERATOR(public.op) must match the bare operator")
	}
}

func TestPlanSearchPathQualifiersOptional(t *testing.T) {
	t.Cleanup(func() { currentScope.Store(publicScope) })
	live := func(path ...string) *Live {
		l := liveWithTablePK("app.users", map[string]*LiveColumn{
			"id":    {Type: "uuid", Default: "extensions.uuid_generate_v4()"},
			"email": {Type: "extensions.citext"},
		})
		lt := l.Tables["app.users"]
		lt.ConstraintDefs = map[string]string{"users_email_chk": "CHECK ((email OPERATOR(extensions.<>) ''::extensions.citext))"}
		lt.Constraints["users_email_chk"] = true
		sig := "app.by_email(p extensions.citext)"
		l.Functions[sig] = true
		l.FunctionDefs = map[string]*LiveFunction{publicScope.functionSignature(sig): {Signature: sig, Kind: "f", Body: "select 1", Returns: "integer", Args: "p extensions.citext", Security: "invoker", Volatility: "volatile"}}
		l.FunctionPublicExec = map[string]bool{publicScope.functionSignature(sig): true}
		l.Schemas["app"], l.Schemas["extensions"] = true, true
		l.SearchPath = path
		return l
	}
	desired := func(prefix string) *schema.Database {
		return &schema.Database{
			Tables: map[string]*schema.Table{"app.users": {
				Name: "users",
				Columns: map[string]*schema.Column{
					"id":    {Type: "uuid", Default: prefix + "uuid_generate_v4()"},
					"email": {Type: prefix + "citext"},
				},
				PrimaryKey:  []string{"id"},
				Constraints: []*schema.Constraint{{Name: "users_email_chk", Type: "check", Expression: "email <> ''"}},
			}},
			Functions: map[string]*schema.Function{"app.by_email": {Name: "by_email", ArgsSig: "(p " + prefix + "citext)", Returns: "integer", Language: "sql", Body: "select 1"}},
		}
	}
	for _, c := range []struct {
		name   string
		path   []string
		prefix string
		clean  bool
	}{
		{"qualified, default search_path", []string{"public"}, "extensions.", false},
		{"qualified, extensions on the search_path", []string{"public", "extensions"}, "extensions.", true},
		{"unqualified, extensions on the search_path", []string{"pgy", "public", "extensions"}, "", true},
		{"unqualified, default search_path", []string{"public"}, "", false},
	} {
		sql := Render(Plan(live(c.path...), desired(c.prefix), true))
		if c.clean && sql != "" {
			t.Errorf("%s: expected no changes, got:\n%s", c.name, sql)
		}
		if !c.clean && sql == "" {
			t.Errorf("%s: expected changes", c.name)
		}
	}
	d := desired("extensions.")
	d.Tables["app.users"].Constraints[0].Expression = "email OPERATOR(extensions.<>) ''"
	for _, path := range [][]string{nil, {"public"}, {"public", "extensions"}, {"app", "public"}} {
		if sql := Render(Plan(live(path...), d, true)); sql != "" {
			t.Errorf("fully qualified YAML, search_path %v: expected no changes, got:\n%s", path, sql)
		}
	}
}

func TestScopedLiveKeepsCallerSnapshot(t *testing.T) {
	t.Cleanup(func() { currentScope.Store(publicScope) })
	sig := publicScope.functionSignature("app.f(p ext.t)")
	l := &Live{
		FunctionDefs: map[string]*LiveFunction{sig: {}},
		Owners:       map[string]string{"proc:" + sig: "owner", "rel:app.t": "owner"},
		SearchPath:   []string{"ext", "public"},
	}
	s := l.scoped()
	if s == l || l.FunctionDefs[sig] == nil || l.Owners["proc:"+sig] == "" {
		t.Fatal("scoped must leave the caller's snapshot as introspection keyed it")
	}
	if s.FunctionDefs["app.f(p t)"] == nil || s.Owners["proc:app.f(p t)"] != "owner" || s.Owners["rel:app.t"] != "owner" {
		t.Errorf("scoped keys: %v %v", s.FunctionDefs, s.Owners)
	}
	if again := s.scoped(); again != s {
		t.Error("a scoped snapshot must not be rekeyed again")
	}
	if c := cloneLive(s); c.scope != s.scope || len(c.SearchPath) != 2 {
		t.Error("cloneLive must keep the search_path and the scope")
	}
}

func TestDatabaseSearchPathSetsSession(t *testing.T) {
	d := &schema.Database{
		Extensions:       []*schema.Extension{{Name: "citext", Schema: "extensions"}},
		DatabaseSettings: map[string]string{"search_path": `"$user", public, extensions`, "timezone": "UTC"},
		Tables: map[string]*schema.Table{"app.users": {
			Name:    "users",
			Columns: map[string]*schema.Column{"email": {Type: "citext"}},
		}},
	}
	plan := Plan(emptyLive(), d, false)
	want := []string{
		`create extension "citext" schema "extensions";`,
		`do $$ begin execute format('alter database %I set search_path = "$user", public, extensions', current_database()); end $$;`,
		`set search_path = "$user", public, extensions;`,
		`do $$ begin execute format('alter database %I set timezone = ''UTC''', current_database()); end $$;`,
		`create table if not exists "app"."users" ("email" citext not null);`,
	}
	at := 0
	for _, s := range plan.Creates {
		if at < len(want) && s == want[at] {
			at++
		}
	}
	if at != len(want) {
		t.Fatalf("expected, in this order, %q\ngot %q", want, plan.Creates)
	}
	for _, s := range plan.Creates {
		if strings.HasPrefix(s, "set timezone") {
			t.Errorf("only search_path is set for the session, got %q", s)
		}
	}

	live := emptyLive()
	live.DatabaseConfig = map[string]string{"search_path": `"$user", public, extensions`, "timezone": "UTC"}
	if sql := Render(Plan(live, &schema.Database{DatabaseSettings: d.DatabaseSettings}, false)); sql != "" {
		t.Errorf("a search_path the database already has must not be set again:\n%s", sql)
	}
	sql := Render(Plan(live, &schema.Database{DatabaseSettings: map[string]string{"timezone": "UTC"}}, false))
	if !strings.Contains(sql, "reset search_path") || strings.Contains(sql, "\nreset search_path") || strings.Contains(sql, "\nset search_path") {
		t.Errorf("resetting the database's search_path must leave the session's alone:\n%s", sql)
	}
}
