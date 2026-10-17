package group

import (
    "sort"
    "strings"

    "github.com/suprbdev/pgy/internal/diff"
    "github.com/suprbdev/pgy/internal/schema"
)

const (
    kindTable     = "table"
    kindView      = "view"
    kindType      = "type"
    kindDomain    = "domain"
    kindSequence  = "sequence"
    kindFunction  = "function"
    kindProcedure = "procedure"
)

var (
    allKinds    = []string{kindTable, kindView, kindType, kindDomain, kindSequence, kindFunction, kindProcedure}
    typeKinds   = []string{kindType, kindDomain}
    callKinds    = []string{kindFunction}
    exprKinds    = []string{kindFunction, kindType, kindDomain}
    defaultKinds = []string{kindFunction, kindSequence, kindType, kindDomain}
    returnKinds  = []string{kindType, kindDomain, kindTable, kindView}
    policyKinds  = []string{kindTable, kindView, kindFunction, kindProcedure, kindType, kindDomain}
)

type ref struct {
    kind string
    key  string
}

type token struct {
    qual string
    name string
    lit  bool
}

func declared(d *schema.Database) []ref {
    if d == nil { return nil }
    var out []ref
    add := func(kind string, keys []string) {
        sort.Strings(keys)
        for _, k := range keys { out = append(out, ref{kind, k}) }
    }
    add(kindTable, nonNilKeys(d.Tables))
    add(kindView, nonNilKeys(d.Views))
    add(kindType, nonNilKeys(d.Types))
    add(kindDomain, nonNilKeys(d.Domains))
    add(kindSequence, nonNilKeys(d.Sequences))
    add(kindFunction, nonNilKeys(d.Functions))
    add(kindProcedure, nonNilKeys(d.Procedures))
    return out
}

func nonNilKeys[V comparable](m map[string]V) []string {
    var zero V
    out := make([]string, 0, len(m))
    for k, v := range m {
        if v != zero { out = append(out, k) }
    }
    return out
}

func isDeclared(d *schema.Database, r ref) bool {
    if d == nil { return false }
    switch r.kind {
    case kindTable:
        return d.Tables[r.key] != nil
    case kindView:
        return d.Views[r.key] != nil
    case kindType:
        return d.Types[r.key] != nil
    case kindDomain:
        return d.Domains[r.key] != nil
    case kindSequence:
        return d.Sequences[r.key] != nil
    case kindFunction:
        return d.Functions[r.key] != nil
    case kindProcedure:
        return d.Procedures[r.key] != nil
    }
    return false
}

func existsLive(l *diff.Live, r ref) bool {
    if l == nil { return false }
    switch r.kind {
    case kindTable:
        return l.Tables[r.key] != nil
    case kindView:
        return l.Views[r.key] || l.MatViews[r.key]
    case kindType:
        return l.Types[r.key]
    case kindDomain:
        return l.Domains[r.key]
    case kindSequence:
        return l.Sequences[r.key]
    case kindFunction:
        return hasSignaturePrefix(l.Functions, r.key)
    case kindProcedure:
        return hasSignaturePrefix(l.Procedures, r.key)
    }
    return false
}

func hasSignaturePrefix(m map[string]bool, key string) bool {
    prefix := strings.ToLower(key) + "("
    for sig, ok := range m {
        if ok && strings.HasPrefix(strings.ToLower(strings.ReplaceAll(sig, " ", "")), prefix) { return true }
    }
    return false
}

func qualifyPublic(name string) string {
    name = strings.TrimSpace(name)
    if name == "" || strings.Contains(name, ".") { return name }
    return "public." + name
}

func ownedByTable(s string) string {
    parts := strings.Split(strings.TrimSpace(s), ".")
    switch len(parts) {
    case 2:
        return "public." + parts[0]
    case 3:
        return parts[0] + "." + parts[1]
    }
    return ""
}

type index struct {
    full   *schema.Database
    byName map[string][]ref
}

func newIndex(full *schema.Database) *index {
    ix := &index{full: full, byName: map[string][]ref{}}
    for _, r := range declared(full) {
        name := strings.ToLower(r.key)
        if i := strings.LastIndexByte(name, '.'); i >= 0 { name = name[i+1:] }
        ix.byName[name] = append(ix.byName[name], r)
    }
    return ix
}

func (ix *index) resolveDeps(deps []string) []ref {
    var out []ref
    for _, raw := range deps {
        key := schema.ResolveDependency(raw, ix.full)
        if key == "" { continue }
        for _, kind := range allKinds {
            r := ref{kind, key}
            if !isDeclared(ix.full, r) { continue }
            if kind == kindFunction && strings.EqualFold(strings.TrimSpace(ix.full.Functions[key].Returns), "trigger") { continue }
            out = append(out, r)
        }
    }
    return out
}

func (ix *index) mentions(text string, kinds []string) []ref {
    var out []ref
    for _, t := range tokens(text) {
        out = append(out, ix.match(t, kinds)...)
    }
    return out
}

func (ix *index) match(t token, kinds []string) []ref {
    var out []ref
    for _, r := range ix.byName[t.name] {
        if t.lit && r.kind != kindSequence { continue }
        if !containsKind(kinds, r.kind) { continue }
        if t.qual != "" {
            schemaName := strings.ToLower(r.key)
            if i := strings.LastIndexByte(schemaName, '.'); i >= 0 { schemaName = schemaName[:i] }
            if schemaName != t.qual { continue }
        }
        out = append(out, r)
    }
    return out
}

func containsKind(kinds []string, kind string) bool {
    for _, k := range kinds {
        if k == kind { return true }
    }
    return false
}

func tokens(s string) []token {
    var out []token
    var scan func(s string, lit bool)
    scan = func(s string, lit bool) {
        last := ""
        dotted := false
        emit := func(name string) {
            t := token{name: name, lit: lit}
            if dotted { t.qual = last }
            out = append(out, t)
            last = name
            dotted = false
        }
        for i := 0; i < len(s); {
            c := s[i]
            switch {
            case c == '\'' && !lit:
                j := i + 1
                for j < len(s) {
                    if s[j] == '\'' {
                        if j+1 < len(s) && s[j+1] == '\'' {
                            j += 2
                            continue
                        }
                        break
                    }
                    j++
                }
                scan(s[i+1:min(j, len(s))], true)
                i = j + 1
                last, dotted = "", false
            case c == '"':
                j := strings.IndexByte(s[i+1:], '"')
                if j < 0 { j = len(s) - i - 1 }
                emit(strings.ToLower(s[i+1 : i+1+j]))
                i += j + 2
            case isIdentStart(c):
                j := i
                for j < len(s) && isIdentChar(s[j]) { j++ }
                emit(strings.ToLower(s[i:j]))
                i = j
            case c >= '0' && c <= '9':
                for i < len(s) && isIdentChar(s[i]) { i++ }
                last, dotted = "", false
            case c == '.':
                dotted = last != ""
                i++
            default:
                last, dotted = "", false
                i++
            }
        }
    }
    scan(s, false)
    return out
}

func isIdentStart(c byte) bool {
    return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentChar(c byte) bool {
    return isIdentStart(c) || c == '$' || (c >= '0' && c <= '9')
}

type availability struct {
    ix   *index
    cur  *schema.Database
    live *diff.Live
}

func (a *availability) pending(r ref) bool {
    return isDeclared(a.ix.full, r) && !isDeclared(a.cur, r) && !existsLive(a.live, r)
}

func (a *availability) pendingColumn(table, col string) bool {
    ft := a.ix.full.Tables[table]
    if ft == nil || ft.Columns[col] == nil { return false }
    if ct := a.cur.Tables[table]; ct != nil && ct.Columns[col] != nil { return false }
    if a.live != nil {
        if lt := a.live.Tables[table]; lt != nil && lt.Columns[col] != nil { return false }
    }
    return true
}

func (a *availability) pendingColumns(table string, cols []string) bool {
    for _, c := range cols {
        if a.pendingColumn(table, c) { return true }
    }
    return false
}

func (a *availability) blocked(text, table string, kinds []string) bool {
    for _, t := range tokens(text) {
        if !t.lit && a.pendingColumn(table, t.name) { return true }
        for _, r := range a.ix.match(t, kinds) {
            if a.pending(r) { return true }
        }
    }
    return false
}

func schemaOf(key string) string {
    if i := strings.IndexByte(key, '.'); i >= 0 { return key[:i] }
    return "public"
}

func (a *availability) deferUnready() {
    later := map[string]bool{}
    for _, r := range declared(a.ix.full) { later[schemaOf(r.key)] = true }
    for _, r := range declared(a.cur) { delete(later, schemaOf(r.key)) }
    delete(later, "public")
    if a.live != nil {
        for s, ok := range a.live.Schemas {
            if ok { delete(later, s) }
        }
    }
    for s := range later {
        delete(a.cur.SchemaGrants, s)
        delete(a.cur.SchemaComments, s)
    }

    for fq, t := range a.cur.Tables {
        if t == nil { continue }
        for _, c := range t.Columns {
            if c != nil && c.Default != "" && a.blocked(c.Default, fq, defaultKinds) {
                c.Default = ""
            }
        }
        if a.pendingColumns(fq, t.PrimaryKey) { t.PrimaryKey = nil }
        t.Indexes = keep(t.Indexes, func(ix *schema.Index) bool {
            return ix == nil || !a.blocked(strings.Join(ix.Columns, " ")+" "+ix.Where, fq, exprKinds)
        })
        t.Constraints = keep(t.Constraints, func(ct *schema.Constraint) bool {
            return ct == nil || !a.blocked(ct.Expression+" "+strings.Join(ct.Columns, " "), fq, exprKinds)
        })
        t.Triggers = keep(t.Triggers, func(tr *schema.Trigger) bool {
            return tr == nil || !a.blocked(tr.Procedure+" "+tr.When+" "+strings.Join(tr.Events, " "), fq, callKinds)
        })
        policies := len(t.Policies)
        t.Policies = keep(t.Policies, func(p *schema.Policy) bool {
            return p == nil || !a.blocked(p.Using+" "+p.WithCheck, fq, policyKinds)
        })
        if len(t.Policies) < policies && t.RowLevelSecurity != nil && *t.RowLevelSecurity {
            t.RowLevelSecurity = nil
        }
    }

    for fq, t := range a.cur.Tables {
        if t == nil { continue }
        t.ForeignKeys = keep(t.ForeignKeys, func(fk *schema.ForeignKey) bool {
            if fk == nil { return true }
            refTable := qualifyPublic(fk.RefTable)
            return !a.pendingColumns(fq, fk.Columns) &&
                !a.pending(ref{kindTable, refTable}) &&
                !a.pendingColumns(refTable, fk.RefColumns) &&
                !a.keyPending(refTable, fk.RefColumns)
        })
    }
}

func (a *availability) keyPending(table string, cols []string) bool {
    if len(cols) == 0 { return false }
    ft := a.ix.full.Tables[table]
    if ft == nil || !hasKey(ft, cols) { return false }
    if ct := a.cur.Tables[table]; ct != nil && hasKey(ct, cols) { return false }
    if a.live != nil {
        if lt := a.live.Tables[table]; lt != nil && liveHasKey(table, lt, ft, cols) { return false }
    }
    return true
}

func primaryKey(t *schema.Table) []string {
    if len(t.PrimaryKey) > 0 { return t.PrimaryKey }
    var pk []string
    for cn, c := range t.Columns {
        if c != nil && c.PrimaryKey { pk = append(pk, cn) }
    }
    return pk
}

func hasKey(t *schema.Table, cols []string) bool {
    if sameColumns(primaryKey(t), cols) { return true }
    if len(cols) == 1 {
        if c := t.Columns[cols[0]]; c != nil && c.Unique { return true }
    }
    for _, ct := range t.Constraints {
        if ct != nil && strings.EqualFold(ct.Type, "unique") && sameColumns(ct.Columns, cols) { return true }
    }
    for _, ix := range t.Indexes {
        if ix != nil && ix.Unique && ix.Where == "" && sameColumns(ix.Columns, cols) { return true }
    }
    return false
}

func liveHasKey(table string, lt *diff.LiveTable, ft *schema.Table, cols []string) bool {
    if lt.HasPK && sameColumns(primaryKey(ft), cols) { return true }
    if len(cols) == 1 && lt.Constraints[strings.ReplaceAll(table+"_"+cols[0], ".", "_")+"_key"] { return true }
    for _, ct := range ft.Constraints {
        if ct != nil && strings.EqualFold(ct.Type, "unique") && sameColumns(ct.Columns, cols) && lt.Constraints[ct.Name] { return true }
    }
    for _, ix := range ft.Indexes {
        if ix == nil || !ix.Unique || ix.Where != "" || !sameColumns(ix.Columns, cols) { continue }
        name := ix.Name
        if name == "" { name = strings.ReplaceAll(table+"_"+strings.Join(ix.Columns, "_"), ".", "_") + "_idx" }
        if lt.Indexes[name] { return true }
    }
    return false
}

func sameColumns(a, b []string) bool {
    if len(a) == 0 || len(a) != len(b) { return false }
    set := map[string]bool{}
    for _, c := range a { set[c] = true }
    for _, c := range b {
        if !set[c] { return false }
    }
    return true
}

func keep[T any](in []T, ok func(T) bool) []T {
    if in == nil { return nil }
    out := make([]T, 0, len(in))
    for _, v := range in {
        if ok(v) { out = append(out, v) }
    }
    return out
}
