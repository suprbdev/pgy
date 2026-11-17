package diff

import (
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// pinSearchPathQ sets the search_path every introspection query runs with,
// so the text PostgreSQL deparses (types, expressions, constraint, index,
// trigger and view definitions, regclass and regproc output) is the same in
// every session: the objects of public and pg_catalog print without their
// schema, everything else with it.
const pinSearchPathQ = "set local search_path = public"

// sessionSearchPathQ reads the schemas on the session's own search_path; it
// has to run before pinSearchPathQ.
const sessionSearchPathQ = "select current_schemas(false)::text[]"

// nameScope is the set of schemas whose qualifier is optional when a
// declared definition is compared with a live one: public, which
// introspection prints unqualified, and the schemas on the search_path of
// the session that read the live snapshot, where the migration resolves a
// name the YAML leaves unqualified. Each scope memoizes the normalizers
// that depend on it.
type nameScope struct {
	// path lists the optional schemas other than public.
	path    []string
	schemas map[string]bool
	// folded matches an optional qualifier in lower-cased text without
	// double quotes.
	folded *regexp.Regexp
	// pathQualifier and pathOperator match, in SQL as it is written, the
	// qualifier of a schema in path and an OPERATOR(schema.op) call for
	// one; both are nil when path is empty.
	pathQualifier *regexp.Regexp
	pathOperator  *regexp.Regexp
	// literalQualifier matches an optional qualifier at the start of an
	// object name spelled as a string literal.
	literalQualifier *regexp.Regexp

	columnTypes    sync.Map
	defaultExprs   sync.Map
	constraintDefs sync.Map
}

// publicScope is the scope of a snapshot whose session search_path is
// unknown or holds nothing but public.
var publicScope = newNameScope(nil)

// currentScope is the scope the comparison helpers use. Planning makes it
// that of the live snapshot it compares with (see Live.scoped); pgy plans
// against one database per process.
var currentScope atomic.Pointer[nameScope]

// activeScope returns the scope comparisons run in.
func activeScope() *nameScope {
	if sc := currentScope.Load(); sc != nil {
		return sc
	}
	return publicScope
}

// schemaPattern matches schema as an identifier in SQL: quoted, or bare in
// any case when that spells the same name.
func schemaPattern(schema string) string {
	quoted := regexp.QuoteMeta(`"` + strings.ReplaceAll(schema, `"`, `""`) + `"`)
	if plainIdentRe.MatchString(schema) {
		return `(?i:` + regexp.QuoteMeta(schema) + `)|` + quoted
	}
	return quoted
}

func newNameScope(path []string) *nameScope {
	sc := &nameScope{path: path, schemas: map[string]bool{"public": true}}
	folded, spelled := []string{"public"}, []string{}
	literal := []string{"public", `"public"`}
	for _, s := range path {
		sc.schemas[s] = true
		folded = append(folded, regexp.QuoteMeta(strings.ToLower(s)))
		spelled = append(spelled, schemaPattern(s))
		literal = append(literal, regexp.QuoteMeta(`"`+strings.ReplaceAll(s, `"`, `""`)+`"`))
		if plainIdentRe.MatchString(s) {
			literal = append(literal, regexp.QuoteMeta(s))
		}
	}
	sc.folded = regexp.MustCompile(`(^|[^a-z0-9_$.])(?:` + strings.Join(folded, "|") + `)\.`)
	sc.literalQualifier = regexp.MustCompile(`^(?:` + strings.Join(literal, "|") + `)\.`)
	if len(spelled) > 0 {
		alt := `(?:` + strings.Join(spelled, "|") + `)`
		sc.pathQualifier = regexp.MustCompile(`(^|[^\w$."])` + alt + `\.`)
		sc.pathOperator = regexp.MustCompile(`(?i:\boperator)\s*\(\s*` + alt + `\s*\.\s*([^\s()]+)\s*\)`)
	}
	return sc
}

// scopeFor returns the scope for a session search_path: the current one
// when it is for the same schemas, so its memoized results are kept.
func scopeFor(searchPath []string) *nameScope {
	path := []string{}
	for _, s := range searchPath {
		if s != "public" && !systemSchema(s) && !slices.Contains(path, s) {
			path = append(path, s)
		}
	}
	if len(path) == 0 {
		return publicScope
	}
	if cur := currentScope.Load(); cur != nil && slices.Equal(cur.path, path) {
		return cur
	}
	return newNameScope(path)
}

// unqualifyFolded removes the optional qualifiers from s, which is
// lower-cased and has no double quotes.
func (sc *nameScope) unqualifyFolded(s string) string {
	return sc.folded.ReplaceAllString(s, "$1")
}

// unqualifySQL removes from masked, SQL whose string literals are masked
// (see maskStringLiterals), the qualifiers of the schemas in path, and
// turns OPERATOR(schema.op) for one of them into the bare operator.
// PostgreSQL always prints both for those schemas, which the pinned
// search_path leaves out. Qualifiers of public are left to the callers.
func (sc *nameScope) unqualifySQL(masked string) string {
	if sc.pathQualifier == nil {
		return masked
	}
	masked = sc.pathOperator.ReplaceAllString(masked, "$1")
	return sc.pathQualifier.ReplaceAllString(masked, "$1")
}

// unqualifyLiteral removes a leading optional qualifier from an object name
// spelled as a string literal.
func (sc *nameScope) unqualifyLiteral(name string) (string, bool) {
	loc := sc.literalQualifier.FindStringIndex(name)
	if loc == nil {
		return name, false
	}
	return name[loc[1]:], true
}

// mentions reports whether s contains the name of an optional schema.
func (sc *nameScope) mentions(s string) bool {
	if strings.Contains(s, "public") {
		return true
	}
	for _, p := range sc.path {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// scoped makes l's search_path the scope comparisons run in and returns l
// with the function signatures that key its maps spelled in that scope.
// Introspection keys them as the pinned search_path prints them; a declared
// signature may leave out the schemas on the session's search_path, and is
// looked up without them.
func (l *Live) scoped() *Live {
	if l == nil {
		currentScope.Store(publicScope)
		return nil
	}
	sc := scopeFor(l.SearchPath)
	currentScope.Store(sc)
	if l.scope == sc || (l.scope == nil && sc == publicScope) {
		return l
	}
	out := *l
	out.scope = sc
	proc := func(key string) string {
		if sig, ok := strings.CutPrefix(key, "proc:"); ok {
			return "proc:" + sc.functionSignature(sig)
		}
		return key
	}
	out.FunctionDefs = rekeyed(l.FunctionDefs, sc.functionSignature)
	out.Procedures = rekeyed(l.Procedures, sc.functionSignature)
	out.ProcedureDefs = rekeyed(l.ProcedureDefs, sc.functionSignature)
	out.AggregateDefs = rekeyed(l.AggregateDefs, sc.functionSignature)
	out.FunctionGrants = rekeyed(l.FunctionGrants, sc.functionSignature)
	out.FunctionPublicExec = rekeyed(l.FunctionPublicExec, sc.functionSignature)
	out.FunctionComments = rekeyed(l.FunctionComments, sc.functionSignature)
	out.Owners = rekeyed(l.Owners, proc)
	out.ExtensionMembers = rekeyed(l.ExtensionMembers, proc)
	return &out
}

// rekeyed returns m with every key replaced by key(k).
func rekeyed[V any](m map[string]V, key func(string) string) map[string]V {
	if m == nil {
		return nil
	}
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[key(k)] = v
	}
	return out
}
