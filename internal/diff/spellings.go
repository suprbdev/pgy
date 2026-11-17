package diff

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/suprbdev/pgy/internal/schema"
)

var (
	regCastLiteralRe = regexp.MustCompile(`(?i)\x00(\d+)\x00\s*::\s*(?:"?pg_catalog"?\.)?reg[a-z]+\b`)
	seqCallLiteralRe = regexp.MustCompile(`(?i)\b(?:nextval|currval|setval)\s*\(\s*\x00(\d+)\x00`)
)

// objectLiterals returns the indexes of the string literals in masked (see
// maskStringLiterals) that name a database object: those cast to a reg*
// type and the sequence argument of nextval, currval and setval.
func objectLiterals(masked string) map[int]bool {
	out := map[int]bool{}
	for _, re := range []*regexp.Regexp{regCastLiteralRe, seqCallLiteralRe} {
		for _, m := range re.FindAllStringSubmatch(masked, -1) {
			if i, err := strconv.Atoi(m[1]); err == nil {
				out[i] = true
			}
		}
	}
	return out
}

// objectLiteralNames collects, without their optional qualifier (see
// nameScope), the object names that defs spell as string literals (see
// objectLiterals).
func objectLiteralNames(defs ...string) map[string]bool {
	sc := activeScope()
	names := map[string]bool{}
	for _, def := range defs {
		masked, lits := maskStringLiterals(def)
		for i := range objectLiterals(masked) {
			if i < len(lits) {
				name, _ := sc.unqualifyLiteral(lits[i][1 : len(lits[i])-1])
				names[name] = true
			}
		}
	}
	return names
}

// unqualifyObjectLiterals drops an optional qualifier (see nameScope)
// inside the string literals of s that name a database object, and inside
// those that spell a name in names. PostgreSQL prints 'public.seq'::regclass
// as 'seq'::regclass while public is on the search_path, and YAML often
// leaves the cast out (to_tsvector('public.cfg', body)), so such a literal
// is only known to be an object name from the other side of the comparison.
// Other literals keep the qualifier: there 'public.x' and 'x' are different
// values. d, when not nil, notes each qualifier dropped.
func unqualifyObjectLiterals(s string, names map[string]bool, d *exprDrops) string {
	sc := activeScope()
	if !sc.mentions(s) {
		return s
	}
	masked, lits := maskStringLiterals(s)
	marked := objectLiterals(masked)
	changed := false
	for i, lit := range lits {
		name, ok := sc.unqualifyLiteral(lit[1 : len(lit)-1])
		if ok && (marked[i] || names[name]) {
			if d != nil {
				d.qualifiers = append(d.qualifiers, qualifiedName{identName(lit[1 : len(lit)-2-len(name)]), name})
			}
			lits[i] = "'" + name + "'"
			changed = true
		}
	}
	if !changed {
		return s
	}
	return exprPlaceholderRe.ReplaceAllStringFunc(masked, func(m string) string {
		i, _ := strconv.Atoi(exprPlaceholderRe.FindStringSubmatch(m)[1])
		return lits[i]
	})
}

// splitTriggerCall splits a CREATE TRIGGER statement at its EXECUTE
// FUNCTION / PROCEDURE keywords into the part before them and the function
// call after them.
func splitTriggerCall(stmt string) (head, call string, ok bool) {
	for i := 0; i < len(stmt); i++ {
		if stmt[i] == '\'' {
			for i++; i < len(stmt); i++ {
				if stmt[i] == '\'' {
					if i+1 < len(stmt) && stmt[i+1] == '\'' {
						i++
						continue
					}
					break
				}
			}
			continue
		}
		if (i > 0 && isWordByte(stmt[i-1])) || len(stmt)-i < len("execute") || !strings.EqualFold(stmt[i:i+len("execute")], "execute") {
			continue
		}
		rest := strings.TrimLeft(stmt[i+len("execute"):], " \t\r\n")
		for _, kw := range []string{"function", "procedure"} {
			if len(rest) > len(kw) && strings.EqualFold(rest[:len(kw)], kw) && strings.ContainsRune(" \t\r\n", rune(rest[len(kw)])) {
				return stmt[:i], strings.TrimSpace(rest[len(kw):]), true
			}
		}
	}
	return "", "", false
}

// parseTriggerCall splits a trigger's function call into the function name,
// lower-cased unless quoted and without an optional (see nameScope) or
// pg_catalog qualifier, and its arguments as the strings PostgreSQL stores
// in pg_trigger.tgargs.
func parseTriggerCall(call string) (name string, args []string, ok bool) {
	call = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(call), ";"))
	open, inIdent := -1, false
	for i := 0; i < len(call) && open < 0; i++ {
		switch {
		case call[i] == '"':
			inIdent = !inIdent
		case call[i] == '(' && !inIdent:
			open = i
		}
	}
	if open < 0 {
		return triggerFunctionName(call), nil, call != ""
	}
	if !strings.HasSuffix(call, ")") {
		return "", nil, false
	}
	args, ok = parseTriggerArgs(call[open+1 : len(call)-1])
	return triggerFunctionName(call[:open]), args, ok
}

func triggerFunctionName(s string) string {
	parts := []string{}
	var cur strings.Builder
	inIdent := false
	rs := []rune(strings.TrimSpace(s))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '"' && inIdent && i+1 < len(rs) && rs[i+1] == '"':
			cur.WriteRune('"')
			i++
		case r == '"':
			inIdent = !inIdent
		case r == '.' && !inIdent:
			parts = append(parts, cur.String())
			cur.Reset()
		case inIdent:
			cur.WriteRune(r)
		default:
			cur.WriteString(strings.ToLower(string(r)))
		}
	}
	parts = append(parts, cur.String())
	if len(parts) == 2 && (parts[0] == "pg_catalog" || activeScope().schemas[parts[0]]) {
		parts = parts[1:]
	}
	return strings.Join(parts, ".")
}

// parseTriggerArgs reads a trigger's argument list. PostgreSQL turns every
// argument into a string: a bare identifier is folded to lower case, a
// quoted identifier keeps its case, an integer loses leading zeros, and
// '...', E'...' and $tag$...$tag$ literals give their value.
func parseTriggerArgs(s string) ([]string, bool) {
	args := []string{}
	i := 0
	skipSpace := func() {
		for i < len(s) && strings.ContainsRune(" \t\r\n", rune(s[i])) {
			i++
		}
	}
	for skipSpace(); i < len(s); skipSpace() {
		var arg string
		switch c := s[i]; {
		case c == '\'':
			v, n, ok := readQuoted(s[i:], '\'', false)
			if !ok {
				return nil, false
			}
			arg, i = v, i+n
		case (c == 'E' || c == 'e') && i+1 < len(s) && s[i+1] == '\'':
			v, n, ok := readQuoted(s[i+1:], '\'', true)
			if !ok {
				return nil, false
			}
			arg, i = v, i+1+n
		case c == '"':
			v, n, ok := readQuoted(s[i:], '"', false)
			if !ok {
				return nil, false
			}
			arg, i = v, i+n
		case c == '$':
			end := strings.IndexByte(s[i+1:], '$')
			if end < 0 {
				return nil, false
			}
			tag := s[i : i+end+2]
			closing := strings.Index(s[i+len(tag):], tag)
			if closing < 0 {
				return nil, false
			}
			arg, i = s[i+len(tag):i+len(tag)+closing], i+2*len(tag)+closing
		default:
			start := i
			for i < len(s) && s[i] != ',' && !strings.ContainsRune(" \t\r\n", rune(s[i])) {
				i++
			}
			arg = strings.ToLower(s[start:i])
			if n, err := strconv.ParseInt(arg, 10, 32); err == nil && n >= 0 {
				arg = strconv.FormatInt(n, 10)
			}
		}
		args = append(args, arg)
		if skipSpace(); i < len(s) {
			if s[i] != ',' {
				return nil, false
			}
			i++
		}
	}
	return args, true
}

// readQuoted reads the literal or identifier at the start of s, which opens
// with quote, and returns its value and its length in s. A doubled quote
// stands for one, and with escapes a backslash sequence is decoded the way
// an E'...' string is.
func readQuoted(s string, quote byte, escapes bool) (string, int, bool) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == quote && i+1 < len(s) && s[i+1] == quote:
			b.WriteByte(quote)
			i++
		case c == quote:
			return b.String(), i + 1, true
		case c == '\\' && escapes && i+1 < len(s):
			i++
			switch e := s[i]; e {
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'x', 'u', 'U':
				width := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
				j := i + 1
				for j < len(s) && j-i <= width && strings.IndexByte("0123456789abcdefABCDEF", s[j]) >= 0 {
					j++
				}
				n, err := strconv.ParseUint(s[i+1:j], 16, 32)
				if err != nil || (e != 'x' && j-i-1 != width) || !utf8.ValidRune(rune(n)) {
					return "", 0, false
				}
				if e == 'x' {
					b.WriteByte(byte(n))
				} else {
					b.WriteRune(rune(n))
				}
				i = j - 1
			case '0', '1', '2', '3', '4', '5', '6', '7':
				j := i
				for j < len(s) && j-i < 3 && s[j] >= '0' && s[j] <= '7' {
					j++
				}
				n, _ := strconv.ParseUint(s[i:j], 8, 16)
				b.WriteByte(byte(n))
				i = j - 1
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, false
}

// triggerCallsEqual compares the function calls of two CREATE TRIGGER
// statements by function name and by argument values, so that every
// spelling of an argument PostgreSQL accepts matches the single-quoted
// form pg_get_triggerdef() prints.
func triggerCallsEqual(desired, live string) (equal, ok bool) {
	dn, da, dok := parseTriggerCall(desired)
	ln, la, lok := parseTriggerCall(live)
	if !dok || !lok {
		return false, false
	}
	return dn == ln && slices.Equal(da, la), true
}

// excludeElement is one element of an exclusion constraint.
type excludeElement struct {
	key, collation, opclass, ordering, operator string
	opclassParams                               map[string]string
}

// excludeDef is a parsed exclusion constraint definition, without the
// leading EXCLUDE keyword. The offsets point into the parsed text: with
// and withEnd delimit the WITH (storage parameters) clause, and an absent
// clause leaves both at the position where one belongs.
type excludeDef struct {
	method     string
	elements   []excludeElement
	include    string
	storage    map[string]string
	tablespace string
	where      string
	tail       string
	with       int
	withEnd    int
}

var (
	excludeUsingRe      = regexp.MustCompile(`(?i)^using\s+("[^"]+"|[\w$]+)\s*`)
	excludeTablespaceRe = regexp.MustCompile(`(?i)^using\s+index\s+tablespace\s+("(?:[^"]|"")+"|[\w$]+)`)
	excludeClauseRe     = regexp.MustCompile(`(?i)^(include|with|where)\s*\(`)
	excludeCollateRe    = regexp.MustCompile(`(?i)^collate\s+((?:"(?:[^"]|"")+"|[\w$]+)(?:\.(?:"(?:[^"]|"")+"|[\w$]+))?)\s*`)
	excludeOpclassRe    = regexp.MustCompile(`^((?:"(?:[^"]|"")+"|[\w$]+)(?:\.(?:"(?:[^"]|"")+"|[\w$]+))?)\s*`)
	excludeOrderingRe   = regexp.MustCompile(`(?i)^(?:asc|desc|nulls)\b`)
	visibleOperatorRe   = regexp.MustCompile(`(?i)\boperator\s*\(\s*"?(?:pg_catalog|public)"?\s*\.\s*([^\s()]+)\s*\)`)
)

// bareVisibleOperators turns each OPERATOR(pg_catalog.op) and
// OPERATOR(public.op) of a declared definition into the bare operator.
// Introspection searches both schemas, so PostgreSQL prints an operator it
// finds there without them. It prints the qualified form only when the bare
// operator resolves to another one, which is why a live definition keeps it.
// d, when not nil, notes each qualifier dropped.
func bareVisibleOperators(s string, d *exprDrops) string {
	masked, lits := maskStringLiterals(s)
	bare := newDropRecorder(d, lits).replace(visibleOperatorRe, masked, "$1", (*dropRecorder).operatorAt)
	if bare == masked {
		return s
	}
	return exprPlaceholderRe.ReplaceAllStringFunc(bare, func(m string) string {
		i, _ := strconv.Atoi(exprPlaceholderRe.FindStringSubmatch(m)[1])
		return lits[i]
	})
}

// closingParen returns the index of the parenthesis that closes the one at
// s[open], skipping string literals and quoted identifiers, or -1.
func closingParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\'', '"':
			q := s[i]
			for i++; i < len(s) && s[i] != q; i++ {
			}
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parseExclude parses an exclusion constraint's definition as YAML gives it
// in expression and as pg_get_constraintdef() prints it after EXCLUDE:
// [USING method] (element WITH operator, ...) [INCLUDE (...)] [WITH (...)]
// [USING INDEX TABLESPACE name] [WHERE (...)], followed by anything else
// (DEFERRABLE ...) in tail.
func parseExclude(def string) (*excludeDef, bool) {
	e := &excludeDef{method: "btree"}
	pos := len(def) - len(strings.TrimLeft(def, " \t\r\n"))
	if m := excludeUsingRe.FindStringSubmatch(def[pos:]); m != nil {
		e.method = strings.ToLower(strings.Trim(m[1], `"`))
		pos += len(m[0])
	}
	if pos >= len(def) || def[pos] != '(' {
		return nil, false
	}
	end := closingParen(def, pos)
	if end < 0 {
		return nil, false
	}
	for _, item := range splitTopLevel(def[pos+1 : end]) {
		el, ok := parseExcludeElement(item)
		if !ok {
			return nil, false
		}
		e.elements = append(e.elements, el)
	}
	pos = end + 1
	e.with, e.withEnd = pos, pos
	for {
		rest := def[pos:]
		trimmed := strings.TrimLeft(rest, " \t\r\n")
		at := pos + len(rest) - len(trimmed)
		if m := excludeTablespaceRe.FindStringSubmatch(trimmed); m != nil {
			e.tablespace = strings.ReplaceAll(strings.Trim(m[1], `"`), `""`, `"`)
			pos = at + len(m[0])
			continue
		}
		m := excludeClauseRe.FindStringSubmatch(trimmed)
		if m == nil {
			e.tail = strings.TrimSpace(trimmed)
			return e, len(e.elements) > 0
		}
		open := at + len(m[0]) - 1
		end := closingParen(def, open)
		if end < 0 {
			return nil, false
		}
		body := def[open+1 : end]
		switch strings.ToLower(m[1]) {
		case "include":
			e.include = body
			e.with, e.withEnd = end+1, end+1
		case "with":
			e.storage = parseStorageParams(body)
			e.with, e.withEnd = pos, end+1
		case "where":
			e.where = body
		}
		pos = end + 1
	}
}

// parseExcludeElement parses one exclusion constraint element:
// {column | function call | (expression)} [COLLATE collation]
// [opclass [(parameters)]] [ASC | DESC] [NULLS FIRST | LAST] WITH operator.
func parseExcludeElement(item string) (excludeElement, bool) {
	el := excludeElement{}
	with := -1
	lower := strings.ToLower(item)
	for i, depth := 0, 0; i < len(item); i++ {
		switch item[i] {
		case '\'', '"':
			q := item[i]
			for i++; i < len(item) && item[i] != q; i++ {
			}
		case '(':
			depth++
		case ')':
			depth--
		case ' ', '\t', '\n':
			if depth == 0 && strings.HasPrefix(lower[i+1:], "with") && len(item) > i+5 && strings.ContainsRune(" \t\n", rune(item[i+5])) {
				with = i
			}
		}
	}
	if with < 0 {
		return el, false
	}
	el.operator = strings.ToLower(strings.Join(strings.Fields(item[with+5:]), ""))
	left := strings.TrimSpace(item[:with])
	if left == "" || el.operator == "" {
		return el, false
	}
	keyEnd := 0
	if left[0] == '(' {
		keyEnd = closingParen(left, 0) + 1
	} else {
		for keyEnd < len(left) && !strings.ContainsRune(" \t\n(", rune(left[keyEnd])) {
			if left[keyEnd] == '"' {
				for keyEnd++; keyEnd < len(left) && left[keyEnd] != '"'; keyEnd++ {
				}
			}
			keyEnd++
		}
		if keyEnd < len(left) && left[keyEnd] == '(' {
			keyEnd = closingParen(left, keyEnd) + 1
		}
	}
	if keyEnd <= 0 || keyEnd > len(left) {
		return el, false
	}
	el.key = left[:keyEnd]
	rest := strings.TrimSpace(left[keyEnd:])
	if m := excludeCollateRe.FindStringSubmatch(rest); m != nil {
		el.collation = m[1]
		rest = rest[len(m[0]):]
	}
	if rest != "" && !excludeOrderingRe.MatchString(rest) {
		m := excludeOpclassRe.FindStringSubmatch(rest)
		if m == nil {
			return el, false
		}
		el.opclass = m[1]
		rest = rest[len(m[0]):]
		if strings.HasPrefix(rest, "(") {
			end := closingParen(rest, 0)
			if end < 0 {
				return el, false
			}
			_, el.opclassParams = splitOpclass(el.opclass + rest[:end+1])
			rest = strings.TrimSpace(rest[end+1:])
		}
	}
	el.ordering = strings.ToLower(strings.Join(strings.Fields(rest), " "))
	return el, true
}

// parseStorageParams splits the inside of a WITH (name = value, ...) clause
// into lower-cased names and their values as written.
func parseStorageParams(s string) map[string]string {
	out := map[string]string{}
	for _, part := range splitTopLevel(s) {
		k, v, _ := strings.Cut(part, "=")
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// excludeStorage returns the storage parameters an exclusion constraint
// declares for its index: those in its expression's WITH clause, overridden
// by the with block. nil means neither is there, which leaves them unmanaged.
func excludeStorage(ct *schema.Constraint) map[string]string {
	e, ok := parseExclude(ct.Expression)
	if !ok || (e.storage == nil && ct.With == nil) {
		return ct.With
	}
	out := map[string]string{}
	for k, v := range e.storage {
		out[k] = v
	}
	for k, v := range ct.With {
		out[strings.ToLower(k)] = v
	}
	return out
}

// constraintStorage returns the storage parameters ct declares for the index
// that backs it, or nil when they are unmanaged.
func constraintStorage(ct *schema.Constraint) map[string]string {
	if strings.EqualFold(ct.Type, "exclude") {
		return excludeStorage(ct)
	}
	return ct.With
}

// excludeExpression returns an exclusion constraint's expression with the
// with block's storage parameters merged into its WITH clause.
func excludeExpression(ct *schema.Constraint) string {
	if len(ct.With) == 0 {
		return ct.Expression
	}
	e, ok := parseExclude(ct.Expression)
	if !ok {
		return ct.Expression
	}
	return ct.Expression[:e.with] + renderStorageParams(excludeStorage(ct), ct.WithOrder) + ct.Expression[e.withEnd:]
}

// excludeMatchesLive compares an exclusion constraint with its live
// definition element by element, so that spellings PostgreSQL does not
// print compare equal: a missing USING (btree), an operator class that is
// the default one for the column, operator(pg_catalog.op) or
// operator(public.op) for an operator found without the qualifier, and an
// operator spelled with or without a schema on the session's search_path
// (see nameScope). Storage parameters and the index
// tablespace are left out, since they are changed in place. ok is false
// when either definition cannot be parsed.
func excludeMatchesLive(lt *LiveTable, name string, ct *schema.Constraint) (equal, ok bool) {
	liveDef := strings.TrimSpace(lt.ConstraintDefs[name])
	if len(liveDef) < len("EXCLUDE ") || !strings.EqualFold(liveDef[:len("EXCLUDE ")], "EXCLUDE ") {
		return false, false
	}
	want, wok := parseExclude(ct.Expression)
	have, hok := parseExclude(liveDef[len("EXCLUDE "):])
	if !wok || !hok {
		return false, false
	}
	if want.method != have.method || len(want.elements) != len(have.elements) {
		return false, true
	}
	opclasses := lt.IndexOpclasses[name]
	sc := activeScope()
	for i, w := range want.elements {
		h := have.elements[i]
		if !constraintDefsEqual(w.key, h.key) || collationName(w.collation) != collationName(h.collation) || w.ordering != h.ordering || !opclassParamsEqual(w.opclassParams, h.opclassParams) {
			return false, true
		}
		switch {
		case len(opclasses) != len(want.elements):
			if opclassName(w.opclass) != opclassName(h.opclass) {
				return false, true
			}
		case w.opclass == "":
			if !opclasses[i].Default {
				return false, true
			}
		case !opclassMatches(w.opclass, opclasses[i]):
			return false, true
		}
		wop, hop := sc.unqualifySQL(w.operator), sc.unqualifySQL(h.operator)
		if wop != hop && visibleOperatorRe.ReplaceAllString(wop, "$1") != hop {
			return false, true
		}
	}
	tail := strings.TrimSpace(want.tail + deferralClause(ct.Deferrable, ct.InitiallyDeferred))
	return constraintDefsEqual(want.include, have.include) && constraintDefsEqual(want.where, have.where) &&
		normalizeConstraintDef(tail) == normalizeConstraintDef(have.tail), true
}

// tsTemplatesEqual reports whether a dictionary's declared template names
// its live one. An unqualified declared name also matches a template of
// that name in another schema, which is where CREATE TEXT SEARCH DICTIONARY
// finds an extension's template through the search_path.
func tsTemplatesEqual(want, live string) bool {
	w, l := opclassName(want), opclassName(live)
	if w == l {
		return true
	}
	i := strings.LastIndexByte(l, '.')
	return !strings.Contains(w, ".") && i >= 0 && l[i+1:] == w
}
