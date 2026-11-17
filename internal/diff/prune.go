package diff

import (
    "fmt"
    "sort"
    "strings"

    "github.com/suprbdev/pgy/internal/schema"
)

// PublicGrants is how grants blocks treat the PUBLIC pseudo-role.
type PublicGrants int

const (
    // PublicByKind covers PUBLIC where PostgreSQL gives it nothing by
    // default, and elsewhere only when a block lists public.
    PublicByKind PublicGrants = iota
    // PublicAuthoritative covers PUBLIC in every grants block, so PostgreSQL's
    // default EXECUTE and USAGE are revoked unless the block lists public.
    PublicAuthoritative
    // PublicExplicit covers PUBLIC only in blocks that list public.
    PublicExplicit
)

// Options controls optional Plan behaviour.
type Options struct {
    // Unsafe enables destructive changes: DROP COLUMN, type changes,
    // constraint/index redefinition, disabling RLS.
    Unsafe bool
    // Prune, together with Unsafe, drops objects the desired schema no
    // longer declares: tables, views, materialized views, functions,
    // procedures, sequences, domains and types in managed schemas, and
    // triggers, constraints and indexes on declared tables (see planPrune),
    // and the extensions a present extensions block leaves out (see
    // planExtensionDrops).
    Prune bool
    // KeepExtensions names extensions Prune never drops
    // (prune.keep_extensions).
    KeepExtensions []string
    // PublicGrants sets which grants blocks cover PUBLIC
    // (policy.public_grants); see planGrantsWarned.
    PublicGrants PublicGrants
    // MigrationsTable is pgy's own bookkeeping table ("schema.table"), never
    // pruned. Empty means pgy.migrations.
    MigrationsTable string
}

// planPrune returns the DROP statements for live objects the desired schema
// no longer declares. Only schemas that contain at least one desired object
// are considered, and extension members, partitions, inherited constraints,
// triggers and indexes, constraint-backed indexes, primary keys, sequences
// owned by a column and pgy's migrations table are never dropped, nor is
// anything on a relation an extension owns, a table declared without
// columns or a view declared without a query.
//
// early holds the drops that must precede column drops (triggers,
// constraints, indexes, views); late holds the drops of objects other
// objects may still reference until then (tables, routines, sequences,
// domains, types). Objects of one kind are dropped in a single statement so
// dependencies among them do not dictate an order.
func planPrune(live *Live, desired *schema.Database, migrationsTable string) (early, late []string) {
    if migrationsTable == "" { migrationsTable = "pgy.migrations" }
    managed := managedSchemas(desired)
    inScope := func(key string) bool {
        i := strings.IndexByte(key, '.')
        return i > 0 && managed[key[:i]] && !InDottedSchema(live, key)
    }

    for _, fq := range sortedKeys(desired.Tables) {
        dt, lt := desired.Tables[fq], live.Tables[fq]
        if dt == nil || lt == nil || dt.AttributeOnly() || live.ExtensionMembers["rel:"+fq] || attachesPartition(dt, lt) || foreignKindMismatch(dt, lt) { continue }
        early = append(early, pruneTableObjects(fq, dt, lt)...)
    }
    for _, fq := range sortedKeys(desired.Views) {
        vw := desired.Views[fq]
        if vw == nil || strings.TrimSpace(vw.Query) == "" || live.ExtensionMembers["rel:"+fq] { continue }
        if lt := live.MatViewTables[fq]; vw.Materialized && lt != nil {
            early = append(early, pruneIndexes(fq, vw.Indexes, lt)...)
        }
        if vt := live.ViewTables[fq]; !vw.Materialized && vt != nil {
            early = append(early, pruneTableObjects(fq, &schema.Table{Triggers: vw.Triggers, Rules: vw.Rules}, vt)...)
        }
    }

    undeclared := func(liveSet map[string]bool, declared func(string) bool, extKind string) []string {
        out := []string{}
        for k, ok := range liveSet {
            if !ok || !inScope(k) || declared(k) || live.ExtensionMembers[extKind+":"+k] { continue }
            out = append(out, k)
        }
        sort.Strings(out)
        return out
    }
    isView := func(k string) bool { return desired.Views[k] != nil }
    dropList := func(kind string, keys []string) []string {
        if len(keys) == 0 { return nil }
        parts := make([]string, len(keys))
        for i, k := range keys { parts[i] = pqIdent(k) }
        return []string{fmt.Sprintf("drop %s %s;", kind, strings.Join(parts, ", "))}
    }
    early = append(early, dropList("view", undeclared(live.Views, isView, "rel"))...)
    early = append(early, dropList("materialized view", undeclared(live.MatViews, isView, "rel"))...)

    tables, foreignTables := map[string]bool{}, map[string]bool{}
    for k, lt := range live.Tables {
        switch {
        case lt == nil || lt.IsPartition || k == migrationsTable:
        case lt.Foreign != nil:
            foreignTables[k] = true
        default:
            tables[k] = true
        }
    }
    late = append(late, dropList("foreign table", undeclared(foreignTables, func(k string) bool { return desired.Tables[k] != nil }, "rel"))...)
    late = append(late, dropList("table", undeclared(tables, func(k string) bool { return desired.Tables[k] != nil }, "rel"))...)

    late = append(late, pruneRoutines(live, desired, inScope)...)

    sequences := map[string]bool{}
    for k, ok := range live.Sequences {
        if ok && !live.OwnedSequences[k] { sequences[k] = true }
    }
    late = append(late, dropList("sequence", undeclared(sequences, func(k string) bool { return desired.Sequences[k] != nil }, "rel"))...)
    late = append(late, dropList("domain", undeclared(live.Domains, func(k string) bool { return desired.Domains[k] != nil }, "type"))...)
    late = append(late, dropList("type", undeclared(live.Types, func(k string) bool { return desired.Types[k] != nil }, "type"))...)
    tsConfigs := map[string]bool{}
    for k := range live.TSConfigs { tsConfigs[k] = true }
    early = append(early, dropList("text search configuration", undeclared(tsConfigs, func(k string) bool { return desired.TSConfigs[k] != nil }, "tscfg"))...)
    late = append(late, dropList("text search dictionary", undeclared(live.TSDictionaries, func(k string) bool { return desired.TSDictionaries[k] != nil }, "tsdict"))...)
    late = append(late, dropList("collation", undeclared(live.Collations, func(k string) bool { return desired.Collations[k] != nil }, "coll"))...)
    return early, late
}

// pruneServers returns the DROP statements for the live servers a servers
// block does not list, each after the drops of its user mappings, which
// DROP SERVER does not remove. A server that foreign tables still use once
// planPrune's drops have run is kept and reported instead, since dropping
// it would fail. Without a servers block, servers are unmanaged.
func pruneServers(live *Live, desired *schema.Database, migrationsTable string) (drops []string, warnings []Warning) {
    if desired.Servers == nil { return nil, nil }
    if migrationsTable == "" { migrationsTable = "pgy.migrations" }
    managed := managedSchemas(desired)
    parentOf := map[string]string{}
    for parent, children := range live.Partitions {
        for _, c := range children { parentOf[c] = parent }
    }
    pruned := func(fq string) bool {
        for depth := 0; depth < 32 && parentOf[fq] != ""; depth++ { fq = parentOf[fq] }
        i := strings.IndexByte(fq, '.')
        lt := live.Tables[fq]
        return lt != nil && !lt.IsPartition && i > 0 && managed[fq[:i]] && !InDottedSchema(live, fq) && desired.Tables[fq] == nil && fq != migrationsTable && !live.ExtensionMembers["rel:"+fq]
    }
    users := map[string][]string{}
    for _, fq := range sortedKeys(live.Tables) {
        if lt := live.Tables[fq]; lt != nil && lt.Foreign != nil && !pruned(fq) { users[lt.Foreign.Server] = append(users[lt.Foreign.Server], fq) }
    }
    for _, name := range sortedKeys(live.Servers) {
        if desired.Servers[name] != nil { continue }
        if left := users[name]; len(left) > 0 {
            warnings = append(warnings, Warning{Message: fmt.Sprintf("server %s is not in the servers block but still has foreign tables this plan keeps (%s), so it is not dropped; drop or move them first", name, strings.Join(left, ", "))})
            continue
        }
        for _, role := range live.Servers[name].UserMappings {
            if role != "public" { role = quoteIdent(role) }
            drops = append(drops, fmt.Sprintf("drop user mapping for %s server %s;", role, quoteIdent(name)))
        }
        drops = append(drops, fmt.Sprintf("drop server %s;", quoteIdent(name)))
    }
    return drops, warnings
}

// managedSchemas returns the schemas holding at least one desired object.
func managedSchemas(d *schema.Database) map[string]bool {
    out := map[string]bool{}
    add := func(keys []string) {
        for _, k := range keys {
            if i := strings.IndexByte(k, '.'); i > 0 { out[k[:i]] = true }
        }
    }
    add(sortedKeys(d.Tables))
    add(sortedKeys(d.Views))
    add(sortedKeys(d.Functions))
    add(sortedKeys(d.Procedures))
    add(sortedKeys(d.Types))
    add(sortedKeys(d.Domains))
    add(sortedKeys(d.Sequences))
    add(sortedKeys(d.Collations))
    add(sortedKeys(d.TSConfigs))
    add(sortedKeys(d.TSDictionaries))
    return out
}

// pruneTableObjects drops a declared table's triggers, constraints and
// indexes that the YAML no longer declares. Constraints are only dropped
// when their live definition is known, so a primary key is never mistaken
// for something else.
func pruneTableObjects(fq string, dt *schema.Table, lt *LiveTable) []string {
    out := []string{}

    wantTriggers := map[string]bool{}
    for _, tr := range dt.Triggers {
        if tr != nil { wantTriggers[truncateIdent(tr.Name)] = true }
    }
    for _, name := range sortedKeys(lt.Triggers) {
        if wantTriggers[name] || lt.TriggerInherited[name] { continue }
        out = append(out, fmt.Sprintf("drop trigger %s on %s;", quoteIdent(name), pqIdent(fq)))
    }

    wantConstraints := map[string]bool{}
    for _, fk := range dt.ForeignKeys {
        if fk != nil { wantConstraints[truncateIdent(fk.Name)] = true }
    }
    for _, ct := range dt.Constraints {
        if ct != nil { wantConstraints[truncateIdent(ct.Name)] = true }
    }
    for cn, c := range dt.Columns {
        if c != nil && c.Unique { wantConstraints[LiveUniqueKeyName(fq, cn, lt.Constraints)] = true }
    }
    fks, others := []string{}, []string{}
    for _, name := range sortedKeys(lt.ConstraintDefs) {
        def := strings.ToUpper(strings.TrimSpace(lt.ConstraintDefs[name]))
        if wantConstraints[name] || lt.ConstraintInherited[name] || strings.HasPrefix(def, "PRIMARY KEY") || strings.HasPrefix(def, "NOT NULL") { continue }
        stmt := fmt.Sprintf("alter table %s drop constraint %s;", pqIdent(fq), quoteIdent(name))
        if strings.HasPrefix(def, "FOREIGN KEY") {
            fks = append(fks, stmt)
        } else {
            others = append(others, stmt)
        }
    }
    wantStats := map[string]bool{}
    for _, st := range dt.Statistics {
        if st != nil { wantStats[truncateIdent(st.Name)] = true }
    }
    for _, name := range sortedKeys(lt.Statistics) {
        if !wantStats[name] { out = append(out, fmt.Sprintf("drop statistics %s;", pqIdent(schemaPrefixOf(fq)+name))) }
    }
    wantRules := map[string]bool{}
    for _, r := range dt.Rules {
        if r != nil { wantRules[truncateIdent(r.Name)] = true }
    }
    for _, name := range sortedKeys(lt.Rules) {
        if !wantRules[name] { out = append(out, fmt.Sprintf("drop rule %s on %s;", quoteIdent(name), pqIdent(fq))) }
    }
    out = append(out, fks...)
    out = append(out, others...)
    return append(out, pruneIndexes(fq, dt.Indexes, lt)...)
}

// pruneIndexes drops a relation's live indexes that are not declared. Only
// indexes with a known live definition are considered, so constraint-backed
// and partition-inherited indexes can be told apart and kept.
func pruneIndexes(fq string, declared []*schema.Index, lt *LiveTable) []string {
    want := map[string]bool{}
    for _, ix := range declared {
        if ix != nil && len(ix.Columns) > 0 { want[LiveIndexName(fq, ix, lt.Indexes)] = true }
    }
    schemaPrefix := ""
    if i := strings.IndexByte(fq, '.'); i >= 0 { schemaPrefix = fq[:i+1] }
    out := []string{}
    for _, name := range sortedKeys(lt.IndexDefs) {
        li := lt.IndexDefs[name]
        if li == nil || li.Constraint || li.Inherited || want[name] { continue }
        out = append(out, fmt.Sprintf("drop index %s;", pqIdent(schemaPrefix+name)))
    }
    return out
}

// pruneRoutines drops undeclared plain functions and procedures in managed
// schemas. Window functions are left alone, and so are routines written in
// C or internal, which YAML cannot declare.
func pruneRoutines(live *Live, desired *schema.Database, inScope func(string) bool) []string {
    want := map[string]bool{}
    for k, f := range desired.Functions {
        if f != nil { want[normalizeFunctionSignature(schema.RoutineName(k)+f.ArgsSig)] = true }
    }
    for k, p := range desired.Procedures {
        if p != nil { want[normalizeFunctionSignature(schema.RoutineName(k)+p.ArgsSig)] = true }
    }
    for k, ag := range desired.Aggregates {
        if ag != nil { want[normalizeFunctionSignature(schema.RoutineName(k)+aggregateArgs(ag.ArgsSig))] = true }
    }
    collect := func(norm, sig string) (string, bool) {
        if sig == "" || want[norm] || live.ExtensionMembers["proc:"+norm] || !inScope(schema.RoutineName(sig)) { return "", false }
        name := schema.RoutineName(sig)
        return pqIdent(name) + sig[len(name):], true
    }
    funcs, procs, aggs := []string{}, []string{}, []string{}
    for norm, lf := range live.FunctionDefs {
        if lf == nil || lf.Internal { continue }
        if lf.Kind == "a" && live.AggregateDefs[norm] != nil {
            if s, ok := collect(norm, lf.Signature); ok { aggs = append(aggs, aggregateDropArgs(s)) }
            continue
        }
        if lf.Kind != "f" || CompiledLanguage(lf.Language) { continue }
        if s, ok := collect(norm, lf.Signature); ok { funcs = append(funcs, s) }
    }
    for norm, lp := range live.ProcedureDefs {
        if lp == nil || CompiledLanguage(lp.Language) { continue }
        if s, ok := collect(norm, lp.Signature); ok { procs = append(procs, s) }
    }
    sort.Strings(funcs)
    sort.Strings(procs)
    sort.Strings(aggs)
    out := []string{}
    if len(aggs) > 0 { out = append(out, "drop aggregate "+strings.Join(aggs, ", ")+";") }
    if len(funcs) > 0 { out = append(out, "drop function "+strings.Join(funcs, ", ")+";") }
    if len(procs) > 0 { out = append(out, "drop procedure "+strings.Join(procs, ", ")+";") }
    return out
}

// aggregateDropArgs turns the empty argument list of a count(*)-style
// aggregate into (*), as DROP AGGREGATE requires.
func aggregateDropArgs(sig string) string {
    if strings.HasSuffix(sig, "()") { return strings.TrimSuffix(sig, "()") + "(*)" }
    return sig
}

func sortedKeys[V any](m map[string]V) []string {
    out := make([]string, 0, len(m))
    for k := range m { out = append(out, k) }
    sort.Strings(out)
    return out
}
