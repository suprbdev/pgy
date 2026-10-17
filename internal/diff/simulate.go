package diff

import (
    "strings"

    "github.com/suprbdev/pgy/internal/schema"
)

func Simulate(live *Live, desired *schema.Database) *Live {
    out := cloneLive(live)
    if desired == nil { return out }

    for _, keys := range [][]string{
        mapKeys(desired.Types), mapKeys(desired.Functions), mapKeys(desired.Procedures),
        mapKeys(desired.Tables), mapKeys(desired.Views), mapKeys(desired.Sequences), mapKeys(desired.Domains),
    } {
        for _, k := range keys {
            if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
                out.Schemas[parts[0]] = true
            } else {
                out.Schemas["public"] = true
            }
        }
    }
    for s, g := range desired.SchemaGrants {
        out.SchemaGrants[s] = simulateGrants(out.SchemaGrants[s], g)
    }
    for s, cm := range desired.SchemaComments {
        if cm != "" { out.SchemaComments[s] = cm }
    }

    for _, ext := range desired.Extensions {
        if ext != nil && ext.Name != "" { out.Extensions[ext.Name] = true }
    }

    for name, r := range desired.Roles {
        if r == nil { continue }
        out.Roles[name] = true
        for _, parent := range r.InRoles {
            if parent == "" { continue }
            if out.RoleMembers[name] == nil { out.RoleMembers[name] = map[string]bool{} }
            out.RoleMembers[name][parent] = true
        }
        if r.Comment != "" { out.RoleComments[name] = r.Comment }
    }

    for k, td := range desired.Types {
        if td == nil { continue }
        out.Types[k] = true
        if td.Kind == "enum" {
            have := map[string]bool{}
            for _, l := range out.EnumLabels[k] { have[l] = true }
            for _, l := range td.Labels {
                if !have[l] {
                    out.EnumLabels[k] = append(out.EnumLabels[k], l)
                    have[l] = true
                }
            }
        }
        if td.Comment != "" { out.TypeComments[k] = td.Comment }
    }

    for k, dm := range desired.Domains {
        if dm == nil || dm.Type == "" { continue }
        out.Domains[k] = true
        if dm.Comment != "" { out.TypeComments[k] = dm.Comment }
    }

    for k, sq := range desired.Sequences {
        if sq == nil { continue }
        out.Sequences[k] = true
        if sq.Comment != "" { out.RelComments[k] = sq.Comment }
    }

    for k, f := range desired.Functions {
        if f == nil { continue }
        simulateFunction(out, k, f)
    }

    for k, pr := range desired.Procedures {
        if pr == nil { continue }
        norm := normalizeFunctionSignature(k + pr.ArgsSig)
        existed := out.Procedures[norm]
        lp := &LiveProcedure{Security: "invoker"}
        if prev := out.ProcedureDefs[norm]; prev != nil { lp.Security = prev.Security }
        lp.Body = pr.Body
        if pr.Security != "" { lp.Security = strings.ToLower(pr.Security) }
        out.Procedures[norm] = true
        out.ProcedureDefs[norm] = lp
        if pr.RevokePublic {
            out.FunctionPublicExec[norm] = false
        } else if !existed {
            out.FunctionPublicExec[norm] = true
        }
        if pr.Grants != nil { out.FunctionGrants[norm] = simulateGrants(out.FunctionGrants[norm], pr.Grants) }
        if pr.Comment != "" { out.FunctionComments[norm] = pr.Comment }
    }

    for k, vw := range desired.Views {
        if vw == nil || vw.Query == "" { continue }
        if vw.Materialized {
            out.MatViews[k] = true
        } else {
            out.Views[k] = true
        }
        if vw.Grants != nil { out.TableGrants[k] = simulateGrants(out.TableGrants[k], vw.Grants) }
        if vw.Comment != "" { out.RelComments[k] = vw.Comment }
    }

    for fq, dt := range desired.Tables {
        if dt == nil { continue }
        simulateTable(out, fq, dt)
    }
    return out
}

func simulateFunction(out *Live, k string, f *schema.Function) {
    sig := k + f.ArgsSig
    norm := normalizeFunctionSignature(sig)
    prev := out.FunctionDefs[norm]
    hasBody := strings.TrimSpace(f.Body) != ""
    if prev != nil || hasBody {
        lf := &LiveFunction{Volatility: "volatile", Security: "invoker"}
        replaced := hasBody && (prev == nil || strings.TrimSpace(f.Body) != strings.TrimSpace(prev.Body))
        if prev != nil && !replaced { *lf = *prev }
        if hasBody { lf.Body = f.Body }
        if f.Volatility != "" { lf.Volatility = strings.ToLower(f.Volatility) }
        if f.Security != "" { lf.Security = strings.ToLower(f.Security) }
        if f.Strict != nil { lf.Strict = *f.Strict }
        if f.Leakproof != nil { lf.Leakproof = *f.Leakproof }
        out.Functions[sig] = true
        out.FunctionDefs[norm] = lf
        if prev == nil { out.FunctionPublicExec[norm] = true }
    } else {
        out.Functions[sig] = true
    }
    if f.RevokePublic { out.FunctionPublicExec[norm] = false }
    if f.Grants != nil { out.FunctionGrants[norm] = simulateGrants(out.FunctionGrants[norm], f.Grants) }
    if f.Comment != "" { out.FunctionComments[norm] = f.Comment }
}

func simulateTable(out *Live, fq string, dt *schema.Table) {
    lt := out.Tables[fq]
    if lt == nil {
        lt = &LiveTable{}
        out.Tables[fq] = lt
    }
    ensureLiveTableMaps(lt)

    for cn, c := range dt.Columns {
        if c == nil { continue }
        lc := lt.Columns[cn]
        if lc == nil {
            lc = &LiveColumn{}
            lt.Columns[cn] = lc
        }
        if c.Type != "" { lc.Type = c.Type }
        lc.Nullable = c.Nullable
        lc.Default = c.Default
        if c.Identity != "" { lc.Identity = c.Identity }
        if c.Comment != "" { lc.Comment = c.Comment }
        if c.Unique {
            lt.Constraints[strings.ReplaceAll(fq+"_"+cn, ".", "_")+"_key"] = true
        }
        if c.PrimaryKey { lt.HasPK = true }
        if c.Grants != nil {
            if out.ColumnGrants[fq] == nil { out.ColumnGrants[fq] = map[string]map[string]map[string]bool{} }
            out.ColumnGrants[fq][cn] = simulateGrants(out.ColumnGrants[fq][cn], c.Grants)
        }
    }
    if len(dt.PrimaryKey) > 0 { lt.HasPK = true }

    for _, fk := range dt.ForeignKeys {
        if fk == nil || len(fk.Columns) == 0 || fk.RefTable == "" { continue }
        lt.Constraints[fk.Name] = true
        delete(lt.ConstraintDefs, fk.Name)
        if fk.Comment != "" { lt.ConstraintComments[fk.Name] = fk.Comment }
    }
    for _, ix := range dt.Indexes {
        if ix == nil || len(ix.Columns) == 0 { continue }
        name := indexName(fq, ix)
        lt.Indexes[name] = true
        delete(lt.IndexOpclasses, name)
        if ix.Comment != "" { lt.IndexComments[name] = ix.Comment }
    }
    for _, ct := range dt.Constraints {
        if ct == nil || ct.Type == "" || renderConstraintDef(ct) == "" { continue }
        lt.Constraints[ct.Name] = true
        delete(lt.ConstraintDefs, ct.Name)
    }
    for _, tr := range dt.Triggers {
        if tr == nil || tr.Procedure == "" { continue }
        lt.Triggers[tr.Name] = ""
    }
    for _, pol := range dt.Policies {
        if pol == nil || pol.Name == "" { continue }
        cmd := strings.ToLower(pol.For)
        if cmd == "" { cmd = "all" }
        lt.Policies[pol.Name] = &LivePolicy{
            Cmd:       cmd,
            Roles:     append([]string(nil), pol.To...),
            Using:     pol.Using,
            WithCheck: pol.WithCheck,
        }
    }
    if dt.RowLevelSecurity != nil { lt.RLSEnabled = *dt.RowLevelSecurity }
    if dt.Grants != nil { out.TableGrants[fq] = simulateGrants(out.TableGrants[fq], dt.Grants) }
    if dt.Comment != "" { out.RelComments[fq] = dt.Comment }
}

func simulateGrants(live map[string]map[string]bool, desired map[string][]string) map[string]map[string]bool {
    out := map[string]map[string]bool{}
    for role, privs := range desired {
        out[role] = map[string]bool{}
        for _, p := range privs { out[role][strings.ToLower(p)] = true }
    }
    for priv := range live["public"] {
        if out["public"] == nil { out["public"] = map[string]bool{} }
        out["public"][priv] = true
    }
    return out
}

func mapKeys[V any](m map[string]V) []string {
    out := make([]string, 0, len(m))
    for k := range m { out = append(out, k) }
    return out
}

func cloneMap[V any](m map[string]V, cp func(V) V) map[string]V {
    out := make(map[string]V, len(m))
    for k, v := range m {
        if cp != nil { v = cp(v) }
        out[k] = v
    }
    return out
}

func cloneSet(m map[string]bool) map[string]bool { return cloneMap(m, nil) }

func cloneACL(m map[string]map[string]bool) map[string]map[string]bool { return cloneMap(m, cloneSet) }

func cloneGrantMap(m map[string]map[string]map[string]bool) map[string]map[string]map[string]bool {
    return cloneMap(m, cloneACL)
}

func cloneLive(l *Live) *Live {
    if l == nil { l = &Live{} }
    return &Live{
        Schemas:            cloneSet(l.Schemas),
        Tables:             cloneMap(l.Tables, cloneLiveTable),
        Types:              cloneSet(l.Types),
        Functions:          cloneSet(l.Functions),
        Extensions:         cloneSet(l.Extensions),
        Views:              cloneSet(l.Views),
        MatViews:           cloneSet(l.MatViews),
        Sequences:          cloneSet(l.Sequences),
        Domains:            cloneSet(l.Domains),
        Roles:              cloneSet(l.Roles),
        RoleMembers:        cloneMap(l.RoleMembers, cloneSet),
        RoleComments:       cloneMap(l.RoleComments, nil),
        TableGrants:        cloneGrantMap(l.TableGrants),
        FunctionGrants:     cloneGrantMap(l.FunctionGrants),
        SchemaGrants:       cloneGrantMap(l.SchemaGrants),
        ColumnGrants:       cloneMap(l.ColumnGrants, cloneGrantMap),
        FunctionPublicExec: cloneSet(l.FunctionPublicExec),
        RelComments:        cloneMap(l.RelComments, nil),
        FunctionComments:   cloneMap(l.FunctionComments, nil),
        TypeComments:       cloneMap(l.TypeComments, nil),
        SchemaComments:     cloneMap(l.SchemaComments, nil),
        FunctionDefs: cloneMap(l.FunctionDefs, func(f *LiveFunction) *LiveFunction {
            if f == nil { return nil }
            c := *f
            return &c
        }),
        Procedures: cloneSet(l.Procedures),
        ProcedureDefs: cloneMap(l.ProcedureDefs, func(p *LiveProcedure) *LiveProcedure {
            if p == nil { return nil }
            c := *p
            return &c
        }),
        EnumLabels: cloneMap(l.EnumLabels, func(v []string) []string { return append([]string(nil), v...) }),
    }
}

func cloneLiveTable(t *LiveTable) *LiveTable {
    if t == nil { return nil }
    c := *t
    c.Columns = cloneMap(t.Columns, func(lc *LiveColumn) *LiveColumn {
        if lc == nil { return nil }
        cp := *lc
        return &cp
    })
    c.Constraints = cloneSet(t.Constraints)
    c.ConstraintDefs = cloneMap(t.ConstraintDefs, nil)
    c.Indexes = cloneSet(t.Indexes)
    if t.IndexOpclasses != nil {
        c.IndexOpclasses = cloneMap(t.IndexOpclasses, func(v []LiveIndexOpclass) []LiveIndexOpclass {
            return append([]LiveIndexOpclass(nil), v...)
        })
    }
    c.Triggers = cloneMap(t.Triggers, nil)
    c.Policies = cloneMap(t.Policies, func(p *LivePolicy) *LivePolicy {
        if p == nil { return nil }
        cp := *p
        cp.Roles = append([]string(nil), p.Roles...)
        return &cp
    })
    c.IndexComments = cloneMap(t.IndexComments, nil)
    c.ConstraintComments = cloneMap(t.ConstraintComments, nil)
    return &c
}

func ensureLiveTableMaps(t *LiveTable) {
    if t.Columns == nil { t.Columns = map[string]*LiveColumn{} }
    if t.Constraints == nil { t.Constraints = map[string]bool{} }
    if t.ConstraintDefs == nil { t.ConstraintDefs = map[string]string{} }
    if t.Indexes == nil { t.Indexes = map[string]bool{} }
    if t.Triggers == nil { t.Triggers = map[string]string{} }
    if t.Policies == nil { t.Policies = map[string]*LivePolicy{} }
    if t.IndexComments == nil { t.IndexComments = map[string]string{} }
    if t.ConstraintComments == nil { t.ConstraintComments = map[string]string{} }
}
