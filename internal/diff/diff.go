package diff

import (
    "context"
    "fmt"
    "sort"
    "strings"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/suprbdev/pgy/internal/schema"
)

type Live struct{
    Schemas map[string]bool
    Tables map[string]*LiveTable
    Types map[string]bool
    Functions map[string]bool
    Extensions map[string]bool
    Views map[string]bool
    MatViews map[string]bool
}
type LiveTable struct{
    Columns map[string]*LiveColumn
}
type LiveColumn struct{
    Type string
    Nullable bool
    Default string
}

func Introspect(ctx context.Context, pool *pgxpool.Pool) (*Live, error) {
    l := &Live{
        Schemas: map[string]bool{},
        Tables: map[string]*LiveTable{},
        Types: map[string]bool{},
        Functions: map[string]bool{},
        Extensions: map[string]bool{},
        Views: map[string]bool{},
        MatViews: map[string]bool{},
    }
    
    // Query existing schemas
    schemaQ := `
        select schema_name
        from information_schema.schemata
        where schema_name not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    schemaRows, err := pool.Query(ctx, schemaQ)
    if err != nil { return nil, err }
    for schemaRows.Next() {
        var schemaName string
        if err := schemaRows.Scan(&schemaName); err != nil { 
            schemaRows.Close()
            return nil, err 
        }
        l.Schemas[schemaName] = true
    }
    schemaRows.Close()
    
    // Query all tables (not just those with columns)
    tableQ := `
        select table_schema, table_name
        from information_schema.tables
        where table_schema not in ('pg_catalog', 'information_schema', 'pg_toast')
        and table_type = 'BASE TABLE'
    `
    tableRows, err := pool.Query(ctx, tableQ)
    if err != nil { return nil, err }
    for tableRows.Next() {
        var schemaName, tableName string
        if err := tableRows.Scan(&schemaName, &tableName); err != nil {
            tableRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s", schemaName, tableName)
        l.Tables[key] = &LiveTable{Columns: map[string]*LiveColumn{}}
    }
    tableRows.Close()
    
    // Query columns to enrich table info
    const q = `
        select table_schema, table_name, column_name, data_type, is_nullable, coalesce(column_default, '')
        from information_schema.columns
        where table_schema not in ('pg_catalog','information_schema')
        order by table_schema, table_name, ordinal_position
    `
    rows, err := pool.Query(ctx, q)
    if err != nil { return nil, err }
    defer rows.Close()
    for rows.Next() {
        var schemaName, tableName, colName, dataType, isNullable, def string
        if err := rows.Scan(&schemaName, &tableName, &colName, &dataType, &isNullable, &def); err != nil { return nil, err }
        key := fmt.Sprintf("%s.%s", schemaName, tableName)
        t := l.Tables[key]
        if t == nil { t = &LiveTable{Columns: map[string]*LiveColumn{}}; l.Tables[key] = t }
        t.Columns[colName] = &LiveColumn{Type: dataType, Nullable: isNullable == "YES", Default: def}
    }
    if err := rows.Err(); err != nil { return nil, err }
    
    // Query existing types
    typeQ := `
        select n.nspname, t.typname
        from pg_type t
        join pg_namespace n on n.oid = t.typnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and t.typtype in ('e', 'c')
    `
    typeRows, err := pool.Query(ctx, typeQ)
    if err != nil { return nil, err }
    for typeRows.Next() {
        var schemaName, typeName string
        if err := typeRows.Scan(&schemaName, &typeName); err != nil {
            typeRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s", schemaName, typeName)
        l.Types[key] = true
    }
    typeRows.Close()
    
    // Query existing functions
    funcQ := `
        select n.nspname, p.proname, pg_get_function_identity_arguments(p.oid) as args
        from pg_proc p
        join pg_namespace n on n.oid = p.pronamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    funcRows, err := pool.Query(ctx, funcQ)
    if err != nil { return nil, err }
    for funcRows.Next() {
        var schemaName, funcName, args string
        if err := funcRows.Scan(&schemaName, &funcName, &args); err != nil {
            funcRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s(%s)", schemaName, funcName, args)
        l.Functions[key] = true
    }
    funcRows.Close()
    
    // Query existing extensions
    extQ := `select extname from pg_extension`
    extRows, err := pool.Query(ctx, extQ)
    if err != nil { return nil, err }
    for extRows.Next() {
        var extName string
        if err := extRows.Scan(&extName); err != nil {
            extRows.Close()
            return nil, err
        }
        l.Extensions[extName] = true
    }
    extRows.Close()

    // Query existing views
    viewQ := `
        select table_schema, table_name
        from information_schema.views
        where table_schema not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    viewRows, err := pool.Query(ctx, viewQ)
    if err != nil { return nil, err }
    for viewRows.Next() {
        var schemaName, viewName string
        if err := viewRows.Scan(&schemaName, &viewName); err != nil {
            viewRows.Close()
            return nil, err
        }
        l.Views[fmt.Sprintf("%s.%s", schemaName, viewName)] = true
    }
    viewRows.Close()

    // Query existing materialized views
    matViewQ := `
        select schemaname, matviewname
        from pg_matviews
        where schemaname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    matViewRows, err := pool.Query(ctx, matViewQ)
    if err != nil { return nil, err }
    for matViewRows.Next() {
        var schemaName, viewName string
        if err := matViewRows.Scan(&schemaName, &viewName); err != nil {
            matViewRows.Close()
            return nil, err
        }
        l.MatViews[fmt.Sprintf("%s.%s", schemaName, viewName)] = true
    }
    matViewRows.Close()

    return l, nil
}

type PlanDiff struct{
    Creates []string
    Alters  []string
    Drops   []string
}

func (p *PlanDiff) Summary() map[string]int {
    return map[string]int{
        "creates": len(p.Creates),
        "alters": len(p.Alters),
        "drops": len(p.Drops),
    }
}

func Plan(live *Live, desired *schema.Database, unsafe bool) *PlanDiff {
    plan := &PlanDiff{}
    
    // Collect all schemas needed from desired entities
    neededSchemas := map[string]bool{}
    // Extensions don't require schemas, skip them
    for k := range desired.Types {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    for k := range desired.Functions {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    for k := range desired.Tables {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    for k := range desired.Views {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    
    // Generate CREATE SCHEMA statements for missing schemas (public is always present)
    // SCHEMAS HAVE HIGHEST PRIORITY - must be created first
    schemaNames := make([]string, 0, len(neededSchemas))
    for s := range neededSchemas {
        if s == "public" { continue } // public schema always exists
        schemaNames = append(schemaNames, s)
    }
    sort.Strings(schemaNames)
    for _, schemaName := range schemaNames {
        if !live.Schemas[schemaName] {
            plan.Creates = append(plan.Creates, fmt.Sprintf("create schema if not exists %s;", pqIdent(schemaName)))
        }
    }
    
    // EXTENSIONS HAVE SECOND PRIORITY - created after schemas, before everything else
    extNames := make([]string, 0, len(desired.Extensions))
    for _, ext := range desired.Extensions {
        if ext != nil && ext.Name != "" {
            if !live.Extensions[ext.Name] {
                extNames = append(extNames, ext.Name)
            }
        }
    }
    sort.Strings(extNames)
    for _, extName := range extNames {
        ext := findExtension(desired, extName)
        if ext == nil { continue }
        stmt := "create extension "
        if ext.IfNotExists { stmt += "if not exists " }
        stmt += pqIdent(ext.Name) + ";"
        plan.Creates = append(plan.Creates, stmt)
    }
    
    // Topologically sort remaining entities (types, functions, tables) respecting dependsOn
    sorted, err := schema.TopologicalSort(desired)
    if err != nil {
        // fallback to old behavior on error
        sorted = []schema.Entity{}
    }
    
    // Generate SQL in dependency order (excluding extensions, already handled above)
    for _, e := range sorted {
        switch e.Kind {
        case "extension":
            // Extensions already handled above, skip
            continue
        case "type":
            td, ok := desired.Types[e.Key]
            if !ok || td == nil { continue }
            // Check if type already exists
            if live.Types[e.Key] { continue }
            if td.Kind == "enum" {
                labels := make([]string, 0, len(td.Labels))
                for _, l := range td.Labels { labels = append(labels, quoteString(l)) }
                plan.Creates = append(plan.Creates, fmt.Sprintf("create type %s as enum (%s);", pqIdent(e.Key), strings.Join(labels, ", ")))
            } else if td.Kind == "composite" {
                attrs := []string{}
                keys := make([]string, 0, len(td.Attributes))
                for k := range td.Attributes { keys = append(keys, k) }
                sort.Strings(keys)
                for _, an := range keys { attrs = append(attrs, fmt.Sprintf("%s %s", pqIdent(an), td.Attributes[an])) }
                plan.Creates = append(plan.Creates, fmt.Sprintf("create type %s as (%s);", pqIdent(e.Key), strings.Join(attrs, ", ")))
            }
        case "function":
            f, ok := desired.Functions[e.Key]
            if !ok || f == nil { continue }
            // Check if function already exists - normalize signature for comparison
            // ArgsSig already includes parentheses, e.g., "(key text, default_value jsonb default null)"
            desiredSig := e.Key + f.ArgsSig
            normalizedDesired := normalizeFunctionSignature(desiredSig)
            found := false
            for liveSig := range live.Functions {
                normalizedLive := normalizeFunctionSignature(liveSig)
                if normalizedLive == normalizedDesired {
                    found = true
                    break
                }
            }
            if found { continue }
            setClauses := ""
            if len(f.Set) > 0 {
                keys := make([]string, 0, len(f.Set))
                for k := range f.Set { keys = append(keys, k) }
                sort.Strings(keys)
                for _, k := range keys {
                    setClauses += fmt.Sprintf(" set %s = %s", k, f.Set[k])
                }
            }
            attrs := []string{}
            if f.Security != "" { attrs = append(attrs, "security "+f.Security) }
            if f.Volatility != "" { attrs = append(attrs, f.Volatility) }
            if f.Strict { attrs = append(attrs, "strict") }
            attrsStr := strings.Join(attrs, " ")
            if attrsStr != "" { attrsStr = " " + attrsStr }
            body := f.Body
            stmt := fmt.Sprintf("create function %s%s returns %s language %s%s as $$\n%s\n$$;", pqIdent(e.Key)+f.ArgsSig, "", f.Returns, f.Language, attrsStr+setClauses, body)
            plan.Creates = append(plan.Creates, stmt)
        case "view":
            vw, ok := desired.Views[e.Key]
            if !ok || vw == nil || vw.Query == "" { continue }
            if vw.Materialized {
                if live.MatViews[e.Key] { continue }
                plan.Creates = append(plan.Creates, fmt.Sprintf("create materialized view if not exists %s as %s;", pqIdent(e.Key), vw.Query))
            } else {
                if live.Views[e.Key] { continue }
                plan.Creates = append(plan.Creates, fmt.Sprintf("create or replace view %s as %s;", pqIdent(e.Key), vw.Query))
            }
        case "table":
            // Handle tables in dependency order
            dt, ok := desired.Tables[e.Key]
            if !ok || dt == nil { continue }
            fq := e.Key
            lt := live.Tables[fq]
            if lt == nil {
                cols := make([]string, 0, len(dt.Columns))
                if len(dt.ColumnOrder) > 0 {
                    for _, cn := range dt.ColumnOrder {
                        if c, ok := dt.Columns[cn]; ok {
                            cols = append(cols, renderColumn(cn, c))
                        }
                    }
                    // include any remaining columns not listed (fallback)
                    for cn, c := range dt.Columns {
                        found := false
                        for _, on := range dt.ColumnOrder { if on == cn { found = true; break } }
                        if !found { cols = append(cols, renderColumn(cn, c)) }
                    }
                } else {
                    for cn, c := range dt.Columns {
                        cols = append(cols, renderColumn(cn, c))
                    }
                    sort.Strings(cols)
                }
                plan.Creates = append(plan.Creates, fmt.Sprintf("create table if not exists %s (%s);", pqIdent(fq), strings.Join(cols, ", ")))
                // constraints and indexes and triggers
                if len(dt.PrimaryKey) > 0 {
                    plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s add primary key (%s);", pqIdent(fq), joinIdentList(dt.PrimaryKey)))
                } else {
                    // derive from column PrimaryKey flags
                    pkCols := []string{}
                    for cn, c := range dt.Columns { if c.PrimaryKey { pkCols = append(pkCols, cn) } }
                    sort.Strings(pkCols)
                    if len(pkCols) > 0 {
                        plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s add primary key (%s);", pqIdent(fq), joinIdentList(pkCols)))
                    }
                }
                for _, fk := range dt.ForeignKeys {
                    if fk == nil || len(fk.Columns) == 0 || fk.RefTable == "" { continue }
                    stmt := fmt.Sprintf("alter table %s add constraint %s foreign key (%s) references %s(%s)", pqIdent(fq), pqIdent(fk.Name), joinIdentList(fk.Columns), pqIdent(fk.RefTable), joinIdentList(fk.RefColumns))
                    if fk.OnDelete != "" { stmt += " on delete " + strings.ToLower(fk.OnDelete) }
                    stmt += ";"
                    plan.Alters = append(plan.Alters, stmt)
                }
                for _, ix := range dt.Indexes {
                    if ix == nil || len(ix.Columns) == 0 { continue }
                    uniq := ""
                    if ix.Unique { uniq = " unique" }
                    name := ix.Name
                    if name == "" { name = strings.ReplaceAll(fq+"_"+strings.Join(ix.Columns, "_"), ".", "_") + "_idx" }
                    plan.Creates = append(plan.Creates, fmt.Sprintf("create%s index if not exists %s on %s(%s);", uniq, pqIdent(name), pqIdent(fq), joinIdentList(ix.Columns)))
                }
                for _, ct := range dt.Constraints {
                    if ct == nil || ct.Type == "" { continue }
                    typ := strings.ToLower(ct.Type)
                    stmt := fmt.Sprintf("alter table %s add constraint %s ", pqIdent(fq), pqIdent(ct.Name))
                    switch typ {
                    case "check":
                        stmt += fmt.Sprintf("check (%s);", ct.Expression)
                    case "unique":
                        stmt += fmt.Sprintf("unique (%s);", joinIdentList(ct.Columns))
                    case "exclude":
                        stmt += fmt.Sprintf("exclude %s;", ct.Expression)
                    }
                    plan.Alters = append(plan.Alters, stmt)
                }
                for _, tr := range dt.Triggers {
                    if tr == nil || tr.Procedure == "" { continue }
                    events := strings.ToUpper(strings.Join(tr.Events, " or "))
                    stmt := fmt.Sprintf("create trigger %s %s %s on %s for each %s execute procedure %s;", pqIdent(tr.Name), strings.ToUpper(tr.Timing), events, pqIdent(fq), strings.ToLower(tr.Level), tr.Procedure)
                    plan.Creates = append(plan.Creates, stmt)
                }
            } else {
                // existing table: add missing columns
                for cn, c := range dt.Columns {
                    if _, ok := lt.Columns[cn]; !ok {
                        plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s add column %s;", pqIdent(fq), renderColumn(cn, c)))
                    }
                }
                // drops
                if unsafe {
                    for cn := range lt.Columns {
                        if _, ok := dt.Columns[cn]; !ok {
                            plan.Drops = append(plan.Drops, fmt.Sprintf("alter table %s drop column %s;", pqIdent(fq), pqIdent(cn)))
                        }
                    }
                }
            }
        }
    }
    return plan
}

func Render(p *PlanDiff) string {
    statements := make([]string, 0, len(p.Creates)+len(p.Alters)+len(p.Drops))
    statements = append(statements, p.Creates...)
    statements = append(statements, p.Alters...)
    statements = append(statements, p.Drops...)
    if len(statements) == 0 { return "" }
    return strings.Join(statements, "\n") + "\n"
}

func renderColumn(name string, c *schema.Column) string {
    parts := []string{pqIdent(name), c.Type}
    if !c.Nullable { parts = append(parts, "not null") }
    if c.Default != "" { parts = append(parts, "default "+c.Default) }
    return strings.Join(parts, " ")
}

func pqIdent(fq string) string {
    // support schema.table and simple name
    if strings.Contains(fq, ".") {
        parts := strings.SplitN(fq, ".", 2)
        return `"` + parts[0] + `"."` + parts[1] + `"`
    }
    return `"` + fq + `"`
}

func joinIdentList(cols []string) string {
    parts := make([]string, 0, len(cols))
    for _, c := range cols { parts = append(parts, pqIdent(c)) }
    return strings.Join(parts, ", ")
}

func quoteString(s string) string {
    // naive single-quote escaping
    return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func findExtension(db *schema.Database, name string) *schema.Extension {
    for _, ext := range db.Extensions {
        if ext != nil && ext.Name == name {
            return ext
        }
    }
    return nil
}

// normalizeFunctionSignature normalizes function signatures for comparison
// Removes default values and normalizes whitespace and type names
// Input formats: "schema.func(args)" or "schema.func (args)"
// Args may contain defaults like "key text, val jsonb default null"
func normalizeFunctionSignature(sig string) string {
    // Find the opening parenthesis
    parenIdx := strings.Index(sig, "(")
    if parenIdx < 0 {
        // No args, return normalized name only
        return strings.ToLower(strings.TrimSpace(sig))
    }
    
    funcPart := strings.TrimSpace(sig[:parenIdx])
    argsPart := strings.TrimSpace(sig[parenIdx+1:])
    // Remove closing paren if present
    argsPart = strings.TrimSuffix(argsPart, ")")
    
    // Normalize function name (lowercase, remove extra spaces)
    funcPart = strings.ToLower(strings.ReplaceAll(funcPart, " ", ""))
    
    // Parse and normalize arguments
    // Split by comma, but be careful of nested structures
    args := []string{}
    current := ""
    depth := 0
    for _, r := range argsPart {
        switch r {
        case '(':
            depth++
            current += string(r)
        case ')':
            depth--
            current += string(r)
        case ',':
            if depth == 0 {
                arg := normalizeArg(strings.TrimSpace(current))
                if arg != "" {
                    args = append(args, arg)
                }
                current = ""
            } else {
                current += string(r)
            }
        default:
            current += string(r)
        }
    }
    if current != "" {
        arg := normalizeArg(strings.TrimSpace(current))
        if arg != "" {
            args = append(args, arg)
        }
    }
    
    // Reconstruct normalized signature with consistent spacing
    normalizedArgs := strings.Join(args, ", ")
    return fmt.Sprintf("%s(%s)", funcPart, normalizedArgs)
}

// normalizeArg removes default values from a function argument and normalizes types
// e.g., "key text default null" -> "key text"
// Format is typically: "param_name type_name" or "param_name type_name default value"
func normalizeArg(arg string) string {
    // Remove default clauses (case insensitive)
    arg = strings.TrimSpace(arg)
    
    // Find where "default" keyword starts (case insensitive)
    defaultIdx := -1
    words := strings.Fields(arg)
    for i, word := range words {
        if strings.EqualFold(word, "default") {
            defaultIdx = i
            break
        }
    }
    
    if defaultIdx < 0 {
        // No default clause, normalize what we have
        return normalizeArgNoDefault(arg)
    }
    
    // Take everything before "default"
    beforeDefault := strings.Join(words[:defaultIdx], " ")
    return normalizeArgNoDefault(beforeDefault)
}

// normalizeArgNoDefault normalizes an argument without default clause
func normalizeArgNoDefault(arg string) string {
    // Format: "param_name type_name" or "param_name schema.type_name"
    // Normalize whitespace and type names
    words := strings.Fields(arg)
    if len(words) < 2 {
        return strings.ToLower(strings.TrimSpace(arg))
    }
    
    // Parameter name (first word) - lowercase for comparison
    paramName := strings.ToLower(words[0])
    
    // Type name (rest) - normalize to lowercase
    typeName := strings.ToLower(strings.Join(words[1:], " "))
    
    // Normalize common PostgreSQL type aliases and variations to canonical forms
    // Order matters - do longer matches first
    typeName = strings.ReplaceAll(typeName, "character varying", "varchar")
    typeName = strings.ReplaceAll(typeName, "double precision", "float8")
    typeName = strings.ReplaceAll(typeName, "integer", "int")
    typeName = strings.ReplaceAll(typeName, "int4", "int")
    typeName = strings.ReplaceAll(typeName, "int8", "bigint")
    typeName = strings.ReplaceAll(typeName, "boolean", "bool")
    typeName = strings.ReplaceAll(typeName, "character", "char")
    
    // Normalize whitespace (multiple spaces to single)
    typeName = strings.Join(strings.Fields(typeName), " ")
    
    return paramName + " " + typeName
}


