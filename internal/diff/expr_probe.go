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
// text comparison takes for different from the live one, or for the same
// only by dropping a cast or a schema from one side (see
// ProbeExpressions).
type ExprProbe struct {
	// Declared is the definition that was probed. The result applies to
	// that definition only.
	Declared string
	// Live is the live definition it was compared with, laid out like
	// Parts. That the two differ applies to that live definition only, and
	// not, for instance, to a later step of a split plan, which sees the
	// declared definition in place.
	Live []string
	// Parts is the definition as PostgreSQL deparses it, with the function
	// Introspect reads the live one with: one element, except for an index
	// (its key columns, then its predicate) and a policy (USING, then WITH
	// CHECK).
	Parts []string
	// Err is why the definition couldn't be read back: PostgreSQL rejected
	// it, as it does one that uses a column or a function this plan
	// creates, or it names the probe's copy of its table (see
	// selfReference). "" when it was read back.
	Err string
}

// exprSlot is a declared definition together with the live one it is
// compared with.
type exprSlot interface {
	// equal is the text comparison's verdict.
	equal() bool
	// dropped tells, when equal holds, how what the text comparison
	// dropped from the declared definition differs from what it dropped
	// from the live one.
	dropped() exprDropDiff
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
	// differ marks the definition as changed in the live snapshot, for a
	// probe that read parts, laid out like live, back from the declared
	// definition.
	differ(e *liveEdit, parts []string)
}

// exprChanged marks a live definition that the probe read back different
// from the declared one although the text comparison takes the two for the
// same. It is an SQL comment, which no normalisation removes and
// PostgreSQL never prints, so every comparison of the edited snapshot finds
// the definition changed.
const exprChanged = " /*\u2260*/"

// markChanged returns def with exprChanged added before a trailing NOT
// VALID, which stays where the comparisons look for it.
func markChanged(def string) string {
	if liveNotValid(def) {
		t := strings.TrimSpace(def)
		return t[:len(t)-len(notValidSuffix)] + exprChanged + notValidSuffix
	}
	return def + exprChanged
}

// verbatim is embedded by the slots whose definition is compared exactly as
// it is read back.
type verbatim struct{}

func (verbatim) canonical(parts []string) []string { return parts }

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
	verbatim
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
	return &columnDefaultSlot{fq: fq, cn: cn, c: c, lc: lc}
}

func (s *columnDefaultSlot) equal() bool      { return defaultsEqual(s.c.Default, s.lc.Default) }
func (s *columnDefaultSlot) declared() string { return s.c.Default }
func (s *columnDefaultSlot) live() []string   { return []string{s.lc.Default} }
func (s *columnDefaultSlot) attach(target string, id int) probeAttach {
	return attachDefault(target, id, s.cn, s.c.Default)
}
func (s *columnDefaultSlot) spell(e *liveEdit) {
	if lc := e.table(s.fq).Columns[s.cn]; lc != nil {
		lc.Default = s.c.Default
	}
}
func (s *columnDefaultSlot) dropped() exprDropDiff { return defaultsDropped(s.c.Default, s.lc.Default) }
func (s *columnDefaultSlot) differ(e *liveEdit, _ []string) {
	if lc := e.table(s.fq).Columns[s.cn]; lc != nil {
		lc.Default = markChanged(lc.Default)
	}
}

func attachDefault(target string, id int, column, expr string) probeAttach {
	return probeAttach{
		stmt: probeStmt{ID: id, SQL: fmt.Sprintf("alter table %s alter column %s set default %s", pqIdent(target), quoteIdent(column), expr)},
		kind: "default", name: column,
	}
}

type generatedSlot struct {
	verbatim
	fq, cn string
	c      *schema.Column
	lc     *LiveColumn
}

func newGeneratedSlot(fq, cn string, c *schema.Column, lc *LiveColumn) exprSlot {
	if c == nil || lc == nil || c.Generated == "" || lc.Generated == "" {
		return nil
	}
	return &generatedSlot{fq: fq, cn: cn, c: c, lc: lc}
}

func (s *generatedSlot) equal() bool      { return constraintDefsEqual(s.c.Generated, s.lc.Generated) }
func (s *generatedSlot) declared() string { return s.c.Generated }
func (s *generatedSlot) live() []string   { return []string{s.lc.Generated} }
func (s *generatedSlot) attach(target string, id int) probeAttach {
	name := fmt.Sprintf("pgy_probe_g%d", id)
	sql := fmt.Sprintf("alter table %s add column %s \x00 generated always as (%s) stored", pqIdent(target), quoteIdent(name), s.c.Generated)
	sql = strings.Replace(strings.ReplaceAll(sql, "%", "%%"), "\x00", "%s", 1)
	typeQ := fmt.Sprintf("select format_type(a.atttypid, a.atttypmod) from pg_attribute a where a.attrelid = %s::regclass and a.attname = %s and a.attnum > 0 and not a.attisdropped",
		quoteString(pqIdent(target)), quoteString(s.cn))
	return probeAttach{stmt: probeStmt{ID: id, SQL: sql, Type: typeQ}, kind: "default", name: name}
}
func (s *generatedSlot) spell(e *liveEdit) {
	if lc := e.table(s.fq).Columns[s.cn]; lc != nil {
		lc.Generated = s.c.Generated
	}
}
func (s *generatedSlot) dropped() exprDropDiff {
	return constraintDefsDropped(s.c.Generated, s.lc.Generated)
}
func (s *generatedSlot) differ(e *liveEdit, _ []string) {
	if lc := e.table(s.fq).Columns[s.cn]; lc != nil {
		lc.Generated = markChanged(lc.Generated)
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
func (s *constraintSlot) dropped() exprDropDiff { return constraintDropped(s.lt, s.name, s.ct) }
func (s *constraintSlot) differ(e *liveEdit, _ []string) {
	t := e.table(s.fq)
	t.ConstraintDefs[s.name] = markChanged(t.ConstraintDefs[s.name])
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
	verbatim
	fq, name string
	matView  bool
	ix       *schema.Index
	cols     map[string]*schema.Column
	li       *LiveIndex
}

// newIndexSlot returns the slot of an index whose key expressions or
// predicate PostgreSQL may print another way than the YAML writes them: an
// index with the same number of keys live, a predicate on both sides or on
// neither, and at least one expression, on either side.
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
	for i, c := range ix.Columns {
		base, _ := splitIndexOrdering(c)
		expression = expression || isIndexExpression(base) && cols[base] == nil || !quotedIdentRe.MatchString(li.Keys[i]) && isIndexExpression(li.Keys[i])
	}
	if !expression {
		return nil
	}
	return &indexSlot{fq: fq, name: name, matView: matView, ix: ix, cols: cols, li: li}
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
func (s *indexSlot) spell(e *liveEdit) {
	relation := e.table
	if s.matView {
		relation = e.matView
	}
	if li := relation(s.fq).IndexDefs[s.name]; li != nil {
		li.Keys, li.Predicate = s.keys(), s.ix.Where
	}
}
func (s *indexSlot) dropped() exprDropDiff {
	out := exprDropDiff{}
	for i, base := range s.keys() {
		out.add(constraintDefsDropped(base, s.li.Keys[i]))
	}
	out.add(constraintDefsDropped(s.ix.Where, s.li.Predicate))
	return out
}
func (s *indexSlot) differ(e *liveEdit, parts []string) {
	relation := e.table
	if s.matView {
		relation = e.matView
	}
	li := relation(s.fq).IndexDefs[s.name]
	if li == nil {
		return
	}
	li.Keys = slices.Clone(li.Keys)
	live, marked := s.live(), false
	for i := range live {
		if i < len(parts) && parts[i] == live[i] {
			continue
		}
		if i < len(li.Keys) {
			li.Keys[i] += exprChanged
		} else {
			li.Predicate += exprChanged
		}
		marked = true
	}
	if !marked && len(li.Keys) > 0 {
		li.Keys[0] += exprChanged
	}
}

type policySlot struct {
	verbatim
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
	return &policySlot{fq: fq, name: name, pol: pol, lp: lp}
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
func (s *policySlot) spell(e *liveEdit) {
	if lp := e.table(s.fq).Policies[s.name]; lp != nil {
		lp.Using, lp.WithCheck = s.pol.Using, s.pol.WithCheck
	}
}
func (s *policySlot) dropped() exprDropDiff {
	out := constraintDefsDropped(s.pol.Using, s.lp.Using)
	out.add(constraintDefsDropped(s.pol.WithCheck, s.lp.WithCheck))
	return out
}
func (s *policySlot) differ(e *liveEdit, parts []string) {
	lp := e.table(s.fq).Policies[s.name]
	if lp == nil {
		return
	}
	if len(parts) == 2 && parts[0] == s.lp.Using {
		lp.WithCheck += exprChanged
		return
	}
	lp.Using += exprChanged
}

type triggerSlot struct {
	verbatim
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
	return &triggerSlot{fq: fq, name: name, tr: tr, lt: lt}
}

func (s *triggerSlot) equal() bool      { return triggerDefsEqual(s.declared(), s.lt.Triggers[s.name]) }
func (s *triggerSlot) declared() string { return renderTrigger(s.fq, s.tr) }
func (s *triggerSlot) live() []string   { return []string{s.lt.Triggers[s.name]} }
func (s *triggerSlot) attach(target string, id int) probeAttach {
	return probeAttach{stmt: probeStmt{ID: id, SQL: renderTrigger(target, s.tr)}, kind: "trigger", name: s.name}
}
func (s *triggerSlot) spell(e *liveEdit) { e.table(s.fq).Triggers[s.name] = s.declared() }
func (s *triggerSlot) dropped() exprDropDiff {
	return triggerDefsDropped(s.declared(), s.lt.Triggers[s.name])
}
func (s *triggerSlot) differ(e *liveEdit, _ []string) {
	t := e.table(s.fq)
	def := t.Triggers[s.name]
	if head, _, ok := splitTriggerCall(def); ok {
		t.Triggers[s.name] = head + strings.TrimSpace(exprChanged) + " " + def[len(head):]
		return
	}
	t.Triggers[s.name] = def + exprChanged
}

type viewDefaultSlot struct {
	verbatim
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
	return &viewDefaultSlot{fq: fq, col: col, want: want, have: have}
}

func (s *viewDefaultSlot) equal() bool      { return defaultsEqual(s.want, s.have) }
func (s *viewDefaultSlot) declared() string { return s.want }
func (s *viewDefaultSlot) live() []string   { return []string{s.have} }
func (s *viewDefaultSlot) attach(target string, id int) probeAttach {
	return attachDefault(target, id, s.col, s.want)
}
func (s *viewDefaultSlot) spell(e *liveEdit)     { e.viewDefaults(s.fq)[s.col] = s.want }
func (s *viewDefaultSlot) dropped() exprDropDiff { return defaultsDropped(s.want, s.have) }
func (s *viewDefaultSlot) differ(e *liveEdit, _ []string) {
	e.viewDefaults(s.fq)[s.col] += exprChanged
}

type domainDefaultSlot struct {
	verbatim
	fq string
	dm *schema.Domain
	ld *LiveDomain
}

func newDomainDefaultSlot(fq string, dm *schema.Domain, ld *LiveDomain) exprSlot {
	if dm == nil || ld == nil || dm.Default == "" || ld.Default == "" {
		return nil
	}
	return &domainDefaultSlot{fq: fq, dm: dm, ld: ld}
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
func (s *domainDefaultSlot) spell(e *liveEdit) { e.domain(s.fq).Default = s.dm.Default }
func (s *domainDefaultSlot) dropped() exprDropDiff {
	return defaultsDropped(s.dm.Default, s.ld.Default)
}
func (s *domainDefaultSlot) differ(e *liveEdit, _ []string) { e.domain(s.fq).Default += exprChanged }

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
func (s *domainCheckSlot) dropped() exprDropDiff {
	return constraintDefsDropped(s.declared(), s.ld.Checks[s.name])
}
func (s *domainCheckSlot) differ(e *liveEdit, _ []string) {
	d := e.domain(s.fq)
	d.Checks[s.name] = markChanged(d.Checks[s.name])
}

type routineArgsSlot struct {
	verbatim
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
func (s *routineArgsSlot) dropped() exprDropDiff {
	out := exprDropDiff{}
	d, l := argDefaults(s.args), argDefaults(s.liveArgs)
	if len(d) != len(l) {
		return out
	}
	for i := range d {
		if d[i] != "" && l[i] != "" && d[i] != l[i] {
			out.add(defaultsDropped(d[i], l[i]))
		}
	}
	return out
}

// differ marks every live argument default, which makes the plan replace
// the routine.
func (s *routineArgsSlot) differ(e *liveEdit, _ []string) {
	args := splitTopLevelQuotedParens(s.liveArgs)
	for i, a := range args {
		if _, def := splitArgDefault(a); def != "" {
			args[i] = strings.TrimRight(a, " \t\r\n") + exprChanged
		}
	}
	if s.procedure {
		e.procedure(s.norm).Args = strings.Join(args, ",")
	} else {
		e.function(s.norm).Args = strings.Join(args, ",")
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

func (e *liveEdit) compositeAttributes(fq string) map[string]string {
	return editable(e, "composite", fq, &e.live().CompositeAttributes, func(m map[string]string) map[string]string { return cloneMap(m, nil) })
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

// applyExprProbes returns live with the server's verdict on each definition
// of live.ExprProbes. Where the probe deparsed the declared definition to
// exactly the live text, the live definition takes the declared spelling,
// so the comparisons of the plan find the two equal. Where it deparsed it to
// something else although the text comparison takes the two for the same,
// the live definition is marked changed (see exprChanged), so they find
// the two different. A definition the probe couldn't attach or didn't
// cover keeps its live text, and is compared as text. The declared types
// of live.TypeProbes are applied the same way (see applyTypeProbes). live
// itself is not modified.
func applyExprProbes(live *Live, desired *schema.Database) *Live {
	return applyExprProbesAs(live, desired, false)
}

// simulateExprProbes is applyExprProbes for Simulate, whose snapshot has
// the declared definitions in place: one the probe found different from
// the live one takes the declared spelling as well, since the plan that
// Simulate stands for replaced it.
func simulateExprProbes(live *Live, desired *schema.Database) *Live {
	return applyExprProbesAs(live, desired, true)
}

func applyExprProbesAs(live *Live, desired *schema.Database, simulated bool) *Live {
	if live == nil || desired == nil || len(live.ExprProbes)+len(live.TypeProbes) == 0 {
		return live
	}
	e := &liveEdit{src: live}
	applyTypeProbes(e, live, desired)
	for key, p := range live.ExprProbes {
		if p == nil || p.Err != "" {
			continue
		}
		s := resolveExprSlot(live, desired, key)
		if s == nil || s.declared() != p.Declared {
			continue
		}
		switch parts, live := s.canonical(p.Parts), s.live(); {
		case slices.Equal(live, parts):
			s.spell(e)
		case slices.Equal(live, p.Live) && s.equal():
			if simulated {
				s.spell(e)
			} else {
				s.differ(e, parts)
			}
		}
	}
	return e.result()
}

// probeRelation is a table, view or materialized view whose definitions
// are probed on a temporary table LIKE it. The copy is created under the
// relation's own name, so that the definitions can name it, and renamed to
// Temp before they are read back, so that it hides no object of that name
// from the deparser.
type probeRelation struct {
	Schema string      `json:"schema"`
	Name   string      `json:"name"`
	Like   string      `json:"like"`
	Temp   string      `json:"temp"`
	Run    []probeStmt `json:"run"`
}

// selfReference is the error of a definition that still names the probe's
// copy after it was read back: it reads its own table through a name
// without a schema, and what that name finds depends on the search_path of
// the session that runs the migration.
const selfReference = "it reads its own table through a name without a schema"

// probeSpec is the input of pg_temp.pgy_probe: the declared types to
// canonicalise, the relations to copy with the statements to run on each
// copy, and the statements for domains and routines.
type probeSpec struct {
	Types     []string         `json:"types,omitempty"`
	Relations []*probeRelation `json:"relations"`
	Run       []probeStmt      `json:"run"`
}

// probeDef is one definition pg_temp.pgy_probe read back.
type probeDef struct {
	Kind  string   `json:"kind"`
	Name  string   `json:"name"`
	Parts []string `json:"parts"`
}

// probeFailure is the error of the statement with the given ID.
type probeFailure struct {
	ID  int    `json:"id"`
	Err string `json:"err"`
}

// probeRelationReply is what pg_temp.pgy_probe read back from the copy of
// one relation, or why it couldn't copy it.
type probeRelationReply struct {
	Err  string     `json:"err"`
	Defs []probeDef `json:"defs"`
}

// probeReply is the output of pg_temp.pgy_probe. Relations lines up with
// probeSpec.Relations.
type probeReply struct {
	Errors    []probeFailure       `json:"errors"`
	Relations []probeRelationReply `json:"relations"`
	Defs      []probeDef           `json:"defs"`
	Types     []probeType          `json:"types"`
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
	// all probes every definition considered.
	all bool
	// pending holds the definitions the text comparison takes for the
	// same as the live ones only by dropping the schema of a name from one
	// side, which matters only when pg_catalog has an object of the name
	// too (see resolve).
	pending []pendingProbe
}

// pendingProbe is a definition that waits on names, and the function that
// adds it.
type pendingProbe struct {
	names []string
	add   func()
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

// probeTarget is a relation whose definitions are probed: fq is its
// declared name and source the name it has in the database. For a table,
// table and live are its declaration and its live columns, which tell the
// columns the plan renames.
type probeTarget struct {
	fq, source string
	table      *schema.Table
	live       *LiveTable
}

// columnRenames returns the statements that give the copy of t the names
// the plan's column renames lead to (see renamer.tableParts), so that the
// declared definitions find the columns they name.
func (t probeTarget) columnRenames(target string) []probeStmt {
	out := []probeStmt{}
	if t.table == nil || t.live == nil {
		return out
	}
	has := map[string]bool{}
	for cn := range t.live.Columns {
		has[cn] = true
	}
	for _, cn := range sortedKeys(t.table.Columns) {
		c := t.table.Columns[cn]
		if c == nil || c.RenamedFrom == "" || !has[c.RenamedFrom] || has[cn] {
			continue
		}
		delete(has, c.RenamedFrom)
		has[cn] = true
		out = append(out, probeStmt{ID: -1, SQL: fmt.Sprintf("alter table %s rename column %s to %s", pqIdent(target), quoteIdent(c.RenamedFrom), quoteIdent(cn))})
	}
	return out
}

// consider adds a definition with add when the text comparison takes it
// for different from the live one, when it takes the two for the same only
// by dropping a cast from one side (see exprDropDiff.casts), or when every
// definition is probed. One it takes for the same only by dropping a schema
// from one side waits in pending.
func (p *exprProbePlan) consider(slot exprSlot, add func()) {
	if p.all || !slot.equal() {
		add()
		return
	}
	switch d := slot.dropped(); {
	case d.casts:
		add()
	case len(d.names) > 0:
		p.pending = append(p.pending, pendingProbe{names: d.names, add: add})
	}
}

// pendingNames returns the names the pending definitions wait on, sorted.
func (p *exprProbePlan) pendingNames() []string {
	out := []string{}
	for _, q := range p.pending {
		out = append(out, q.names...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// resolve adds the pending definitions that wait on a name in shared, and
// drops the others.
func (p *exprProbePlan) resolve(shared map[string]bool) {
	for _, q := range p.pending {
		if slices.ContainsFunc(q.names, func(n string) bool { return shared[n] }) {
			q.add()
		}
	}
	p.pending = nil
}

// relation considers a definition of relation t.
func (p *exprProbePlan) relation(t probeTarget, key ExprKey, slot exprSlot) {
	if slot != nil {
		p.consider(slot, func() { p.addRelation(t, key, slot) })
	}
}

func (p *exprProbePlan) addRelation(t probeTarget, key ExprKey, slot exprSlot) {
	sch, name := splitQualified(t.fq)
	target := "pg_temp." + name
	i, ok := p.relations[t.fq]
	if !ok {
		i = len(p.spec.Relations)
		p.relations[t.fq] = i
		p.spec.Relations = append(p.spec.Relations, &probeRelation{Schema: sch, Name: name, Like: pqIdent(t.source), Temp: fmt.Sprintf("pgy_probe_t%d", i), Run: t.columnRenames(target)})
	}
	p.add(key, slot, i, target)
}

// domain considers a definition of the domain declared as fq, which is
// source in the database. The first one added creates the probe's domain
// over the same base type.
func (p *exprProbePlan) domain(fq, source string, key ExprKey, slot exprSlot) {
	if slot != nil {
		p.consider(slot, func() { p.addDomain(fq, source, key, slot) })
	}
}

func (p *exprProbePlan) addDomain(fq, source string, key ExprKey, slot exprSlot) {
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

// routine considers the arguments of a function or procedure, which are
// probed on a temporary routine of their own.
func (p *exprProbePlan) routine(key ExprKey, slot exprSlot) {
	if slot != nil {
		p.consider(slot, func() { p.add(key, slot, -1, "") })
	}
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
// counterpart the text comparison takes for different, or for the same
// only by dropping something from one side (see exprProbePlan.consider),
// or, with all, every declared definition that has a live counterpart, and
// the declared types the text comparison takes for different from the live
// ones (see typesToProbe). live is the snapshot as introspected.
func collectExprProbes(live *Live, desired *schema.Database, all bool) *exprProbePlan {
	p := &exprProbePlan{relations: map[string]int{}, domains: map[string]string{}, all: all}
	p.spec.Relations, p.spec.Run = []*probeRelation{}, []probeStmt{}
	scoped := live.scoped()
	renamed, _ := applyRenames(scoped, desired)
	p.spec.Types = typesToProbe(renamed, desired)

	for _, fq := range sortedKeys(desired.Tables) {
		dt, lt := desired.Tables[fq], renamed.Tables[fq]
		if dt == nil || lt == nil || foreignKindMismatch(dt, lt) {
			continue
		}
		source, ok := renameSourceOf(fq, dt.RenamedFrom, func(k string) bool { return scoped.Tables[k] != nil })
		if !ok {
			continue
		}
		t := probeTarget{fq: fq, source: source, table: dt, live: scoped.Tables[source]}
		for _, cn := range sortedKeys(dt.Columns) {
			c, lc := dt.Columns[cn], lt.Columns[cn]
			p.relation(t, ExprKey{ExprDefault, fq, cn}, newColumnDefaultSlot(fq, cn, c, lc))
			p.relation(t, ExprKey{ExprGenerated, fq, cn}, newGeneratedSlot(fq, cn, c, lc))
		}
		for _, ct := range dt.Constraints {
			if ct != nil {
				p.relation(t, ExprKey{ExprConstraint, fq, truncateIdent(ct.Name)}, newConstraintSlot(fq, ct, lt))
			}
		}
		for _, ix := range dt.Indexes {
			if ix != nil && len(ix.Columns) > 0 {
				p.relation(t, ExprKey{ExprIndex, fq, LiveIndexName(fq, ix, lt.Indexes)}, newIndexSlot(fq, false, ix, dt.Columns, lt))
			}
		}
		for _, pol := range dt.Policies {
			if pol != nil {
				p.relation(t, ExprKey{ExprPolicy, fq, truncateIdent(pol.Name)}, newPolicySlot(fq, pol, lt))
			}
		}
		for _, tr := range dt.Triggers {
			if tr != nil {
				p.relation(t, ExprKey{ExprTrigger, fq, truncateIdent(tr.Name)}, newTriggerSlot(fq, tr, lt))
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
					p.relation(probeTarget{fq: fq, source: source}, ExprKey{ExprIndex, fq, LiveIndexName(fq, ix, lt.Indexes)}, newIndexSlot(fq, true, ix, nil, lt))
				}
			}
			continue
		}
		if len(vw.ColumnDefaults) == 0 || !scoped.Views[fq] {
			continue
		}
		for _, col := range sortedKeys(vw.ColumnDefaults) {
			p.relation(probeTarget{fq: fq, source: fq}, ExprKey{ExprViewDefault, fq, col}, newViewDefaultSlot(renamed, fq, col, vw))
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
		probe := &ExprProbe{Declared: r.slot.declared(), Live: r.slot.live()}
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
		if r.relation >= 0 && slices.ContainsFunc(parts, func(part string) bool { return strings.Contains(part, p.spec.Relations[r.relation].Temp) }) {
			probe.Err = selfReference
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
// The deparser names the table a definition is on in two places: a
// trigger's ON clause, where shown replaces it with the name the live
// definition has, and the qualifier of a column or whole-row reference,
// where own, the quoted name of the live table, replaces tmp.
//
// pgy_probe_run executes statements on the session's own search_path,
// which is how the migration will run them, each in a subtransaction, and
// returns the errors.
//
// pgy_probe canonicalises the declared types first (see typeProbeFuncs),
// before any temporary table could hide a type of its name. It then copies
// each relation into a temporary table of the same name and runs its
// statements. It renames the copy afterwards, which is cheaper than
// dropping it: a temporary table hides the tables and types of its name in
// other schemas, from the deparser and from the next relation's
// definitions.
var exprProbeFuncs = []string{`
create function pg_temp.pgy_probe_defs(rel oid, tmp text, own text, shown text) returns jsonb language sql stable set search_path = public as $f$
    select coalesce(jsonb_agg(jsonb_build_object('kind', d.kind, 'name', d.name, 'parts', d.parts)), '[]'::jsonb)
    from (
        select 'default' as kind, a.attname::text as name,
               jsonb_build_array(replace(pg_get_expr(ad.adbin, ad.adrelid), tmp || '.', own || '.')) as parts
        from pg_attrdef ad
        join pg_attribute a on a.attrelid = ad.adrelid and a.attnum = ad.adnum
        where ad.adrelid = rel
        union all
        select 'constraint', con.conname::text, jsonb_build_array(replace(pg_get_constraintdef(con.oid), tmp || '.', own || '.'))
        from pg_constraint con
        where con.conrelid = rel and con.contype in ('c', 'x')
        union all
        select 'index', i.relname::text,
               to_jsonb(array(select replace(case when x.indkey[k - 1] = 0 then pg_get_indexdef(x.indexrelid, k, true)
                                                  else (select quote_ident(a.attname) from pg_attribute a where a.attrelid = x.indrelid and a.attnum = x.indkey[k - 1]) end,
                                             tmp || '.', own || '.')
                              from generate_series(1, x.indnkeyatts) k order by k)
                        || replace(coalesce(pg_get_expr(x.indpred, x.indrelid, true), ''), tmp || '.', own || '.'))
        from pg_index x
        join pg_class i on i.oid = x.indexrelid
        where x.indrelid = rel
        union all
        select 'policy', pol.polname::text,
               jsonb_build_array(replace(coalesce(pg_get_expr(pol.polqual, pol.polrelid), ''), tmp || '.', own || '.'),
                                 replace(coalesce(pg_get_expr(pol.polwithcheck, pol.polrelid), ''), tmp || '.', own || '.'))
        from pg_policy pol
        where pol.polrelid = rel
        union all
        select 'trigger', t.tgname::text,
               jsonb_build_array(replace(pg_get_triggerdef(t.oid), ' ON pg_temp.' || tmp || ' ', ' ON ' || shown || ' '))
        from pg_trigger t
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
    rel oid;
    failure text;
    failed jsonb[] := '{}';
    relations jsonb[] := '{}';
    types jsonb;
begin
    types := pg_temp.pgy_probe_types(coalesce(spec->'types', '[]'));
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
        rel := format('pg_temp.%I', r->>'name')::regclass;
        execute format('alter table pg_temp.%I rename to %I', r->>'name', r->>'temp');
        relations := relations || jsonb_build_object('defs',
            pg_temp.pgy_probe_defs(rel, r->>'temp', format('%I', r->>'name'), format('%I.%I', r->>'schema', r->>'name')));
    end loop;
    failed := failed || pg_temp.pgy_probe_run(spec->'run');
    return jsonb_build_object('errors', to_jsonb(failed), 'relations', to_jsonb(relations), 'defs', pg_temp.pgy_probe_globals(), 'types', types);
end
$f$`}

// catalogSharedNamesQ returns which of the names in $1 are the names of a
// function, an operator or a text search configuration both in pg_catalog
// and in another schema, where a schema qualifier decides which one an
// expression means.
const catalogSharedNamesQ = `
    select coalesce(array_agg(distinct c.name), '{}') from (
        select p.proname::text as name from pg_proc p join pg_proc o on o.proname = p.proname and o.pronamespace <> p.pronamespace
        where p.pronamespace = 'pg_catalog'::regnamespace and p.proname = any($1::text[])
        union all
        select p.oprname::text from pg_operator p join pg_operator o on o.oprname = p.oprname and o.oprnamespace <> p.oprnamespace
        where p.oprnamespace = 'pg_catalog'::regnamespace and p.oprname = any($1::text[])
        union all
        select p.cfgname::text from pg_ts_config p join pg_ts_config o on o.cfgname = p.cfgname and o.cfgnamespace <> p.cfgnamespace
        where p.cfgnamespace = 'pg_catalog'::regnamespace and p.cfgname = any($1::text[])
    ) c`

// catalogSharedNames returns which of names pg_catalog shares
// with another schema (see catalogSharedNamesQ).
func catalogSharedNames(ctx context.Context, pool *pgxpool.Pool, names []string) (map[string]bool, error) {
	var shared []string
	if err := pool.QueryRow(ctx, catalogSharedNamesQ, names).Scan(&shared); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(shared))
	for _, n := range shared {
		out[n] = true
	}
	return out, nil
}

// exprProbeWorkers is how many connections runExprProbes splits the
// relations across, and exprProbeChunk the fewest relations worth a
// connection of their own; copying one takes about a millisecond.
const (
	exprProbeWorkers = 4
	exprProbeChunk   = 50
)

// runExprProbes runs spec, on one connection, or with its relations split
// across several when there are many of them. The types and the statements
// for domains and routines stay with the first.
func runExprProbes(ctx context.Context, pool *pgxpool.Pool, spec *probeSpec) (*probeReply, error) {
	workers := min(exprProbeWorkers, (len(spec.Relations)+exprProbeChunk-1)/exprProbeChunk)
	if workers <= 1 {
		return runExprProbe(ctx, pool, spec)
	}
	specs, index := make([]*probeSpec, workers), make([][]int, workers)
	for w := range specs {
		specs[w] = &probeSpec{Relations: []*probeRelation{}, Run: []probeStmt{}}
	}
	specs[0].Types, specs[0].Run = spec.Types, spec.Run
	for i, r := range spec.Relations {
		w := i % workers
		specs[w].Relations, index[w] = append(specs[w].Relations, r), append(index[w], i)
	}
	type result struct {
		worker int
		reply  *probeReply
		err    error
	}
	results := make(chan result, workers)
	for w := range specs {
		go func(w int) {
			reply, err := runExprProbe(ctx, pool, specs[w])
			results <- result{w, reply, err}
		}(w)
	}
	merged := &probeReply{Relations: make([]probeRelationReply, len(spec.Relations))}
	var firstErr error
	for range specs {
		r := <-results
		if r.err == nil && len(r.reply.Relations) != len(index[r.worker]) {
			r.err = fmt.Errorf("unexpected reply")
		}
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		for j, rel := range r.reply.Relations {
			merged.Relations[index[r.worker][j]] = rel
		}
		merged.Errors, merged.Defs, merged.Types = append(merged.Errors, r.reply.Errors...), append(merged.Defs, r.reply.Defs...), append(merged.Types, r.reply.Types...)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return merged, nil
}

// runExprProbe sends a probe in one pipelined round trip: a transaction
// that creates the probe's functions, calls pg_temp.pgy_probe and rolls
// back. When the server stops at an error the transaction is rolled back
// on its own.
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
	funcs := append(slices.Clone(typeProbeFuncs), exprProbeFuncs...)
	for _, q := range funcs {
		batch.ExecParams(q, nil, nil, nil, nil)
	}
	call := 3 + len(funcs)
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
// It also lets PostgreSQL decide on the definitions that match as text
// only because the text comparison dropped something that can change their
// meaning from one side and not from the other (see exprDrops): a cast of
// something other than a constant (an index on email::text against one on
// the citext column email), or the schema of a function, operator or text
// search configuration that pg_catalog has too (public.gen_random_uuid()
// against core's gen_random_uuid()). For the second, one query asks the
// server which of the names pg_catalog shares with another schema. YAML
// spelled as pgy pull writes it drops the same from both sides, and costs
// nothing.
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
// In the same round trip, the declared types of columns, composite
// attributes and domains that differ from the live ones as text
// (geography(Point) against geography(Point,4326), numeric(10) against
// numeric(10,0), varchar( 5 ) against character varying(5)) are resolved
// on the session's search_path and printed as Introspect prints the live
// ones (see typeProbeFuncs).
//
// The results go to live.ExprProbes and live.TypeProbes, which planning
// applies (see applyExprProbes): a probe takes the statements for a
// definition or a type spelled another way out of the plan, and adds those
// for a definition that only looked the same. Nothing is sent to the
// server when every definition and type matches as text, dropping the same
// from both sides. A server that can't run the probe, such as a read-only
// standby or a role without the TEMPORARY privilege, leaves both nil, and
// everything is compared as text; the error is returned for the caller to
// report.
func ProbeExpressions(ctx context.Context, pool *pgxpool.Pool, live *Live, desired *schema.Database) error {
	return probeExpressions(ctx, pool, live, desired, false)
}

// ProbeEveryExpression is ProbeExpressions for every declared definition
// of the kinds it covers that has a live counterpart, also those that
// match as text. It catches what the text comparison hides without
// dropping a cast or a schema, such as other parentheses (a + b * c
// against (a + b) * c), at the cost of copying every table concerned.
func ProbeEveryExpression(ctx context.Context, pool *pgxpool.Pool, live *Live, desired *schema.Database) error {
	return probeExpressions(ctx, pool, live, desired, true)
}

func probeExpressions(ctx context.Context, pool *pgxpool.Pool, live *Live, desired *schema.Database, all bool) error {
	if live == nil || desired == nil || pool == nil || live.Offline {
		return nil
	}
	plan := collectExprProbes(live, desired, all)
	if len(plan.pending) > 0 {
		shared, err := catalogSharedNames(ctx, pool, plan.pendingNames())
		if err != nil {
			return fmt.Errorf("expression probe: %w", err)
		}
		plan.resolve(shared)
	}
	if len(plan.requests)+len(plan.spec.Types) == 0 {
		return nil
	}
	reply, err := runExprProbes(ctx, pool, &plan.spec)
	if err != nil {
		return fmt.Errorf("expression probe: %w", err)
	}
	if len(plan.requests) > 0 {
		live.ExprProbes = plan.results(reply)
	}
	if len(plan.spec.Types) > 0 {
		live.TypeProbes = typeResults(reply)
	}
	return nil
}
