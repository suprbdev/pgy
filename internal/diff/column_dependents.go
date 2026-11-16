package diff

import (
    "fmt"
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
// recreates it from the YAML afterwards. pre maps a table to the DROP
// statements to run, dependents first, before its columns change type;
// post restores the grants, comments and owners of recreated views that the
// YAML leaves unmanaged. A dependent the YAML doesn't declare, which pgy
// can't recreate, gets a warning and leaves the column's dependents alone.
func recreateColumnDependents(live *Live, desired *schema.Database, unsafe bool) (out *Live, pre map[string][]string, post []string, warns []Warning) {
    out = live
    if live == nil || desired == nil || len(live.ColumnDependents) == 0 { return out, nil, nil, nil }
    changing := typeChangingColumns(live, desired, unsafe)
    if len(changing) == 0 { return out, nil, nil, nil }

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
                if d.Kind == "view" || d.Kind == "matview" { visit(d.Name) } else { objects = append(objects, d) }
            }
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
    return out, pre, post, warns
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
