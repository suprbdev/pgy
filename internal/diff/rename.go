package diff

import (
    "fmt"
    "regexp"
    "strconv"
    "strings"

    "github.com/suprbdev/pgy/internal/schema"
)

// applyRenames applies the renamedFrom declarations in desired to a copy of
// live and returns that copy with the statements performing them. An object
// is renamed only when its old name exists live and its new name does not,
// so a declaration that has already been applied is a no-op. References to a
// renamed object inside other live definitions (foreign keys, triggers,
// column and argument types, expressions naming a renamed column) are
// rewritten the way PostgreSQL rewrites them, so the rest of the plan sees
// the renamed object as unchanged. live itself is returned when nothing is
// renamed.
func applyRenames(live *Live, desired *schema.Database) (*Live, []string) {
    if live == nil || desired == nil || !hasRenames(desired) { return live, nil }
    r := &renamer{l: cloneLive(live)}
    r.relations(desired)
    r.types(desired)
    r.routines(desired)
    r.tableParts(desired)
    if len(r.stmts) == 0 { return live, nil }
    return r.l, r.stmts
}

type renamer struct {
    l     *Live
    stmts []string
}

func hasRenames(d *schema.Database) bool {
    for _, t := range d.Tables {
        if t == nil { continue }
        if t.RenamedFrom != "" { return true }
        for _, c := range t.Columns {
            if c != nil && c.RenamedFrom != "" { return true }
        }
        for _, ix := range t.Indexes {
            if ix != nil && ix.RenamedFrom != "" { return true }
        }
        for _, c := range t.Constraints {
            if c != nil && c.RenamedFrom != "" { return true }
        }
        for _, fk := range t.ForeignKeys {
            if fk != nil && fk.RenamedFrom != "" { return true }
        }
    }
    for _, v := range d.Views {
        if v == nil { continue }
        if v.RenamedFrom != "" { return true }
        for _, ix := range v.Indexes {
            if ix != nil && ix.RenamedFrom != "" { return true }
        }
    }
    for _, s := range d.Sequences {
        if s != nil && s.RenamedFrom != "" { return true }
    }
    for _, td := range d.Types {
        if td != nil && (td.RenamedFrom != "" || len(td.RenamedLabels) > 0) { return true }
    }
    for _, dm := range d.Domains {
        if dm != nil && dm.RenamedFrom != "" { return true }
    }
    for _, f := range d.Functions {
        if f != nil && f.RenamedFrom != "" { return true }
    }
    for _, p := range d.Procedures {
        if p != nil && p.RenamedFrom != "" { return true }
    }
    return false
}

// renameSource qualifies a renamedFrom value with the schema of the object
// it belongs to, unless it is already qualified.
func renameSource(key, from string) string {
    if strings.Contains(from, ".") { return from }
    return key[:strings.IndexByte(key, '.')] + "." + from
}

func splitQualified(fq string) (string, string) {
    i := strings.IndexByte(fq, '.')
    return fq[:i], fq[i+1:]
}

// relocateStmts moves kind old to new: SET SCHEMA when the schema differs,
// then RENAME TO when the name differs. args is the argument list for
// routines and "" otherwise.
func relocateStmts(kind, old, new, args string) []string {
    oldSchema, oldName := splitQualified(old)
    newSchema, newName := splitQualified(new)
    out := []string{}
    cur := old
    if oldSchema != newSchema {
        out = append(out, fmt.Sprintf("alter %s %s%s set schema %s;", kind, pqIdent(old), args, pqIdent(newSchema)))
        cur = newSchema + "." + oldName
    }
    if oldName != newName {
        out = append(out, fmt.Sprintf("alter %s %s%s rename to %s;", kind, pqIdent(cur), args, pqIdent(newName)))
    }
    return out
}

func moveKey[V any](m map[string]V, old, new string) {
    if v, ok := m[old]; ok {
        delete(m, old)
        m[new] = v
    }
}

func (r *renamer) allTables() []*LiveTable {
    out := []*LiveTable{}
    for _, m := range []map[string]*LiveTable{r.l.Tables, r.l.ViewTables, r.l.MatViewTables} {
        for _, t := range m {
            if t != nil { out = append(out, t) }
        }
    }
    return out
}

func (r *renamer) relations(d *schema.Database) {
    l := r.l
    for _, k := range sortedKeys(d.Tables) {
        t := d.Tables[k]
        if t == nil || t.RenamedFrom == "" { continue }
        old := renameSource(k, t.RenamedFrom)
        if l.Tables[old] == nil || l.Tables[k] != nil { continue }
        r.stmts = append(r.stmts, relocateStmts("table", old, k, "")...)
        r.moveRelation(old, k)
    }
    for _, k := range sortedKeys(d.Views) {
        vw := d.Views[k]
        if vw == nil || vw.RenamedFrom == "" { continue }
        old := renameSource(k, vw.RenamedFrom)
        kind, set := "view", l.Views
        if vw.Materialized { kind, set = "materialized view", l.MatViews }
        if !set[old] || set[k] { continue }
        r.stmts = append(r.stmts, relocateStmts(kind, old, k, "")...)
        r.moveRelation(old, k)
    }
    for _, k := range sortedKeys(d.Sequences) {
        sq := d.Sequences[k]
        if sq == nil || sq.RenamedFrom == "" { continue }
        old := renameSource(k, sq.RenamedFrom)
        if !l.Sequences[old] || l.Sequences[k] { continue }
        r.stmts = append(r.stmts, relocateStmts("sequence", old, k, "")...)
        r.moveRelation(old, k)
    }
}

func (r *renamer) moveRelation(old, new string) {
    l := r.l
    moveKey(l.Tables, old, new)
    moveKey(l.Views, old, new)
    moveKey(l.MatViews, old, new)
    moveKey(l.ViewOptions, old, new)
    moveKey(l.ViewColumnComments, old, new)
    moveKey(l.MatViewTables, old, new)
    moveKey(l.ViewTables, old, new)
    moveKey(l.Sequences, old, new)
    moveKey(l.SequenceDefs, old, new)
    moveKey(l.OwnedSequences, old, new)
    moveKey(l.TableGrants, old, new)
    moveKey(l.ColumnGrants, old, new)
    moveKey(l.RelComments, old, new)
    moveKey(l.Owners, "rel:"+old, "rel:"+new)
    for k, ls := range l.SequenceDefs {
        if ls != nil && strings.HasPrefix(ls.Owner, old+".") {
            cp := *ls
            cp.Owner = new + ls.Owner[len(old):]
            l.SequenceDefs[k] = &cp
        }
    }
    fk := regexp.MustCompile(`REFERENCES ` + relationPattern(old) + `\(`)
    on := regexp.MustCompile(` ON ` + relationPattern(old) + ` `)
    for _, t := range r.allTables() {
        for n, def := range t.ConstraintDefs { t.ConstraintDefs[n] = fk.ReplaceAllLiteralString(def, "REFERENCES "+printedRelation(new)+"(") }
        for n, def := range t.Triggers { t.Triggers[n] = on.ReplaceAllLiteralString(def, " ON "+printedRelation(new)+" ") }
    }
}

func (r *renamer) types(d *schema.Database) {
    l := r.l
    for _, k := range sortedKeys(d.Types) {
        td := d.Types[k]
        if td == nil || td.RenamedFrom == "" { continue }
        old := renameSource(k, td.RenamedFrom)
        if !l.Types[old] || l.Types[k] { continue }
        r.stmts = append(r.stmts, relocateStmts("type", old, k, "")...)
        r.moveType(old, k)
    }
    for _, k := range sortedKeys(d.Domains) {
        dm := d.Domains[k]
        if dm == nil || dm.RenamedFrom == "" { continue }
        old := renameSource(k, dm.RenamedFrom)
        if !l.Domains[old] || l.Domains[k] { continue }
        r.stmts = append(r.stmts, relocateStmts("domain", old, k, "")...)
        r.moveType(old, k)
    }
    for _, k := range sortedKeys(d.Types) {
        td := d.Types[k]
        if td == nil || len(td.RenamedLabels) == 0 { continue }
        labels, ok := l.EnumLabels[k]
        if !ok { continue }
        labels = append([]string(nil), labels...)
        for _, label := range sortedKeys(td.RenamedLabels) {
            i, j := indexOf(labels, td.RenamedLabels[label]), indexOf(labels, label)
            if i < 0 || j >= 0 { continue }
            r.stmts = append(r.stmts, fmt.Sprintf("alter type %s rename value %s to %s;", pqIdent(k), quoteString(labels[i]), quoteString(label)))
            labels[i] = label
        }
        l.EnumLabels[k] = labels
    }
}

func indexOf(list []string, s string) int {
    for i, v := range list {
        if v == s { return i }
    }
    return -1
}

func (r *renamer) moveType(old, new string) {
    l := r.l
    moveKey(l.Types, old, new)
    moveKey(l.Domains, old, new)
    moveKey(l.DomainDefs, old, new)
    moveKey(l.EnumLabels, old, new)
    moveKey(l.CompositeAttributes, old, new)
    moveKey(l.TypeGrants, old, new)
    moveKey(l.TypeComments, old, new)
    moveKey(l.Owners, "type:"+old, "type:"+new)
    whole := regexp.MustCompile(`^` + relationPattern(old) + `((?:\[\])*)$`)
    retype := func(s string) string {
        if sm := whole.FindStringSubmatch(s); sm != nil { return printedRelation(new) + sm[1] }
        return s
    }
    for _, t := range r.allTables() {
        for _, lc := range t.Columns {
            if lc != nil { lc.Type = retype(lc.Type) }
        }
    }
    for k, attrs := range l.CompositeAttributes {
        cp := make(map[string]string, len(attrs))
        for a, typ := range attrs { cp[a] = retype(typ) }
        l.CompositeAttributes[k] = cp
    }
    inArgs := regexp.MustCompile(`(^|[\s(,])` + relationPattern(old) + `($|[\s),\[])`)
    for norm, lf := range l.FunctionDefs {
        if lf == nil { continue }
        sig := renameInArgs(lf.Signature, inArgs, printedRelation(new))
        if sig == lf.Signature { continue }
        r.moveFunction(norm, normalizeFunctionSignature(sig), sig)
    }
    for norm, lp := range l.ProcedureDefs {
        if lp == nil { continue }
        sig := renameInArgs(lp.Signature, inArgs, printedRelation(new))
        if sig == lp.Signature { continue }
        r.moveProcedure(norm, normalizeFunctionSignature(sig), sig)
    }
}

// renameInArgs replaces a type name inside the argument list of a routine
// signature "schema.name(args)".
func renameInArgs(sig string, re *regexp.Regexp, repl string) string {
    i := strings.IndexByte(sig, '(')
    if i < 0 { return sig }
    args := sig[i:]
    for n := 0; n < 3; n++ {
        args = re.ReplaceAllStringFunc(args, func(m string) string {
            sm := re.FindStringSubmatch(m)
            return sm[1] + repl + sm[len(sm)-1]
        })
    }
    return sig[:i] + args
}

func (r *renamer) routines(d *schema.Database) {
    l := r.l
    for _, k := range sortedKeys(d.Functions) {
        f := d.Functions[k]
        if f == nil || f.RenamedFrom == "" { continue }
        name := schema.RoutineName(k)
        old := renameSource(name, f.RenamedFrom)
        oldNorm, newNorm := normalizeFunctionSignature(old+f.ArgsSig), normalizeFunctionSignature(name+f.ArgsSig)
        lf := l.FunctionDefs[oldNorm]
        if lf == nil || l.FunctionDefs[newNorm] != nil { continue }
        r.stmts = append(r.stmts, relocateStmts("function", old, name, grantFuncArgs(f.ArgsSig))...)
        sig := name + lf.Signature[strings.IndexByte(lf.Signature, '('):]
        r.moveFunction(oldNorm, newNorm, sig)
        exec := regexp.MustCompile(`EXECUTE (FUNCTION|PROCEDURE) ` + relationPattern(old) + `\(`)
        for _, t := range r.allTables() {
            for n, def := range t.Triggers {
                t.Triggers[n] = exec.ReplaceAllString(def, "EXECUTE $1 "+strings.ReplaceAll(printedRelation(name), "$", "$$")+"(")
            }
        }
    }
    for _, k := range sortedKeys(d.Procedures) {
        p := d.Procedures[k]
        if p == nil || p.RenamedFrom == "" { continue }
        name := schema.RoutineName(k)
        old := renameSource(name, p.RenamedFrom)
        oldNorm, newNorm := normalizeFunctionSignature(old+p.ArgsSig), normalizeFunctionSignature(name+p.ArgsSig)
        lp := l.ProcedureDefs[oldNorm]
        if lp == nil || l.ProcedureDefs[newNorm] != nil { continue }
        r.stmts = append(r.stmts, relocateStmts("procedure", old, name, grantFuncArgs(p.ArgsSig))...)
        r.moveProcedure(oldNorm, newNorm, name+lp.Signature[strings.IndexByte(lp.Signature, '('):])
    }
}

func (r *renamer) moveFunction(oldNorm, newNorm, newSig string) {
    l := r.l
    if lf := l.FunctionDefs[oldNorm]; lf != nil {
        cp := *lf
        cp.Signature = newSig
        delete(l.FunctionDefs, oldNorm)
        l.FunctionDefs[newNorm] = &cp
    }
    for raw := range l.Functions {
        if normalizeFunctionSignature(raw) == oldNorm {
            delete(l.Functions, raw)
            l.Functions[newSig] = true
        }
    }
    moveKey(l.FunctionGrants, oldNorm, newNorm)
    moveKey(l.FunctionComments, oldNorm, newNorm)
    moveKey(l.FunctionPublicExec, oldNorm, newNorm)
    moveKey(l.AggregateDefs, oldNorm, newNorm)
    moveKey(l.Owners, "proc:"+oldNorm, "proc:"+newNorm)
}

func (r *renamer) moveProcedure(oldNorm, newNorm, newSig string) {
    l := r.l
    if lp := l.ProcedureDefs[oldNorm]; lp != nil {
        cp := *lp
        cp.Signature = newSig
        delete(l.ProcedureDefs, oldNorm)
        l.ProcedureDefs[newNorm] = &cp
    }
    moveKey(l.Procedures, oldNorm, newNorm)
    moveKey(l.FunctionGrants, oldNorm, newNorm)
    moveKey(l.FunctionComments, oldNorm, newNorm)
    moveKey(l.FunctionPublicExec, oldNorm, newNorm)
    moveKey(l.Owners, "proc:"+oldNorm, "proc:"+newNorm)
}

func (r *renamer) tableParts(d *schema.Database) {
    for _, k := range sortedKeys(d.Tables) {
        dt, lt := d.Tables[k], r.l.Tables[k]
        if dt == nil || lt == nil { continue }
        for _, cn := range sortedKeys(dt.Columns) {
            c := dt.Columns[cn]
            if c == nil || c.RenamedFrom == "" || lt.Columns[c.RenamedFrom] == nil || lt.Columns[cn] != nil { continue }
            r.stmts = append(r.stmts, fmt.Sprintf("alter table %s rename column %s to %s;", pqIdent(k), pqIdent(c.RenamedFrom), pqIdent(cn)))
            moveKey(lt.Columns, c.RenamedFrom, cn)
            if g := r.l.ColumnGrants[k]; g != nil { moveKey(g, c.RenamedFrom, cn) }
            r.rewriteColumnRefs(k, lt, c.RenamedFrom, cn)
        }
        r.renameIndexes(k, lt, dt.Indexes)
        for _, c := range dt.Constraints {
            if c != nil { r.renameConstraint(k, lt, c.RenamedFrom, c.Name) }
        }
        for _, fk := range dt.ForeignKeys {
            if fk != nil { r.renameConstraint(k, lt, fk.RenamedFrom, fk.Name) }
        }
    }
    for _, k := range sortedKeys(d.Views) {
        vw := d.Views[k]
        if vw == nil || !vw.Materialized { continue }
        if lt := r.l.MatViewTables[k]; lt != nil { r.renameIndexes(k, lt, vw.Indexes) }
    }
}

func (r *renamer) renameIndexes(fq string, lt *LiveTable, indexes []*schema.Index) {
    sch, _ := splitQualified(fq)
    for _, ix := range indexes {
        if ix == nil || ix.RenamedFrom == "" || ix.Name == "" { continue }
        old, name := truncateIdent(ix.RenamedFrom), truncateIdent(ix.Name)
        if !lt.Indexes[old] || lt.Indexes[name] { continue }
        r.stmts = append(r.stmts, fmt.Sprintf("alter index %s rename to %s;", pqIdent(sch+"."+old), pqIdent(name)))
        moveIndex(lt, old, name)
    }
}

func moveIndex(lt *LiveTable, old, name string) {
    moveKey(lt.Indexes, old, name)
    moveKey(lt.IndexDefs, old, name)
    moveKey(lt.IndexOpclasses, old, name)
    moveKey(lt.IndexComments, old, name)
    if lt.ReplicaIdentity == "index "+old { lt.ReplicaIdentity = "index " + name }
    if lt.ClusteredOn == old { lt.ClusteredOn = name }
}

func (r *renamer) renameConstraint(fq string, lt *LiveTable, from, to string) {
    if from == "" || to == "" { return }
    old, name := truncateIdent(from), truncateIdent(to)
    if !lt.Constraints[old] || lt.Constraints[name] { return }
    r.stmts = append(r.stmts, fmt.Sprintf("alter table %s rename constraint %s to %s;", pqIdent(fq), pqIdent(old), pqIdent(name)))
    moveKey(lt.Constraints, old, name)
    moveKey(lt.ConstraintDefs, old, name)
    moveKey(lt.ConstraintComments, old, name)
    moveKey(lt.ConstraintInherited, old, name)
    if lt.Indexes[old] && !lt.Indexes[name] { moveIndex(lt, old, name) }
}

// rewriteColumnRefs renames column old to new inside the live definitions
// that name it: the table's constraints, indexes, triggers, policies, rules,
// statistics, defaults and generated expressions, and other tables' foreign
// keys referencing it.
func (r *renamer) rewriteColumnRefs(fq string, lt *LiveTable, old, new string) {
    re := regexp.MustCompile(`(^|[^\w$"])` + identPattern(old) + `($|[^\w$"])`)
    rw := func(s string) string { return replaceIdent(s, re, printedIdent(new)) }
    list := func(in []string) []string {
        if in == nil { return nil }
        out := make([]string, len(in))
        for i, s := range in { out[i] = rw(s) }
        return out
    }
    for n, def := range lt.ConstraintDefs {
        if strings.HasPrefix(def, "FOREIGN KEY (") {
            if end := strings.IndexByte(def, ')'); end > 0 {
                lt.ConstraintDefs[n] = rw(def[:end+1]) + def[end+1:]
            }
            continue
        }
        lt.ConstraintDefs[n] = rw(def)
    }
    for n, li := range lt.IndexDefs {
        if li == nil { continue }
        cp := *li
        cp.Keys, cp.Include, cp.Predicate = list(li.Keys), list(li.Include), rw(li.Predicate)
        lt.IndexDefs[n] = &cp
    }
    for n, def := range lt.Triggers { lt.Triggers[n] = rw(def) }
    for n, p := range lt.Policies {
        if p == nil { continue }
        cp := *p
        cp.Using, cp.WithCheck = rw(p.Using), rw(p.WithCheck)
        lt.Policies[n] = &cp
    }
    for n, ru := range lt.Rules {
        if ru == nil { continue }
        cp := *ru
        cp.Where = rw(ru.Where)
        lt.Rules[n] = &cp
    }
    for n, st := range lt.Statistics {
        if st == nil { continue }
        cp := *st
        cp.Items = list(st.Items)
        lt.Statistics[n] = &cp
    }
    for _, lc := range lt.Columns {
        if lc != nil { lc.Default, lc.Generated = rw(lc.Default), rw(lc.Generated) }
    }
    refs := regexp.MustCompile(`(REFERENCES ` + relationPattern(fq) + `\()([^)]*)\)`)
    for _, t := range r.allTables() {
        for n, def := range t.ConstraintDefs {
            t.ConstraintDefs[n] = refs.ReplaceAllStringFunc(def, func(m string) string {
                sm := refs.FindStringSubmatch(m)
                return sm[1] + rw(sm[len(sm)-1]) + ")"
            })
        }
    }
}

// replaceIdent replaces each identifier matched by re (with its boundary
// characters captured as the first and last groups) by repl, outside string
// literals.
func replaceIdent(s string, re *regexp.Regexp, repl string) string {
    if s == "" { return s }
    masked, lits := maskStringLiterals(s)
    for n := 0; n < 3; n++ {
        masked = re.ReplaceAllStringFunc(masked, func(m string) string {
            sm := re.FindStringSubmatch(m)
            return sm[1] + repl + sm[len(sm)-1]
        })
    }
    return exprPlaceholderRe.ReplaceAllStringFunc(masked, func(m string) string {
        i, _ := strconv.Atoi(exprPlaceholderRe.FindStringSubmatch(m)[1])
        return lits[i]
    })
}

var plainIdentRe = regexp.MustCompile(`^[a-z_][a-z0-9_$]*$`)

var reservedWords = map[string]bool{}

func init() {
    for _, w := range strings.Fields(`all analyse analyze and any array as asc asymmetric both case cast check collate column
        constraint create current_catalog current_date current_role current_time current_timestamp current_user default
        deferrable desc distinct do else end except false fetch for foreign from grant group having in initially intersect
        into lateral leading limit localtime localtimestamp not null offset on only or order placing primary references
        returning select session_user some symmetric system_user table then to trailing true union unique user using
        variadic when where window with`) {
        reservedWords[w] = true
    }
}

// printedIdent quotes an identifier the way PostgreSQL's quote_ident does.
func printedIdent(s string) string {
    if plainIdentRe.MatchString(s) && !reservedWords[s] { return s }
    return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// printedRelation prints schema.name as catalog functions do with the
// default search_path: public objects unqualified.
func printedRelation(fq string) string {
    sch, name := splitQualified(fq)
    if sch == "public" { return printedIdent(name) }
    return printedIdent(sch) + "." + printedIdent(name)
}

// identPattern matches an identifier in either its quoted or, when that is
// a valid spelling, bare form.
func identPattern(s string) string {
    alts := []string{regexp.QuoteMeta(`"` + strings.ReplaceAll(s, `"`, `""`) + `"`)}
    if plainIdentRe.MatchString(s) { alts = append(alts, regexp.QuoteMeta(s)) }
    return "(?:" + strings.Join(alts, "|") + ")"
}

// relationPattern matches schema.name as catalog functions may print it;
// the schema is optional for public.
func relationPattern(fq string) string {
    sch, name := splitQualified(fq)
    if sch == "public" { return "(?:" + identPattern(sch) + `\.)?` + identPattern(name) }
    return identPattern(sch) + `\.` + identPattern(name)
}
