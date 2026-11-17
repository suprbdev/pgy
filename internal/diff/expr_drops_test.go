package diff

import (
	"slices"
	"testing"
)

func TestExprDropsReportWhatTheComparisonDropped(t *testing.T) {
	cases := []struct {
		desired, live string
		casts         bool
		names         []string
	}{
		{"email", "(email)::text", true, nil},
		{"email::text", "(email)::text", false, nil},
		{"(email::text)", "(email)::text", false, nil},
		{"lower(email::text)", "lower((email)::text)", false, nil},
		{"cast(email as text) = 'x'", "((email)::text = 'x'::text)", false, nil},
		{"email::text = lower(email::text)", "(email = lower(email))", true, nil},
		{"(attrs -> 'qty')::int > 5", "((attrs -> 'qty'::text) > '5'::text)", true, nil},
		{"x::int > 5", "((x)::integer > 5)", false, nil},
		{"now()::timestamptz", "(now())::timestamp with time zone", false, nil},
		{"status = 'active'", "(status = 'active'::text)", false, nil},
		{"n >= 0", "(n >= (0)::numeric)", false, nil},
		{"status in ('a', 'b')", "(status = ANY (ARRAY['a'::text, 'b'::text]))", false, nil},
		{"interval '1 day' < ttl", "('1 day'::interval < ttl)", false, nil},
		{"email = 'x'::text", "(email = 'x'::citext)", true, nil},
		{"gen_random_uuid()", "public.gen_random_uuid()", false, []string{"gen_random_uuid"}},
		{"public.gen_random_uuid()", "gen_random_uuid()", false, []string{"gen_random_uuid"}},
		{"public.gen_random_uuid()", "public.gen_random_uuid()", false, nil},
		{"pg_catalog.lower(name)", "lower(name)", false, []string{"lower"}},
		{"a operator(public.=) b", "(a OPERATOR(pg_catalog.=) b)", false, []string{"=", "="}},
		{"col operator(public.<>) ''", "(col <> ''::text)", false, []string{"<>"}},
		{"nextval('public.seq'::regclass)", "nextval('seq'::regclass)", false, []string{"seq"}},
	}
	for _, c := range cases {
		if !constraintDefsEqual(c.desired, c.live) {
			t.Errorf("%q and %q must compare equal as text", c.desired, c.live)
			continue
		}
		got := constraintDefsDropped(c.desired, c.live)
		slices.Sort(got.names)
		if got.casts != c.casts || !slices.Equal(got.names, c.names) {
			t.Errorf("%q against %q: want casts %v, names %v; got %v, %v", c.desired, c.live, c.casts, c.names, got.casts, got.names)
		}
	}
	for _, c := range [][2]string{{"now()", "(now())::date"}, {"'a'", "'a'::text"}, {"public.gen_random_uuid()", "gen_random_uuid()"}} {
		if !defaultsEqual(c[0], c[1]) {
			t.Fatalf("%q and %q must compare equal as defaults", c[0], c[1])
		}
	}
	if d := defaultsDropped("now()", "(now())::date"); !d.casts {
		t.Errorf("a default PostgreSQL casts is reported, got %+v", d)
	}
	if d := defaultsDropped("'a'", "'a'::text"); d.casts || len(d.names) > 0 {
		t.Errorf("a cast PostgreSQL adds to a literal is not reported, got %+v", d)
	}
	if d := defaultsDropped("public.gen_random_uuid()", "gen_random_uuid()"); !slices.Equal(d.names, []string{"gen_random_uuid"}) {
		t.Errorf("a default's schema is reported, got %+v", d)
	}
}

func TestExprDropsOnTheSearchPath(t *testing.T) {
	useSearchPath(t, "app", "public", "extensions")
	got := constraintDefsDropped("is_ok(mood) and email <> ''", "(app.is_ok(mood) AND (email OPERATOR(extensions.<>) ''::extensions.citext))")
	slices.Sort(got.names)
	if got.casts || !slices.Equal(got.names, []string{"<>", "is_ok"}) {
		t.Errorf("want the schemas of the search_path reported, got %+v", got)
	}
	if got := constraintDefsDropped("app.is_ok(mood)", "app.is_ok(mood)"); got.casts || len(got.names) > 0 {
		t.Errorf("the same qualifier on both sides is no difference, got %+v", got)
	}
}

func TestExprDropsTriggerFunction(t *testing.T) {
	d := triggerDefsDropped(`create trigger "t" before update on "app"."t" for each row execute procedure touch()`,
		`CREATE TRIGGER t BEFORE UPDATE ON app.t FOR EACH ROW EXECUTE FUNCTION public.touch()`)
	if d.casts || !slices.Equal(d.names, []string{"touch"}) {
		t.Errorf("want the function's schema reported, got %+v", d)
	}
	d = triggerDefsDropped(`create trigger "t" before update on "app"."t" for each row execute procedure public.touch()`,
		`CREATE TRIGGER t BEFORE UPDATE ON app.t FOR EACH ROW EXECUTE FUNCTION touch()`)
	if d.casts || len(d.names) > 0 {
		t.Errorf("public. on the declared function is how pgy pull writes it, got %+v", d)
	}
	d = triggerDefsDropped(`create trigger "t" before update on "app"."t" for each row when (new.n <> '') execute procedure app.touch()`,
		`CREATE TRIGGER t BEFORE UPDATE ON app.t FOR EACH ROW WHEN (((new.n)::text <> ''::text)) EXECUTE FUNCTION app.touch()`)
	if !d.casts || len(d.names) > 0 {
		t.Errorf("want the cast in WHEN reported, got %+v", d)
	}
}

func TestExprDropsLeaveTheTextAlone(t *testing.T) {
	useSearchPath(t, "app", "public", "extensions")
	for _, s := range []string{
		"(email)::text = lower((email)::text)",
		"cast(cast(x as text) as varchar(10)) in ('a', 'b')",
		"(ARRAY['a'::character varying, 'b'::character varying])::text[]",
		"x OPERATOR(extensions.<>) ''::extensions.citext and pg_catalog.lower(y) like 'a%'",
		"interval '1 day' + date '2020-1-1' between a and b",
		"nextval('public.seq'::regclass)",
		`"Mixed"::"char" || position('a' in b)`,
		"((emb)::halfvec(3))",
	} {
		d := &exprDrops{}
		if got, want := canonicalizeExprNoting(s, d), canonicalizeExpr(s); got != want {
			t.Errorf("noting what is dropped changed the text of %q: %q, want %q", s, got, want)
		}
	}
}

func TestExprDropsRetypedCasts(t *testing.T) {
	for _, c := range [][2]string{
		{"(name::varchar(10))", "(name)::character varying(20)"},
		{"emb::halfvec(3)", "(emb)::halfvec(4)"},
		{"(binary_quantize(emb)::bit(3))", "(binary_quantize(emb))::bit(8)"},
		{"n::numeric(6,2) > 1", "((n)::numeric(8,2) > (1)::numeric)"},
		{"x::text = 'a'", "((x)::citext = 'a'::citext)"},
	} {
		if constraintDefsEqual(c[0], c[1]) || defaultsEqual(c[0], c[1]) {
			t.Errorf("%q casts to another type than %q, so they differ", c[0], c[1])
		}
	}
	for _, c := range [][2]string{
		{"name::varchar(10) = 'a'", "(((name)::character varying(10))::text = 'a'::text)"},
		{"x::int", "(x)::integer"},
		{"x::varchar(10)[]", "(x)::character varying(10)[]"},
		{"current_setting('a')::citext", "(current_setting('a'::text))::extensions.citext"},
		{"'a'::varchar(10)", "'a'::character varying(20)"},
		{"lower(x)", "lower((x)::text)"},
	} {
		if !constraintDefsEqual(c[0], c[1]) {
			t.Errorf("%q and %q must still compare equal as text", c[0], c[1])
		}
	}
}
