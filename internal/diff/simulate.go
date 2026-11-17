package diff

import (
    "sort"
    "strconv"
    "strings"

    "github.com/suprbdev/pgy/internal/schema"
)

func Simulate(live *Live, desired *schema.Database) *Live {
    out := cloneLive(live.scoped())
    if desired == nil { return out }
    out, _ = applyRenames(out, desired)
    if desired.DatabaseSettings != nil { out.DatabaseConfig = simulateConfig(desired.DatabaseSettings) }
    for role, set := range desired.DatabaseRoleSettings {
        if out.DatabaseRoleConfig == nil { out.DatabaseRoleConfig = map[string]map[string]string{} }
        out.DatabaseRoleConfig[resolveRole(role, out.CurrentUser)] = simulateConfig(set)
    }
    for fq, dt := range desired.Tables {
        if dt != nil && dt.Data != nil {
            if out.TableData == nil { out.TableData = map[string]*LiveTableData{} }
            out.TableData[fq] = &LiveTableData{Changed: map[int][]string{}}
        }
    }

    for _, keys := range [][]string{
        mapKeys(desired.Types), mapKeys(desired.Functions), mapKeys(desired.Procedures),
        mapKeys(desired.Tables), mapKeys(desired.Views), mapKeys(desired.Sequences), mapKeys(desired.Domains), mapKeys(desired.Collations), mapKeys(desired.TSConfigs), mapKeys(desired.TSDictionaries),
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
        if ext == nil || ext.Name == "" { continue }
        le := &LiveExtension{Relocatable: true, Created: !out.Extensions[ext.Name]}
        out.Extensions[ext.Name] = true
        prev := out.ExtensionDefs[ext.Name]
        if prev != nil { *le = *prev }
        if ext.Schema != "" {
            out.Schemas[ext.Schema] = true
            if prev == nil || prev.Relocatable {
                if prev != nil && prev.Schema != ext.Schema && le.MovedFrom == "" { le.MovedFrom = prev.Schema }
                le.Schema = ext.Schema
            }
        }
        if ext.Version != "" { le.Version = ext.Version }
        if ext.Comment != "" { le.Comment = ext.Comment }
        out.ExtensionDefs[ext.Name] = le
    }

    for name, r := range desired.Roles {
        if r == nil { continue }
        out.Roles[name] = true
        lr := &LiveRole{Inherit: true, ConnectionLimit: -1}
        if prev := out.RoleDefs[name]; prev != nil { *lr = *prev }
        if prev := out.RoleDefs[name]; prev == nil || len(r.Managed) > 0 {
            set := func(key string, dst *bool, v bool) { if prev == nil || r.Managed[key] { *dst = v } }
            set("login", &lr.Login, r.Login)
            set("superuser", &lr.Superuser, r.Superuser)
            set("createdb", &lr.CreateDB, r.CreateDB)
            set("createrole", &lr.CreateRole, r.CreateRole)
            set("replication", &lr.Replication, r.Replication)
            set("bypassRLS", &lr.BypassRLS, r.BypassRLS)
            set("inherit", &lr.Inherit, !r.NoInherit)
            if prev == nil || r.Managed["connectionLimit"] { lr.ConnectionLimit = r.ConnectionLimit }
        }
        if r.ValidUntil != "" && (out.RoleDefs[name] == nil || r.Managed["validUntil"]) { lr.ValidUntil = normalizeValidUntil(r.ValidUntil) }
        if r.Set != nil { lr.Config = simulateConfig(r.Set) }
        out.RoleDefs[name] = lr
        for _, parent := range r.InRoles {
            if parent == "" { continue }
            if out.RoleMembers[name] == nil { out.RoleMembers[name] = map[string]bool{} }
            out.RoleMembers[name][parent] = true
            if out.RoleMemberOptions == nil { out.RoleMemberOptions = map[string]map[string]*LiveMembership{} }
            if out.RoleMemberOptions[name] == nil { out.RoleMemberOptions[name] = map[string]*LiveMembership{} }
            lm := &LiveMembership{Inherit: lr.Inherit, Set: true}
            if prevM := out.RoleMemberOptions[name][parent]; prevM != nil { *lm = *prevM }
            if o := r.MemberOptions[parent]; o != nil {
                if o.Admin != nil { lm.Admin = *o.Admin }
                if o.Inherit != nil { lm.Inherit = *o.Inherit }
                if o.Set != nil { lm.Set = *o.Set }
            }
            out.RoleMemberOptions[name][parent] = lm
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
        if td.Kind == "composite" {
            attrs := map[string]string{}
            for n, t := range out.CompositeAttributes[k] { attrs[n] = t }
            for n, t := range td.Attributes { attrs[n] = t }
            out.CompositeAttributes[k] = attrs
            if td.AttributeCollations != nil {
                if out.CompositeCollations == nil { out.CompositeCollations = map[string]map[string]string{} }
                out.CompositeCollations[k] = cloneMap(td.AttributeCollations, nil)
            }
            if td.AttributeComments != nil {
                if out.CompositeComments == nil { out.CompositeComments = map[string]map[string]string{} }
                out.CompositeComments[k] = cloneMap(td.AttributeComments, nil)
            }
        }
        if td.Comment != "" { out.TypeComments[k] = td.Comment }
        if td.Grants != nil { out.TypeGrants[k] = simulateGrants(out.TypeGrants[k], td.Grants) }
        if td.RevokePublic {
            if out.TypePublicRevoked == nil { out.TypePublicRevoked = map[string]bool{} }
            out.TypePublicRevoked[k] = true
        }
    }

    simulateOwnersAndDefaults(out, desired)
    simulateLanguages(out, desired)
    simulateWrappers(out, desired)

    for name, s := range desired.Servers {
        if s == nil { continue }
        ls := &LiveServer{Wrapper: s.Wrapper, Type: s.Type, Version: s.Version, Owner: s.Owner, Comment: s.Comment}
        if prev := out.Servers[name]; prev != nil {
            ls.UserMapping, ls.UserMappings, ls.Options = prev.UserMapping, prev.UserMappings, prev.Options
            if s.Version == "" { ls.Version = prev.Version }
            if s.Owner == "" { ls.Owner = prev.Owner }
            if s.Comment == "" { ls.Comment = prev.Comment }
        } else if s.Owner == "" {
            ls.Owner = out.CurrentUser
        }
        if s.Options != nil { ls.Options = cloneMap(s.Options, nil) }
        if prev := out.Servers[name]; prev != nil { ls.Grants = prev.Grants }
        if s.Grants != nil { ls.Grants = simulateGrants(ls.Grants, s.Grants) }
        if out.Servers == nil { out.Servers = map[string]*LiveServer{} }
        out.Servers[name] = ls
    }
    for name, p := range desired.Publications {
        if p == nil { continue }
        lp := &LivePublication{AllTables: p.AllTables, Tables: map[string]LivePublicationTable{}, Publish: []string{"insert", "update", "delete", "truncate"}, Comment: p.Comment}
        if prev := out.Publications[truncateIdent(name)]; prev != nil && prev.AllTables == p.AllTables {
            lp.Publish, lp.ViaRoot, lp.GeneratedColumns = prev.Publish, prev.ViaRoot, prev.GeneratedColumns
            if p.Comment == "" { lp.Comment = prev.Comment }
        }
        lp.Owner = out.CurrentUser
        if prev := out.Publications[truncateIdent(name)]; prev != nil { lp.Owner = prev.Owner }
        if p.Owner != "" { lp.Owner = resolveRole(p.Owner, out.CurrentUser) }
        for _, t := range p.Tables {
            cols := append([]string(nil), t.Columns...)
            sort.Strings(cols)
            if len(cols) == 0 { cols = nil }
            lp.Tables[t.Table] = LivePublicationTable{Columns: cols, Where: t.Where, Only: t.Only}
        }
        lp.Schemas = append([]string(nil), p.Schemas...)
        sort.Strings(lp.Schemas)
        if p.Publish != nil {
            order := map[string]int{"insert": 0, "update": 1, "delete": 2, "truncate": 3}
            lp.Publish = append([]string(nil), p.Publish...)
            sort.Slice(lp.Publish, func(i, j int) bool { return order[lp.Publish[i]] < order[lp.Publish[j]] })
        }
        if p.ViaPartitionRoot != nil { lp.ViaRoot = *p.ViaPartitionRoot }
        if p.GeneratedColumns != "" { lp.GeneratedColumns = p.GeneratedColumns }
        out.Publications[truncateIdent(name)] = lp
    }

    for name, et := range desired.EventTriggers {
        if et == nil || et.Event == "" || et.Function == "" { continue }
        le := &LiveEventTrigger{Event: strings.ToLower(et.Event), Function: qualifyName(strings.TrimSuffix(strings.TrimSpace(et.Function), "()")), Enabled: "origin", Comment: et.Comment}
        for _, t := range et.Tags { le.Tags = append(le.Tags, strings.ToUpper(strings.TrimSpace(t))) }
        if et.Enabled != "" { le.Enabled = et.Enabled }
        out.EventTriggers[truncateIdent(name)] = le
    }

    for k, td := range desired.TSDictionaries {
        if td == nil || td.Template == "" { continue }
        out.TSDictionaries[k] = true
        if td.Comment != "" { out.TSComments["dict:"+k] = td.Comment }
        ld := &schema.TSDictionary{Template: strings.TrimPrefix(td.Template, "pg_catalog.")}
        if prev := out.TSDictionaryDefs[k]; prev != nil { ld.Options = prev.Options }
        if td.Options != nil {
            ld.Options = map[string]string{}
            for o, v := range td.Options { ld.Options[strings.ToLower(o)] = v }
        }
        if out.TSDictionaryDefs == nil { out.TSDictionaryDefs = map[string]*schema.TSDictionary{} }
        out.TSDictionaryDefs[k] = ld
    }
    for k, tc := range desired.TSConfigs {
        if tc == nil || (tc.Copy == "" && tc.Parser == "" && out.TSConfigs[k] == nil) { continue }
        m := map[string][]string{}
        for tok, d := range out.TSConfigs[k] { m[tok] = d }
        for tok, d := range tc.Mappings {
            if len(d) == 0 { delete(m, tok) } else { m[tok] = append([]string(nil), d...) }
        }
        out.TSConfigs[k] = m
        if tc.Comment != "" { out.TSComments["config:"+k] = tc.Comment }
    }

    for k, co := range desired.Collations {
        if co == nil { continue }
        out.Collations[k] = true
        if co.Comment != "" { out.CollationComments[k] = co.Comment }
    }

    for k, dm := range desired.Domains {
        if dm == nil || dm.Type == "" { continue }
        out.Domains[k] = true
        ld := &LiveDomain{Default: dm.Default, NotNull: dm.NotNull, Checks: map[string]string{}}
        for name, e := range desiredDomainChecks(k, dm) { ld.Checks[name] = "CHECK (" + e + ")" }
        ld.CheckComments = map[string]string{}
        for name, c := range dm.CheckComments { ld.CheckComments[truncateIdent(name)] = c }
        out.DomainDefs[k] = ld
        if dm.Grants != nil { out.TypeGrants[k] = simulateGrants(out.TypeGrants[k], dm.Grants) }
        if dm.RevokePublic {
            if out.TypePublicRevoked == nil { out.TypePublicRevoked = map[string]bool{} }
            out.TypePublicRevoked[k] = true
        }
        if dm.Comment != "" { out.TypeComments[k] = dm.Comment }
    }

    for k, sq := range desired.Sequences {
        if sq == nil { continue }
        out.Sequences[k] = true
        if sq.OwnedBy != "" && !strings.EqualFold(strings.TrimSpace(sq.OwnedBy), "none") { out.OwnedSequences[k] = true }
        ls := &LiveSequence{}
        if prev := out.SequenceDefs[k]; prev != nil { *ls = *prev }
        for _, o := range []struct{ dst *string; v string }{
            {&ls.Type, sq.As}, {&ls.Increment, sq.Increment}, {&ls.MinValue, sq.MinValue},
            {&ls.MaxValue, sq.MaxValue}, {&ls.Start, sq.Start}, {&ls.Cache, sq.Cache},
        } {
            if o.v != "" { *o.dst = strings.TrimSpace(o.v) }
        }
        if sq.Cycle != nil { ls.Cycle = *sq.Cycle }
        if sq.Unlogged != nil { ls.Unlogged = *sq.Unlogged }
        if sq.OwnedBy != "" { ls.Owner = qualifyOwnedBy(k, sq.OwnedBy) }
        out.SequenceDefs[k] = ls
        if sq.Comment != "" { out.RelComments[k] = sq.Comment }
        if sq.Grants != nil { out.TableGrants[k] = simulateGrants(out.TableGrants[k], sq.Grants) }
    }

    for k, f := range desired.Functions {
        if f == nil { continue }
        simulateFunction(out, k, f)
    }

    for k, pr := range desired.Procedures {
        if pr == nil { continue }
        norm := normalizeFunctionSignature(schema.RoutineName(k) + pr.ArgsSig)
        existed := out.Procedures[norm]
        lp := &LiveProcedure{Signature: schema.RoutineName(k) + pr.ArgsSig, Security: "invoker"}
        prev := out.ProcedureDefs[norm]
        if prev != nil { lp.Security = prev.Security }
        if prev == nil || procedureChanged(pr, prev) {
            lp.Config = simulateConfig(pr.Set)
        } else {
            lp.Config = prev.Config
        }
        lp.Body, lp.SQLBody = pr.Body, schema.IsSQLStandardBody(pr.Language, pr.Body)
        lp.Language, _ = schema.SplitLanguage(pr.Language)
        lp.Transform = schema.RoutineTransform(pr.Language, pr.Transform)
        if lp.Transform == nil && prev != nil { lp.Transform = prev.Transform }
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

    for k, ag := range desired.Aggregates {
        if ag == nil || ag.SFunc == "" { continue }
        norm := normalizeFunctionSignature(schema.RoutineName(k) + aggregateArgs(ag.ArgsSig))
        parallel := ag.Parallel
        if parallel == "" { parallel = "unsafe" }
        opts := map[string]string{}
        for _, o := range schema.AggregateOptions {
            v := ag.Options[o[1]]
            switch {
            case strings.HasSuffix(o[1], "_extra"):
                v = strconv.FormatBool(strings.EqualFold(v, "true"))
            case strings.HasSuffix(o[1], "_modify") && v == "":
                v = "read_only"
            case strings.HasSuffix(o[1], "space") && v == "":
                v = "0"
            }
            opts[o[1]] = v
        }
        _, minitcond := ag.Options["minitcond"]
        out.AggregateDefs[norm] = &LiveAggregate{SFunc: ag.SFunc, SType: ag.SType, FinalFunc: ag.FinalFunc, InitCond: ag.InitCond, HasInitCond: ag.HasInitCond(), HasMInitCond: minitcond, CombineFunc: ag.CombineFunc, Parallel: parallel, Options: opts}
        out.FunctionDefs[norm] = &LiveFunction{Signature: schema.RoutineName(k) + aggregateArgs(ag.ArgsSig), Kind: "a"}
        if ag.Comment != "" { out.FunctionComments[norm] = ag.Comment }
    }

    for k, vw := range desired.Views {
        if vw == nil { continue }
        if vw.Query == "" {
            if vw.Materialized { out.MatViews[k] = true } else { out.Views[k] = true }
        } else if vw.Materialized {
            out.MatViews[k] = true
            delete(out.ViewProbes, k)
            if vw.With != nil {
                lt := out.MatViewTables[k]
                if lt == nil { lt = &LiveTable{}; out.MatViewTables[k] = lt }
                lt.Options = map[string]string{}
                for o, v := range vw.With { lt.Options[strings.ToLower(o)] = v }
            }
            if vw.Tablespace != "" {
                lt := out.MatViewTables[k]
                if lt == nil { lt = &LiveTable{}; out.MatViewTables[k] = lt }
                lt.Tablespace = vw.Tablespace
            }
            if len(vw.Indexes) > 0 {
                lt := out.MatViewTables[k]
                if lt == nil { lt = &LiveTable{}; out.MatViewTables[k] = lt }
                ensureLiveTableMaps(lt)
                for _, ix := range vw.Indexes {
                    if ix == nil || len(ix.Columns) == 0 { continue }
                    name := indexName(k, ix)
                    lt.Indexes[name] = true
                    delete(lt.IndexOpclasses, name)
                    delete(lt.IndexDefs, name)
                    if ix.Comment != "" { lt.IndexComments[name] = ix.Comment }
                }
            }
        } else {
            out.Views[k] = true
            delete(out.ViewProbes, k)
            if len(vw.Triggers)+len(vw.Rules) > 0 {
                vt := out.ViewTables[k]
                if vt == nil { vt = &LiveTable{}; out.ViewTables[k] = vt }
                ensureLiveTableMaps(vt)
                for _, r := range vw.Rules {
                    if r == nil || r.Event == "" || r.Do == "" { continue }
                    vt.Rules[truncateIdent(r.Name)] = &LiveRule{Event: strings.ToLower(r.Event), Instead: r.Instead, Where: r.Where}
                    if r.Comment != "" { vt.RuleComments[truncateIdent(r.Name)] = r.Comment }
                }
                for _, tr := range vw.Triggers {
                    if tr == nil || tr.Procedure == "" { continue }
                    vt.Triggers[truncateIdent(tr.Name)] = ""
                    if tr.Comment != "" { vt.TriggerComments[truncateIdent(tr.Name)] = tr.Comment }
                }
            }
            if opts := viewOptions(vw); len(opts) > 0 {
                vo := map[string]string{}
                for name, v := range out.ViewOptions[k] { vo[name] = v }
                for _, o := range opts {
                    if o[1] == "" { delete(vo, o[0]) } else { vo[o[0]] = o[1] }
                }
                out.ViewOptions[k] = vo
            }
        }
        if vw.Grants != nil { out.TableGrants[k] = simulateGrants(out.TableGrants[k], vw.Grants) }
        if vw.Comment != "" { out.RelComments[k] = vw.Comment }
        for col, cm := range vw.ColumnComments {
            if out.ViewColumnComments[k] == nil { out.ViewColumnComments[k] = map[string]string{} }
            out.ViewColumnComments[k][col] = cm
        }
        for col, def := range vw.ColumnDefaults {
            if out.ViewColumnDefaults == nil { out.ViewColumnDefaults = map[string]map[string]string{} }
            if out.ViewColumnDefaults[k] == nil { out.ViewColumnDefaults[k] = map[string]string{} }
            out.ViewColumnDefaults[k][col] = def
        }
    }

    for fq, dt := range desired.Tables {
        if dt == nil { continue }
        simulateTable(out, fq, dt)
    }
    return out
}

func simulateFunction(out *Live, k string, f *schema.Function) {
    sig := schema.RoutineName(k) + f.ArgsSig
    norm := normalizeFunctionSignature(sig)
    prev := out.FunctionDefs[norm]
    hasBody := f.HasBody()
    if prev != nil || hasBody {
        lf := &LiveFunction{Signature: sig, Kind: "f", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: defaultFunctionCost(f.Language), Rows: defaultFunctionRows(f.Returns)}
        replaced := hasBody && (prev == nil || strings.TrimSpace(f.Body) != strings.TrimSpace(prev.Body) ||
            routineLanguageChanged(f.Language, prev.Language) || transformChanged(schema.RoutineTransform(f.Language, f.Transform), prev.Transform))
        if prev != nil && !replaced { *lf = *prev }
        if hasBody {
            lf.Body, lf.Returns, lf.SQLBody = f.Body, f.Returns, schema.IsSQLStandardBody(f.Language, f.Body)
            lf.Language, _ = schema.SplitLanguage(f.Language)
            if t := schema.RoutineTransform(f.Language, f.Transform); t != nil {
                lf.Transform = t
            } else if prev != nil {
                lf.Transform = prev.Transform
            }
        }
        if f.Volatility != "" { lf.Volatility = strings.ToLower(f.Volatility) }
        if f.Security != "" { lf.Security = strings.ToLower(f.Security) }
        if f.Strict != nil { lf.Strict = *f.Strict }
        if f.Leakproof != nil { lf.Leakproof = *f.Leakproof }
        if replaced {
            lf.Parallel, lf.Cost, lf.Rows = "unsafe", defaultFunctionCost(f.Language), defaultFunctionRows(f.Returns)
        }
        if f.Parallel != "" { lf.Parallel = f.Parallel }
        if f.Cost != nil { lf.Cost = *f.Cost }
        if f.Rows != nil { lf.Rows = *f.Rows }
        if f.Set != nil || replaced { lf.Config = simulateConfig(f.Set) }
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

// simulateConfig is the proconfig a CREATE OR REPLACE or ALTER ... SET with
// the given YAML set block leaves behind.
func simulateConfig(set map[string]string) map[string]string {
    out := map[string]string{}
    for k, v := range set { out[strings.ToLower(k)] = normalizeConfigValue(v) }
    return out
}

func simulateTable(out *Live, fq string, dt *schema.Table) {
    lt := out.Tables[fq]
    if foreignKindMismatch(dt, lt) { return }
    if lt == nil {
        lt = &LiveTable{}
        out.Tables[fq] = lt
    }
    ensureLiveTableMaps(lt)

    for cn, c := range dt.Columns {
        if c == nil { continue }
        lc := lt.Columns[cn]
        override := inheritedColumn(dt, c, lc)
        if lc == nil {
            lc = &LiveColumn{Inherited: override}
            lt.Columns[cn] = lc
        }
        if c.Type != "" { lc.Type = c.Type }
        if !override || c.NullableSet {
            lc.Nullable = c.Nullable
            if !c.Nullable { lc.NotNullNotValid = "" }
            if !c.Nullable && c.NotValid { lc.NotNullNotValid = "simulated" }
            if c.Nullable { lc.NotNullName = "" } else if !override && c.NotNullName != "" { lc.NotNullName = truncateIdent(c.NotNullName) }
        }
        if !override || c.Default != "" { lc.Default = c.Default }
        if c.Identity != "" {
            lc.Identity = c.Identity
            ls := &LiveSequence{}
            if lc.IdentitySequence != nil { *ls = *lc.IdentitySequence }
            if o := c.IdentityOptions; o != nil {
                for _, f := range []struct{ dst *string; v string }{{&ls.Increment, o.Increment}, {&ls.MinValue, o.MinValue}, {&ls.MaxValue, o.MaxValue}, {&ls.Start, o.Start}, {&ls.Cache, o.Cache}} {
                    if v := strings.TrimSpace(f.v); v != "" { *f.dst = v }
                }
                if o.Cycle != nil { ls.Cycle = *o.Cycle }
                if o.Name != "" { ls.Name = IdentitySequenceName(fq, o.Name) }
            }
            lc.IdentitySequence = ls
        }
        if c.Generated != "" { lc.Generated, lc.Default, lc.Virtual = c.Generated, "", c.Virtual }
        if c.Collate != "" { lc.Collation = collationName(c.Collate) }
        if c.Compression == "default" {
            lc.Compression = ""
        } else if c.Compression != "" {
            lc.Compression = c.Compression
        }
        if c.Storage != "" { lc.Storage = c.Storage }
        if c.StatisticsTarget != nil {
            lc.StatisticsTarget = nil
            if v := *c.StatisticsTarget; v >= 0 { lc.StatisticsTarget = &v }
        }
        for name, v := range map[string]*float64{"n_distinct": c.NDistinct, "n_distinct_inherited": c.NDistinctInherited} {
            if v == nil { continue }
            opts := map[string]string{}
            for k, ov := range lc.Options { opts[k] = ov }
            delete(opts, name)
            if *v != 0 { opts[name] = strconv.FormatFloat(*v, 'g', -1, 64) }
            lc.Options = opts
        }
        if c.FDWOptions != nil {
            lc.FDWOptions = cloneMap(c.FDWOptions, nil)
        }
        if c.Comment != "" { lc.Comment = c.Comment }
        if c.Unique {
            lt.Constraints[uniqueKeyName(fq, cn)] = true
        }
        if c.PrimaryKey { lt.HasPK = true }
        if c.Grants != nil {
            if out.ColumnGrants[fq] == nil { out.ColumnGrants[fq] = map[string]map[string]map[string]bool{} }
            out.ColumnGrants[fq][cn] = simulateGrants(out.ColumnGrants[fq][cn], c.Grants)
        }
    }
    if len(dt.PrimaryKey) > 0 { lt.HasPK = true }
    if lt.HasPK {
        name, old := truncateIdent(dt.PrimaryKeyName), LivePrimaryKeyName(lt)
        if name == "" { name = old }
        if name == "" { name = DefaultPrimaryKeyName(fq) }
        switch {
        case old == "":
            lt.ConstraintDefs[name] = "PRIMARY KEY"
            lt.Constraints[name] = true
        case old != name:
            lt.ConstraintDefs[name], lt.Constraints[name] = lt.ConstraintDefs[old], true
            delete(lt.ConstraintDefs, old)
            delete(lt.Constraints, old)
            if c, ok := lt.ConstraintComments[old]; ok {
                lt.ConstraintComments[name] = c
                delete(lt.ConstraintComments, old)
            }
        }
        if dt.PrimaryKeyComment != "" { lt.ConstraintComments[name] = dt.PrimaryKeyComment }
    }

    for _, fk := range dt.ForeignKeys {
        if fk == nil || len(fk.Columns) == 0 || fk.RefTable == "" { continue }
        name := truncateIdent(fk.Name)
        lt.Constraints[name] = true
        delete(lt.ConstraintDefs, name)
        if fk.Comment != "" { lt.ConstraintComments[name] = fk.Comment }
    }
    for _, ix := range dt.Indexes {
        if ix == nil || len(ix.Columns) == 0 { continue }
        name := indexName(fq, ix)
        lt.Indexes[name] = true
        delete(lt.IndexOpclasses, name)
        delete(lt.IndexDefs, name)
        if ix.Comment != "" { lt.IndexComments[name] = ix.Comment }
    }
    for _, ct := range dt.Constraints {
        if ct == nil || ct.Type == "" || renderConstraintDef(ct) == "" { continue }
        name := truncateIdent(ct.Name)
        lt.Constraints[name] = true
        delete(lt.ConstraintDefs, name)
        if ct.Comment != "" { lt.ConstraintComments[name] = ct.Comment }
    }
    for _, tr := range dt.Triggers {
        if tr == nil || tr.Procedure == "" { continue }
        lt.Triggers[truncateIdent(tr.Name)] = ""
        if tr.Enabled != "" { lt.TriggerEnabled[truncateIdent(tr.Name)] = tr.Enabled }
        if tr.Comment != "" { lt.TriggerComments[truncateIdent(tr.Name)] = tr.Comment }
    }
    for _, st := range dt.Statistics {
        if st == nil || len(st.Columns) == 0 { continue }
        target := -1
        if st.StatisticsTarget != nil { target = *st.StatisticsTarget }
        lt.Statistics[truncateIdent(st.Name)] = &LiveStatistic{Kinds: statisticKinds(st.Kinds), Items: append([]string(nil), st.Columns...), Comment: st.Comment, Target: target}
    }
    if dt.ReplicaIdentity != "" { lt.ReplicaIdentity = dt.ReplicaIdentity }
    switch {
    case strings.EqualFold(dt.ClusterOn, "none"):
        lt.ClusteredOn = ""
    case dt.ClusterOn != "":
        lt.ClusteredOn = truncateIdent(dt.ClusterOn)
    }
    for _, r := range dt.Rules {
        if r == nil || r.Event == "" || r.Do == "" { continue }
        lt.Rules[truncateIdent(r.Name)] = &LiveRule{Event: strings.ToLower(r.Event), Instead: r.Instead, Where: r.Where}
        if r.Comment != "" { lt.RuleComments[truncateIdent(r.Name)] = r.Comment }
    }
    for _, pol := range dt.Policies {
        if pol == nil || pol.Name == "" { continue }
        cmd := strings.ToLower(pol.For)
        if cmd == "" { cmd = "all" }
        roles := []string{}
        if !policyToPublic(pol.To) {
            for _, r := range pol.To { roles = append(roles, resolveRole(r, out.CurrentUser)) }
        }
        if pol.Comment != "" { lt.PolicyComments[truncateIdent(pol.Name)] = pol.Comment }
        lt.Policies[truncateIdent(pol.Name)] = &LivePolicy{
            Restrictive: pol.As == "restrictive",
            Cmd:         cmd,
            Roles:       roles,
            Using:       pol.Using,
            WithCheck:   pol.WithCheck,
        }
    }
    if dt.RowLevelSecurity != nil { lt.RLSEnabled = *dt.RowLevelSecurity }
    if dt.ForceRowLevelSecurity != nil { lt.RLSForced = *dt.ForceRowLevelSecurity }
    if dt.Unlogged != nil { lt.Unlogged = *dt.Unlogged }
    if dt.Foreign != nil {
        lf := &LiveForeignTable{Server: dt.Foreign.Server}
        if lt.Foreign != nil { lf.Options = lt.Foreign.Options }
        if dt.Foreign.Options != nil { lf.Options = cloneMap(dt.Foreign.Options, nil) }
        lt.Foreign = lf
    }
    if dt.PartitionBy != nil { lt.Partitioned = true }
    if dt.PartitionOf != "" { lt.IsPartition = true }
    lt.OfType = dt.Of
    if dt.Inherits != nil { lt.Inherits = append([]string(nil), dt.Inherits...) }
    if dt.With != nil {
        lt.Options = map[string]string{}
        for k, v := range dt.With { lt.Options[strings.ToLower(k)] = v }
    }
    if dt.Tablespace != "" { lt.Tablespace = dt.Tablespace }
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
        ExtensionDefs:      cloneMap(l.ExtensionDefs, func(e *LiveExtension) *LiveExtension { c := *e; return &c }),
        AvailableExtensions: l.AvailableExtensions,
        Views:              cloneSet(l.Views),
        MatViews:           cloneSet(l.MatViews),
        ViewOptions:        cloneMap(l.ViewOptions, func(m map[string]string) map[string]string { return cloneMap(m, nil) }),
        MatViewTables:      cloneMap(l.MatViewTables, cloneLiveTable),
        ViewColumnComments: cloneMap(l.ViewColumnComments, func(m map[string]string) map[string]string { return cloneMap(m, nil) }),
        ViewColumnDefaults: cloneMap(l.ViewColumnDefaults, func(m map[string]string) map[string]string { return cloneMap(m, nil) }),
        ViewTables:         cloneMap(l.ViewTables, cloneLiveTable),
        Sequences:          cloneSet(l.Sequences),
        Collations:         cloneSet(l.Collations),
        TSConfigs:          cloneMap(l.TSConfigs, func(m map[string][]string) map[string][]string { return cloneMap(m, func(v []string) []string { return append([]string(nil), v...) }) }),
        TSDictionaries:     cloneSet(l.TSDictionaries),
        TSDictionaryDefs:   cloneMap(l.TSDictionaryDefs, func(d *schema.TSDictionary) *schema.TSDictionary { c := *d; c.Options = cloneMap(d.Options, nil); return &c }),
        TSComments:         cloneMap(l.TSComments, nil),
        Servers:            cloneMap(l.Servers, func(s *LiveServer) *LiveServer { c := *s; c.Options = cloneMap(s.Options, nil); return &c }),
        Wrappers:           cloneMap(l.Wrappers, func(w *LiveWrapper) *LiveWrapper { c := *w; c.Options = cloneMap(w.Options, nil); return &c }),
        Languages:          cloneMap(l.Languages, func(g *LiveLanguage) *LiveLanguage { c := *g; return &c }),
        Publications:       cloneMap(l.Publications, func(p *LivePublication) *LivePublication { c := *p; c.Tables = cloneMap(p.Tables, nil); c.Schemas = append([]string(nil), p.Schemas...); return &c }),
        EventTriggers:      cloneMap(l.EventTriggers, func(e *LiveEventTrigger) *LiveEventTrigger { c := *e; c.Tags = append([]string(nil), e.Tags...); return &c }),
        CollationComments:  cloneMap(l.CollationComments, nil),
        SequenceDefs:       cloneMap(l.SequenceDefs, func(s *LiveSequence) *LiveSequence { c := *s; return &c }),
        DomainDefs:         cloneMap(l.DomainDefs, func(d *LiveDomain) *LiveDomain { c := *d; c.Checks = cloneMap(d.Checks, nil); return &c }),
        Domains:            cloneSet(l.Domains),
        Roles:              cloneSet(l.Roles),
        RoleDefs:           cloneMap(l.RoleDefs, func(r *LiveRole) *LiveRole { c := *r; return &c }),
        Owners:             cloneMap(l.Owners, nil),
        DefaultACLs:        cloneGrantMap(l.DefaultACLs),
        DefaultACLRows:     cloneSet(l.DefaultACLRows),
        CurrentUser:        l.CurrentUser,
        DatabaseConfig:     cloneMap(l.DatabaseConfig, nil),
        DatabaseRoleConfig: cloneMap(l.DatabaseRoleConfig, func(m map[string]string) map[string]string { return cloneMap(m, nil) }),
        ServerVersion:      l.ServerVersion,
        Offline:            l.Offline,
        DefaultTablespace:  l.DefaultTablespace,
        SearchPath:         l.SearchPath,
        scope:              l.scope,
        ViewProbes:         cloneMap(l.ViewProbes, nil),
        ViewDependents:     cloneMap(l.ViewDependents, nil),
        RoleMembers:        cloneMap(l.RoleMembers, cloneSet),
        RoleMemberOptions:  cloneMap(l.RoleMemberOptions, func(m map[string]*LiveMembership) map[string]*LiveMembership {
            return cloneMap(m, func(v *LiveMembership) *LiveMembership { c := *v; return &c })
        }),
        RoleComments:       cloneMap(l.RoleComments, nil),
        TableGrants:        cloneGrantMap(l.TableGrants),
        TypeGrants:         cloneGrantMap(l.TypeGrants),
        TypePublicRevoked:  cloneSet(l.TypePublicRevoked),
        FunctionGrants:     cloneGrantMap(l.FunctionGrants),
        SchemaGrants:       cloneGrantMap(l.SchemaGrants),
        ColumnGrants:       cloneMap(l.ColumnGrants, cloneGrantMap),
        FunctionPublicExec: cloneSet(l.FunctionPublicExec),
        RelComments:        cloneMap(l.RelComments, nil),
        ColumnDependents:   l.ColumnDependents,
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
        AggregateDefs: cloneMap(l.AggregateDefs, func(a *LiveAggregate) *LiveAggregate { c := *a; c.Options = cloneMap(a.Options, nil); return &c }),
        CompositeAttributes: cloneMap(l.CompositeAttributes, func(m map[string]string) map[string]string { return cloneMap(m, nil) }),
        CompositeCollations: cloneMap(l.CompositeCollations, func(m map[string]string) map[string]string { return cloneMap(m, nil) }),
        CompositeComments:   cloneMap(l.CompositeComments, func(m map[string]string) map[string]string { return cloneMap(m, nil) }),
        ExtensionMembers:   cloneSet(l.ExtensionMembers),
        MemberExtension:    l.MemberExtension,
        InitPrivs:          l.InitPrivs,
        BaseTypes:          cloneSet(l.BaseTypes),
        OwnedSequences:     cloneSet(l.OwnedSequences),
        Partitions:         cloneMap(l.Partitions, func(v []string) []string { return append([]string(nil), v...) }),
        PartitionBounds:    cloneMap(l.PartitionBounds, nil),
        TableData:          cloneMap(l.TableData, func(d *LiveTableData) *LiveTableData { c := *d; return &c }),
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
    if t.IndexDefs != nil {
        c.IndexDefs = cloneMap(t.IndexDefs, func(li *LiveIndex) *LiveIndex {
            if li == nil { return nil }
            cp := *li
            return &cp
        })
    }
    c.Triggers = cloneMap(t.Triggers, nil)
    c.ConstraintInherited = cloneSet(t.ConstraintInherited)
    c.Options = cloneMap(t.Options, nil)
    c.TriggerInherited = cloneSet(t.TriggerInherited)
    c.Policies = cloneMap(t.Policies, func(p *LivePolicy) *LivePolicy {
        if p == nil { return nil }
        cp := *p
        cp.Roles = append([]string(nil), p.Roles...)
        return &cp
    })
    c.IndexComments = cloneMap(t.IndexComments, nil)
    c.ConstraintComments = cloneMap(t.ConstraintComments, nil)
    c.TriggerComments = cloneMap(t.TriggerComments, nil)
    c.Rules = cloneMap(t.Rules, func(r *LiveRule) *LiveRule { cp := *r; return &cp })
    c.Statistics = cloneMap(t.Statistics, func(s *LiveStatistic) *LiveStatistic { cp := *s; return &cp })
    c.TriggerEnabled = cloneMap(t.TriggerEnabled, nil)
    c.RuleComments = cloneMap(t.RuleComments, nil)
    c.PolicyComments = cloneMap(t.PolicyComments, nil)
    return &c
}

func ensureLiveTableMaps(t *LiveTable) {
    if t.Columns == nil { t.Columns = map[string]*LiveColumn{} }
    if t.Constraints == nil { t.Constraints = map[string]bool{} }
    if t.ConstraintDefs == nil { t.ConstraintDefs = map[string]string{} }
    if t.Indexes == nil { t.Indexes = map[string]bool{} }
    if t.Triggers == nil { t.Triggers = map[string]string{} }
    if t.ConstraintInherited == nil { t.ConstraintInherited = map[string]bool{} }
    if t.TriggerComments == nil { t.TriggerComments = map[string]string{} }
    if t.Rules == nil { t.Rules = map[string]*LiveRule{} }
    if t.Statistics == nil { t.Statistics = map[string]*LiveStatistic{} }
    if t.TriggerEnabled == nil { t.TriggerEnabled = map[string]string{} }
    if t.RuleComments == nil { t.RuleComments = map[string]string{} }
    if t.PolicyComments == nil { t.PolicyComments = map[string]string{} }
    if t.TriggerInherited == nil { t.TriggerInherited = map[string]bool{} }
    if t.Policies == nil { t.Policies = map[string]*LivePolicy{} }
    if t.IndexComments == nil { t.IndexComments = map[string]string{} }
    if t.ConstraintComments == nil { t.ConstraintComments = map[string]string{} }
}

// simulateOwnersAndDefaults records the owners and default privileges a plan
// for desired leaves behind.
func simulateOwnersAndDefaults(out *Live, desired *schema.Database) {
    set := func(key, owner string) { if owner != "" { out.Owners[key] = resolveRole(owner, out.CurrentUser) } }
    for s, o := range desired.SchemaOwners { set("schema:"+s, o) }
    for k, td := range desired.Types { if td != nil { set("type:"+k, td.Owner) } }
    for k, dm := range desired.Domains { if dm != nil { set("type:"+k, dm.Owner) } }
    for k, sq := range desired.Sequences { if sq != nil { set("rel:"+k, sq.Owner) } }
    for k, dt := range desired.Tables { if dt != nil { set("rel:"+k, dt.Owner) } }
    for k, vw := range desired.Views { if vw != nil { set("rel:"+k, vw.Owner) } }
    for k, f := range desired.Functions {
        if f != nil { set("proc:"+normalizeFunctionSignature(schema.RoutineName(k)+f.ArgsSig), f.Owner) }
    }
    for k, p := range desired.Procedures {
        if p != nil { set("proc:"+normalizeFunctionSignature(schema.RoutineName(k)+p.ArgsSig), p.Owner) }
    }
    for _, dp := range desired.DefaultPrivileges {
        if dp != nil {
            for _, nsp := range dp.Schemas { out.Schemas[nsp] = true }
        }
    }
    if out.DefaultACLs == nil { out.DefaultACLs = map[string]map[string]map[string]bool{} }
    if out.DefaultACLRows == nil { out.DefaultACLRows = map[string]bool{} }
    applyDefaultPrivileges(out.DefaultACLs, out.DefaultACLRows, out.CurrentUser, desired.DefaultPrivileges)
}

// applyDefaultPrivileges updates acls and rows (as Live.DefaultACLs and
// Live.DefaultACLRows) with the grants dps declare.
func applyDefaultPrivileges(acls map[string]map[string]map[string]bool, rows map[string]bool, currentUser string, dps []*schema.DefaultPrivilege) {
    for _, dp := range dps {
        if dp == nil { continue }
        role := resolveRole(dp.ForRole, currentUser)
        if role == "" { role = currentUser }
        schemas := dp.Schemas
        if len(schemas) == 0 { schemas = []string{""} }
        for kind, grants := range dp.Grants {
            for _, nsp := range schemas {
                key := role + "|" + nsp + "|" + kind
                if acls[key] == nil { acls[key] = map[string]map[string]bool{} }
                if _, implicit := defaultACLImplicit[kind]; implicit && nsp == "" && !rows[key] {
                    acls[key]["public"] = map[string]bool{defaultACLImplicit[kind]: true}
                }
                rows[key] = true
                for grantee, privs := range grants {
                    set := map[string]bool{}
                    for _, p := range privs { set[strings.ToLower(p)] = true }
                    acls[key][grantKey(grantee, currentUser)] = set
                }
            }
        }
    }
}

// defaultFunctionCost is the COST PostgreSQL assigns when none is given: 1
// for C and internal functions, 100 otherwise.
func defaultFunctionCost(language string) float64 {
    switch strings.ToLower(strings.TrimSpace(language)) {
    case "c", "internal":
        return 1
    }
    return 100
}

// defaultFunctionRows is the ROWS PostgreSQL assigns when none is given:
// 1000 for set-returning functions, 0 otherwise.
func defaultFunctionRows(returns string) float64 {
    r := strings.ToLower(strings.TrimSpace(returns))
    if strings.HasPrefix(r, "setof ") || strings.HasPrefix(r, "table") {
        return 1000
    }
    return 0
}
