package diff

import (
	"regexp"
	"slices"
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// exprDrops is what normalising a definition for the text comparison
// (canonicalizeExpr, normalizedDefaultExpr, normalizeConstraintDef) removed
// that can change what the definition means: casts and schema qualifiers.
// The normalised text is the same with or without it. Two definitions that
// normalise to the same text lost the same on the way, or differ in a way
// only PostgreSQL can judge (see exprDrops.differ).
type exprDrops struct {
	// casts holds the operand and the type, both normalised and joined
	// with a NUL, of each cast of anything but a constant.
	casts []string
	// constants holds the same for each cast of a constant: a literal, a
	// number, true, false, null, or an array or row of them.
	constants []string
	// qualifiers holds each name that lost its schema, also in a string
	// literal that names an object ('public.seq'::regclass).
	qualifiers []qualifiedName
}

// qualifiedName is a name and the schema qualifier it was written with,
// unquoted, or lower-cased when they weren't quoted.
type qualifiedName struct {
	schema, name string
}

// normalizedExpr is a definition normalised for the text comparison,
// with what the normalisation dropped.
type normalizedExpr struct {
	text  string
	drops exprDrops
}

// exprDropDiff is how what the normalisation dropped from two definitions
// that are equal as text differs. The zero value means it dropped the same
// from both, so they are the same definition.
type exprDropDiff struct {
	// casts reports a cast of something other than a constant that only
	// one side has or that has another type on each side, and a constant
	// with another type on each side. A constant cast on one side only
	// does not count: PostgreSQL prints the type it gave a literal, which
	// a declared definition leaves to the context.
	casts bool
	// names lists the names that lost a schema qualifier on one side only,
	// or a different one on each side. That changes the meaning only when
	// pg_catalog has an object of the name too (see catalogSharedNames).
	names []string
}

// add merges y into x.
func (x *exprDropDiff) add(y exprDropDiff) {
	x.casts = x.casts || y.casts
	x.names = append(x.names, y.names...)
}

// differ compares what the normalisation dropped from d with what it
// dropped from o, for two definitions that normalise to the same text.
func (d *exprDrops) differ(o *exprDrops) exprDropDiff {
	out := exprDropDiff{casts: !sameElements(d.casts, o.casts) || constantCastsDiffer(d.constants, o.constants)}
	for _, q := range unmatchedNames(d.qualifiers, o.qualifiers) {
		out.names = append(out.names, q.name)
	}
	return out
}

// with returns d with more qualifiers, leaving d as it is.
func (d *exprDrops) with(qualifiers []qualifiedName) *exprDrops {
	if len(qualifiers) == 0 {
		return d
	}
	return &exprDrops{casts: d.casts, constants: d.constants, qualifiers: append(slices.Clone(qualifiers), d.qualifiers...)}
}

func sameElements(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 || slices.Equal(a, b) {
		return true
	}
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// retyped reports whether an expression that d and o both cast is cast to
// other types on each side, such as (emb::halfvec(3)) against
// (emb::halfvec(4)): the two definitions differ unless PostgreSQL stores
// them the same way. A side that casts it to a type the other side casts it
// to as well, and to another type on top (PostgreSQL adds ::text to a
// varchar it compares), is not retyped. Casts of constants don't count
// (see exprDropDiff.casts).
func (d *exprDrops) retyped(o *exprDrops) bool {
	if len(d.casts) == 0 || len(o.casts) == 0 || slices.Equal(d.casts, o.casts) {
		return false
	}
	td, to := castTypes(d.casts), castTypes(o.casts)
	for operand, ts := range td {
		if other, ok := to[operand]; ok && !slices.ContainsFunc(ts, func(t string) bool { return slices.Contains(other, t) }) {
			return true
		}
	}
	return false
}

// castTypes groups the types of casts by their operand.
func castTypes(items []string) map[string][]string {
	out := map[string][]string{}
	for _, it := range items {
		c, t, _ := strings.Cut(it, "\x00")
		if !slices.Contains(out[c], t) {
			out[c] = append(out[c], t)
		}
	}
	return out
}

// constantCastsDiffer reports whether a constant cast on both sides has
// another type on each.
func constantCastsDiffer(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 || slices.Equal(a, b) {
		return false
	}
	ta, tb := castTypes(a), castTypes(b)
	for c, ts := range ta {
		if other, ok := tb[c]; ok && !sameElements(ts, other) {
			return true
		}
	}
	return false
}

// unmatchedNames returns the qualified names one side has more often than
// the other.
func unmatchedNames(a, b []qualifiedName) []qualifiedName {
	if slices.Equal(a, b) {
		return nil
	}
	count := map[qualifiedName]int{}
	for _, q := range a {
		count[q]++
	}
	for _, q := range b {
		count[q]--
	}
	out := []qualifiedName{}
	for _, q := range append(slices.Clone(a), b...) {
		if count[q] != 0 {
			out = append(out, q)
			count[q] = 0
		}
	}
	return out
}

// dropRecorder notes in d what canonicalizeExpr removes from a definition
// whose string literals maskStringLiterals replaced with lits. A nil
// recorder notes nothing and adds no work.
type dropRecorder struct {
	d    *exprDrops
	lits []string
}

func newDropRecorder(d *exprDrops, lits []string) *dropRecorder {
	if d == nil {
		return nil
	}
	return &dropRecorder{d: d, lits: lits}
}

// replace is re.ReplaceAllString(s, repl), passing each match to note
// first.
func (r *dropRecorder) replace(re *regexp.Regexp, s, repl string, note func(r *dropRecorder, s string, m []int)) string {
	if r == nil {
		return re.ReplaceAllString(s, repl)
	}
	ms := re.FindAllStringSubmatchIndex(s, -1)
	if ms == nil {
		return s
	}
	out, last := make([]byte, 0, len(s)), 0
	for _, m := range ms {
		note(r, s, m)
		out = append(out, s[last:m[0]]...)
		out = re.ExpandString(out, repl, s, m)
		last = m[1]
	}
	return string(append(out, s[last:]...))
}

// unqualifyPath is sc.unqualifySQL, noting the qualifiers it removes.
func (r *dropRecorder) unqualifyPath(sc *nameScope, masked string) string {
	if r == nil || sc.pathQualifier == nil {
		return sc.unqualifySQL(masked)
	}
	masked = r.replace(sc.pathOperator, masked, "$1", (*dropRecorder).operatorAt)
	return r.replace(sc.pathQualifier, masked, "$1", (*dropRecorder).qualifierAt)
}

// qualifierAt notes the schema qualifier matched at m, whose first group
// is the character before it, with the name that follows it. The qualifier
// of a cast's type is left to the cast.
func (r *dropRecorder) qualifierAt(s string, m []int) {
	if m[3] > m[2] && s[m[2]] == ':' && m[2] > 0 && s[m[2]-1] == ':' {
		return
	}
	r.d.qualifiers = append(r.d.qualifiers, qualifiedName{identName(s[m[3] : m[1]-1]), leadingName(s[m[1]:])})
}

// operatorAt notes the OPERATOR(schema.op) matched at m, whose first group
// is the operator.
func (r *dropRecorder) operatorAt(s string, m []int) {
	open := m[0] + strings.IndexByte(s[m[0]:m[1]], '(')
	sch := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s[open+1:m[2]]), "."))
	r.d.qualifiers = append(r.d.qualifiers, qualifiedName{identName(sch), s[m[2]:m[3]]})
}

// castAt notes the ::type cast matched at m.
func (r *dropRecorder) castAt(s string, m []int) {
	r.cast(s[castOperandStart(s, m[0]):m[0]], s[m[0]+2:m[1]])
}

// typedLiteralAt notes the type 'literal' constant matched at m, whose
// first group is the literal.
func (r *dropRecorder) typedLiteralAt(s string, m []int) {
	r.cast(s[m[2]:m[3]], strings.Fields(s[m[0]:m[2]])[0])
}

// castCall returns the note rewriteCastCalls takes for CAST(x AS type).
func (r *dropRecorder) castCall() func(operand, typ string) {
	if r == nil {
		return nil
	}
	return r.cast
}

// cast notes a cast of operand, a part of the masked definition, to typ.
func (r *dropRecorder) cast(operand, typ string) {
	operand = strings.TrimSpace(operand)
	item := normalizeConstraintDef(unmaskStringLiterals(operand, r.lits)) + "\x00" + castTypeName(typ)
	if isConstantOperand(operand) {
		r.d.constants = append(r.d.constants, item)
		return
	}
	r.d.casts = append(r.d.casts, item)
}

// noteLeftoverCast notes a ::type that canonicalizeExpr left in place, which
// the normalisers remove character by character.
func (d *exprDrops) noteLeftoverCast(typ string) {
	d.casts = append(d.casts, "\x00"+castTypeName(typ))
}

// castTypeName returns the type of a cast as normalizeColumnType spells
// it, without a schema: a type's schema is left to the rest of the
// comparison, as the text comparison does, while its name and modifiers
// count.
func castTypeName(typ string) string {
	t := normalizeColumnType(typ)
	head := t
	if i := strings.IndexAny(head, "(["); i >= 0 {
		head = head[:i]
	}
	if j := strings.LastIndexByte(head, '.'); j >= 0 {
		return t[j+1:]
	}
	return t
}

// dropPublic removes every public. from seg, a lower-cased part of a
// definition without quotes or spaces, noting the names it qualified.
func (d *exprDrops) dropPublic(seg string) string {
	const public = "public."
	if !strings.Contains(seg, public) {
		return seg
	}
	for rest := seg; ; {
		i := strings.Index(rest, public)
		if i < 0 {
			break
		}
		rest = rest[i+len(public):]
		d.qualifiers = append(d.qualifiers, qualifiedName{"public", leadingName(rest)})
	}
	return strings.ReplaceAll(seg, public, "")
}

// dropSequenceQualifier removes schema from the sequences nextval names in
// s, a normalised default, noting each.
func (d *exprDrops) dropSequenceQualifier(s, schema string) string {
	prefix := "nextval('" + schema + "."
	if !strings.Contains(s, prefix) {
		return s
	}
	for rest := s; ; {
		i := strings.Index(rest, prefix)
		if i < 0 {
			break
		}
		rest = rest[i+len(prefix):]
		name, _, _ := strings.Cut(rest, "'")
		d.qualifiers = append(d.qualifiers, qualifiedName{schema, name})
	}
	return strings.ReplaceAll(s, prefix, "nextval('")
}

var constantOperandRe = regexp.MustCompile(`(?i)^(?:[\s()\[\],+-]|\x00\d+\x00|(?:\d+\.?\d*|\.\d+)(?:e[+-]?\d+)?|\b(?:array|row|true|false|null)\b)*$`)

// isConstantOperand reports whether operand, a part of a masked
// definition, is a constant: literals, numbers, true, false and null, and
// arrays and rows of them, cast or not.
func isConstantOperand(operand string) bool {
	operand = exprCastRe.ReplaceAllString(operand, "")
	return strings.ContainsFunc(operand, func(c rune) bool { return !strings.ContainsRune(" \t\r\n()[],+-", c) }) &&
		constantOperandRe.MatchString(operand)
}

// castOperandStart returns where the operand of the cast at s[at:] starts
// in s, a masked definition: a literal's placeholder, a parenthesised
// expression or an array with the function or array name before it, or a
// name.
func castOperandStart(s string, at int) int {
	i := at
	for i > 0 && strings.IndexByte(" \t\r\n", s[i-1]) >= 0 {
		i--
	}
	if i == 0 {
		return 0
	}
	switch s[i-1] {
	case 0:
		if j := strings.LastIndexByte(s[:i-1], 0); j >= 0 {
			return j
		}
		return i
	case ')', ']':
		j := openingBracket(s, i-1)
		if j < 0 {
			return i
		}
		i = j
	}
	return nameStart(s, i)
}

// openingBracket returns the index of the ( or [ that the ) or ] at
// s[end] closes, or -1.
func openingBracket(s string, end int) int {
	depth := 0
	for i := end; i >= 0; i-- {
		switch s[i] {
		case ')', ']':
			depth++
		case '(', '[':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// nameStart returns where the dotted, possibly quoted, name that ends at
// s[end] starts.
func nameStart(s string, end int) int {
	i := end
	for i > 0 {
		switch c := s[i-1]; {
		case isWordByte(c) || c == '$' || c == '.' || c >= 0x80:
			i--
		case c == '"':
			j := strings.LastIndexByte(s[:i-1], '"')
			if j < 0 {
				return i
			}
			i = j
		default:
			return i
		}
	}
	return i
}

// identName returns an identifier as the catalogs spell it: unquoted, or
// lower-cased when it isn't quoted.
func identName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return strings.ToLower(s)
}

// leadingName returns the identifier or operator at the start of s, as
// identName spells it.
func leadingName(s string) string {
	if strings.HasPrefix(s, `"`) {
		for i := 1; i < len(s); i++ {
			if s[i] == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					i++
					continue
				}
				return identName(s[:i+1])
			}
		}
		return s[1:]
	}
	i := 0
	for i < len(s) && (isWordByte(s[i]) || s[i] == '$' || s[i] >= 0x80) {
		i++
	}
	if i == 0 {
		for i < len(s) && strings.IndexByte("+-*/<>=~!@#%^&|`?", s[i]) >= 0 {
			i++
		}
	}
	return strings.ToLower(s[:i])
}

// defaultsDropped is, for a declared and a live default that defaultsEqual
// takes for the same, how what the comparison dropped from each differs.
func defaultsDropped(desired, live string) exprDropDiff {
	if d, l := normalizedDefaultExpr(desired), normalizedDefaultExpr(live); d.text == l.text {
		return d.drops.differ(&l.drops)
	}
	return constraintDefsDropped(desired, live)
}

// constraintDefsDropped is, for a declared and a live definition that
// constraintDefsEqual takes for the same, how what the comparison dropped
// from each differs.
func constraintDefsDropped(desired, live string) exprDropDiff {
	if d, l, ok := constraintDefsMatchDrops(desired, live); ok {
		return d.differ(l)
	}
	ops := &exprDrops{}
	bare := bareVisibleOperators(desired, ops)
	if bare == desired {
		return exprDropDiff{}
	}
	if d, l, ok := constraintDefsMatchDrops(bare, live); ok {
		return d.with(ops.qualifiers).differ(l)
	}
	return exprDropDiff{}
}

// triggerDefsDropped is, for a declared and a live trigger that
// triggerDefsEqual takes for the same, how what the comparison dropped
// from each differs, including the schema of the trigger's function. A
// declared public. against a live function without a schema is left out:
// it is how pgy pull writes the function of a trigger, and it differs only
// for a function pg_catalog has too, which --strict-expressions covers.
func triggerDefsDropped(desired, live string) exprDropDiff {
	dh, dc, dok := splitTriggerCall(desired)
	lh, lc, lok := splitTriggerCall(live)
	if dok && lok {
		if _, ok := triggerCallsEqual(dc, lc); ok {
			out := constraintDefsDropped(dh, lh)
			ds, name := triggerFunctionSchema(dc)
			if ls, _ := triggerFunctionSchema(lc); ds != ls && (ds != "public" || ls != "") {
				out.names = append(out.names, name)
			}
			return out
		}
	}
	n := func(s string) *normalizedExpr {
		return normalizedConstraintDef(strings.TrimSuffix(strings.TrimSpace(s), ";"))
	}
	return n(desired).drops.differ(&n(live).drops)
}

// triggerFunctionSchema returns the schema a trigger's function call names
// its function with, "" when it has none, and the function's name.
func triggerFunctionSchema(call string) (schema, name string) {
	call = strings.TrimSpace(call)
	inIdent, dot := false, -1
	for i := 0; i < len(call); i++ {
		switch c := call[i]; {
		case c == '"':
			inIdent = !inIdent
		case inIdent:
		case c == '.' && dot < 0:
			dot = i
		case c == '(':
			call = call[:i]
		}
	}
	if dot < 0 || dot >= len(call) {
		return "", identName(call)
	}
	return identName(call[:dot]), identName(call[dot+1:])
}

// constraintDropped is, for a constraint that constraintMatchesLive finds
// equal to its live definition, how what the comparison dropped from each
// differs.
func constraintDropped(lt *LiveTable, name string, ct *schema.Constraint) exprDropDiff {
	if strings.EqualFold(ct.Type, "exclude") {
		if d, ok := excludeDropped(lt, name, ct); ok {
			return d
		}
	}
	c := *ct
	c.Tablespace = ""
	return constraintDefsDropped(stripConstraintWith(renderConstraintDef(&c)), stripConstraintWith(lt.ConstraintDefs[name]))
}

var schemaOperatorRe = regexp.MustCompile(`(?i)^operator\s*\(\s*(.+?)\s*\.\s*([^\s()]+)\s*\)$`)

// excludeDropped is constraintDropped for an exclusion constraint that
// excludeMatchesLive compares element by element. ok is false when either
// definition cannot be parsed.
func excludeDropped(lt *LiveTable, name string, ct *schema.Constraint) (out exprDropDiff, ok bool) {
	liveDef := strings.TrimSpace(lt.ConstraintDefs[name])
	if len(liveDef) < len("EXCLUDE ") || !strings.EqualFold(liveDef[:len("EXCLUDE ")], "EXCLUDE ") {
		return out, false
	}
	want, wok := parseExclude(ct.Expression)
	have, hok := parseExclude(liveDef[len("EXCLUDE "):])
	if !wok || !hok || len(want.elements) != len(have.elements) {
		return out, false
	}
	operator := func(op string) (string, string) {
		if m := schemaOperatorRe.FindStringSubmatch(strings.TrimSpace(op)); m != nil {
			return identName(m[1]), m[2]
		}
		return "", strings.TrimSpace(op)
	}
	for i, w := range want.elements {
		h := have.elements[i]
		out.add(constraintDefsDropped(w.key, h.key))
		ws, op := operator(w.operator)
		if hs, _ := operator(h.operator); ws != hs {
			out.names = append(out.names, op)
		}
	}
	out.add(constraintDefsDropped(want.include, have.include))
	out.add(constraintDefsDropped(want.where, have.where))
	return out, true
}
