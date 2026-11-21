package diff

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	comparedDecimalRe     = regexp.MustCompile(`^[-+]?(?:\d+\.\d*|\.\d+)$`)
	comparedCastRe        = regexp.MustCompile(`(?i)^::\s*(numeric|decimal|real|float4|float8|double\s+precision|smallint|integer|int|int2|int4|int8|bigint)\b(?:\s*\(\s*\d+(?:\s*,\s*-?\d+)?\s*\))?`)
	comparedUpperBoundRe  = regexp.MustCompile(`(?i)\bbetween\s+(?:symmetric\s+)?[-+]?\s*(?:[\w.]+|"[^"]*"|\x00\d+\x00)\s+and\s*$`)
	comparedLowerBoundRe  = regexp.MustCompile(`(?i)\bbetween(?:\s+symmetric)?\s*$`)
	comparedQuantifiedRe  = regexp.MustCompile(`(?i)(?:<>|<=|>=|!=|[<>=])\s*(?:any|some|all)\s*\(\s*array\s*$`)
	comparedInRe          = regexp.MustCompile(`(?i)\bin\s*$`)
	comparedComparisonOps = map[string]bool{"<": true, ">": true, "=": true, "<=": true, ">=": true, "<>": true, "!=": true}
	comparedEndWords      = map[string]bool{"and": true, "or": true, "not": true, "when": true, "then": true, "else": true, "end": true, "case": true}
)

// canonicalizeComparedNumbers rewrites each decimal constant in s, whose
// string literals maskStringLiterals replaced with lits, to its shortest
// spelling with a fractional part (1.50 and 1.500 become 1.5, .50 becomes
// 0.5, 2.00 becomes 2.0) where it is a whole operand of a comparison: of
// <, >, =, <=, >=, <> or !=, a bound of BETWEEN, or an element of an IN
// list or of the ARRAY of = ANY / <> ALL. A comparison's result does not
// depend on the scale of a numeric constant, which PostgreSQL keeps as
// written and prints back. A quoted constant ('-1.50'::numeric, as
// PostgreSQL prints a negative one) is rewritten in lits, and only when it
// is cast to a numeric type. A constant cast to any other type, or used
// anywhere else (a default, an argument, arithmetic), keeps its spelling,
// since there its scale can show in the result. Integer constants are left
// as they are: 1 and 1.0 have different types.
func canonicalizeComparedNumbers(s string, lits []string) string {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '"':
			j := strings.IndexByte(s[i+1:], '"')
			if j < 0 {
				return s
			}
			i += j + 2
		case c == 0:
			j := strings.IndexByte(s[i+1:], 0)
			if j < 0 {
				return s
			}
			end := i + j + 2
			if n, err := strconv.Atoi(s[i+1 : end-1]); err == nil && n < len(lits) {
				lit := lits[n]
				if inner := lit[1 : len(lit)-1]; comparedDecimalRe.MatchString(inner) && comparedOperand(s, i, end, true) {
					lits[n] = "'" + shortestDecimal(inner) + "'"
				}
			}
			i = end
		case (c >= '0' && c <= '9') || c == '.':
			j := i
			for j < len(s) && (isDigit(s[j]) || s[j] == '.') {
				j++
			}
			lead := i > 0 && (isWordByte(s[i-1]) || s[i-1] == '.' || s[i-1] == '$')
			if !lead && (j == len(s) || !isWordByte(s[j])) && comparedDecimalRe.MatchString(s[i:j]) && comparedOperand(s, i, j, false) {
				b.WriteString(s[last:i])
				b.WriteString(shortestDecimal(s[i:j]))
				last = j
			}
			i = j
		case isWordByte(c):
			for i < len(s) && (isWordByte(s[i]) || s[i] == '$') {
				i++
			}
		default:
			i++
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// sameComparedNumbers reports whether two deparsed definitions are the
// same once canonicalizeComparedNumbers has rewritten both.
func sameComparedNumbers(a, b string) bool {
	if a == b {
		return true
	}
	ma, la := maskStringLiterals(a)
	mb, lb := maskStringLiterals(b)
	return unmaskStringLiterals(canonicalizeComparedNumbers(ma, la), la) == unmaskStringLiterals(canonicalizeComparedNumbers(mb, lb), lb)
}

// shortestDecimal returns the decimal constant d, which may have a sign,
// without leading zeros in its integer part and trailing zeros in its
// fractional part, keeping one digit in each.
func shortestDecimal(d string) string {
	sign := ""
	if d[0] == '-' || d[0] == '+' {
		sign, d = d[:1], d[1:]
	}
	whole, frac, _ := strings.Cut(d, ".")
	if whole = strings.TrimLeft(whole, "0"); whole == "" {
		whole = "0"
	}
	if frac = strings.TrimRight(frac, "0"); frac == "" {
		frac = "0"
	}
	return sign + whole + "." + frac
}

// comparedOperand reports whether the constant at s[start:end] is a whole
// operand of a comparison (see canonicalizeComparedNumbers), looking past
// grouping parentheses, a unary sign and casts to numeric types around it.
// needCast requires such a cast.
func comparedOperand(s string, start, end int, needCast bool) bool {
	l, r, cast := start, end, false
	for {
		rr := skipSpaceRight(s, r)
		if m := comparedCastRe.FindStringIndex(s[rr:]); m != nil {
			r, cast = rr+m[1], true
			continue
		}
		if rr+1 < len(s) && s[rr] == ':' && s[rr+1] == ':' {
			return false
		}
		ll := skipSpaceLeft(s, l)
		if ll > 0 && (s[ll-1] == '-' || s[ll-1] == '+') && unarySign(s, ll-1) {
			l = ll - 1
			continue
		}
		if ll > 0 && s[ll-1] == '(' && rr < len(s) && s[rr] == ')' && !callParen(s, ll-1) {
			l, r = ll-1, rr+1
			continue
		}
		l, r = ll, rr
		break
	}
	if needCast && !cast {
		return false
	}
	left, right := tokenBefore(s, l), tokenAfter(s, r)
	switch {
	case comparedComparisonOps[left] && operandEnd(right, false):
		return true
	case operandEnd(left, true) && comparedComparisonOps[right]:
		return true
	case comparedLowerBoundRe.MatchString(s[:l]) && strings.EqualFold(right, "and"):
		return true
	case strings.EqualFold(left, "and") && operandEnd(right, false) && comparedUpperBoundRe.MatchString(s[:l]):
		return true
	case (left == "(" || left == ",") && (right == "," || right == ")"):
		open := listOpener(s, l)
		return open >= 0 && s[open] == '(' && comparedInRe.MatchString(s[:open])
	case (left == "[" || left == ",") && (right == "," || right == "]"):
		open := listOpener(s, l)
		return open >= 0 && s[open] == '[' && comparedQuantifiedRe.MatchString(s[:open])
	}
	return false
}

// operandEnd reports whether tok, the token next to an operand, ends it:
// the start or end of the definition, a comma, a parenthesis that opens
// (left) or closes (right) a group, or a keyword that binds less tightly
// than a comparison.
func operandEnd(tok string, left bool) bool {
	switch tok {
	case "", ",":
		return true
	case "(":
		return left
	case ")":
		return !left
	}
	return comparedEndWords[strings.ToLower(tok)]
}

// unarySign reports whether the + or - at s[i] is a unary sign: one that
// follows no operand.
func unarySign(s string, i int) bool {
	j := skipSpaceLeft(s, i)
	if j == 0 {
		return true
	}
	switch c := s[j-1]; {
	case c == ')' || c == ']' || c == '"' || c == 0 || c == '.':
		return false
	case isWordByte(c):
		return comparedEndWords[strings.ToLower(tokenBefore(s, j))] || strings.EqualFold(tokenBefore(s, j), "between")
	}
	return true
}

// callParen reports whether the ( at s[i] opens the arguments of a call or
// a list (f(x), IN (x), ANY (x)) rather than a group.
func callParen(s string, i int) bool {
	j := skipSpaceLeft(s, i)
	if j == 0 {
		return false
	}
	if c := s[j-1]; c == '"' || c == ']' {
		return true
	}
	if !isWordByte(s[j-1]) {
		return false
	}
	w := strings.ToLower(tokenBefore(s, j))
	return !comparedEndWords[w] && w != "between" && w != "symmetric"
}

// listOpener returns the index of the ( or [ that opens the list holding
// the element that ends at s[i], or -1.
func listOpener(s string, i int) int {
	depth := 0
	for k := i - 1; k >= 0; k-- {
		switch s[k] {
		case ')', ']':
			depth++
		case '(', '[':
			if depth == 0 {
				return k
			}
			depth--
		}
	}
	return -1
}

// tokenBefore returns the token that ends at s[i], skipping spaces: a word,
// a run of operator characters or a single other character, "" at the
// start.
func tokenBefore(s string, i int) string {
	i = skipSpaceLeft(s, i)
	if i == 0 {
		return ""
	}
	j := i - 1
	switch {
	case isWordByte(s[j]):
		for j > 0 && isWordByte(s[j-1]) {
			j--
		}
	case isOperatorByte(s[j]):
		for j > 0 && isOperatorByte(s[j-1]) {
			j--
		}
	}
	return s[j:i]
}

// tokenAfter is tokenBefore for the token that starts at s[i], "" at the
// end.
func tokenAfter(s string, i int) string {
	i = skipSpaceRight(s, i)
	if i == len(s) {
		return ""
	}
	j := i + 1
	switch {
	case isWordByte(s[i]):
		for j < len(s) && isWordByte(s[j]) {
			j++
		}
	case isOperatorByte(s[i]):
		for j < len(s) && isOperatorByte(s[j]) {
			j++
		}
	}
	return s[i:j]
}

func skipSpaceLeft(s string, i int) int {
	for i > 0 && isSpaceByte(s[i-1]) {
		i--
	}
	return i
}

func skipSpaceRight(s string, i int) int {
	for i < len(s) && isSpaceByte(s[i]) {
		i++
	}
	return i
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isOperatorByte(c byte) bool { return strings.IndexByte("+-*/<>=~!@#%^&|`?", c) >= 0 }
