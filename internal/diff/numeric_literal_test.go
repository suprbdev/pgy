package diff

import "testing"

func TestNumericLiteralCompareEqual(t *testing.T) {
	for _, c := range []struct{ desired, live string }{
		{"check (n > 1.5)", "CHECK ((n > 1.50))"},
		{"check (n > 1.50)", "CHECK ((n > 1.5))"},
		{"check (1.5 < n)", "CHECK ((1.50 < n))"},
		{"check (n >= 1.5 and n <> 2.0)", "CHECK (((n >= 1.500) AND (n <> 2.00)))"},
		{"check (n > .5)", "CHECK ((n > 0.50))"},
		{"check (n > 01.5)", "CHECK ((n > 1.50))"},
		{"check (n > -1.5)", "CHECK ((n > '-1.50'::numeric))"},
		{"check (n > +1.5)", "CHECK ((n > (+ 1.50)))"},
		{"check ((n) > (1.5))", "CHECK ((n > 1.50))"},
		{"check (n > 1.5::numeric(4,2))", "CHECK ((n > 1.50::numeric(4,2)))"},
		{"check (f > 1.5)", "CHECK ((f > (1.50)::double precision))"},
		{"check (n in (1.5, 2.5))", "CHECK ((n = ANY (ARRAY[1.50, 2.5])))"},
		{"check (n not in (1.5))", "CHECK ((n <> 1.50))"},
		{"check (n not in (1.5, 2.50))", "CHECK ((n <> ALL (ARRAY[1.50, 2.5])))"},
		{"check (n between 1.5 and 2.5)", "CHECK (((n >= 1.50) AND (n <= 2.50)))"},
		{"check (n != 1.5 or n is null)", "CHECK (((n <> 1.50) OR (n IS NULL)))"},
		{"check (case when x then n > 1.5 else true end)", "CHECK (CASE WHEN x THEN (n > 1.50) ELSE true END)"},
	} {
		if !constraintDefsEqual(c.desired, c.live) {
			t.Errorf("constraintDefsEqual(%q, %q) = false, want true", c.desired, c.live)
		}
	}
}

func TestNumericLiteralCompareNotEqual(t *testing.T) {
	for _, c := range []struct{ desired, live string }{
		{"check (n > 1.5)", "CHECK ((n > 1.6))"},
		{"check (n > 1.5)", "CHECK ((n > 1.05))"},
		{"check (n > 15.0)", "CHECK ((n > 1.50))"},
		{"check (n > 1)", "CHECK ((n > 1.0))"},
		{"check (n > -1.5)", "CHECK ((n > 1.50))"},
		{"check (n > 1.5 + x)", "CHECK ((n > (1.50 + x)))"},
		{"check (n > x * 1.5)", "CHECK ((n > (x * 1.50)))"},
		{"check (n > round(1.5))", "CHECK ((n > round(1.50)))"},
		{"check (t = 1.5::text)", "CHECK ((t = (1.50)::text))"},
		{"check (t = '1.5')", "CHECK ((t = '1.50'::text))"},
		{"check (n > 1.5e0)", "CHECK ((n > 1.50e0))"},
		{"check (f(1.5, n))", "CHECK (f(1.50, n))"},
		{"check (n in (1.5, 2.5))", "CHECK ((n = ANY (ARRAY[1.50, 2.6])))"},
	} {
		if constraintDefsEqual(c.desired, c.live) {
			t.Errorf("constraintDefsEqual(%q, %q) = true, want false", c.desired, c.live)
		}
	}
}

func TestNumericLiteralDefaultKeepsScale(t *testing.T) {
	if defaultsEqual("1.5", "1.50") {
		t.Error(`defaultsEqual("1.5", "1.50") = true, want false`)
	}
	if !defaultsEqual("(n > 1.5)", "(n > 1.50)") {
		t.Error(`defaultsEqual("(n > 1.5)", "(n > 1.50)") = false, want true`)
	}
}

func TestNumericLiteralProbeReadBack(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"CHECK (((i)::numeric > 1.5))", "CHECK (((i)::numeric > 1.50))", true},
		{"CHECK ((n > '-1.5'::numeric))", "CHECK ((n > '-1.50'::numeric))", true},
		{"CHECK (((i)::numeric > 1.5))", "CHECK (((i)::numeric > 1.6))", false},
		{"CHECK ((t = (1.5)::text))", "CHECK ((t = (1.50)::text))", false},
	} {
		if got := sameComparedNumbers(c.a, c.b); got != c.want {
			t.Errorf("sameComparedNumbers(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestNumericLiteralShortestDecimal(t *testing.T) {
	for in, want := range map[string]string{
		"1.50": "1.5", "1.5": "1.5", "01.500": "1.5", ".50": "0.5", "2.": "2.0", "2.00": "2.0", "-0.10": "-0.1", "100.0": "100.0",
	} {
		if got := shortestDecimal(in); got != want {
			t.Errorf("shortestDecimal(%q) = %q, want %q", in, got, want)
		}
	}
}
