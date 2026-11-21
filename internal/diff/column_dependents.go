package diff

import (
    "fmt"
    "regexp"
    "sort"
    "strconv"
    "strings"

    "github.com/suprbdev/pgy/internal/schema"
)

// typeChangingColumns returns, for each live table, the columns whose type
// or collation the plan changes with ALTER COLUMN ... TYPE (only under
// --unsafe, matching alterColumnStmts).
func typeChangingColumns(live *Live, desired *schema.Database, unsafe bool) map[string][]string {
    out := map[string][]string{}
    if !unsafe { return out }
    for _, fq := range sortedKeys(desired.Tables) {
        dt, lt := desired.Tables[fq], live.Tables[fq]
        if dt == nil || lt == nil || dt.PartitionOf != "" || dt.Of != "" { continue }
        for _, cn := range sortedKeys(dt.Columns) {
            c, lc := dt.Columns[cn], lt.Columns[cn]
            if c == nil || lc == nil || lc.Inherited || c.Type == "" { continue }
            typeChanged := normalizeColumnType(c.Type) != normalizeColumnType(lc.Type)
            collateChanged := c.Collate != "" && collationName(c.Collate) != lc.Collation
            if typeChanged || collateChanged { out[fq] = append(out[fq], cn) }
        }
    }
    return out
}

// recreateColumnDependents handles the objects PostgreSQL won't let ALTER
// COLUMN ... TYPE change under: views and materialized views (and the views
// built on them, whether they name a column or not), triggers, policies and
// rules that use the column or one of those views (see columnDependents).
// Each is dropped first, and the returned live is a copy without it, so the
// plan recreates it from the YAML afterwards. The same goes for what the plan
// replaces or prunes anyway and the new type may not fit: indexes and
// constraints that use the column, and foreign keys whose columns change
// type on both sides. pre maps a table to the DROP statements to run,
// dependents first, before its columns change type; early holds the foreign
// key drops that must precede every table's changes; post restores what
// recreated views had and the YAML leaves unmanaged (see recreateView). A
// view that an earlier statement of the plan already drops (see
// planViewChanges and recreateUpdateDependents) gets DROP VIEW IF EXISTS,
// which still puts the type change ahead of the view's recreation.
// A dependent the YAML doesn't declare, which pgy can't recreate, gets a
// warning and leaves the column's dependents alone.
//
// A generated column in the way (see generatedInTheWay) that the YAML still
// declares generated is dropped after its dependents and before the type
// changes, and readd lists it by table for the plan to add again after
// them, from the YAML; post restores what it had and the YAML leaves
// unmanaged (see restoreColumn). One that regenerationBlocker keeps gets a
// warning instead.
func recreateColumnDependents(live *Live, desired *schema.Database, opts Options) (out *Live, early []string, pre map[string][]string, post []string, readd map[string][]string, warns []Warning) {
    out = live
    if live == nil || desired == nil { return out, nil, nil, nil, nil, nil }
    changing := typeChangingColumns(live, desired, opts.Unsafe)
    regenerate, plain, warns := generatedInTheWay(live, desired, changing, opts)
    if len(changing) == 0 && len(regenerate) == 0 { return out, nil, nil, nil, nil, warns }

    byRelation := map[string][]ColumnDependent{}
    for col, deps := range live.ColumnDependents {
        rel := ColumnRelation(col)
        byRelation[rel] = append(byRelation[rel], deps...)
    }
    readd = map[string][]string{}
    columns := map[string][]string{}
    for fq, cols := range changing { columns[fq] = append([]string(nil), cols...) }
    for _, fq := range sortedKeys(regenerate) {
        for _, g := range sortedKeys(regenerate[fq]) {
            why := regenerate[fq][g]
            if reason := regenerationBlocker(live, desired, byRelation, fq, g, opts.Prune); reason != "" {
                for _, cn := range why.retyped {
                    warns = append(warns, Warning{Table: fq, Kind: "dependents", Message: fmt.Sprintf("changing the type of %s.%s: generated column %s uses it, and PostgreSQL rejects the change while it does; pgy can't drop %s and add it again, because %s, so do that in a hand-written migration", fq, cn, g, g, reason)})
                }
                if why.expression {
                    warns = append(warns, Warning{Table: fq, Message: fmt.Sprintf("generated column %s.%s changed, but PostgreSQL %d has no ALTER COLUMN ... SET EXPRESSION (added in 17), and pgy can't drop the column and add it again, because %s; do that in a hand-written migration", fq, g, live.ServerVersion/10000, reason)})
                }
                continue
            }
            because := fmt.Sprintf("PostgreSQL %d has no ALTER COLUMN ... SET EXPRESSION", live.ServerVersion/10000)
            if len(why.retyped) > 0 {
                cols := make([]string, len(why.retyped))
                for i, cn := range why.retyped { cols[i] = fq + "." + cn }
                because = fmt.Sprintf("it uses %s, whose type changes", strings.Join(cols, ", "))
                if len(cols) > 1 { because = fmt.Sprintf("it uses %s, whose types change", strings.Join(cols, ", ")) }
            }
            warns = append(warns, Warning{Table: fq, Message: fmt.Sprintf("generated column %s.%s is dropped and added again at the end of the table, because %s; the indexes, constraints and statistics on it, and the views, triggers, policies and rules that use it, are created again", fq, g, because)})
            readd[fq] = append(readd[fq], g)
            if indexOf(columns[fq], g) < 0 { columns[fq] = append(columns[fq], g) }
        }
    }
    if len(changing) == 0 && len(readd) == 0 { return out, nil, nil, nil, nil, warns }

    dropped := map[string]bool{}
    for _, fq := range sortedKeys(columns) {
        for _, cn := range columns[fq] {
            views, objects := columnDependents(live, byRelation, fq+"."+cn)
            if len(views) == 0 && len(objects) == 0 { continue }
            if msg := undeclaredDependent(desired, fq, views, objects); msg != "" {
                warns = append(warns, Warning{Table: fq, Kind: "dependents", Message: fmt.Sprintf("changing the type of %s.%s: %s, so pgy can't drop and recreate it, and PostgreSQL rejects the change while it exists; declare it, or change it in a hand-written migration", fq, cn, msg)})
                continue
            }
            if out == live { out = cloneLive(live) }
            if pre == nil { pre = map[string][]string{} }
            for _, d := range dedupeDependents(objects) {
                key := d.Kind + ":" + d.Table + ":" + d.Name
                if dropped[key] { continue }
                dropped[key] = true
                lt := out.Tables[d.Table]
                if lt == nil { lt = out.ViewTables[d.Table] }
                switch d.Kind {
                case "trigger":
                    pre[fq] = append(pre[fq], fmt.Sprintf("drop trigger %s on %s;", quoteIdent(d.Name), pqIdent(d.Table)))
                    if lt != nil { delete(lt.Triggers, d.Name); delete(lt.TriggerComments, d.Name) }
                case "policy":
                    pre[fq] = append(pre[fq], fmt.Sprintf("drop policy %s on %s;", quoteIdent(d.Name), pqIdent(d.Table)))
                    if lt != nil { delete(lt.Policies, d.Name); delete(lt.PolicyComments, d.Name) }
                case "rule":
                    pre[fq] = append(pre[fq], fmt.Sprintf("drop rule %s on %s;", quoteIdent(d.Name), pqIdent(d.Table)))
                    if lt != nil { delete(lt.Rules, d.Name); delete(lt.RuleComments, d.Name) }
                }
            }
            for _, v := range views {
                if dropped["view:"+v] { continue }
                dropped["view:"+v] = true
                kind, guard := "view", ""
                if live.MatViews[v] { kind = "materialized view" }
                if !live.Views[v] && !live.MatViews[v] { guard = "if exists " }
                pre[fq] = append(pre[fq], fmt.Sprintf("drop %s %s%s;", kind, guard, pqIdent(v)))
                post = append(post, recreateView(live, out, desired, v, opts.Prune)...)
            }
        }
    }

    if out == live { out = cloneLive(live) }
    if pre == nil { pre = map[string][]string{} }
    early = dropRetypedForeignKeys(out, desired, changing)
    for _, fq := range sortedKeys(columns) {
        target := "alter table " + pqIdent(fq)
        for _, g := range plain[fq] {
            pre[fq] = append(pre[fq], fmt.Sprintf("%s alter column %s drop expression;", target, quoteIdent(g)))
            out.Tables[fq].Columns[g].Generated = ""
        }
        pre[fq] = append(pre[fq], releaseRetypedColumns(out, desired, fq, changing[fq], opts.Prune)...)
        for _, g := range readd[fq] {
            pre[fq] = append(pre[fq], fmt.Sprintf("%s drop column %s;", target, quoteIdent(g)))
            post = append(post, restoreColumn(live, desired.Tables[fq].Columns[g], fq, g)...)
            forgetColumn(out, fq, g)
        }
        if len(pre[fq]) == 0 { delete(pre, fq) }
    }
    return out, early, pre, post, readd, warns
}

// dropRetypedForeignKeys drops the declared foreign keys whose columns
// change type on both the referencing and the referenced side: PostgreSQL
// checks a foreign key after each side's change, when the two types may no
// longer be comparable. Each is removed from live, so the plan adds it again
// once both types have changed. The statements belong before every table's
// changes, since either table may come first.
func dropRetypedForeignKeys(live *Live, desired *schema.Database, changing map[string][]string) []string {
    retyped := func(fq string, cols []string) bool {
        for _, c := range cols {
            if indexOf(changing[fq], constraintColumnName(c)) >= 0 { return true }
        }
        return false
    }
    out := []string{}
    for _, fq := range sortedKeys(desired.Tables) {
        dt, lt := desired.Tables[fq], live.Tables[fq]
        if dt == nil || lt == nil { continue }
        for _, fk := range dt.ForeignKeys {
            if fk == nil || len(fk.Columns) == 0 || fk.RefTable == "" { continue }
            name := truncateIdent(fk.Name)
            if !lt.Constraints[name] || unaddableForeignKey(live, dt, fk) || !retyped(fq, fk.Columns) || !retyped(qualifyName(fk.RefTable), fk.RefColumns) { continue }
            out = append(out, fmt.Sprintf("alter table %s drop constraint %s;", pqIdent(fq), pqIdent(name)))
            forgetConstraint(lt, name)
        }
    }
    return out
}

// releaseRetypedColumns returns the statements that must run before table
// fq's columns cols change type, and removes what they drop from live so the
// plan creates it again from the YAML afterwards. They drop the indexes and
// the check, unique, exclusion and foreign key constraints on those columns
// that the plan replaces (their definition changed) or, with prune, drops
// (the YAML no longer declares them): left in place, the old definition is
// rebuilt against the new type and may not fit it.
func releaseRetypedColumns(live *Live, desired *schema.Database, fq string, cols []string, prune bool) (stmts []string) {
    dt, lt := desired.Tables[fq], live.Tables[fq]
    if dt == nil || lt == nil { return nil }
    target := "alter table " + pqIdent(fq)
    uses := columnRefs(cols...)
    decl := declarationsOf(fq, dt, lt)

    for _, name := range sortedKeys(lt.IndexDefs) {
        li := lt.IndexDefs[name]
        if li == nil || li.Constraint || li.Inherited || !lt.Indexes[name] || !uses(indexParts(li)...) { continue }
        if ix := decl.indexes[name]; ix != nil && !indexChanged(ix, lt, name) || ix == nil && !prune { continue }
        stmts = append(stmts, fmt.Sprintf("drop index %s;", pqIdent(schemaPrefixOf(fq)+name)))
        forgetIndex(lt, name)
    }

    for _, name := range sortedKeys(lt.ConstraintDefs) {
        def := lt.ConstraintDefs[name]
        upper := strings.ToUpper(strings.TrimSpace(def))
        if def == "" || lt.ConstraintInherited[name] || decl.columnKeys[name] || strings.HasPrefix(upper, "PRIMARY KEY") || strings.HasPrefix(upper, "NOT NULL") { continue }
        replaced, declared := false, false
        if strings.HasPrefix(upper, "FOREIGN KEY") {
            local, _, _ := strings.Cut(def, ")")
            if !uses(local) { continue }
            fk := decl.foreignKeys[name]
            declared, replaced = fk != nil, fk != nil && !constraintDefsEqual(renderFKDef(fk), def) && !unaddableForeignKey(live, dt, fk)
        } else {
            if !uses(def) { continue }
            ct := decl.constraints[name]
            declared, replaced = ct != nil, ct != nil && !constraintMatchesLive(lt, name, ct)
        }
        if !replaced && (declared || !prune) { continue }
        stmts = append(stmts, fmt.Sprintf("%s drop constraint %s;", target, pqIdent(name)))
        forgetConstraint(lt, name)
    }
    return stmts
}

// columnRefs returns a function that reports whether one of the definitions
// it is given names one of cols, outside string literals.
func columnRefs(cols ...string) func(defs ...string) bool {
    refs := make([]*regexp.Regexp, len(cols))
    for i, cn := range cols { refs[i] = regexp.MustCompile(`(^|[^\w$"])` + identPattern(cn) + `($|[^\w$"])`) }
    return func(defs ...string) bool {
        for _, def := range defs {
            if def == "" { continue }
            masked, _ := maskStringLiterals(def)
            for _, re := range refs {
                if re.MatchString(masked) { return true }
            }
        }
        return false
    }
}

// indexParts returns the parts of an index definition that can name a
// column: its keys, its INCLUDE columns and its predicate.
func indexParts(li *LiveIndex) []string {
    return append(append([]string{li.Predicate}, li.Keys...), li.Include...)
}

// tableDeclarations holds what desired declares on a table, by the names
// the objects have live: indexes, foreign keys, check, unique and exclusion
// constraints, the unique constraints of columns declared unique, and
// extended statistics.
type tableDeclarations struct {
    indexes     map[string]*schema.Index
    foreignKeys map[string]*schema.ForeignKey
    constraints map[string]*schema.Constraint
    columnKeys  map[string]bool
    statistics  map[string]bool
}

// declarationsOf collects what dt declares on table fq, whose live state is
// lt.
func declarationsOf(fq string, dt *schema.Table, lt *LiveTable) tableDeclarations {
    d := tableDeclarations{map[string]*schema.Index{}, map[string]*schema.ForeignKey{}, map[string]*schema.Constraint{}, map[string]bool{}, map[string]bool{}}
    for _, ix := range dt.Indexes {
        if ix != nil && len(ix.Columns) > 0 { d.indexes[LiveIndexName(fq, ix, lt.Indexes)] = ix }
    }
    for _, fk := range dt.ForeignKeys {
        if fk != nil && len(fk.Columns) > 0 && fk.RefTable != "" { d.foreignKeys[truncateIdent(fk.Name)] = fk }
    }
    for _, ct := range dt.Constraints {
        if ct != nil && renderConstraintDef(ct) != "" { d.constraints[truncateIdent(ct.Name)] = ct }
    }
    for cn, c := range dt.Columns {
        if c != nil && c.Unique { d.columnKeys[LiveUniqueKeyName(fq, cn, lt.Constraints)] = true }
    }
    for _, st := range dt.Statistics {
        if st != nil { d.statistics[StatisticKey(fq, st.Schema, truncateIdent(st.Name))] = true }
    }
    return d
}

// generatedInTheWay returns the generated columns that stand in the way of
// the plan under --unsafe: those that use a column whose type changes
// (changing), and, before PostgreSQL 17, which has no SET EXPRESSION, those
// whose expression changes. A stored one that the YAML drops or declares as
// a plain column goes in plain, to lose its expression first. One the YAML
// still declares generated goes in regenerate, by table and column, with
// why: it is to be dropped and added again, if regenerationBlocker allows
// it. A virtual one the YAML gives up, which has no expression to drop, gets
// a warning.
func generatedInTheWay(live *Live, desired *schema.Database, changing map[string][]string, opts Options) (regenerate map[string]map[string]*regeneration, plain map[string][]string, warns []Warning) {
    regenerate, plain = map[string]map[string]*regeneration{}, map[string][]string{}
    add := func(fq, g string) *regeneration {
        if regenerate[fq] == nil { regenerate[fq] = map[string]*regeneration{} }
        if regenerate[fq][g] == nil { regenerate[fq][g] = &regeneration{} }
        return regenerate[fq][g]
    }
    for _, fq := range sortedKeys(changing) {
        dt, lt := desired.Tables[fq], live.Tables[fq]
        for _, cn := range changing[fq] {
            for _, d := range dedupeDependents(live.ColumnDependents[fq+"."+cn]) {
                lg := lt.Columns[d.Name]
                if d.Kind != "generated" || d.Table != fq || lg == nil || lg.Generated == "" { continue }
                switch c := dt.Columns[d.Name]; {
                case c != nil && c.Generated != "":
                    r := add(fq, d.Name)
                    r.retyped = append(r.retyped, cn)
                case !lg.Virtual:
                    if indexOf(plain[fq], d.Name) < 0 { plain[fq] = append(plain[fq], d.Name) }
                default:
                    warns = append(warns, Warning{Table: fq, Kind: "dependents", Message: fmt.Sprintf("changing the type of %s.%s: generated column %s uses it, and PostgreSQL rejects the change while it does; drop %s and add it again, along with the indexes and constraints on it, in a hand-written migration", fq, cn, d.Name, d.Name)})
                }
            }
        }
    }
    if !opts.Unsafe || live.ServerVersion <= 0 || live.ServerVersion >= 170000 { return regenerate, plain, warns }
    for _, fq := range sortedKeys(desired.Tables) {
        dt, lt := desired.Tables[fq], live.Tables[fq]
        if dt == nil || lt == nil || dt.PartitionOf != "" { continue }
        for _, g := range sortedKeys(dt.Columns) {
            c, lc := dt.Columns[g], lt.Columns[g]
            if c == nil || lc == nil || lc.Inherited || c.Generated == "" || lc.Generated == "" || constraintDefsEqual(c.Generated, lc.Generated) { continue }
            add(fq, g).expression = true
        }
    }
    return regenerate, plain, warns
}

// regeneration says why a generated column has to be dropped and added
// again: it uses the retyped columns of its table, or its expression
// changed on a server without SET EXPRESSION.
type regeneration struct {
    retyped    []string
    expression bool
}

// regenerationBlocker returns why generated column g of table fq can't be
// dropped and added again from the YAML, or "" when it can: the table is a
// foreign, typed, partitioned or inheritance parent table, the column is in
// the primary key or has a collation the YAML doesn't declare, a foreign
// key or a publication refers to it, an index, constraint or statistics
// object on it isn't declared and prune is off, or a view, trigger, policy
// or rule that uses it isn't declared (see columnDependents).
func regenerationBlocker(live *Live, desired *schema.Database, byRelation map[string][]ColumnDependent, fq, g string, prune bool) string {
    dt, lt := desired.Tables[fq], live.Tables[fq]
    uses := columnRefs(g)
    switch {
    case dt.Foreign != nil || lt.Foreign != nil:
        return fq + " is a foreign table"
    case dt.Of != "" || lt.OfType != "":
        return fq + " is a typed table"
    case lt.Partitioned || len(live.Partitions[fq]) > 0:
        return fq + " is partitioned"
    case dt.Columns[g].PrimaryKey || uses(lt.ConstraintDefs[LivePrimaryKeyName(lt)]):
        return "it is part of the primary key"
    case dt.Columns[g].Collate == "" && lt.Columns[g].Collation != "":
        return "its collation " + lt.Columns[g].Collation + " isn't declared"
    }
    for _, pc := range dt.PrimaryKey {
        if constraintColumnName(pc) == g { return "it is part of the primary key" }
    }
    for _, tn := range sortedKeys(live.Tables) {
        if indexOf(live.Tables[tn].Inherits, fq) >= 0 { return fq + " has inheritance children" }
    }
    refs := regexp.MustCompile(`REFERENCES ` + relationPattern(fq) + `\(([^)]*)\)`)
    for _, tn := range sortedKeys(live.Tables) {
        for _, name := range sortedKeys(live.Tables[tn].ConstraintDefs) {
            if m := refs.FindStringSubmatch(live.Tables[tn].ConstraintDefs[name]); m != nil && uses(m[1]) { return fmt.Sprintf("foreign key %s on %s references it", name, tn) }
        }
    }
    for _, name := range sortedKeys(live.Publications) {
        if pt, ok := live.Publications[name].Tables[fq]; ok && (indexOf(pt.Columns, g) >= 0 || uses(pt.Where)) { return "publication " + name + " lists it" }
    }
    decl := declarationsOf(fq, dt, lt)
    for _, name := range sortedKeys(lt.IndexDefs) {
        li := lt.IndexDefs[name]
        if li == nil || li.Constraint || li.Inherited || decl.indexes[name] != nil || prune || !uses(indexParts(li)...) { continue }
        return "index " + name + " on it isn't declared"
    }
    for _, name := range sortedKeys(lt.ConstraintDefs) {
        def := lt.ConstraintDefs[name]
        upper := strings.ToUpper(strings.TrimSpace(def))
        if lt.ConstraintInherited[name] || strings.HasPrefix(upper, "NOT NULL") { continue }
        if strings.HasPrefix(upper, "FOREIGN KEY") {
            local, _, _ := strings.Cut(def, ")")
            if !uses(local) { continue }
            if fk := decl.foreignKeys[name]; fk != nil && unaddableForeignKey(live, dt, fk) { return "foreign key " + name + " on it couldn't be added again" }
            if decl.foreignKeys[name] == nil && !prune { return "foreign key " + name + " on it isn't declared" }
            continue
        }
        if !uses(def) || decl.constraints[name] != nil || decl.columnKeys[name] || prune { continue }
        return "constraint " + name + " on it isn't declared"
    }
    for _, name := range sortedKeys(lt.Statistics) {
        if st := lt.Statistics[name]; st != nil && !decl.statistics[name] && !prune && uses(st.Items...) { return "statistics " + name + " on it isn't declared" }
    }
    views, objects := columnDependents(live, byRelation, fq+"."+g)
    if msg := undeclaredDependent(desired, fq, views, objects); msg != "" { return strings.TrimSuffix(msg, " depends on it and isn't declared") + " uses it and isn't declared" }
    return ""
}

// forgetColumn removes column cn from table fq of live, with the indexes,
// constraints and statistics that use it and its column grants, as after
// DROP COLUMN.
func forgetColumn(live *Live, fq, cn string) {
    lt := live.Tables[fq]
    uses := columnRefs(cn)
    for _, name := range sortedKeys(lt.IndexDefs) {
        if li := lt.IndexDefs[name]; li != nil && !li.Constraint && uses(indexParts(li)...) { forgetIndex(lt, name) }
    }
    for _, name := range sortedKeys(lt.ConstraintDefs) {
        def := lt.ConstraintDefs[name]
        if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(def)), "FOREIGN KEY") { def, _, _ = strings.Cut(def, ")") }
        if uses(def) { forgetConstraint(lt, name) }
    }
    for _, name := range sortedKeys(lt.Statistics) {
        if st := lt.Statistics[name]; st != nil && uses(st.Items...) { delete(lt.Statistics, name) }
    }
    delete(lt.Columns, cn)
    if g := live.ColumnGrants[fq]; g != nil { delete(g, cn) }
}

// restoreColumn returns the statements that give column cn of table fq,
// added again as c declares it, back what it has in live and c leaves
// unmanaged: its compression, storage, statistics target, attribute
// options, comment and grants.
func restoreColumn(live *Live, c *schema.Column, fq, cn string) []string {
    lc := live.Tables[fq].Columns[cn]
    target := fmt.Sprintf("alter table %s alter column %s", pqIdent(fq), quoteIdent(cn))
    out := []string{}
    if c.Compression == "" && lc.Compression != "" { out = append(out, fmt.Sprintf("%s set compression %s;", target, lc.Compression)) }
    if c.Storage == "" && lc.Storage != "" && lc.Storage != lc.TypeStorage { out = append(out, fmt.Sprintf("%s set storage %s;", target, lc.Storage)) }
    if c.StatisticsTarget == nil && lc.StatisticsTarget != nil && *lc.StatisticsTarget >= 0 {
        out = append(out, fmt.Sprintf("%s set statistics %d;", target, *lc.StatisticsTarget))
    }
    options := []string{}
    for _, o := range []struct {
        name    string
        managed bool
    }{{"n_distinct", c.NDistinct != nil}, {"n_distinct_inherited", c.NDistinctInherited != nil}} {
        if v, ok := lc.Options[o.name]; ok && !o.managed { options = append(options, o.name+" = "+v) }
    }
    if len(options) > 0 { out = append(out, fmt.Sprintf("%s set (%s);", target, strings.Join(options, ", "))) }
    if c.Comment == "" && lc.Comment != "" {
        out = append(out, fmt.Sprintf("comment on column %s.%s is %s;", pqIdent(fq), quoteIdent(cn), quoteString(lc.Comment)))
    }
    if c.Grants == nil {
        want := map[string][]string{}
        for role, privs := range live.ColumnGrants[fq][cn] { want[role] = sortedKeys(privs) }
        _, grants := columnGrantDiffStmts(fq, cn, want, nil, false)
        out = append(out, grants...)
    }
    return out
}

// unaddableForeignKey reports whether fk, once dropped, could not be added
// to existing table dt again: before PostgreSQL 18 a partitioned table takes
// no NOT VALID foreign key.
func unaddableForeignKey(live *Live, dt *schema.Table, fk *schema.ForeignKey) bool {
    return fk.NotValid && dt.PartitionBy != nil && live.ServerVersion > 0 && live.ServerVersion < 180000
}

// forgetIndex removes index name from lt, as after DROP INDEX.
func forgetIndex(lt *LiveTable, name string) {
    delete(lt.Indexes, name)
    delete(lt.IndexDefs, name)
    delete(lt.IndexOpclasses, name)
    delete(lt.IndexComments, name)
    if lt.ReplicaIdentity == "index "+name { lt.ReplicaIdentity = "nothing" }
    if lt.ClusteredOn == name { lt.ClusteredOn = "" }
}

// forgetConstraint removes constraint name, and the index behind it, from
// lt, as after DROP CONSTRAINT.
func forgetConstraint(lt *LiveTable, name string) {
    delete(lt.Constraints, name)
    delete(lt.ConstraintDefs, name)
    delete(lt.ConstraintComments, name)
    forgetIndex(lt, name)
}

// columnDropBlocked returns the warning for a DROP COLUMN of column cn of
// table fq that PostgreSQL would reject, because an object the plan keeps
// still uses the column, or nil. Such an object is a view or materialized
// view the YAML doesn't declare (or declares without a query), a trigger or
// rule its table or view doesn't list, a policy its table doesn't list when
// it has no policies block, which would drop it, and a generated column
// whose own drop is left out (leftOut, by column, says whether --prune would
// let it go). Undeclared views, triggers and rules that prune drops first
// don't count. prunable reports whether --prune would drop all of them.
func columnDropBlocked(live *Live, desired *schema.Database, fq, cn string, leftOut map[string]bool, prune bool) (w *Warning, prunable bool) {
    managed := managedSchemas(desired)
    exists := func(v string) bool { return live.Views[v] || live.MatViews[v] }
    declaredRelation := func(rel string) bool { return desired.Tables[rel] != nil || desired.Views[rel] != nil }
    names, policies := []string{}, false
    prunable = true
    for _, d := range dedupeDependents(live.ColumnDependents[fq+"."+cn]) {
        canPrune := false
        switch d.Kind {
        case "view", "matview":
            vw := desired.Views[d.Name]
            if !exists(d.Name) || vw != nil && strings.TrimSpace(vw.Query) != "" { continue }
            schemaName, _, _ := strings.Cut(d.Name, ".")
            canPrune = vw == nil && managed[schemaName] && !live.ExtensionMembers["rel:"+d.Name]
            names = append(names, "view "+d.Name)
            if d.Kind == "matview" { names[len(names)-1] = "materialized " + names[len(names)-1] }
        case "trigger", "rule":
            if live.Tables[d.Table] == nil && !exists(d.Table) || dependentDeclared(desired, d) { continue }
            inherited := false
            if lt := live.Tables[d.Table]; lt != nil { inherited = lt.TriggerInherited[d.Name] && d.Kind == "trigger" }
            schemaName, _, _ := strings.Cut(d.Table, ".")
            undeclaredView := live.Views[d.Table] && desired.Views[d.Table] == nil && managed[schemaName] && !live.ExtensionMembers["rel:"+d.Table]
            canPrune = !inherited && (declaredRelation(d.Table) || undeclaredView)
            names = append(names, fmt.Sprintf("%s %s on %s", d.Kind, d.Name, d.Table))
        case "policy":
            if dependentDeclared(desired, d) { continue }
            if dt := desired.Tables[d.Table]; dt != nil && dt.Policies != nil { continue }
            policies = policies || desired.Tables[d.Table] != nil
            names = append(names, fmt.Sprintf("policy %s on %s", d.Name, d.Table))
        case "generated":
            p, kept := leftOut[d.Name]
            if d.Table != fq || !kept { continue }
            canPrune = p
            names = append(names, "generated column "+d.Name)
        default:
            continue
        }
        if canPrune && prune { names = names[:len(names)-1]; continue }
        prunable = prunable && canPrune
    }
    if len(names) == 0 { return nil, false }
    uses, them, exist := "uses", "it", "it exists"
    if len(names) > 1 { uses, them, exist = "use", "them", "they exist" }
    hint := "--prune drops " + them + " first"
    if !prunable {
        hint = "drop " + them + " first, in a hand-written migration"
        if policies { hint += " (a policies block drops the policies it doesn't list)" }
    }
    msg := fmt.Sprintf("dropping column %s.%s: %s still %s it, and PostgreSQL rejects the drop while %s, so the column is kept; %s", fq, cn, strings.Join(names, ", "), uses, exist, hint)
    return &Warning{Table: fq, Kind: "dependents", Message: msg}, prunable
}

// columnDependents returns what has to go before column col
// ("schema.relation.column") can change type: the views and materialized
// views that use it, directly or through other views, each one listed after
// the views that use it, and the triggers, policies and rules that use the
// column or one of those views, except those on the views themselves, which
// go with them. Views are followed through live.ViewDependents, so a view
// that uses another one as a whole (count(*)) is found too.
func columnDependents(live *Live, byRelation map[string][]ColumnDependent, col string) (views []string, objects []ColumnDependent) {
    seen := map[string]bool{}
    found := []ColumnDependent{}
    var visit func(v string)
    visit = func(v string) {
        if seen[v] { return }
        seen[v] = true
        for _, d := range byRelation[v] {
            switch d.Kind {
            case "view", "matview":
                visit(d.Name)
            case "generated":
            default:
                found = append(found, d)
            }
        }
        for _, w := range live.ViewDependents[v] { visit(w) }
        views = append(views, v)
    }
    for _, d := range live.ColumnDependents[col] {
        switch d.Kind {
        case "view", "matview":
            visit(d.Name)
        case "generated":
        default:
            found = append(found, d)
        }
    }
    for _, d := range found {
        if !seen[d.Table] { objects = append(objects, d) }
    }
    return views, objects
}

// undeclaredDependent names the first of views and objects that desired
// doesn't declare (a view without a query, or a trigger, policy or rule its
// table or view doesn't list), or returns "".
func undeclaredDependent(desired *schema.Database, fq string, views []string, objects []ColumnDependent) string {
    for _, v := range views {
        if vw := desired.Views[v]; vw == nil || strings.TrimSpace(vw.Query) == "" {
            return "view " + v + " depends on it and isn't declared"
        }
    }
    for _, d := range objects {
        if !dependentDeclared(desired, d) { return fmt.Sprintf("%s %s on %s depends on it and isn't declared", d.Kind, d.Name, d.Table) }
    }
    return ""
}

// dependentDeclared reports whether desired lists trigger, policy or rule d
// on its table or view.
func dependentDeclared(desired *schema.Database, d ColumnDependent) bool {
    var triggers []*schema.Trigger
    var policies []*schema.Policy
    var rules []*schema.Rule
    if dt := desired.Tables[d.Table]; dt != nil {
        triggers, policies, rules = dt.Triggers, dt.Policies, dt.Rules
    } else if vw := desired.Views[d.Table]; vw != nil {
        triggers, rules = vw.Triggers, vw.Rules
    }
    switch d.Kind {
    case "trigger":
        for _, tr := range triggers {
            if tr != nil && truncateIdent(tr.Name) == d.Name { return true }
        }
    case "policy":
        for _, p := range policies {
            if p != nil && truncateIdent(p.Name) == d.Name { return true }
        }
    case "rule":
        for _, r := range rules {
            if r != nil && truncateIdent(r.Name) == d.Name { return true }
        }
    }
    return false
}

func dedupeDependents(deps []ColumnDependent) []ColumnDependent {
    seen := map[ColumnDependent]bool{}
    out := []ColumnDependent{}
    for _, d := range deps {
        if !seen[d] { seen[d] = true; out = append(out, d) }
    }
    sort.Slice(out, func(i, j int) bool {
        if out[i].Table != out[j].Table { return out[i].Table < out[j].Table }
        if out[i].Kind != out[j].Kind { return out[i].Kind < out[j].Kind }
        return out[i].Name < out[j].Name
    })
    return out
}

// recreateView removes view or materialized view v from out, as DROP does,
// so that the plan creates it again from the YAML, and returns the
// statements that give the new one back what v has in live and its YAML
// leaves unmanaged (see restoreView). Every path that drops a view to create
// it again goes through here: a changed view (planViewChanges), an extension
// update (recreateUpdateDependents) and a column type change
// (recreateColumnDependents). The live view options stay in out, so CREATE
// OR REPLACE VIEW keeps those the YAML doesn't manage.
func recreateView(live, out *Live, desired *schema.Database, v string, prune bool) []string {
    var post []string
    if vw := desired.Views[v]; vw != nil { post = restoreView(live, vw, v, prune) }
    delete(out.Views, v)
    delete(out.MatViews, v)
    delete(out.MatViewTables, v)
    delete(out.ViewTables, v)
    delete(out.ViewColumnComments, v)
    delete(out.ViewColumnDefaults, v)
    delete(out.ViewProbes, v)
    delete(out.RelComments, v)
    delete(out.TableGrants, v)
    delete(out.ColumnGrants, v)
    delete(out.Owners, "rel:"+v)
    return post
}

// restoreView returns the statements that give view v, created again from
// vw, back what it has in live and vw leaves unmanaged: the grants on the
// view and on the columns vw declares no grants for, its comment, column
// comments and column defaults, the column settings of a materialized view,
// its owner, and the comments of its declared triggers and rules.
// The triggers and rules vw doesn't declare are created again from their
// live definitions, with their comments, unless prune drops them anyway.
func restoreView(live *Live, vw *schema.View, v string, prune bool) []string {
    out := []string{}
    target := "view " + pqIdent(v)
    if live.MatViews[v] { target = "materialized " + target }
    if vw.Grants == nil {
        want := map[string][]string{}
        for role, privs := range live.TableGrants[v] { want[role] = sortedKeys(privs) }
        _, grants := roleGrantStmts(want, nil, func(privs []string) string { return strings.Join(privs, ", ") + " on table " + pqIdent(v) })
        out = append(out, grants...)
    }
    for _, col := range sortedKeys(live.ColumnGrants[v]) {
        if vw.ColumnGrants[col] != nil { continue }
        want := map[string][]string{}
        for role, privs := range live.ColumnGrants[v][col] { want[role] = sortedKeys(privs) }
        _, grants := columnGrantDiffStmts(v, col, want, nil, false)
        out = append(out, grants...)
    }
    if c := live.RelComments[v]; vw.Comment == "" && c != "" {
        out = append(out, fmt.Sprintf("comment on %s is %s;", target, quoteString(c)))
    }
    for _, col := range sortedKeys(live.ViewColumnComments[v]) {
        if vw.ColumnComments[col] == "" {
            out = append(out, fmt.Sprintf("comment on column %s.%s is %s;", pqIdent(v), quoteIdent(col), quoteString(live.ViewColumnComments[v][col])))
        }
    }
    for _, col := range sortedKeys(live.ViewColumnDefaults[v]) {
        if _, managed := vw.ColumnDefaults[col]; !managed {
            out = append(out, fmt.Sprintf("alter view %s alter column %s set default %s;", pqIdent(v), quoteIdent(col), live.ViewColumnDefaults[v][col]))
        }
    }
    if mt := live.MatViewTables[v]; mt != nil && live.MatViews[v] {
        keep := map[string]*schema.Column{}
        for col, lc := range mt.Columns {
            c := vw.Columns[col]
            if c == nil { c = &schema.Column{} }
            k := &schema.Column{}
            if c.Storage == "" && lc.TypeStorage != "" && lc.Storage != lc.TypeStorage { k.Storage = lc.Storage }
            if c.Compression == "" { k.Compression = lc.Compression }
            if c.StatisticsTarget == nil { k.StatisticsTarget = lc.StatisticsTarget }
            if f, err := strconv.ParseFloat(lc.Options["n_distinct"], 64); err == nil && c.NDistinct == nil { k.NDistinct = &f }
            keep[col] = k
        }
        out = append(out, matViewColumnStmts(v, &schema.View{Columns: keep}, nil)...)
    }
    if vt := live.ViewTables[v]; vt != nil {
        triggers, rules := map[string]*schema.Trigger{}, map[string]*schema.Rule{}
        for _, tr := range vw.Triggers {
            if tr != nil { triggers[truncateIdent(tr.Name)] = tr }
        }
        for _, r := range vw.Rules {
            if r != nil { rules[truncateIdent(r.Name)] = r }
        }
        for _, name := range sortedKeys(vt.Triggers) {
            tr, def, comment := triggers[name], vt.Triggers[name], vt.TriggerComments[name]
            if tr == nil && (prune || def == "") { continue }
            if tr == nil { out = append(out, strings.TrimRight(def, "; \n")+";") }
            if comment != "" && (tr == nil || tr.Comment == "") {
                out = append(out, fmt.Sprintf("comment on trigger %s on %s is %s;", quoteIdent(name), pqIdent(v), quoteString(comment)))
            }
        }
        for _, name := range sortedKeys(vt.Rules) {
            r, lr, comment := rules[name], vt.Rules[name], vt.RuleComments[name]
            if r == nil && (prune || lr == nil || lr.Def == "") { continue }
            if r == nil { out = append(out, strings.TrimRight(lr.Def, "; \n")+";") }
            if comment != "" && (r == nil || r.Comment == "") {
                out = append(out, fmt.Sprintf("comment on rule %s on %s is %s;", quoteIdent(name), pqIdent(v), quoteString(comment)))
            }
        }
    }
    if o := live.Owners["rel:"+v]; vw.Owner == "" && o != "" && o != live.CurrentUser {
        out = append(out, fmt.Sprintf("alter %s owner to %s;", target, grantRole(o)))
    }
    return out
}
