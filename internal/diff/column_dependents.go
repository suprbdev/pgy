package diff

import (
    "fmt"
    "regexp"
    "sort"
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
// built on them), triggers, policies and rules that use the column. Each is
// dropped first, and the returned live is a copy without it, so the plan
// recreates it from the YAML afterwards. The same goes for what the plan
// replaces or prunes anyway and the new type may not fit: indexes and
// constraints that use the column, and foreign keys whose columns change
// type on both sides. pre maps a table to the DROP statements to run,
// dependents first, before its columns change type; early holds the foreign
// key drops that must precede every table's changes; post restores the
// grants, comments and owners of recreated views that the YAML leaves
// unmanaged. A dependent the YAML doesn't declare, which pgy can't recreate,
// gets a warning and leaves the column's dependents alone, as does a
// generated column that stays generated.
func recreateColumnDependents(live *Live, desired *schema.Database, opts Options) (out *Live, early []string, pre map[string][]string, post []string, warns []Warning) {
    out = live
    if live == nil || desired == nil { return out, nil, nil, nil, nil }
    changing := typeChangingColumns(live, desired, opts.Unsafe)
    if len(changing) == 0 { return out, nil, nil, nil, nil }

    byRelation := map[string][]ColumnDependent{}
    for col, deps := range live.ColumnDependents {
        rel := col[:strings.LastIndexByte(col, '.')]
        byRelation[rel] = append(byRelation[rel], deps...)
    }
    dropped := map[string]bool{}
    for _, fq := range sortedKeys(changing) {
        for _, cn := range changing[fq] {
            direct := live.ColumnDependents[fq+"."+cn]
            if len(direct) == 0 { continue }
            views, objects := []string{}, []ColumnDependent{}
            seen := map[string]bool{}
            var visit func(v string)
            visit = func(v string) {
                if seen[v] { return }
                seen[v] = true
                for _, d := range byRelation[v] {
                    if d.Kind == "view" || d.Kind == "matview" { visit(d.Name) }
                }
                views = append(views, v)
            }
            for _, d := range direct {
                switch d.Kind {
                case "view", "matview":
                    visit(d.Name)
                case "generated":
                default:
                    objects = append(objects, d)
                }
            }
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
                kind := "view"
                if live.MatViews[v] { kind = "materialized view" }
                pre[fq] = append(pre[fq], fmt.Sprintf("drop %s %s;", kind, pqIdent(v)))
                post = append(post, restoreViewAttributes(live, desired.Views[v], v, kind)...)
                delete(out.Views, v)
                delete(out.MatViews, v)
                delete(out.ViewOptions, v)
                delete(out.ViewColumnComments, v)
                delete(out.ViewColumnDefaults, v)
                delete(out.MatViewTables, v)
                delete(out.ViewTables, v)
                delete(out.RelComments, v)
                delete(out.TableGrants, v)
                delete(out.Owners, "rel:"+v)
            }
        }
    }

    if out == live { out = cloneLive(live) }
    if pre == nil { pre = map[string][]string{} }
    early = dropRetypedForeignKeys(out, desired, changing)
    for _, fq := range sortedKeys(changing) {
        stmts, w := releaseRetypedColumns(out, desired, fq, changing[fq], opts.Prune)
        pre[fq] = append(pre[fq], stmts...)
        warns = append(warns, w...)
        if len(pre[fq]) == 0 { delete(pre, fq) }
    }
    return out, early, pre, post, warns
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
// rebuilt against the new type and may not fit it. A stored generated column
// that uses one of the columns and that the YAML drops or declares as a
// plain column loses its expression first; one that stays generated is
// reported, because PostgreSQL refuses the type change while it exists.
func releaseRetypedColumns(live *Live, desired *schema.Database, fq string, cols []string, prune bool) (stmts []string, warns []Warning) {
    dt, lt := desired.Tables[fq], live.Tables[fq]
    if dt == nil || lt == nil { return nil, nil }
    target := "alter table " + pqIdent(fq)

    refs := make([]*regexp.Regexp, len(cols))
    for i, cn := range cols {
        refs[i] = regexp.MustCompile(`(^|[^\w$"])` + identPattern(cn) + `($|[^\w$"])`)
        for _, d := range dedupeDependents(live.ColumnDependents[fq+"."+cn]) {
            lg := lt.Columns[d.Name]
            if d.Kind != "generated" || d.Table != fq || lg == nil || lg.Generated == "" { continue }
            if c := dt.Columns[d.Name]; (c == nil || c.Generated == "") && !lg.Virtual {
                stmts = append(stmts, fmt.Sprintf("%s alter column %s drop expression;", target, pqIdent(d.Name)))
                lg.Generated = ""
                continue
            }
            warns = append(warns, Warning{Table: fq, Kind: "dependents", Message: fmt.Sprintf("changing the type of %s.%s: generated column %s uses it, and PostgreSQL rejects the change while it does; drop %s and add it again, along with the indexes and constraints on it, in a hand-written migration", fq, cn, d.Name, d.Name)})
        }
    }

    uses := func(defs ...string) bool {
        for _, def := range defs {
            if def == "" { continue }
            masked, _ := maskStringLiterals(def)
            for _, re := range refs {
                if re.MatchString(masked) { return true }
            }
        }
        return false
    }

    declaredIndexes := map[string]*schema.Index{}
    for _, ix := range dt.Indexes {
        if ix != nil && len(ix.Columns) > 0 { declaredIndexes[LiveIndexName(fq, ix, lt.Indexes)] = ix }
    }
    for _, name := range sortedKeys(lt.IndexDefs) {
        li := lt.IndexDefs[name]
        if li == nil || li.Constraint || li.Inherited || !lt.Indexes[name] || !uses(append(append([]string{li.Predicate}, li.Keys...), li.Include...)...) { continue }
        if ix := declaredIndexes[name]; ix != nil && !indexChanged(ix, lt, name) || ix == nil && !prune { continue }
        stmts = append(stmts, fmt.Sprintf("drop index %s;", pqIdent(schemaPrefixOf(fq)+name)))
        forgetIndex(lt, name)
    }

    declaredFKs, declaredConstraints, columnKeys := map[string]*schema.ForeignKey{}, map[string]*schema.Constraint{}, map[string]bool{}
    for _, fk := range dt.ForeignKeys {
        if fk != nil && len(fk.Columns) > 0 && fk.RefTable != "" { declaredFKs[truncateIdent(fk.Name)] = fk }
    }
    for _, ct := range dt.Constraints {
        if ct != nil && renderConstraintDef(ct) != "" { declaredConstraints[truncateIdent(ct.Name)] = ct }
    }
    for cn, c := range dt.Columns {
        if c != nil && c.Unique { columnKeys[LiveUniqueKeyName(fq, cn, lt.Constraints)] = true }
    }
    for _, name := range sortedKeys(lt.ConstraintDefs) {
        def := lt.ConstraintDefs[name]
        upper := strings.ToUpper(strings.TrimSpace(def))
        if def == "" || lt.ConstraintInherited[name] || columnKeys[name] || strings.HasPrefix(upper, "PRIMARY KEY") || strings.HasPrefix(upper, "NOT NULL") { continue }
        replaced, declared := false, false
        if strings.HasPrefix(upper, "FOREIGN KEY") {
            local, _, _ := strings.Cut(def, ")")
            if !uses(local) { continue }
            fk := declaredFKs[name]
            declared, replaced = fk != nil, fk != nil && !constraintDefsEqual(renderFKDef(fk), def) && !unaddableForeignKey(live, dt, fk)
        } else {
            if !uses(def) { continue }
            ct := declaredConstraints[name]
            declared, replaced = ct != nil, ct != nil && !constraintMatchesLive(lt, name, ct)
        }
        if !replaced && (declared || !prune) { continue }
        stmts = append(stmts, fmt.Sprintf("%s drop constraint %s;", target, pqIdent(name)))
        forgetConstraint(lt, name)
    }
    return stmts, warns
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

// undeclaredDependent names the first of views and objects that desired
// doesn't declare (a view without a query, or a trigger, policy or rule its
// table doesn't list), or returns "".
func undeclaredDependent(desired *schema.Database, fq string, views []string, objects []ColumnDependent) string {
    for _, v := range views {
        if vw := desired.Views[v]; vw == nil || strings.TrimSpace(vw.Query) == "" {
            return "view " + v + " depends on it and isn't declared"
        }
    }
    for _, d := range objects {
        dt := desired.Tables[d.Table]
        declared := false
        if dt != nil {
            switch d.Kind {
            case "trigger":
                for _, tr := range dt.Triggers { declared = declared || (tr != nil && truncateIdent(tr.Name) == d.Name) }
            case "policy":
                for _, p := range dt.Policies { declared = declared || (p != nil && truncateIdent(p.Name) == d.Name) }
            case "rule":
                for _, r := range dt.Rules { declared = declared || (r != nil && truncateIdent(r.Name) == d.Name) }
            }
        }
        if !declared { return fmt.Sprintf("%s %s on %s depends on it and isn't declared", d.Kind, d.Name, d.Table) }
    }
    return ""
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

// restoreViewAttributes returns the statements that give a recreated view
// back the grants, comment, column comments and owner it has live, for each
// one the YAML leaves unmanaged.
func restoreViewAttributes(live *Live, vw *schema.View, v, kind string) []string {
    out := []string{}
    target := kind + " " + pqIdent(v)
    if vw.Grants == nil {
        want := map[string][]string{}
        for role, privs := range live.TableGrants[v] { want[role] = sortedKeys(privs) }
        _, grants := roleGrantStmts(want, nil, func(privs []string) string { return strings.Join(privs, ", ") + " on table " + pqIdent(v) })
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
    if o := live.Owners["rel:"+v]; vw.Owner == "" && o != "" && o != live.CurrentUser {
        out = append(out, fmt.Sprintf("alter %s owner to %s;", target, grantRole(o)))
    }
    return out
}
