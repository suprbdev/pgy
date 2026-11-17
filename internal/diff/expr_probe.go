package diff

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/schema"
)

// ExprKind is the kind of declared definition an ExprProbe is about.
type ExprKind string

// The kinds of definition ProbeExpressions compares through the server.
const (
	ExprDefault       ExprKind = "default"
	ExprGenerated     ExprKind = "generated"
	ExprConstraint    ExprKind = "constraint"
	ExprIndex         ExprKind = "index"
	ExprPolicy        ExprKind = "policy"
	ExprTrigger       ExprKind = "trigger"
	ExprViewDefault   ExprKind = "view default"
	ExprDomainDefault ExprKind = "domain default"
	ExprDomainCheck   ExprKind = "domain check"
	ExprFunctionArgs  ExprKind = "function arguments"
	ExprProcedureArgs ExprKind = "procedure arguments"
)

// ExprKey identifies a declared definition. Object is the "schema.name" of
// the table, view, materialized view or domain it belongs to, or the key of
// its function or procedure in schema.Database. Name is the column,
// constraint, index, policy or trigger, and "" for a domain's default and a
// routine's arguments.
type ExprKey struct {
	Kind   ExprKind
	Object string
	Name   string
}

// ExprProbe is what PostgreSQL made of one declared definition that the
// text comparison takes for different from the live one (see
// ProbeExpressions).
type ExprProbe struct {
	// Declared is the definition that was probed. The result applies to
	// that definition only.
	Declared string
	// Parts is the definition as PostgreSQL deparses it, with the function
	// Introspect reads the live one with: one element, except for an index
	// (its key columns, then its predicate) and a policy (USING, then WITH
	// CHECK).
	Parts []string
	// Err is why PostgreSQL rejected the definition, such as a column or a
	// function this plan creates; "" when it didn't.
	Err string
}

// exprSlot is a declared definition together with the live one it is
// compared with.
type exprSlot interface {
	// equal is the text comparison's verdict.
	equal() bool
	// declared identifies the declared definition (see ExprProbe.Declared).
	declared() string
	// live returns the live definition laid out like ExprProbe.Parts.
	live() []string
	// attach returns the statement that attaches the declared definition
	// to target, the probe's copy of the object, and where the result is
	// read back. id is unique among the statements of one probe.
	attach(target string, id int) probeAttach
	// canonical makes parts, as read back from the probe's copy, comparable
	// with live.
	canonical(parts []string) []string
	// spell gives the live snapshot the declared spelling.
	spell(e *liveEdit)
}

// probeStmt is one statement of a probe. When Type is set it is a query
// for a type name, which replaces the %s in SQL.
type probeStmt struct {
	ID   int    `json:"id"`
	SQL  string `json:"sql"`
	Type string `json:"type,omitempty"`
}

// probeAttach is a probe statement and the kind and name its result is
// read back under.
type probeAttach struct {
	stmt       probeStmt
	kind, name string
}

type columnDefaultSlot struct {
	fq, cn string
	c      *schema.Column
	lc     *LiveColumn
}

func newColumnDefaultSlot(fq, cn string, c *schema.Column, lc *LiveColumn) exprSlot {
	if c == nil || lc == nil || c.Default == "" || lc.Default == "" {
		return nil
	}
	if c.Identity != "" || lc.Identity != "" || c.Generated != "" || lc.Generated != "" {
		return nil
	}
	return &columnDefaultSlot{fq, cn, c, lc}
}

func (s *columnDefaultSlot) equal() bool      { return defaultsEqual(s.c.Default, s.lc.Default) }
func (s *columnDefaultSlot) declared() string { return s.c.Default }
func (s *columnDefaultSlot) live() []string   { return []string{s.lc.Default} }
func (s *columnDefaultSlot) attach(target string, id int) probeAttach {
	return attachDefault(target, id, s.cn, s.c.Default)
}
func (s *columnDefaultSlot) canonical(parts []string) []string { return parts }
func (s *columnDefaultSlot) spell(e *liveEdit) {
	if lc := e.table(s.fq).Columns[s.cn]; lc != nil {
		lc.Default = s.c.Default
	}
}

func attachDefault(target string, id int, column, expr string) probeAttach {
	return probeAttach{
		stmt: probeStmt{ID: id, SQL: fmt.Sprintf("alter table %s alter column %s set default %s", pqIdent(target), quoteIdent(column), expr)},
		kind: "default", name: column,
	}
}

type generatedSlot struct {
	fq, cn string
	c      *schema.Column
	lc     *LiveColumn
}

func newGeneratedSlot(fq, cn string, c *schema.Column, lc *LiveColumn) exprSlot {
	if c == nil || lc == nil || c.Generated == "" || lc.Generated == "" {
		return nil
	}
	return &generatedSlot{fq, cn, c, lc}
}

func (s *generatedSlot) equal() bool      { return constraintDefsEqual(s.c.Generated, s.lc.Generated) }
func (s *generatedSlot) declared() string { return s.c.Generated }
func (s *generatedSlot) live() []string   { return []string{s.lc.Generated} }
func (s *generatedSlot) attach(target string, id int) probeAttach {
	name := fmt.Sprintf("pgy_probe_g%d", id)
	sql := fmt.Sprintf("alter table %s add column %s ", pqIdent(target), quoteIdent(name)) + "%s" +
		fmt.Sprintf(" generated always as (%s) stored", strings.ReplaceAll(s.c.Generated, "%", "%%"))
	typeQ := fmt.Sprintf("select format_type(a.atttypid, a.atttypmod) from pg_attribute a where a.attrelid = %s::regclass and a.attname = %s and a.attnum > 0 and not a.attisdropped",
		quoteString(pqIdent(target)), quoteString(s.cn))
	return probeAttach{stmt: probeStmt{ID: id, SQL: sql, Type: typeQ}, kind: "default", name: name}
}
func (s *generatedSlot) canonical(parts []string) []string { return parts }
func (s *generatedSlot) spell(e *liveEdit) {
	if lc := e.table(s.fq).Columns[s.cn]; lc != nil {
		lc.Generated = s.c.Generated
	}
}

type constraintSlot struct {
	fq, name string
	ct       *schema.Constraint
	lt       *LiveTable
	exclude  bool
	// def is the declared definition as the probe attaches it, without
	// what is changed in place: NOT VALID, and an exclusion constraint's
	// index storage parameters and tablespace.
	def string
}

const notValidSuffix = " NOT VALID"

func newConstraintSlot(fq string, ct *schema.Constraint, lt *LiveTable) exprSlot {
	if ct == nil || lt == nil {
		return nil
	}
	name := truncateIdent(ct.Name)
	if !lt.Constraints[name] || lt.ConstraintDefs[name] == "" {
		return nil
	}
	s := &constraintSlot{fq: fq, name: name, ct: ct, lt: lt}
	switch strings.ToLower(ct.Type) {
	case "check":
		c := *ct
		c.NotValid = false
		s.def = renderConstraintDef(&c)
	case "exclude":
		core, ok := excludeCore(ct.Expression)
		if !ok || !strings.HasPrefix(strings.ToUpper(lt.ConstraintDefs[name]), "EXCLUDE ") {
			return nil
		}
		s.exclude, s.def = true, "exclude "+core+deferralClause(ct.Deferrable, ct.InitiallyDeferred)
	default:
		return nil
	}
	return s
}

func (s *constraintSlot) equal() bool      { return constraintMatchesLive(s.lt, s.name, s.ct) }
func (s *constraintSlot) declared() string { return s.def }
func (s *constraintSlot) live() []string {
	return s.canonical([]string{s.lt.ConstraintDefs[s.name]})
}
func (s *constraintSlot) attach(target string, id int) probeAttach {
	name := fmt.Sprintf("pgy_probe_c%d", id)
	return probeAttach{
		stmt: probeStmt{ID: id, SQL: fmt.Sprintf("alter table %s add constraint %s %s", pqIdent(target), quoteIdent(name), s.def)},
		kind: "constraint", name: name,
	}
}
func (s *constraintSlot) canonical(parts []string) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		p = strings.TrimSuffix(strings.TrimSpace(p), notValidSuffix)
		if s.exclude && len(p) > len("EXCLUDE ") {
			if core, ok := excludeCore(p[len("EXCLUDE "):]); ok {
				p = "EXCLUDE " + core
			}
		}
		out[i] = p
	}
	return out
}
func (s *constraintSlot) spell(e *liveEdit) {
	def := s.def
	if s.exclude {
		def = "EXCLUDE " + strings.TrimSpace(s.ct.Expression) + deferralClause(s.ct.Deferrable, s.ct.InitiallyDeferred)
	}
	if liveNotValid(s.lt.ConstraintDefs[s.name]) {
		def += notValidSuffix
	}
	e.table(s.fq).ConstraintDefs[s.name] = def
}

var excludeTablespaceClauseRe = regexp.MustCompile(`(?i)\s+using\s+index\s+tablespace\s+(?:"(?:[^"]|"")+"|[\w$]+)`)

// excludeCore returns an exclusion constraint's definition, as parseExclude
// takes it, without its index storage parameters and index tablespace,
// which are changed in place and compared on their own.
func excludeCore(def string) (string, bool) {
	e, ok := parseExclude(def)
	if !ok {
		return "", false
	}
	masked, lits := maskStringLiterals(def[:e.with] + def[e.withEnd:])
	masked = excludeTablespaceClauseRe.ReplaceAllString(masked, "")
	return strings.TrimSpace(unmaskStringLiterals(masked, lits)), true
}

// unmaskStringLiterals puts the literals maskStringLiterals took out of
// masked back.
func unmaskStringLiterals(masked string, lits []string) string {
	return exprPlaceholderRe.ReplaceAllStringFunc(masked, func(m string) string {
		i, _ := strconv.Atoi(exprPlaceholderRe.FindStringSubmatch(m)[1])
		return lits[i]
	})
}

type indexSlot struct {
	fq, name string
	matView  bool
	ix       *schema.Index
	cols     map[string]*schema.Column
	li       *LiveIndex
}

// newIndexSlot returns the slot of an index whose key expressions or
// predicate PostgreSQL may print another way than the YAML writes them: an
// index with the same number of keys live, a predicate on both sides or on
// neither, and at least one expression.
func newIndexSlot(fq string, matView bool, ix *schema.Index, cols map[string]*schema.Column, lt *LiveTable) exprSlot {
	if ix == nil || lt == nil || len(ix.Columns) == 0 {
		return nil
	}
	name := LiveIndexName(fq, ix, lt.Indexes)
	li := lt.IndexDefs[name]
	if li == nil || len(li.Keys) != len(ix.Columns) || (ix.Where == "") != (li.Predicate == "") {
		return nil
	}
	expression := ix.Where != ""
	for _, c := range ix.Columns {
		base, _ := splitIndexOrdering(c)
		expression = expression || isIndexExpression(base) && cols[base] == nil
	}
	if !expression {
		return nil
	}
	return &indexSlot{fq, name, matView, ix, cols, li}
}

func (s *indexSlot) method() string {
	if m := strings.ToLower(strings.TrimSpace(s.ix.Using)); m != "" {
		return m
	}
	return "btree"
}

func (s *indexSlot) keys() []string {
	out := make([]string, len(s.ix.Columns))
	for i, c := range s.ix.Columns {
		out[i], _ = splitIndexOrdering(c)
	}
	return out
}

func (s *indexSlot) equal() bool {
	for i, base := range s.keys() {
		if !constraintDefsEqual(base, s.li.Keys[i]) {
			return false
		}
	}
	return constraintDefsEqual(s.ix.Where, s.li.Predicate)
}
func (s *indexSlot) declared() string {
	def := "using " + s.method() + " (" + renderIndexElems(s.ix, s.cols) + ")"
	if s.ix.Where != "" {
		def += fmt.Sprintf(" where (%s)", s.ix.Where)
	}
	return def
}
func (s *indexSlot) live() []string { return append(slices.Clone(s.li.Keys), s.li.Predicate) }
func (s *indexSlot) attach(target string, id int) probeAttach {
	name := fmt.Sprintf("pgy_probe_i%d", id)
	return probeAttach{
		stmt: probeStmt{ID: id, SQL: fmt.Sprintf("create index %s on %s %s", quoteIdent(name), pqIdent(target), s.declared())},
		kind: "index", name: name,
	}
}
func (s *indexSlot) canonical(parts []string) []string { return parts }
func (s *indexSlot) spell(e *liveEdit) {
	lt := e.table(s.fq)
	if s.matView {
		lt = e.matView(s.fq)
	}
	if li := lt.IndexDefs[s.name]; li != nil {
		li.Keys, li.Predicate = s.keys(), s.ix.Where
	}
}

type policySlot struct {
	fq, name string
	pol      *schema.Policy
	lp       *LivePolicy
}

func newPolicySlot(fq string, pol *schema.Policy, lt *LiveTable) exprSlot {
	if pol == nil || lt == nil || pol.Name == "" {
		return nil
	}
	name := truncateIdent(pol.Name)
	lp := lt.Policies[name]
	if lp == nil || (pol.Using == "") != (lp.Using == "") || (pol.WithCheck == "") != (lp.WithCheck == "") || pol.Using+pol.WithCheck == "" {
		return nil
	}
	return &policySlot{fq, name, pol, lp}
}

func (s *policySlot) equal() bool {
	return constraintDefsEqual(s.pol.Using, s.lp.Using) && constraintDefsEqual(s.pol.WithCheck, s.lp.WithCheck)
}
func (s *policySlot) declared() string {
	def := ""
	if s.pol.For != "" {
		def += " for " + strings.ToLower(s.pol.For)
	}
	if s.pol.Using != "" {
		def += fmt.Sprintf(" using (%s)", s.pol.Using)
	}
	if s.pol.WithCheck != "" {
		def += fmt.Sprintf(" with check (%s)", s.pol.WithCheck)
	}
	return strings.TrimSpace(def)
}
func (s *policySlot) live() []string { return []string{s.lp.Using, s.lp.WithCheck} }
func (s *policySlot) attach(target string, id int) probeAttach {
	name := fmt.Sprintf("pgy_probe_p%d", id)
	return probeAttach{
		stmt: probeStmt{ID: id, SQL: fmt.Sprintf("create policy %s on %s %s", quoteIdent(name), pqIdent(target), s.declared())},
		kind: "policy", name: name,
	}
}
func (s *policySlot) canonical(parts []string) []string { return parts }
func (s *policySlot) spell(e *liveEdit) {
	if lp := e.table(s.fq).Policies[s.name]; lp != nil {
		lp.Using, lp.WithCheck = s.pol.Using, s.pol.WithCheck
	}
}

type triggerSlot struct {
	fq, name string
	tr       *schema.Trigger
	lt       *LiveTable
}

func newTriggerSlot(fq string, tr *schema.Trigger, lt *LiveTable) exprSlot {
	if tr == nil || lt == nil || tr.Procedure == "" {
		return nil
	}
	name := truncateIdent(tr.Name)
	if lt.Triggers[name] == "" {
		return nil
	}
	return &triggerSlot{fq, name, tr, lt}
}

func (s *triggerSlot) equal() bool      { return triggerDefsEqual(s.declared(), s.lt.Triggers[s.name]) }
func (s *triggerSlot) declared() string { return renderTrigger(s.fq, s.tr) }
func (s *triggerSlot) live() []string   { return []string{s.lt.Triggers[s.name]} }
func (s *triggerSlot) attach(target string, id int) probeAttach {
	return probeAttach{stmt: probeStmt{ID: id, SQL: renderTrigger(target, s.tr)}, kind: "trigger", name: s.name}
}
func (s *triggerSlot) canonical(parts []string) []string { return parts }
func (s *triggerSlot) spell(e *liveEdit)                 { e.table(s.fq).Triggers[s.name] = s.declared() }

type viewDefaultSlot struct {
	fq, col    string
	want, have string
}

func newViewDefaultSlot(live *Live, fq, col string, vw *schema.View) exprSlot {
	if vw == nil || vw.Materialized || !live.Views[fq] {
		return nil
	}
	want, have := vw.ColumnDefaults[col], live.ViewColumnDefaults[fq][col]
	if want == "" || have == "" {
		return nil
	}
	return &viewDefaultSlot{fq, col, want, have}
}

func (s *viewDefaultSlot) equal() bool      { return defaultsEqual(s.want, s.have) }
func (s *viewDefaultSlot) declared() string { return s.want }
func (s *viewDefaultSlot) live() []string   { return []string{s.have} }
func (s *viewDefaultSlot) attach(target string, id int) probeAttach {
	return attachDefault(target, id, s.col, s.want)
}
func (s *viewDefaultSlot) canonical(parts []string) []string { return parts }
func (s *viewDefaultSlot) spell(e *liveEdit)                 { e.viewDefaults(s.fq)[s.col] = s.want }

type domainDefaultSlot struct {
	fq string
	dm *schema.Domain
	ld *LiveDomain
}

func newDomainDefaultSlot(fq string, dm *schema.Domain, ld *LiveDomain) exprSlot {
	if dm == nil || ld == nil || dm.Default == "" || ld.Default == "" {
		return nil
	}
	return &domainDefaultSlot{fq, dm, ld}
}

func (s *domainDefaultSlot) equal() bool      { return defaultsEqual(s.dm.Default, s.ld.Default) }
func (s *domainDefaultSlot) declared() string { return s.dm.Default }
func (s *domainDefaultSlot) live() []string   { return []string{s.ld.Default} }
func (s *domainDefaultSlot) attach(target string, id int) probeAttach {
	_, name := splitQualified(target)
	return probeAttach{
		stmt: probeStmt{ID: id, SQL: fmt.Sprintf("alter domain %s set default %s", pqIdent(target), s.dm.Default)},
		kind: "domain default", name: name,
	}
}
func (s *domainDefaultSlot) canonical(parts []string) []string { return parts }
func (s *domainDefaultSlot) spell(e *liveEdit)                 { e.domain(s.fq).Default = s.dm.Default }

type domainCheckSlot struct {
	fq, name string
	expr     string
	ld       *LiveDomain
}

func newDomainCheckSlot(fq, name string, dm *schema.Domain, ld *LiveDomain) exprSlot {
	if dm == nil || ld == nil {
		return nil
	}
	expr := desiredDomainChecks(fq, dm)[name]
	if expr == "" || ld.Checks[name] == "" {
		return nil
	}
	return &domainCheckSlot{fq, name, expr, ld}
}

func (s *domainCheckSlot) equal() bool      { return constraintDefsEqual(s.declared(), s.ld.Checks[s.name]) }
func (s *domainCheckSlot) declared() string { return "check (" + s.expr + ")" }
func (s *domainCheckSlot) live() []string   { return s.canonical([]string{s.ld.Checks[s.name]}) }
func (s *domainCheckSlot) attach(target string, id int) probeAttach {
	name := fmt.Sprintf("pgy_probe_c%d", id)
	return probeAttach{
		stmt: probeStmt{ID: id, SQL: fmt.Sprintf("alter domain %s add constraint %s %s", pqIdent(target), quoteIdent(name), s.declared())},
		kind: "constraint", name: name,
	}
}
func (s *domainCheckSlot) canonical(parts []string) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSuffix(strings.TrimSpace(p), notValidSuffix)
	}
	return out
}
func (s *domainCheckSlot) spell(e *liveEdit) {
	def := "CHECK (" + s.expr + ")"
	if liveNotValid(s.ld.Checks[s.name]) {
		def += notValidSuffix
	}
	e.domain(s.fq).Checks[s.name] = def
}

type routineArgsSlot struct {
	procedure     bool
	norm          string
	args, returns string
	liveArgs      string
}

func newFunctionArgsSlot(live *Live, key string, f *schema.Function) exprSlot {
	if f == nil || !f.HasBody() || !declaresArgDefault(f.ArgsSig) {
		return nil
	}
	norm := normalizeFunctionSignature(schema.RoutineName(key) + f.ArgsSig)
	lf := live.FunctionDefs[norm]
	if lf == nil || lf.Args == "" {
		return nil
	}
	return &routineArgsSlot{norm: norm, args: f.ArgsSig, returns: f.Returns, liveArgs: lf.Args}
}

func newProcedureArgsSlot(live *Live, key string, p *schema.Procedure) exprSlot {
	if p == nil || !p.HasBody() || !declaresArgDefault(p.ArgsSig) {
		return nil
	}
	norm := normalizeFunctionSignature(schema.RoutineName(key) + p.ArgsSig)
	lp := live.ProcedureDefs[norm]
	if lp == nil || lp.Args == "" {
		return nil
	}
	return &routineArgsSlot{procedure: true, norm: norm, args: p.ArgsSig, liveArgs: lp.Args}
}

// declaresArgDefault reports whether an argument list may hold a default.
func declaresArgDefault(args string) bool {
	return strings.Contains(args, "=") || strings.Contains(strings.ToLower(args), "default")
}

// equal reports whether no argument has, on both sides, defaults the text
// comparison takes for different. A default that only one side has is a
// change no spelling explains.
func (s *routineArgsSlot) equal() bool {
	d, l := argDefaults(s.args), argDefaults(s.liveArgs)
	if len(d) != len(l) {
		return true
	}
	for i := range d {
		if d[i] != "" && l[i] != "" && d[i] != l[i] && !defaultsEqual(d[i], l[i]) {
			return false
		}
	}
	return true
}
func (s *routineArgsSlot) declared() string {
	if s.procedure || s.returns == "" {
		return s.args
	}
	return s.args + " returns " + s.returns
}
func (s *routineArgsSlot) live() []string { return []string{s.liveArgs} }
func (s *routineArgsSlot) attach(_ string, id int) probeAttach {
	name := fmt.Sprintf("pgy_probe_a%d", id)
	kind := "function"
	if s.procedure {
		kind = "procedure"
	}
	return probeAttach{
		stmt: probeStmt{ID: id, SQL: fmt.Sprintf("create %s pg_temp.%s%s language plpgsql as ''", kind, quoteIdent(name), s.declared())},
		kind: "arguments", name: name,
	}
}
func (s *routineArgsSlot) canonical(parts []string) []string { return parts }
func (s *routineArgsSlot) spell(e *liveEdit) {
	args := strings.TrimSpace(s.args)
	if strings.HasPrefix(args, "(") && strings.HasSuffix(args, ")") {
		args = args[1 : len(args)-1]
	}
	if s.procedure {
		e.procedure(s.norm).Args = args
	} else {
		e.function(s.norm).Args = args
	}
}

// resolveExprSlot finds the declared definition key names and its live
// counterpart, or returns nil when either is missing or their shapes differ
// so that no spelling could make them equal.
func resolveExprSlot(live *Live, desired *schema.Database, key ExprKey) exprSlot {
	fq := key.Object
	switch key.Kind {
	case ExprDefault, ExprGenerated, ExprConstraint, ExprPolicy, ExprTrigger:
		dt, lt := desired.Tables[fq], live.Tables[fq]
		if dt == nil || lt == nil {
			return nil
		}
		switch key.Kind {
		case ExprDefault:
			return newColumnDefaultSlot(fq, key.Name, dt.Columns[key.Name], lt.Columns[key.Name])
		case ExprGenerated:
			return newGeneratedSlot(fq, key.Name, dt.Columns[key.Name], lt.Columns[key.Name])
		case ExprConstraint:
			for _, ct := range dt.Constraints {
				if ct != nil && truncateIdent(ct.Name) == key.Name {
					return newConstraintSlot(fq, ct, lt)
				}
			}
		case ExprPolicy:
			for _, pol := range dt.Policies {
				if pol != nil && truncateIdent(pol.Name) == key.Name {
					return newPolicySlot(fq, pol, lt)
				}
			}
		case ExprTrigger:
			for _, tr := range dt.Triggers {
				if tr != nil && truncateIdent(tr.Name) == key.Name {
					return newTriggerSlot(fq, tr, lt)
				}
			}
		}
	case ExprIndex:
		if dt, lt := desired.Tables[fq], live.Tables[fq]; dt != nil && lt != nil {
			for _, ix := range dt.Indexes {
				if ix != nil && LiveIndexName(fq, ix, lt.Indexes) == key.Name {
					return newIndexSlot(fq, false, ix, dt.Columns, lt)
				}
			}
		}
		if vw, lt := desired.Views[fq], live.MatViewTables[fq]; vw != nil && vw.Materialized && lt != nil {
			for _, ix := range vw.Indexes {
				if ix != nil && LiveIndexName(fq, ix, lt.Indexes) == key.Name {
					return newIndexSlot(fq, true, ix, nil, lt)
				}
			}
		}
	case ExprViewDefault:
		return newViewDefaultSlot(live, fq, key.Name, desired.Views[fq])
	case ExprDomainDefault:
		return newDomainDefaultSlot(fq, desired.Domains[fq], live.DomainDefs[fq])
	case ExprDomainCheck:
		return newDomainCheckSlot(fq, key.Name, desired.Domains[fq], live.DomainDefs[fq])
	case ExprFunctionArgs:
		return newFunctionArgsSlot(live, fq, desired.Functions[fq])
	case ExprProcedureArgs:
		return newProcedureArgsSlot(live, fq, desired.Procedures[fq])
	}
	return nil
}

// liveEdit builds a copy of a live snapshot that shares everything with
// the original except the objects it hands out for editing.
type liveEdit struct {
	src, out *Live
	copied   map[string]bool
}

func (e *liveEdit) live() *Live {
	if e.out == nil {
		c := *e.src
		e.out, e.copied = &c, map[string]bool{}
	}
	return e.out
}

// result returns the edited copy, or the original when nothing was edited.
func (e *liveEdit) result() *Live {
	if e.out == nil {
		return e.src
	}
	return e.out
}

// editable returns the entry of *m under key for editing: the first use of
// a map copies it, and the first use of an entry clones it.
func editable[V any](e *liveEdit, kind, key string, m *map[string]V, clone func(V) V) V {
	if !e.copied[kind] {
		*m, e.copied[kind] = cloneMap(*m, nil), true
	}
	if entry := kind + "\x00" + key; !e.copied[entry] {
		(*m)[key], e.copied[entry] = clone((*m)[key]), true
	}
	return (*m)[key]
}

func (e *liveEdit) table(fq string) *LiveTable {
	return editable(e, "table", fq, &e.live().Tables, cloneLiveTable)
}

func (e *liveEdit) matView(fq string) *LiveTable {
	return editable(e, "materialized view", fq, &e.live().MatViewTables, cloneLiveTable)
}

func (e *liveEdit) viewDefaults(fq string) map[string]string {
	return editable(e, "view", fq, &e.live().ViewColumnDefaults, func(m map[string]string) map[string]string { return cloneMap(m, nil) })
}

func (e *liveEdit) domain(fq string) *LiveDomain {
	return editable(e, "domain", fq, &e.live().DomainDefs, func(d *LiveDomain) *LiveDomain {
		c := *d
		c.Checks = cloneMap(d.Checks, nil)
		return &c
	})
}

func (e *liveEdit) function(norm string) *LiveFunction {
	return editable(e, "function", norm, &e.live().FunctionDefs, func(f *LiveFunction) *LiveFunction { c := *f; return &c })
}

func (e *liveEdit) procedure(norm string) *LiveProcedure {
	return editable(e, "procedure", norm, &e.live().ProcedureDefs, func(p *LiveProcedure) *LiveProcedure { c := *p; return &c })
}

// applyExprProbes returns live with the declared spelling in place of each
// live definition that live.ExprProbes shows PostgreSQL stores the declared
// one as: the probe deparsed the declared definition to exactly the live
// text. The comparisons of the plan then find the two equal. A definition
// the probe deparsed to something else, couldn't attach, or didn't cover
// keeps its live text, and is compared as text. live itself is not
// modified.
func applyExprProbes(live *Live, desired *schema.Database) *Live {
	if live == nil || desired == nil || len(live.ExprProbes) == 0 {
		return live
	}
	e := &liveEdit{src: live}
	for key, p := range live.ExprProbes {
		if p == nil || p.Err != "" {
			continue
		}
		s := resolveExprSlot(live, desired, key)
		if s == nil || s.declared() != p.Declared || !slices.Equal(s.live(), s.canonical(p.Parts)) {
			continue
		}
		s.spell(e)
	}
	return e.result()
}

// probeRelation is a table, view or materialized view whose definitions
// are probed on a temporary table LIKE it, created under its own name so
// that the definitions can name it.
type probeRelation struct {
	Schema string      `json:"schema"`
	Name   string      `json:"name"`
	Like   string      `json:"like"`
	Run    []probeStmt `json:"run"`
}

// probeSpec is the input of pg_temp.pgy_probe: the relations to copy with
// the statements to run on each copy, and the statements for domains and
// routines.
type probeSpec struct {
	Relations []*probeRelation `json:"relations"`
	Run       []probeStmt      `json:"run"`
}

// probeDef is one definition pg_temp.pgy_probe read back.
type probeDef struct {
	Kind  string   `json:"kind"`
	Name  string   `json:"name"`
	Parts []string `json:"parts"`
}

// probeReply is the output of pg_temp.pgy_probe. Relations lines up with
// probeSpec.Relations.
type probeReply struct {
	Errors []struct {
		ID  int    `json:"id"`
		Err string `json:"err"`
	} `json:"errors"`
	Relations []struct {
		Err  string     `json:"err"`
		Defs []probeDef `json:"defs"`
	} `json:"relations"`
	Defs []probeDef `json:"defs"`
}

// probeRequest is one definition to probe. relation is its index in
// probeSpec.Relations, -1 for a domain's or a routine's.
type probeRequest struct {
	key      ExprKey
	slot     exprSlot
	relation int
	attach   probeAttach
}

// exprProbePlan collects the definitions to probe.
type exprProbePlan struct {
	spec      probeSpec
	requests  []*probeRequest
	relations map[string]int
	domains   map[string]string
}

func (p *exprProbePlan) add(key ExprKey, slot exprSlot, relation int, target string) {
	id := len(p.requests)
	r := &probeRequest{key: key, slot: slot, relation: relation, attach: slot.attach(target, id)}
	p.requests = append(p.requests, r)
	if relation < 0 {
		p.spec.Run = append(p.spec.Run, r.attach.stmt)
	} else {
		p.spec.Relations[relation].Run = append(p.spec.Relations[relation].Run, r.attach.stmt)
	}
}

// relation adds a definition of the relation declared as fq, which is
// source in the database.
func (p *exprProbePlan) relation(fq, source string, key ExprKey, slot exprSlot) {
	if slot == nil || slot.equal() {
		return
	}
	sch, name := splitQualified(fq)
	i, ok := p.relations[fq]
	if !ok {
		i = len(p.spec.Relations)
		p.relations[fq] = i
		p.spec.Relations = append(p.spec.Relations, &probeRelation{Schema: sch, Name: name, Like: pqIdent(source)})
	}
	p.add(key, slot, i, "pg_temp."+name)
}

// domain adds a definition of the domain declared as fq, which is source
// in the database. The first one creates the probe's domain over the same
// base type.
func (p *exprProbePlan) domain(fq, source string, key ExprKey, slot exprSlot) {
	if slot == nil || slot.equal() {
		return
	}
	target, ok := p.domains[fq]
	if !ok {
		target = fmt.Sprintf("pg_temp.pgy_probe_d%d", len(p.domains))
		p.domains[fq] = target
		sch, name := splitQualified(source)
		p.spec.Run = append(p.spec.Run, probeStmt{
			ID:  -1,
			SQL: "create domain " + pqIdent(target) + " as %s",
			Type: fmt.Sprintf("select format_type(t.typbasetype, t.typtypmod) from pg_type t join pg_namespace n on n.oid = t.typnamespace where n.nspname = %s and t.typname = %s and t.typtype = 'd'",
				quoteString(sch), quoteString(name)),
		})
	}
	p.add(key, slot, -1, target)
}

// routine adds the arguments of a function or procedure, which are probed
// on a temporary routine of their own.
func (p *exprProbePlan) routine(key ExprKey, slot exprSlot) {
	if slot == nil || slot.equal() {
		return
	}
	p.add(key, slot, -1, "")
}

// renameSourceOf returns the name fq has in the database before the plan's
// renames: fq itself when it exists, else the renamedFrom it was declared
// with when that does.
func renameSourceOf(fq, renamedFrom string, exists func(string) bool) (string, bool) {
	if exists(fq) {
		return fq, true
	}
	if renamedFrom == "" {
		return "", false
	}
	old := renameSource(fq, renamedFrom)
	return old, exists(old)
}

// collectExprProbes finds the declared definitions that have a live
// counterpart the text comparison takes for different. live is the
// snapshot as introspected.
func collectExprProbes(live *Live, desired *schema.Database) *exprProbePlan {
	p := &exprProbePlan{relations: map[string]int{}, domains: map[string]string{}}
	p.spec.Relations, p.spec.Run = []*probeRelation{}, []probeStmt{}
	scoped := live.scoped()
	renamed, _ := applyRenames(scoped, desired)

	for _, fq := range sortedKeys(desired.Tables) {
		dt, lt := desired.Tables[fq], renamed.Tables[fq]
		if dt == nil || lt == nil || foreignKindMismatch(dt, lt) {
			continue
		}
		source, ok := renameSourceOf(fq, dt.RenamedFrom, func(k string) bool { return scoped.Tables[k] != nil })
		if !ok {
			continue
		}
		for _, cn := range sortedKeys(dt.Columns) {
			c, lc := dt.Columns[cn], lt.Columns[cn]
			p.relation(fq, source, ExprKey{ExprDefault, fq, cn}, newColumnDefaultSlot(fq, cn, c, lc))
			p.relation(fq, source, ExprKey{ExprGenerated, fq, cn}, newGeneratedSlot(fq, cn, c, lc))
		}
		for _, ct := range dt.Constraints {
			if ct != nil {
				p.relation(fq, source, ExprKey{ExprConstraint, fq, truncateIdent(ct.Name)}, newConstraintSlot(fq, ct, lt))
			}
		}
		for _, ix := range dt.Indexes {
			if ix != nil && len(ix.Columns) > 0 {
				p.relation(fq, source, ExprKey{ExprIndex, fq, LiveIndexName(fq, ix, lt.Indexes)}, newIndexSlot(fq, false, ix, dt.Columns, lt))
			}
		}
		for _, pol := range dt.Policies {
			if pol != nil {
				p.relation(fq, source, ExprKey{ExprPolicy, fq, truncateIdent(pol.Name)}, newPolicySlot(fq, pol, lt))
			}
		}
		for _, tr := range dt.Triggers {
			if tr != nil {
				p.relation(fq, source, ExprKey{ExprTrigger, fq, truncateIdent(tr.Name)}, newTriggerSlot(fq, tr, lt))
			}
		}
	}

	for _, fq := range sortedKeys(desired.Views) {
		vw := desired.Views[fq]
		if vw == nil {
			continue
		}
		if vw.Materialized {
			lt := renamed.MatViewTables[fq]
			source, ok := renameSourceOf(fq, vw.RenamedFrom, func(k string) bool { return scoped.MatViews[k] })
			if lt == nil || !ok {
				continue
			}
			for _, ix := range vw.Indexes {
				if ix != nil && len(ix.Columns) > 0 {
					p.relation(fq, source, ExprKey{ExprIndex, fq, LiveIndexName(fq, ix, lt.Indexes)}, newIndexSlot(fq, true, ix, nil, lt))
				}
			}
			continue
		}
		if len(vw.ColumnDefaults) == 0 || !scoped.Views[fq] {
			continue
		}
		for _, col := range sortedKeys(vw.ColumnDefaults) {
			p.relation(fq, fq, ExprKey{ExprViewDefault, fq, col}, newViewDefaultSlot(renamed, fq, col, vw))
		}
	}

	for _, fq := range sortedKeys(desired.Domains) {
		dm, ld := desired.Domains[fq], renamed.DomainDefs[fq]
		if dm == nil || ld == nil {
			continue
		}
		source, ok := renameSourceOf(fq, dm.RenamedFrom, func(k string) bool { return scoped.Domains[k] })
		if !ok {
			continue
		}
		p.domain(fq, source, ExprKey{ExprDomainDefault, fq, ""}, newDomainDefaultSlot(fq, dm, ld))
		for _, name := range sortedKeys(desiredDomainChecks(fq, dm)) {
			p.domain(fq, source, ExprKey{ExprDomainCheck, fq, name}, newDomainCheckSlot(fq, name, dm, ld))
		}
	}

	for _, k := range sortedKeys(desired.Functions) {
		p.routine(ExprKey{ExprFunctionArgs, k, ""}, newFunctionArgsSlot(renamed, k, desired.Functions[k]))
	}
	for _, k := range sortedKeys(desired.Procedures) {
		p.routine(ExprKey{ExprProcedureArgs, k, ""}, newProcedureArgsSlot(renamed, k, desired.Procedures[k]))
	}
	return p
}

// results turns the server's reply into the probe of each request.
func (p *exprProbePlan) results(reply *probeReply) map[ExprKey]*ExprProbe {
	failed := map[int]string{}
	for _, e := range reply.Errors {
		failed[e.ID] = e.Err
	}
	read := func(defs []probeDef) map[[2]string][]string {
		out := make(map[[2]string][]string, len(defs))
		for _, d := range defs {
			out[[2]string{d.Kind, d.Name}] = d.Parts
		}
		return out
	}
	global := read(reply.Defs)
	relations := make([]map[[2]string][]string, len(reply.Relations))
	for i, r := range reply.Relations {
		relations[i] = read(r.Defs)
	}
	out := make(map[ExprKey]*ExprProbe, len(p.requests))
	for id, r := range p.requests {
		probe := &ExprProbe{Declared: r.slot.declared()}
		out[r.key] = probe
		defs := global
		if r.relation >= 0 {
			if r.relation >= len(reply.Relations) {
				probe.Err = "no result"
				continue
			}
			if err := reply.Relations[r.relation].Err; err != "" {
				probe.Err = err
				continue
			}
			defs = relations[r.relation]
		}
		if err, ok := failed[id]; ok {
			probe.Err = err
			continue
		}
		parts, ok := defs[[2]string{r.attach.kind, r.attach.name}]
		if !ok {
			probe.Err = "no result"
			continue
		}
		probe.Parts = parts
	}
	return out
}

// exprProbeFuncs creates the probe's functions, one statement each.
//
// pgy_probe_defs and pgy_probe_globals read definitions back with the
// functions and the pinned search_path Introspect uses (see
// pinSearchPathQ), so the text is comparable with the live snapshot's.
// pg_get_triggerdef names the trigger's table, which shown replaces with
// the name the live definition has.
//
// pgy_probe_run executes statements on the session's own search_path,
// which is how the migration will run them, each in a subtransaction, and
// returns the errors.
//
// pgy_probe copies each relation into a temporary table of the same name,
// runs its statements, reads the definitions back and renames the copy
// away, which is cheaper than dropping it, so that the next relation's
// definitions don't resolve a name to it.
var exprProbeFuncs = []string{`
create function pg_temp.pgy_probe_defs(rel oid, shown text) returns jsonb language sql stable set search_path = public as $f$
    select coalesce(jsonb_agg(jsonb_build_object('kind', d.kind, 'name', d.name, 'parts', d.parts)), '[]'::jsonb)
    from (
        select 'default' as kind, a.attname::text as name, jsonb_build_array(pg_get_expr(ad.adbin, ad.adrelid)) as parts
        from pg_attrdef ad
        join pg_attribute a on a.attrelid = ad.adrelid and a.attnum = ad.adnum
        where ad.adrelid = rel
        union all
        select 'constraint', con.conname::text, jsonb_build_array(pg_get_constraintdef(con.oid))
        from pg_constraint con
        where con.conrelid = rel and con.contype in ('c', 'x')
        union all
        select 'index', i.relname::text,
               to_jsonb(array(select case when x.indkey[k - 1] = 0 then pg_get_indexdef(x.indexrelid, k, true)
                                          else (select quote_ident(a.attname) from pg_attribute a where a.attrelid = x.indrelid and a.attnum = x.indkey[k - 1]) end
                              from generate_series(1, x.indnkeyatts) k order by k)
                        || coalesce(pg_get_expr(x.indpred, x.indrelid, true), ''))
        from pg_index x
        join pg_class i on i.oid = x.indexrelid
        where x.indrelid = rel
        union all
        select 'policy', pol.polname::text,
               jsonb_build_array(coalesce(pg_get_expr(pol.polqual, pol.polrelid), ''), coalesce(pg_get_expr(pol.polwithcheck, pol.polrelid), ''))
        from pg_policy pol
        where pol.polrelid = rel
        union all
        select 'trigger', t.tgname::text,
               jsonb_build_array(replace(pg_get_triggerdef(t.oid), ' ON pg_temp.' || quote_ident(c.relname) || ' ', ' ON ' || shown || ' '))
        from pg_trigger t
        join pg_class c on c.oid = t.tgrelid
        where t.tgrelid = rel and not t.tgisinternal
    ) d
$f$`, `
create function pg_temp.pgy_probe_globals() returns jsonb language sql stable set search_path = public as $f$
    select coalesce(jsonb_agg(jsonb_build_object('kind', d.kind, 'name', d.name, 'parts', d.parts)), '[]'::jsonb)
    from (
        select 'domain default' as kind, t.typname::text as name,
               jsonb_build_array(coalesce(pg_get_expr(t.typdefaultbin, 0), t.typdefault, '')) as parts
        from pg_type t
        where t.typnamespace = pg_my_temp_schema() and t.typtype = 'd'
        union all
        select 'constraint', con.conname::text, jsonb_build_array(pg_get_constraintdef(con.oid))
        from pg_constraint con
        join pg_type t on t.oid = con.contypid
        where t.typnamespace = pg_my_temp_schema() and con.contype = 'c'
        union all
        select 'arguments', p.proname::text, jsonb_build_array(coalesce(pg_get_function_arguments(p.oid), ''))
        from pg_proc p
        where p.pronamespace = pg_my_temp_schema() and starts_with(p.proname, 'pgy_probe_a')
    ) d
$f$`, `
create function pg_temp.pgy_probe_run(stmts jsonb) returns jsonb[] language plpgsql as $f$
declare
    st jsonb;
    typ text;
    failed jsonb[] := '{}';
begin
    for st in select * from jsonb_array_elements(stmts) loop
        begin
            if st ? 'type' then
                execute st->>'type' into typ;
                if typ is null then
                    raise exception 'its type was not found';
                end if;
                execute format(st->>'sql', typ);
            else
                execute st->>'sql';
            end if;
        exception when others then
            failed := failed || jsonb_build_object('id', st->'id', 'err', sqlerrm);
        end;
    end loop;
    return failed;
end
$f$`, `
create function pg_temp.pgy_probe(spec jsonb) returns jsonb language plpgsql as $f$
declare
    r jsonb;
    failure text;
    failed jsonb[] := '{}';
    relations jsonb[] := '{}';
    done int := 0;
begin
    for r in select * from jsonb_array_elements(spec->'relations') loop
        failure := null;
        begin
            execute format('create temporary table %I (like %s)', r->>'name', r->>'like');
        exception when others then
            failure := sqlerrm;
        end;
        if failure is not null then
            relations := relations || jsonb_build_object('err', failure);
            continue;
        end if;
        failed := failed || pg_temp.pgy_probe_run(r->'run');
        relations := relations || jsonb_build_object('defs',
            pg_temp.pgy_probe_defs(format('pg_temp.%I', r->>'name')::regclass, format('%I.%I', r->>'schema', r->>'name')));
        execute format('alter table pg_temp.%I rename to %I', r->>'name', 'pgy_probe_t' || done);
        done := done + 1;
    end loop;
    failed := failed || pg_temp.pgy_probe_run(spec->'run');
    return jsonb_build_object('errors', to_jsonb(failed), 'relations', to_jsonb(relations), 'defs', pg_temp.pgy_probe_globals());
end
$f$`}

// runExprProbe sends the whole probe in one pipelined round trip: a
// transaction that creates the probe's functions, calls pg_temp.pgy_probe
// and rolls back. When the server stops at an error the transaction is
// rolled back on its own.
func runExprProbe(ctx context.Context, pool *pgxpool.Pool, spec *probeSpec) (*probeReply, error) {
	payload, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	pg := conn.Conn().PgConn()
	defer func() {
		if pg.TxStatus() != 'I' && !pg.IsClosed() {
			_ = pg.Exec(context.WithoutCancel(ctx), "rollback").Close()
		}
	}()

	batch := &pgconn.Batch{}
	batch.ExecParams("begin", nil, nil, nil, nil)
	batch.ExecParams("set local jit = off", nil, nil, nil, nil)
	batch.ExecParams("set local check_function_bodies = off", nil, nil, nil, nil)
	for _, q := range exprProbeFuncs {
		batch.ExecParams(q, nil, nil, nil, nil)
	}
	call := 3 + len(exprProbeFuncs)
	batch.ExecParams("select pg_temp.pgy_probe($1::jsonb)::text", [][]byte{payload}, nil, nil, nil)
	batch.ExecParams("rollback", nil, nil, nil, nil)
	results, err := pg.ExecBatch(ctx, batch).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(results) <= call || len(results[call].Rows) != 1 || len(results[call].Rows[0]) != 1 {
		return nil, fmt.Errorf("unexpected reply")
	}
	reply := &probeReply{}
	if err := json.Unmarshal(results[call].Rows[0][0], reply); err != nil {
		return nil, err
	}
	return reply, nil
}

// ProbeExpressions lets PostgreSQL decide whether the declared definitions
// that don't match the live ones as text are the same definition spelled
// another way: column defaults, generated column expressions, check and
// exclusion constraints, index expressions and predicates, policies,
// triggers, view column defaults, domain defaults and checks, and the
// argument defaults of functions and procedures. PostgreSQL rewrites what
// it stores (a constant into its type's output form, a function-style cast
// into a cast), which no text comparison can follow for every type.
//
// In one transaction that is always rolled back, each affected table,
// view or materialized view is copied into a temporary table LIKE it,
// which takes an ACCESS SHARE lock and copies columns only; the declared
// definitions are attached to the copy, each in a subtransaction, and read
// back with the function Introspect reads the live definition with. A
// domain's are attached to a temporary domain over the same base type, and
// a routine's arguments to a temporary routine. The declared definitions
// are parsed on the session's own search_path, as the migration will parse
// them, and deparsed on the pinned one (see pinSearchPathQ).
//
// The results go to live.ExprProbes, which planning applies (see
// applyExprProbes). Nothing is sent to the server when every definition
// matches as text. A server that can't run the probe, such as a read-only
// standby or a role without the TEMPORARY privilege, leaves ExprProbes nil,
// and every definition is compared as text; the error is returned for the
// caller to report.
func ProbeExpressions(ctx context.Context, pool *pgxpool.Pool, live *Live, desired *schema.Database) error {
	if live == nil || desired == nil || pool == nil || live.Offline {
		return nil
	}
	plan := collectExprProbes(live, desired)
	if len(plan.requests) == 0 {
		return nil
	}
	reply, err := runExprProbe(ctx, pool, &plan.spec)
	if err != nil {
		return fmt.Errorf("expression probe: %w", err)
	}
	live.ExprProbes = plan.results(reply)
	return nil
}
