package diff

import (
    "fmt"
    "strings"

    "github.com/suprbdev/pgy/internal/schema"
)

// planViewChanges acts on live.ViewProbes. A plain view whose query changed
// but whose columns CREATE OR REPLACE VIEW can keep, or whose query couldn't
// be compared, goes in replace. A plain view whose columns changed, or a
// materialized view whose query changed, needs DROP and CREATE, together
// with the views that select from it: under --unsafe the returned live is a
// copy without them, so they are planned as new (with their comments,
// grants, owner, options, triggers, rules and indexes), and drops holds the
// DROP statements to run first; without --unsafe each gets a warning. A
// view with a dependent the YAML doesn't declare is left alone with a
// warning, since dropping it would drop the dependent too.
func planViewChanges(live *Live, desired *schema.Database, unsafe bool) (out *Live, replace map[string]bool, drops []string, warns []Warning) {
    out, replace = live, map[string]bool{}
    if live == nil || live.ViewProbes == nil || desired == nil { return out, replace, nil, nil }
    recreate := []string{}
    for _, k := range sortedKeys(desired.Views) {
        vw, p := desired.Views[k], live.ViewProbes[k]
        if vw == nil || p == nil || !p.Changed() || vw.Replace && !vw.Materialized { continue }
        switch {
        case !vw.Materialized && p.ColumnsCompatible():
            replace[k] = true
        case !vw.Materialized && p.Err != "":
            replace[k] = true
            warns = append(warns, Warning{Message: fmt.Sprintf("view %s: its query couldn't be compared with the live view (%s), so it is replaced", k, p.Err)})
        case vw.Materialized && p.Err != "":
            warns = append(warns, Warning{Message: fmt.Sprintf("materialized view %s: its query couldn't be compared with the live one (%s), so it is left as it is", k, p.Err)})
        case !unsafe && vw.Materialized:
            warns = append(warns, Warning{Message: fmt.Sprintf("materialized view %s: its query changed; that drops and recreates it, with the views that use it, which needs --unsafe", k)})
        case !unsafe:
            warns = append(warns, Warning{Message: fmt.Sprintf("view %s: its columns changed from (%s) to (%s), which CREATE OR REPLACE VIEW can't do; that drops and recreates it, with the views that use it, which needs --unsafe", k, strings.Join(p.LiveCols, ", "), strings.Join(p.DesiredCols, ", "))})
        default:
            recreate = append(recreate, k)
        }
    }
    if len(recreate) == 0 { return out, replace, nil, warns }

    closure, order := map[string]bool{}, []string{}
    var visit func(k string)
    visit = func(k string) {
        if closure[k] { return }
        closure[k] = true
        for _, dep := range live.ViewDependents[k] { visit(dep) }
        order = append(order, k)
    }
    for _, k := range recreate {
        group, groupOrder := map[string]bool{}, []string{}
        var collect func(k string)
        collect = func(k string) {
            if group[k] { return }
            group[k] = true
            for _, dep := range live.ViewDependents[k] { collect(dep) }
            groupOrder = append(groupOrder, k)
        }
        collect(k)
        undeclared := []string{}
        for _, v := range groupOrder {
            if desired.Views[v] == nil { undeclared = append(undeclared, v) }
        }
        if len(undeclared) > 0 {
            warns = append(warns, Warning{Message: fmt.Sprintf("view %s: its definition changed, but %s, which the YAML doesn't declare, depends on it, and dropping it would drop that too; recreate them in a hand-written migration", k, strings.Join(undeclared, ", "))})
            continue
        }
        visit(k)
    }
    if len(order) == 0 { return out, replace, nil, warns }

    out = cloneLive(live)
    for _, k := range order {
        kind := "view"
        if live.MatViews[k] {
            kind = "materialized view"
            warns = append(warns, Warning{Table: k, Kind: "lock", Message: fmt.Sprintf("recreating materialized view %s refills it under an ACCESS EXCLUSIVE lock, and queries that read it fail between the drop and the create", k)})
        }
        drops = append(drops, fmt.Sprintf("drop %s if exists %s;", kind, pqIdent(k)))
        delete(out.Views, k)
        delete(out.MatViews, k)
        delete(out.MatViewTables, k)
        delete(out.ViewTables, k)
        delete(out.ViewOptions, k)
        delete(out.ViewColumnComments, k)
        delete(out.ViewColumnDefaults, k)
        delete(out.RelComments, k)
        delete(out.TableGrants, k)
        delete(out.Owners, "rel:"+k)
        delete(out.ViewProbes, k)
        delete(replace, k)
    }
    return out, replace, drops, warns
}
