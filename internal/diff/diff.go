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
    Schemas    map[string]bool
    Tables     map[string]*LiveTable
    Types      map[string]bool
    Functions  map[string]bool
    Extensions map[string]bool
    Views      map[string]bool
    MatViews   map[string]bool
    Sequences  map[string]bool
    Domains    map[string]bool
    Roles      map[string]bool
    // RoleMembers: member role -> parent role -> exists (pg_auth_members)
    RoleMembers map[string]map[string]bool
    RoleComments map[string]string // role name -> comment (pg_shdescription)
    // Grants: object key -> role -> privilege -> exists. Object owners excluded.
    TableGrants    map[string]map[string]map[string]bool // key "schema.table"
    FunctionGrants map[string]map[string]map[string]bool // key normalized "schema.name(args)"
    SchemaGrants   map[string]map[string]map[string]bool // key schema name
    // FunctionPublicExec: normalized signature -> PUBLIC still has EXECUTE
    // (true when proacl is NULL, i.e. default privileges, or PUBLIC=X entry present).
    FunctionPublicExec map[string]bool
    // Comments: current COMMENT ON values (absent key = no comment)
    RelComments      map[string]string // "schema.rel" -> comment (tables, views, matviews)
    FunctionComments map[string]string // normalized "schema.name(args)" -> comment
    TypeComments     map[string]string // "schema.type" -> comment
    SchemaComments   map[string]string // schema name -> comment
    // FunctionDefs: normalized signature -> live definition, for replace-on-change
    FunctionDefs map[string]*LiveFunction
    // Procedures: normalized signature -> exists (pg_proc prokind = 'p').
    // Grants/comments for procedures share FunctionGrants/FunctionComments/
    // FunctionPublicExec (same pg_proc key space).
    Procedures map[string]bool
    // ProcedureDefs: normalized signature -> live definition, for replace-on-change
    ProcedureDefs map[string]*LiveProcedure
    // EnumLabels: "schema.type" -> labels in enumsortorder, for ALTER TYPE ... ADD VALUE
    EnumLabels map[string][]string
}

type LiveFunction struct{
    Body       string
    Volatility string // volatile|stable|immutable
    Security   string // definer|invoker
    Strict     bool
}
type LiveProcedure struct{
    Body     string
    Security string // definer|invoker
}
type LiveTable struct{
    Columns     map[string]*LiveColumn
    Constraints map[string]bool // constraint name -> exists
    // ConstraintDefs: constraint name -> pg_get_constraintdef() output,
    // for detecting redefined constraints (drop+add, gated behind --unsafe)
    ConstraintDefs map[string]string
    Indexes     map[string]bool // index name -> exists
    Triggers    map[string]bool // trigger name -> exists
    Policies    map[string]bool // policy name -> exists
    HasPK       bool            // whether a primary key constraint exists
    RLSEnabled  bool            // row level security enabled
}
type LiveColumn struct{
    Type     string
    Nullable bool
    Default  string
    Comment  string
    Identity string // "" (none), "always", or "by default"
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
        Sequences: map[string]bool{},
        Domains: map[string]bool{},
        Roles: map[string]bool{},
        RoleMembers: map[string]map[string]bool{},
        RoleComments: map[string]string{},
        TableGrants: map[string]map[string]map[string]bool{},
        FunctionGrants: map[string]map[string]map[string]bool{},
        SchemaGrants: map[string]map[string]map[string]bool{},
        FunctionPublicExec: map[string]bool{},
        RelComments: map[string]string{},
        FunctionComments: map[string]string{},
        TypeComments: map[string]string{},
        SchemaComments: map[string]string{},
        FunctionDefs: map[string]*LiveFunction{},
        Procedures: map[string]bool{},
        ProcedureDefs: map[string]*LiveProcedure{},
        EnumLabels: map[string][]string{},
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
        l.Tables[key] = &LiveTable{Columns: map[string]*LiveColumn{}, Constraints: map[string]bool{}, ConstraintDefs: map[string]string{}, Indexes: map[string]bool{}, Triggers: map[string]bool{}, Policies: map[string]bool{}}
    }
    tableRows.Close()
    
    // Query columns to enrich table info. Uses pg_catalog rather than
    // information_schema so format_type keeps type modifiers (varchar(255))
    // and resolves arrays/user types (information_schema reports those as
    // ARRAY/USER-DEFINED, which cannot be diffed against YAML types).
    const q = `
        select n.nspname, c.relname, a.attname,
               format_type(a.atttypid, a.atttypmod),
               not a.attnotnull,
               coalesce(pg_get_expr(ad.adbin, ad.adrelid), ''),
               case a.attidentity when 'a' then 'always' when 'd' then 'by default' else '' end
        from pg_attribute a
        join pg_class c on c.oid = a.attrelid
        join pg_namespace n on n.oid = c.relnamespace
        left join pg_attrdef ad on ad.adrelid = a.attrelid and ad.adnum = a.attnum
        where a.attnum > 0 and not a.attisdropped
        and c.relkind in ('r', 'p')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        order by n.nspname, c.relname, a.attnum
    `
    rows, err := pool.Query(ctx, q)
    if err != nil { return nil, err }
    defer rows.Close()
    for rows.Next() {
        var schemaName, tableName, colName, dataType, def, identity string
        var nullable bool
        if err := rows.Scan(&schemaName, &tableName, &colName, &dataType, &nullable, &def, &identity); err != nil { return nil, err }
        key := fmt.Sprintf("%s.%s", schemaName, tableName)
        t := l.Tables[key]
        if t == nil { t = &LiveTable{Columns: map[string]*LiveColumn{}, Constraints: map[string]bool{}, ConstraintDefs: map[string]string{}, Indexes: map[string]bool{}, Triggers: map[string]bool{}, Policies: map[string]bool{}}; l.Tables[key] = t }
        t.Columns[colName] = &LiveColumn{Type: dataType, Nullable: nullable, Default: def, Identity: identity}
    }
    if err := rows.Err(); err != nil { return nil, err }

    // Query existing table constraints (pk, fk, check, unique, exclude) with
    // their full definitions so redefined constraints can be detected. Uses
    // pg_constraint rather than information_schema so pg_get_constraintdef is
    // available (and NOT NULL pseudo-constraints are excluded).
    conQ := `
        select n.nspname, c.relname, con.conname, con.contype::text,
               pg_get_constraintdef(con.oid)
        from pg_constraint con
        join pg_class c on c.oid = con.conrelid
        join pg_namespace n on n.oid = c.relnamespace
        where con.contype in ('p', 'f', 'c', 'u', 'x')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    conRows, err := pool.Query(ctx, conQ)
    if err != nil { return nil, err }
    for conRows.Next() {
        var schemaName, tableName, conName, conType, conDef string
        if err := conRows.Scan(&schemaName, &tableName, &conName, &conType, &conDef); err != nil {
            conRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s", schemaName, tableName)
        if t := l.Tables[key]; t != nil {
            t.Constraints[conName] = true
            t.ConstraintDefs[conName] = conDef
            if conType == "p" {
                t.HasPK = true
            }
        }
    }
    conRows.Close()

    // Query existing indexes
    idxQ := `
        select schemaname, tablename, indexname
        from pg_indexes
        where schemaname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    idxRows, err := pool.Query(ctx, idxQ)
    if err != nil { return nil, err }
    for idxRows.Next() {
        var schemaName, tableName, idxName string
        if err := idxRows.Scan(&schemaName, &tableName, &idxName); err != nil {
            idxRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s", schemaName, tableName)
        if t := l.Tables[key]; t != nil {
            t.Indexes[idxName] = true
        }
    }
    idxRows.Close()

    // Query existing triggers (skip internal triggers, e.g. FK enforcement)
    trgQ := `
        select n.nspname, c.relname, t.tgname
        from pg_trigger t
        join pg_class c on c.oid = t.tgrelid
        join pg_namespace n on n.oid = c.relnamespace
        where not t.tgisinternal
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    trgRows, err := pool.Query(ctx, trgQ)
    if err != nil { return nil, err }
    for trgRows.Next() {
        var schemaName, tableName, trgName string
        if err := trgRows.Scan(&schemaName, &tableName, &trgName); err != nil {
            trgRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s", schemaName, tableName)
        if t := l.Tables[key]; t != nil {
            t.Triggers[trgName] = true
        }
    }
    trgRows.Close()

    // Query row level security state
    rlsQ := `
        select n.nspname, c.relname, c.relrowsecurity
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
        where c.relkind in ('r', 'p')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    rlsRows, err := pool.Query(ctx, rlsQ)
    if err != nil { return nil, err }
    for rlsRows.Next() {
        var schemaName, tableName string
        var enabled bool
        if err := rlsRows.Scan(&schemaName, &tableName, &enabled); err != nil {
            rlsRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s", schemaName, tableName)
        if t := l.Tables[key]; t != nil {
            t.RLSEnabled = enabled
        }
    }
    rlsRows.Close()

    // Query existing policies
    polQ := `
        select n.nspname, c.relname, pol.polname
        from pg_policy pol
        join pg_class c on c.oid = pol.polrelid
        join pg_namespace n on n.oid = c.relnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    polRows, err := pool.Query(ctx, polQ)
    if err != nil { return nil, err }
    for polRows.Next() {
        var schemaName, tableName, polName string
        if err := polRows.Scan(&schemaName, &tableName, &polName); err != nil {
            polRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s", schemaName, tableName)
        if t := l.Tables[key]; t != nil {
            t.Policies[polName] = true
        }
    }
    polRows.Close()

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
    
    // Query existing functions and procedures with definition details for
    // replace-on-change. prokind routes rows: 'p' = procedure, else function.
    funcQ := `
        select n.nspname, p.proname, pg_get_function_identity_arguments(p.oid) as args,
               coalesce(p.prosrc, ''), p.provolatile::text, p.prosecdef, p.proisstrict, p.prokind::text
        from pg_proc p
        join pg_namespace n on n.oid = p.pronamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    funcRows, err := pool.Query(ctx, funcQ)
    if err != nil { return nil, err }
    for funcRows.Next() {
        var schemaName, funcName, args, src, vol, kind string
        var secdef, strict bool
        if err := funcRows.Scan(&schemaName, &funcName, &args, &src, &vol, &secdef, &strict, &kind); err != nil {
            funcRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s(%s)", schemaName, funcName, args)
        if kind == "p" {
            norm := normalizeFunctionSignature(key)
            l.Procedures[norm] = true
            lp := &LiveProcedure{Body: src, Security: "invoker"}
            if secdef { lp.Security = "definer" }
            l.ProcedureDefs[norm] = lp
            continue
        }
        l.Functions[key] = true
        lf := &LiveFunction{Body: src, Strict: strict, Security: "invoker", Volatility: "volatile"}
        if secdef { lf.Security = "definer" }
        switch vol {
        case "i": lf.Volatility = "immutable"
        case "s": lf.Volatility = "stable"
        }
        l.FunctionDefs[normalizeFunctionSignature(key)] = lf
    }
    funcRows.Close()

    // Query enum labels in sort order
    enumQ := `
        select n.nspname, t.typname, e.enumlabel
        from pg_enum e
        join pg_type t on t.oid = e.enumtypid
        join pg_namespace n on n.oid = t.typnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        order by t.oid, e.enumsortorder
    `
    enumRows, err := pool.Query(ctx, enumQ)
    if err != nil { return nil, err }
    for enumRows.Next() {
        var schemaName, typeName, label string
        if err := enumRows.Scan(&schemaName, &typeName, &label); err != nil {
            enumRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s", schemaName, typeName)
        l.EnumLabels[key] = append(l.EnumLabels[key], label)
    }
    enumRows.Close()
    
    // Query existing domains
    domQ := `
        select n.nspname, t.typname
        from pg_type t
        join pg_namespace n on n.oid = t.typnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and t.typtype = 'd'
    `
    domRows, err := pool.Query(ctx, domQ)
    if err != nil { return nil, err }
    for domRows.Next() {
        var schemaName, domName string
        if err := domRows.Scan(&schemaName, &domName); err != nil {
            domRows.Close()
            return nil, err
        }
        l.Domains[fmt.Sprintf("%s.%s", schemaName, domName)] = true
    }
    domRows.Close()

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

    // Query existing sequences (includes serial/identity-owned sequences,
    // which is fine: presence only guards against duplicate CREATE SEQUENCE)
    seqQ := `
        select n.nspname, c.relname
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
        where c.relkind = 'S'
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    seqRows, err := pool.Query(ctx, seqQ)
    if err != nil { return nil, err }
    for seqRows.Next() {
        var schemaName, seqName string
        if err := seqRows.Scan(&schemaName, &seqName); err != nil {
            seqRows.Close()
            return nil, err
        }
        l.Sequences[fmt.Sprintf("%s.%s", schemaName, seqName)] = true
    }
    seqRows.Close()

    // Query table grants (explicit ACL entries, owner excluded)
    tgQ := `
        select n.nspname, c.relname,
               case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end,
               lower(a.privilege_type)
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
        cross join lateral aclexplode(c.relacl) a
        where c.relkind in ('r', 'p')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and a.grantee <> c.relowner
    `
    tgRows, err := pool.Query(ctx, tgQ)
    if err != nil { return nil, err }
    for tgRows.Next() {
        var schemaName, tableName, role, priv string
        if err := tgRows.Scan(&schemaName, &tableName, &role, &priv); err != nil {
            tgRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s", schemaName, tableName)
        addLiveGrant(l.TableGrants, key, role, priv)
    }
    tgRows.Close()

    // Query function grants; left join keeps NULL-acl functions (default privileges,
    // meaning PUBLIC still has EXECUTE) visible for FunctionPublicExec.
    fgQ := `
        select n.nspname, p.proname, pg_get_function_identity_arguments(p.oid),
               coalesce(case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end, ''),
               coalesce(lower(a.privilege_type), ''),
               p.proacl is null,
               coalesce(a.grantee = p.proowner, false)
        from pg_proc p
        join pg_namespace n on n.oid = p.pronamespace
        left join lateral aclexplode(p.proacl) a on true
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    fgRows, err := pool.Query(ctx, fgQ)
    if err != nil { return nil, err }
    for fgRows.Next() {
        var schemaName, funcName, args, role, priv string
        var aclNull, isOwner bool
        if err := fgRows.Scan(&schemaName, &funcName, &args, &role, &priv, &aclNull, &isOwner); err != nil {
            fgRows.Close()
            return nil, err
        }
        key := normalizeFunctionSignature(fmt.Sprintf("%s.%s(%s)", schemaName, funcName, args))
        if aclNull || (role == "public" && priv == "execute") {
            l.FunctionPublicExec[key] = true
        }
        if role == "" || isOwner { continue }
        addLiveGrant(l.FunctionGrants, key, role, priv)
    }
    fgRows.Close()

    // Query schema grants (owner excluded)
    sgQ := `
        select n.nspname,
               case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end,
               lower(a.privilege_type)
        from pg_namespace n
        cross join lateral aclexplode(n.nspacl) a
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and a.grantee <> n.nspowner
    `
    sgRows, err := pool.Query(ctx, sgQ)
    if err != nil { return nil, err }
    for sgRows.Next() {
        var schemaName, role, priv string
        if err := sgRows.Scan(&schemaName, &role, &priv); err != nil {
            sgRows.Close()
            return nil, err
        }
        addLiveGrant(l.SchemaGrants, schemaName, role, priv)
    }
    sgRows.Close()

    // Query existing roles (skip pg_* system roles)
    roleQ := `select rolname from pg_roles where rolname not like 'pg\_%'`
    roleRows, err := pool.Query(ctx, roleQ)
    if err != nil { return nil, err }
    for roleRows.Next() {
        var roleName string
        if err := roleRows.Scan(&roleName); err != nil {
            roleRows.Close()
            return nil, err
        }
        l.Roles[roleName] = true
    }
    roleRows.Close()

    // Query role memberships
    memQ := `
        select m.rolname, r.rolname
        from pg_auth_members am
        join pg_roles m on m.oid = am.member
        join pg_roles r on r.oid = am.roleid
        where m.rolname not like 'pg\_%'
    `
    memRows, err := pool.Query(ctx, memQ)
    if err != nil { return nil, err }
    for memRows.Next() {
        var member, parent string
        if err := memRows.Scan(&member, &parent); err != nil {
            memRows.Close()
            return nil, err
        }
        if l.RoleMembers[member] == nil { l.RoleMembers[member] = map[string]bool{} }
        l.RoleMembers[member][parent] = true
    }
    memRows.Close()

    // Query role comments (shared catalog)
    rcmQ := `
        select r.rolname, d.description
        from pg_shdescription d
        join pg_roles r on r.oid = d.objoid
        where d.classoid = 'pg_authid'::regclass
    `
    rcmRows, err := pool.Query(ctx, rcmQ)
    if err != nil { return nil, err }
    for rcmRows.Next() {
        var roleName, comment string
        if err := rcmRows.Scan(&roleName, &comment); err != nil {
            rcmRows.Close()
            return nil, err
        }
        l.RoleComments[roleName] = comment
    }
    rcmRows.Close()

    // Query comments on relations (tables, views, matviews)
    rcQ := `
        select n.nspname, c.relname, d.description
        from pg_description d
        join pg_class c on c.oid = d.objoid
        join pg_namespace n on n.oid = c.relnamespace
        where d.classoid = 'pg_class'::regclass and d.objsubid = 0
        and c.relkind in ('r', 'p', 'v', 'm', 'S')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    rcRows, err := pool.Query(ctx, rcQ)
    if err != nil { return nil, err }
    for rcRows.Next() {
        var schemaName, relName, comment string
        if err := rcRows.Scan(&schemaName, &relName, &comment); err != nil {
            rcRows.Close()
            return nil, err
        }
        l.RelComments[fmt.Sprintf("%s.%s", schemaName, relName)] = comment
    }
    rcRows.Close()

    // Query column comments
    ccQ := `
        select n.nspname, c.relname, a.attname, d.description
        from pg_description d
        join pg_class c on c.oid = d.objoid
        join pg_namespace n on n.oid = c.relnamespace
        join pg_attribute a on a.attrelid = c.oid and a.attnum = d.objsubid
        where d.classoid = 'pg_class'::regclass and d.objsubid > 0
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    ccRows, err := pool.Query(ctx, ccQ)
    if err != nil { return nil, err }
    for ccRows.Next() {
        var schemaName, relName, colName, comment string
        if err := ccRows.Scan(&schemaName, &relName, &colName, &comment); err != nil {
            ccRows.Close()
            return nil, err
        }
        key := fmt.Sprintf("%s.%s", schemaName, relName)
        if t := l.Tables[key]; t != nil {
            if c := t.Columns[colName]; c != nil { c.Comment = comment }
        }
    }
    ccRows.Close()

    // Query function comments
    fcQ := `
        select n.nspname, p.proname, pg_get_function_identity_arguments(p.oid), d.description
        from pg_description d
        join pg_proc p on p.oid = d.objoid
        join pg_namespace n on n.oid = p.pronamespace
        where d.classoid = 'pg_proc'::regclass
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    fcRows, err := pool.Query(ctx, fcQ)
    if err != nil { return nil, err }
    for fcRows.Next() {
        var schemaName, funcName, args, comment string
        if err := fcRows.Scan(&schemaName, &funcName, &args, &comment); err != nil {
            fcRows.Close()
            return nil, err
        }
        key := normalizeFunctionSignature(fmt.Sprintf("%s.%s(%s)", schemaName, funcName, args))
        l.FunctionComments[key] = comment
    }
    fcRows.Close()

    // Query type comments
    tcQ := `
        select n.nspname, t.typname, d.description
        from pg_description d
        join pg_type t on t.oid = d.objoid
        join pg_namespace n on n.oid = t.typnamespace
        where d.classoid = 'pg_type'::regclass
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    tcRows, err := pool.Query(ctx, tcQ)
    if err != nil { return nil, err }
    for tcRows.Next() {
        var schemaName, typeName, comment string
        if err := tcRows.Scan(&schemaName, &typeName, &comment); err != nil {
            tcRows.Close()
            return nil, err
        }
        l.TypeComments[fmt.Sprintf("%s.%s", schemaName, typeName)] = comment
    }
    tcRows.Close()

    // Query schema comments
    scQ := `
        select n.nspname, d.description
        from pg_description d
        join pg_namespace n on n.oid = d.objoid
        where d.classoid = 'pg_namespace'::regclass
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    scRows, err := pool.Query(ctx, scQ)
    if err != nil { return nil, err }
    for scRows.Next() {
        var schemaName, comment string
        if err := scRows.Scan(&schemaName, &comment); err != nil {
            scRows.Close()
            return nil, err
        }
        l.SchemaComments[schemaName] = comment
    }
    scRows.Close()

    return l, nil
}

// procedureChanged reports whether a live procedure's definition differs from
// desired. Security is compared only when the YAML sets it; body comparison
// is whitespace-trimmed (prosrc stores the body verbatim).
func procedureChanged(p *schema.Procedure, lp *LiveProcedure) bool {
    if strings.TrimSpace(p.Body) != strings.TrimSpace(lp.Body) { return true }
    if p.Security != "" && strings.ToLower(p.Security) != lp.Security { return true }
    return false
}

// functionChanged reports whether a live function's definition differs from
// desired. Volatility/security are compared only when the YAML sets them;
// body comparison is whitespace-trimmed (prosrc stores the body verbatim).
func functionChanged(f *schema.Function, lf *LiveFunction) bool {
    if strings.TrimSpace(f.Body) != strings.TrimSpace(lf.Body) { return true }
    if f.Volatility != "" && strings.ToLower(f.Volatility) != lf.Volatility { return true }
    if f.Security != "" && strings.ToLower(f.Security) != lf.Security { return true }
    if f.Strict != lf.Strict { return true }
    return false
}

// enumAddValueStmts emits ALTER TYPE ... ADD VALUE for desired labels missing
// from live, positioned BEFORE the next desired label that already exists so
// desired order is preserved. Removed/reordered labels are ignored (postgres
// cannot drop enum values).
func enumAddValueStmts(key string, desired, liveLabels []string) []string {
    liveSet := map[string]bool{}
    for _, l := range liveLabels { liveSet[l] = true }
    out := []string{}
    for i, lbl := range desired {
        if liveSet[lbl] { continue }
        pos := ""
        for j := i + 1; j < len(desired); j++ {
            if liveSet[desired[j]] {
                pos = " before " + quoteString(desired[j])
                break
            }
        }
        out = append(out, fmt.Sprintf("alter type %s add value %s%s;", pqIdent(key), quoteString(lbl), pos))
    }
    return out
}

func addLiveGrant(m map[string]map[string]map[string]bool, key, role, priv string) {
    if m[key] == nil { m[key] = map[string]map[string]bool{} }
    if m[key][role] == nil { m[key][role] = map[string]bool{} }
    m[key][role][priv] = true
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
    // Foreign key alters are deferred until all tables are processed so that
    // every PK/unique constraint exists before any FK references it
    // (required for circular FK pairs, which topological sort cannot order).
    deferredFKs := []string{}
    plan := &PlanDiff{}

    // ROLES HAVE HIGHEST PRIORITY - cluster-level, referenced by grants,
    // policies, and memberships later in the plan
    planRoles(plan, live, desired)

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
    for k := range desired.Procedures {
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
    for k := range desired.Sequences {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    for k := range desired.Domains {
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
            // Existing enum: add missing labels (removal/reorder unsupported by postgres)
            if live.Types[e.Key] {
                if td.Kind == "enum" {
                    plan.Alters = append(plan.Alters, enumAddValueStmts(e.Key, td.Labels, live.EnumLabels[e.Key])...)
                }
                continue
            }
            if td.Kind == "enum" {
                labels := make([]string, 0, len(td.Labels))
                for _, l := range td.Labels { labels = append(labels, quoteString(l)) }
                plan.Creates = append(plan.Creates, fmt.Sprintf("create type %s as enum (%s);", pqIdent(e.Key), strings.Join(labels, ", ")))
            } else if td.Kind == "composite" {
                // preserve YAML declaration order (positional ROW(...)::type casts
                // depend on it); attributes missing from the order list follow sorted
                attrs := []string{}
                emitted := map[string]bool{}
                for _, an := range td.AttributeOrder {
                    if at, ok := td.Attributes[an]; ok && !emitted[an] {
                        attrs = append(attrs, fmt.Sprintf("%s %s", pqIdent(an), at))
                        emitted[an] = true
                    }
                }
                keys := make([]string, 0, len(td.Attributes))
                for k := range td.Attributes { if !emitted[k] { keys = append(keys, k) } }
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
            if found {
                lf := live.FunctionDefs[normalizedDesired]
                if lf == nil || !functionChanged(f, lf) { continue }
                // fall through: definition changed, emit CREATE OR REPLACE
            }
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
            verb := "create function"
            if found { verb = "create or replace function" }
            stmt := fmt.Sprintf("%s %s%s returns %s language %s%s as $$\n%s\n$$;", verb, pqIdent(e.Key)+f.ArgsSig, "", f.Returns, f.Language, attrsStr+setClauses, body)
            plan.Creates = append(plan.Creates, stmt)
        case "procedure":
            pr, ok := desired.Procedures[e.Key]
            if !ok || pr == nil { continue }
            norm := normalizeFunctionSignature(e.Key + pr.ArgsSig)
            found := live.Procedures[norm]
            if found {
                lp := live.ProcedureDefs[norm]
                if lp == nil || !procedureChanged(pr, lp) { continue }
                // fall through: definition changed, emit CREATE OR REPLACE
            }
            setClauses := ""
            if len(pr.Set) > 0 {
                keys := make([]string, 0, len(pr.Set))
                for k := range pr.Set { keys = append(keys, k) }
                sort.Strings(keys)
                for _, k := range keys {
                    setClauses += fmt.Sprintf(" set %s = %s", k, pr.Set[k])
                }
            }
            attrsStr := ""
            if pr.Security != "" { attrsStr = " security " + pr.Security }
            verb := "create procedure"
            if found { verb = "create or replace procedure" }
            stmt := fmt.Sprintf("%s %s language %s%s as $$\n%s\n$$;", verb, pqIdent(e.Key)+pr.ArgsSig, pr.Language, attrsStr+setClauses, pr.Body)
            plan.Creates = append(plan.Creates, stmt)
        case "domain":
            dm, ok := desired.Domains[e.Key]
            if !ok || dm == nil || dm.Type == "" { continue }
            // Existing domains are never altered (forward-only; constraint
            // changes would need DROP/ADD CONSTRAINT with table validation)
            if live.Domains[e.Key] { continue }
            plan.Creates = append(plan.Creates, renderDomain(e.Key, dm))
        case "sequence":
            sq, ok := desired.Sequences[e.Key]
            if !ok || sq == nil { continue }
            // Existing sequences are never altered (forward-only, and live
            // option introspection is not supported)
            if live.Sequences[e.Key] { continue }
            plan.Creates = append(plan.Creates, renderSequence(e.Key, sq))
        case "view":
            vw, ok := desired.Views[e.Key]
            if !ok || vw == nil || vw.Query == "" { continue }
            if vw.Materialized {
                if live.MatViews[e.Key] { continue }
                plan.Creates = append(plan.Creates, fmt.Sprintf("create materialized view if not exists %s as %s;", pqIdent(e.Key), vw.Query))
            } else {
                // live view definitions can't be reliably text-compared (pg_get_viewdef
                // rewrites the query), so replacement is opt-in via `replace: true`
                if live.Views[e.Key] && !vw.Replace { continue }
                plan.Creates = append(plan.Creates, fmt.Sprintf("create or replace view %s as %s;", pqIdent(e.Key), vw.Query))
            }
        case "table":
            // Handle tables in dependency order
            dt, ok := desired.Tables[e.Key]
            if !ok || dt == nil { continue }
            fq := e.Key
            lt := live.Tables[fq]
            if lt == nil {
                if dt.PartitionOf != "" {
                    // partition children inherit columns from the parent
                    plan.Creates = append(plan.Creates, fmt.Sprintf("create table if not exists %s partition of %s %s;", pqIdent(fq), pqIdent(dt.PartitionOf), renderPartitionBound(dt.Partition)))
                    applyTableConstraints(plan, fq, dt, nil, &deferredFKs, unsafe)
                    continue
                }
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
                create := fmt.Sprintf("create table if not exists %s (%s)", pqIdent(fq), strings.Join(cols, ", "))
                if dt.PartitionBy != nil {
                    create += " partition by " + renderPartitionBy(dt.PartitionBy)
                }
                plan.Creates = append(plan.Creates, create+";")
                applyTableConstraints(plan, fq, dt, nil, &deferredFKs, unsafe)
            } else {
                // existing table: add missing columns, reconcile attributes
                // of columns present in both
                pkCols := map[string]bool{}
                for _, pc := range dt.PrimaryKey { pkCols[pc] = true }
                colNames := make([]string, 0, len(dt.Columns))
                for cn := range dt.Columns { colNames = append(colNames, cn) }
                sort.Strings(colNames)
                for _, cn := range colNames {
                    c := dt.Columns[cn]
                    lc, ok := lt.Columns[cn]
                    if !ok {
                        plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s add column %s;", pqIdent(fq), renderColumn(cn, c)))
                        continue
                    }
                    plan.Alters = append(plan.Alters, alterColumnStmts(fq, cn, c, lc, pkCols, unsafe)...)
                }
                // apply any missing constraints, indexes, triggers
                applyTableConstraints(plan, fq, dt, lt, &deferredFKs, unsafe)
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
    plan.Alters = append(plan.Alters, deferredFKs...)
    planPolicies(plan, live, desired)
    plan.Alters = append(plan.Alters, planGrants(live, desired)...)
    plan.Alters = append(plan.Alters, planComments(live, desired)...)
    return plan
}

// planRoles emits CREATE ROLE for roles missing from live and GRANT <parent>
// TO <role> for missing memberships. Forward-only: existing roles are never
// altered or dropped; removed memberships are not revoked.
func planRoles(plan *PlanDiff, live *Live, desired *schema.Database) {
    names := make([]string, 0, len(desired.Roles))
    for k := range desired.Roles { names = append(names, k) }
    sort.Strings(names)
    for _, name := range names {
        r := desired.Roles[name]
        if r == nil { continue }
        if !live.Roles[name] {
            plan.Creates = append(plan.Creates, renderRole(r))
        }
        for _, parent := range r.InRoles {
            if parent == "" || live.RoleMembers[name][parent] { continue }
            plan.Alters = append(plan.Alters, fmt.Sprintf("grant %s to %s;", grantRole(parent), grantRole(name)))
        }
    }
}

func renderRole(r *schema.Role) string {
    opts := []string{}
    if r.Login { opts = append(opts, "login") }
    if r.Superuser { opts = append(opts, "superuser") }
    if r.CreateDB { opts = append(opts, "createdb") }
    if r.CreateRole { opts = append(opts, "createrole") }
    if r.Replication { opts = append(opts, "replication") }
    if r.BypassRLS { opts = append(opts, "bypassrls") }
    if r.NoInherit { opts = append(opts, "noinherit") }
    if r.ConnectionLimit >= 0 { opts = append(opts, fmt.Sprintf("connection limit %d", r.ConnectionLimit)) }
    stmt := "create role " + grantRole(r.Name)
    if len(opts) > 0 { stmt += " with " + strings.Join(opts, " ") }
    return stmt + ";"
}

// renderSequence emits CREATE SEQUENCE IF NOT EXISTS with only the options
// set in YAML (postgres defaults apply otherwise).
func renderSequence(fq string, sq *schema.Sequence) string {
    parts := []string{"create sequence if not exists", pqIdent(fq)}
    if sq.As != "" { parts = append(parts, "as "+sq.As) }
    if sq.Increment != "" { parts = append(parts, "increment by "+sq.Increment) }
    if sq.MinValue != "" { parts = append(parts, "minvalue "+sq.MinValue) }
    if sq.MaxValue != "" { parts = append(parts, "maxvalue "+sq.MaxValue) }
    if sq.Start != "" { parts = append(parts, "start with "+sq.Start) }
    if sq.Cache != "" { parts = append(parts, "cache "+sq.Cache) }
    if sq.Cycle { parts = append(parts, "cycle") }
    if sq.OwnedBy != "" { parts = append(parts, "owned by "+ownedByIdent(sq.OwnedBy)) }
    return strings.Join(parts, " ") + ";"
}

// renderPartitionBy renders "range ("col1", "col2")" etc. for PARTITION BY.
// Plain identifiers are quoted; anything else (an expression) passes through.
func renderPartitionBy(pb *schema.PartitionBy) string {
    cols := make([]string, 0, len(pb.Columns))
    for _, c := range pb.Columns { cols = append(cols, partitionKeyExpr(c)) }
    return strings.ToLower(pb.Type) + " (" + strings.Join(cols, ", ") + ")"
}

// partitionKeyExpr quotes a partition key column unless it looks like an
// expression (contains parens, spaces, or an operator).
func partitionKeyExpr(s string) string {
    if strings.ContainsAny(s, "() ,+-/*") {
        return "(" + s + ")"
    }
    return pqIdent(s)
}

// renderPartitionBound renders a partition child's bound clause:
// DEFAULT, FOR VALUES FROM/TO, FOR VALUES IN, or FOR VALUES WITH (hash).
func renderPartitionBound(ps *schema.PartitionSpec) string {
    if ps == nil || ps.Default { return "default" }
    if ps.Modulus >= 0 && ps.Remainder >= 0 {
        return fmt.Sprintf("for values with (modulus %d, remainder %d)", ps.Modulus, ps.Remainder)
    }
    if len(ps.In) > 0 {
        return "for values in (" + joinBoundList(ps.In) + ")"
    }
    return fmt.Sprintf("for values from (%s) to (%s)", joinBoundList(ps.From), joinBoundList(ps.To))
}

func joinBoundList(vals []string) string {
    out := make([]string, 0, len(vals))
    for _, v := range vals { out = append(out, partitionBoundLiteral(v)) }
    return strings.Join(out, ", ")
}

// partitionBoundLiteral renders one bound value: MINVALUE/MAXVALUE keywords,
// numbers, booleans, and NULL stay bare; everything else becomes a string literal.
func partitionBoundLiteral(v string) string {
    t := strings.TrimSpace(v)
    switch strings.ToLower(t) {
    case "minvalue", "maxvalue", "true", "false", "null":
        return strings.ToLower(t)
    }
    if isNumericLiteral(t) { return t }
    return quoteString(t)
}

func isNumericLiteral(s string) bool {
    if s == "" { return false }
    dot := false
    for i, r := range s {
        switch {
        case r >= '0' && r <= '9':
        case r == '-' && i == 0:
        case r == '.' && !dot:
            dot = true
        default:
            return false
        }
    }
    return s != "-" && s != "." && s != "-."
}

// renderDomain emits CREATE DOMAIN with only the options set in YAML.
// CREATE DOMAIN has no IF NOT EXISTS; existence is guarded by live.Domains.
func renderDomain(fq string, dm *schema.Domain) string {
    parts := []string{"create domain", pqIdent(fq), "as", dm.Type}
    if dm.Collate != "" { parts = append(parts, "collate "+pqIdent(dm.Collate)) }
    if dm.Default != "" { parts = append(parts, "default "+dm.Default) }
    if dm.NotNull { parts = append(parts, "not null") }
    if dm.Check != "" {
        if dm.ConstraintName != "" { parts = append(parts, "constraint "+pqIdent(dm.ConstraintName)) }
        parts = append(parts, fmt.Sprintf("check (%s)", dm.Check))
    }
    return strings.Join(parts, " ") + ";"
}

// ownedByIdent quotes a table.column (or schema.table.column) OWNED BY target.
func ownedByIdent(s string) string {
    if strings.EqualFold(strings.TrimSpace(s), "none") { return "none" }
    parts := strings.Split(s, ".")
    for i := range parts { parts[i] = `"` + parts[i] + `"` }
    return strings.Join(parts, ".")
}

// planComments emits COMMENT ON for objects whose desired comment is set and
// differs from live. Empty desired comment = unmanaged (never cleared).
func planComments(live *Live, desired *schema.Database) []string {
    stmts := []string{}
    comment := func(target, want, have string) {
        if want != "" && want != have {
            stmts = append(stmts, fmt.Sprintf("comment on %s is %s;", target, quoteString(want)))
        }
    }

    schemaNames := make([]string, 0, len(desired.SchemaComments))
    for s := range desired.SchemaComments { schemaNames = append(schemaNames, s) }
    sort.Strings(schemaNames)
    for _, s := range schemaNames {
        comment("schema "+pqIdent(s), desired.SchemaComments[s], live.SchemaComments[s])
    }

    roleNames := make([]string, 0, len(desired.Roles))
    for k := range desired.Roles { roleNames = append(roleNames, k) }
    sort.Strings(roleNames)
    for _, k := range roleNames {
        r := desired.Roles[k]
        if r == nil { continue }
        comment("role "+grantRole(k), r.Comment, live.RoleComments[k])
    }

    typeNames := make([]string, 0, len(desired.Types))
    for k := range desired.Types { typeNames = append(typeNames, k) }
    sort.Strings(typeNames)
    for _, k := range typeNames {
        td := desired.Types[k]
        if td == nil { continue }
        comment("type "+pqIdent(k), td.Comment, live.TypeComments[k])
    }

    domainNames := make([]string, 0, len(desired.Domains))
    for k := range desired.Domains { domainNames = append(domainNames, k) }
    sort.Strings(domainNames)
    for _, k := range domainNames {
        dm := desired.Domains[k]
        if dm == nil { continue }
        // domain comments live in pg_type's pg_description entries
        comment("domain "+pqIdent(k), dm.Comment, live.TypeComments[k])
    }

    tableNames := make([]string, 0, len(desired.Tables))
    for k := range desired.Tables { tableNames = append(tableNames, k) }
    sort.Strings(tableNames)
    for _, k := range tableNames {
        dt := desired.Tables[k]
        if dt == nil { continue }
        comment("table "+pqIdent(k), dt.Comment, live.RelComments[k])
        colNames := make([]string, 0, len(dt.Columns))
        for cn := range dt.Columns { colNames = append(colNames, cn) }
        sort.Strings(colNames)
        for _, cn := range colNames {
            c := dt.Columns[cn]
            if c == nil || c.Comment == "" { continue }
            have := ""
            if lt := live.Tables[k]; lt != nil {
                if lc := lt.Columns[cn]; lc != nil { have = lc.Comment }
            }
            comment(fmt.Sprintf("column %s.%s", pqIdent(k), pqIdent(cn)), c.Comment, have)
        }
    }

    viewNames := make([]string, 0, len(desired.Views))
    for k := range desired.Views { viewNames = append(viewNames, k) }
    sort.Strings(viewNames)
    for _, k := range viewNames {
        vw := desired.Views[k]
        if vw == nil { continue }
        kind := "view "
        if vw.Materialized { kind = "materialized view " }
        comment(kind+pqIdent(k), vw.Comment, live.RelComments[k])
    }

    seqNames := make([]string, 0, len(desired.Sequences))
    for k := range desired.Sequences { seqNames = append(seqNames, k) }
    sort.Strings(seqNames)
    for _, k := range seqNames {
        sq := desired.Sequences[k]
        if sq == nil { continue }
        comment("sequence "+pqIdent(k), sq.Comment, live.RelComments[k])
    }

    funcNames := make([]string, 0, len(desired.Functions))
    for k := range desired.Functions { funcNames = append(funcNames, k) }
    sort.Strings(funcNames)
    for _, k := range funcNames {
        f := desired.Functions[k]
        if f == nil { continue }
        norm := normalizeFunctionSignature(k + f.ArgsSig)
        comment("function "+pqIdent(k)+grantFuncArgs(f.ArgsSig), f.Comment, live.FunctionComments[norm])
    }

    procNames := make([]string, 0, len(desired.Procedures))
    for k := range desired.Procedures { procNames = append(procNames, k) }
    sort.Strings(procNames)
    for _, k := range procNames {
        pr := desired.Procedures[k]
        if pr == nil { continue }
        // procedure comments share pg_proc's pg_description entries
        norm := normalizeFunctionSignature(k + pr.ArgsSig)
        comment("procedure "+pqIdent(k)+grantFuncArgs(pr.ArgsSig), pr.Comment, live.FunctionComments[norm])
    }
    return stmts
}

// planGrants reconciles desired grants against live ACLs for schemas, tables,
// and functions. A present grants block is authoritative: missing privileges
// are granted, live privileges absent from the block are revoked. PUBLIC is
// never auto-revoked; for functions use revokePublic.
func planGrants(live *Live, desired *schema.Database) []string {
    stmts := []string{}

    schemaNames := make([]string, 0, len(desired.SchemaGrants))
    for s := range desired.SchemaGrants { schemaNames = append(schemaNames, s) }
    sort.Strings(schemaNames)
    for _, s := range schemaNames {
        stmts = append(stmts, grantDiffStmts("schema "+pqIdent(s), desired.SchemaGrants[s], live.SchemaGrants[s])...)
    }

    tableNames := make([]string, 0, len(desired.Tables))
    for k := range desired.Tables { tableNames = append(tableNames, k) }
    sort.Strings(tableNames)
    for _, k := range tableNames {
        dt := desired.Tables[k]
        if dt == nil || dt.Grants == nil { continue }
        stmts = append(stmts, grantDiffStmts("table "+pqIdent(k), dt.Grants, live.TableGrants[k])...)
    }

    funcNames := make([]string, 0, len(desired.Functions))
    for k := range desired.Functions { funcNames = append(funcNames, k) }
    sort.Strings(funcNames)
    for _, k := range funcNames {
        f := desired.Functions[k]
        if f == nil { continue }
        norm := normalizeFunctionSignature(k + f.ArgsSig)
        target := "function " + pqIdent(k) + grantFuncArgs(f.ArgsSig)
        if f.RevokePublic {
            // new function (not live yet) has default PUBLIC execute
            liveExists := false
            for liveSig := range live.Functions {
                if normalizeFunctionSignature(liveSig) == norm { liveExists = true; break }
            }
            if !liveExists || live.FunctionPublicExec[norm] {
                stmts = append(stmts, fmt.Sprintf("revoke all on %s from public;", target))
            }
        }
        if f.Grants != nil {
            stmts = append(stmts, grantDiffStmts(target, f.Grants, live.FunctionGrants[norm])...)
        }
    }

    procNames := make([]string, 0, len(desired.Procedures))
    for k := range desired.Procedures { procNames = append(procNames, k) }
    sort.Strings(procNames)
    for _, k := range procNames {
        pr := desired.Procedures[k]
        if pr == nil { continue }
        // procedure ACLs share pg_proc's live maps (FunctionGrants/PublicExec)
        norm := normalizeFunctionSignature(k + pr.ArgsSig)
        target := "procedure " + pqIdent(k) + grantFuncArgs(pr.ArgsSig)
        if pr.RevokePublic {
            // new procedure (not live yet) has default PUBLIC execute
            if !live.Procedures[norm] || live.FunctionPublicExec[norm] {
                stmts = append(stmts, fmt.Sprintf("revoke all on %s from public;", target))
            }
        }
        if pr.Grants != nil {
            stmts = append(stmts, grantDiffStmts(target, pr.Grants, live.FunctionGrants[norm])...)
        }
    }
    return stmts
}

// grantDiffStmts returns grant/revoke statements reconciling desired role->privs
// against live role->priv->exists for one target ("table X", "schema Y", ...).
func grantDiffStmts(target string, desired map[string][]string, liveG map[string]map[string]bool) []string {
    out := []string{}
    roles := make([]string, 0, len(desired))
    for r := range desired { roles = append(roles, r) }
    sort.Strings(roles)
    for _, role := range roles {
        missing := []string{}
        for _, priv := range desired[role] {
            p := strings.ToLower(priv)
            if !liveG[role][p] { missing = append(missing, p) }
        }
        if len(missing) > 0 {
            sort.Strings(missing)
            out = append(out, fmt.Sprintf("grant %s on %s to %s;", strings.Join(missing, ", "), target, grantRole(role)))
        }
    }
    liveRoles := make([]string, 0, len(liveG))
    for r := range liveG { liveRoles = append(liveRoles, r) }
    sort.Strings(liveRoles)
    for _, role := range liveRoles {
        if role == "public" { continue } // PUBLIC managed only via revokePublic
        want := map[string]bool{}
        for _, priv := range desired[role] { want[strings.ToLower(priv)] = true }
        extra := []string{}
        for priv := range liveG[role] {
            if !want[priv] { extra = append(extra, priv) }
        }
        if len(extra) > 0 {
            sort.Strings(extra)
            out = append(out, fmt.Sprintf("revoke %s on %s from %s;", strings.Join(extra, ", "), target, grantRole(role)))
        }
    }
    return out
}

// grantRole quotes a role name, leaving the PUBLIC pseudo-role bare.
func grantRole(role string) string {
    if strings.EqualFold(role, "public") { return "public" }
    return `"` + role + `"`
}

// grantFuncArgs strips default clauses from an args signature — GRANT/REVOKE
// ON FUNCTION accepts identity arguments only.
func grantFuncArgs(argsSig string) string {
    inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(argsSig), "("), ")")
    if strings.TrimSpace(inner) == "" { return "()" }
    args := []string{}
    current := ""
    depth := 0
    flush := func() {
        a := strings.TrimSpace(current)
        if lo := strings.Index(strings.ToLower(a), " default "); lo >= 0 { a = strings.TrimSpace(a[:lo]) }
        if eq := strings.Index(a, "="); eq >= 0 { a = strings.TrimSpace(a[:eq]) }
        if a != "" { args = append(args, a) }
        current = ""
    }
    for _, r := range inner {
        switch r {
        case '(':
            depth++
            current += string(r)
        case ')':
            depth--
            current += string(r)
        case ',':
            if depth == 0 { flush() } else { current += string(r) }
        default:
            current += string(r)
        }
    }
    flush()
    return "(" + strings.Join(args, ", ") + ")"
}

// applyTableConstraints emits SQL for primary keys, foreign keys, indexes, constraints, and triggers
// on a table. lt is the live table state (nil for new tables — skip existence checks).
// FK statements go to deferredFKs so the caller can emit them after all PK/unique alters.
// A live constraint whose definition no longer matches the desired one is
// dropped and re-added; since DROP CONSTRAINT can break dependents and FK/unique
// re-validation can fail on existing data, that path is gated behind --unsafe.
func applyTableConstraints(plan *PlanDiff, fq string, dt *schema.Table, lt *LiveTable, deferredFKs *[]string, unsafe bool) {
    liveConstraints := map[string]bool{}
    liveConstraintDefs := map[string]string{}
    liveIndexes := map[string]bool{}
    liveTriggers := map[string]bool{}
    livePK := false
    if lt != nil {
        liveConstraints = lt.Constraints
        liveConstraintDefs = lt.ConstraintDefs
        liveIndexes = lt.Indexes
        liveTriggers = lt.Triggers
        livePK = lt.HasPK
    }

    // Primary key — check by type, not name (Postgres auto-names it tablename_pkey)
    if !livePK {
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
    }

    // Foreign keys
    for _, fk := range dt.ForeignKeys {
        if fk == nil || len(fk.Columns) == 0 || fk.RefTable == "" { continue }
        def := renderFKDef(fk)
        stmt := fmt.Sprintf("alter table %s add constraint %s %s;", pqIdent(fq), pqIdent(fk.Name), def)
        if liveConstraints[fk.Name] {
            if liveDef, ok := liveConstraintDefs[fk.Name]; unsafe && ok && !constraintDefsEqual(def, liveDef) {
                *deferredFKs = append(*deferredFKs, fmt.Sprintf("alter table %s drop constraint %s;", pqIdent(fq), pqIdent(fk.Name)), stmt)
            }
            continue
        }
        *deferredFKs = append(*deferredFKs, stmt)
    }

    // Indexes (always use IF NOT EXISTS)
    for _, ix := range dt.Indexes {
        if ix == nil || len(ix.Columns) == 0 { continue }
        name := ix.Name
        if name == "" { name = strings.ReplaceAll(fq+"_"+strings.Join(ix.Columns, "_"), ".", "_") + "_idx" }
        if liveIndexes[name] { continue }
        uniq := ""
        if ix.Unique { uniq = " unique" }
        plan.Creates = append(plan.Creates, fmt.Sprintf("create%s index if not exists %s on %s(%s);", uniq, pqIdent(name), pqIdent(fq), joinIdentList(ix.Columns)))
    }

    // Named constraints (check, unique, exclude)
    for _, ct := range dt.Constraints {
        if ct == nil || ct.Type == "" { continue }
        def := renderConstraintDef(ct)
        if def == "" { continue }
        stmt := fmt.Sprintf("alter table %s add constraint %s %s;", pqIdent(fq), pqIdent(ct.Name), def)
        if liveConstraints[ct.Name] {
            if liveDef, ok := liveConstraintDefs[ct.Name]; unsafe && ok && !constraintDefsEqual(def, liveDef) {
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s drop constraint %s;", pqIdent(fq), pqIdent(ct.Name)), stmt)
            }
            continue
        }
        plan.Alters = append(plan.Alters, stmt)
    }

    // Column-level unique flags — emit as named unique constraints
    for cn, c := range dt.Columns {
        if !c.Unique { continue }
        name := strings.ReplaceAll(fq+"_"+cn, ".", "_") + "_key"
        if liveConstraints[name] { continue }
        plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s add constraint %s unique (%s);", pqIdent(fq), pqIdent(name), pqIdent(cn)))
    }

    // Triggers
    for _, tr := range dt.Triggers {
        if tr == nil || tr.Procedure == "" { continue }
        if liveTriggers[tr.Name] { continue }
        events := strings.ToUpper(strings.Join(tr.Events, " or "))
        var stmt string
        if tr.Constraint {
            // constraint triggers are always AFTER ... FOR EACH ROW
            deferral := ""
            if tr.InitiallyDeferred {
                deferral = " deferrable initially deferred"
            } else if tr.Deferrable {
                deferral = " deferrable"
            }
            stmt = fmt.Sprintf("create constraint trigger %s AFTER %s on %s%s for each row execute procedure %s;", pqIdent(tr.Name), events, pqIdent(fq), deferral, tr.Procedure)
        } else {
            stmt = fmt.Sprintf("create trigger %s %s %s on %s for each %s execute procedure %s;", pqIdent(tr.Name), strings.ToUpper(tr.Timing), events, pqIdent(fq), strings.ToLower(tr.Level), tr.Procedure)
        }
        plan.Creates = append(plan.Creates, stmt)
    }

}

// planPolicies emits ENABLE ROW LEVEL SECURITY and policy create/drop for all
// tables. Runs after every object create/alter (policies may reference
// functions created later in the same plan than their table — dependsOn cannot
// order this when the table itself is skipped as already live).
func planPolicies(plan *PlanDiff, live *Live, desired *schema.Database) {
    tableNames := make([]string, 0, len(desired.Tables))
    for k := range desired.Tables { tableNames = append(tableNames, k) }
    sort.Strings(tableNames)
    for _, fq := range tableNames {
        dt := desired.Tables[fq]
        if dt == nil { continue }
        lt := live.Tables[fq]

        // Row level security (enable only — forward-only tool, never disables)
        if dt.RowLevelSecurity && (lt == nil || !lt.RLSEnabled) {
            plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s enable row level security;", pqIdent(fq)))
        }

        // Policies — a present policies block is authoritative by name:
        // missing policies are created, live policies not listed are dropped.
        livePolicies := map[string]bool{}
        if lt != nil { livePolicies = lt.Policies }
        desiredPolicies := map[string]bool{}
        for _, pol := range dt.Policies {
            if pol == nil || pol.Name == "" { continue }
            desiredPolicies[pol.Name] = true
            if livePolicies[pol.Name] { continue }
            stmt := fmt.Sprintf("create policy %s on %s", pqIdent(pol.Name), pqIdent(fq))
            if pol.For != "" { stmt += " for " + strings.ToLower(pol.For) }
            if len(pol.To) > 0 {
                roles := make([]string, 0, len(pol.To))
                for _, r := range pol.To { roles = append(roles, grantRole(r)) }
                stmt += " to " + strings.Join(roles, ", ")
            }
            if pol.Using != "" { stmt += fmt.Sprintf(" using (%s)", pol.Using) }
            if pol.WithCheck != "" { stmt += fmt.Sprintf(" with check (%s)", pol.WithCheck) }
            plan.Alters = append(plan.Alters, stmt+";")
        }
        if dt.Policies != nil {
            removed := []string{}
            for name := range livePolicies {
                if !desiredPolicies[name] { removed = append(removed, name) }
            }
            sort.Strings(removed)
            for _, name := range removed {
                plan.Drops = append(plan.Drops, fmt.Sprintf("drop policy %s on %s;", pqIdent(name), pqIdent(fq)))
            }
        }
    }
}

func Render(p *PlanDiff) string {
    statements := make([]string, 0, len(p.Creates)+len(p.Alters)+len(p.Drops))
    statements = append(statements, p.Creates...)
    statements = append(statements, p.Alters...)
    statements = append(statements, p.Drops...)
    if len(statements) == 0 { return "" }
    return strings.Join(statements, "\n") + "\n"
}

// alterColumnStmts reconciles an existing column's live attributes with its
// desired definition. Type changes can rewrite the table or fail on cast, so
// they are gated behind --unsafe (with an optional USING expression from the
// column's `using` property). Identity is create-only and never altered;
// identity/serial columns keep their generated defaults and implicit NOT NULL.
func alterColumnStmts(fq, cn string, c *schema.Column, lc *LiveColumn, pkCols map[string]bool, unsafe bool) []string {
    out := []string{}
    target := fmt.Sprintf("alter table %s alter column %s", pqIdent(fq), pqIdent(cn))

    if unsafe && c.Type != "" && normalizeColumnType(c.Type) != normalizeColumnType(lc.Type) {
        stmt := fmt.Sprintf("%s type %s", target, c.Type)
        if c.Using != "" { stmt += " using " + c.Using }
        out = append(out, stmt+";")
    }

    generated := c.Identity != "" || lc.Identity != "" || isSerialType(c.Type)
    if !generated {
        switch {
        case c.Default != "" && !defaultsEqual(c.Default, lc.Default):
            out = append(out, fmt.Sprintf("%s set default %s;", target, c.Default))
        case c.Default == "" && lc.Default != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(lc.Default)), "nextval("):
            out = append(out, fmt.Sprintf("%s drop default;", target))
        }
    }

    if !c.Nullable && lc.Nullable {
        out = append(out, target+" set not null;")
    } else if c.Nullable && !lc.Nullable && !c.PrimaryKey && !pkCols[cn] && c.Identity == "" && lc.Identity == "" {
        out = append(out, target+" drop not null;")
    }
    return out
}

// columnTypeAliases maps type spellings to one canonical form for comparison.
// Ordered: longer spellings must be rewritten before their substrings.
var columnTypeAliases = [][2]string{
    {"character varying", "varchar"},
    {"double precision", "float8"},
    {"timestamp with time zone", "timestamptz"},
    {"timestamp without time zone", "timestamp"},
    {"time with time zone", "timetz"},
    {"time without time zone", "time"},
    {"character", "char"},
    {"integer", "int"},
    {"int4", "int"},
    {"int8", "bigint"},
    {"int2", "smallint"},
    {"boolean", "bool"},
    {"float4", "real"},
    {"decimal", "numeric"},
    {"bigserial", "bigint"},
    {"smallserial", "smallint"},
    {"serial", "int"},
}

// normalizeColumnType canonicalizes a column type for comparison between YAML
// spellings (varchar(255)) and live format_type output (character varying(255)).
// serial types normalize to their base integer type so a YAML `serial` column
// never diffs against its live `integer` + nextval default form.
func normalizeColumnType(t string) string {
    s := strings.ToLower(strings.TrimSpace(t))
    s = strings.Join(strings.Fields(s), " ")
    // move an inline modifier past a with/without suffix so
    // "timestamp(6) with time zone" aliases like "timestamptz(6)"
    if i := strings.Index(s, "("); i >= 0 {
        if j := strings.Index(s[i:], ")"); j >= 0 {
            base, mod, rest := s[:i], s[i:i+j+1], s[i+j+1:]
            if strings.HasPrefix(rest, " with") {
                s = base + rest + mod
            }
        }
    }
    for _, a := range columnTypeAliases {
        s = strings.ReplaceAll(s, a[0], a[1])
    }
    s = strings.ReplaceAll(s, " (", "(")
    s = strings.ReplaceAll(s, ", ", ",")
    s = strings.ReplaceAll(s, `"`, "")
    return s
}

func isSerialType(t string) bool {
    switch strings.ToLower(strings.TrimSpace(t)) {
    case "serial", "smallserial", "bigserial", "serial2", "serial4", "serial8":
        return true
    }
    return false
}

func defaultsEqual(desired, live string) bool {
    return normalizeDefaultExpr(desired) == normalizeDefaultExpr(live)
}

// normalizeDefaultExpr canonicalizes a default expression for comparison:
// strips ::type casts outside quoted literals (live defaults come back as
// 'x'::text or nextval('s'::regclass)), lowercases everything outside
// single-quoted strings, and collapses whitespace.
func normalizeDefaultExpr(s string) string {
    var b strings.Builder
    inQ := false
    rs := []rune(strings.TrimSpace(s))
    for i := 0; i < len(rs); i++ {
        r := rs[i]
        if r == '\'' {
            inQ = !inQ
            b.WriteRune(r)
            continue
        }
        if inQ {
            b.WriteRune(r)
            continue
        }
        if r == ':' && i+1 < len(rs) && rs[i+1] == ':' {
            i++
            for i+1 < len(rs) && isCastTypeChar(rs[i+1]) { i++ }
            continue
        }
        if r >= 'A' && r <= 'Z' { r += 'a' - 'A' }
        b.WriteRune(r)
    }
    out := strings.Join(strings.Fields(b.String()), " ")
    out = strings.ReplaceAll(out, " (", "(")
    return out
}

// renderFKDef renders the definition body of a foreign key constraint
// (everything after "add constraint <name>").
func renderFKDef(fk *schema.ForeignKey) string {
    def := fmt.Sprintf("foreign key (%s) references %s(%s)", joinIdentList(fk.Columns), pqIdent(fk.RefTable), joinIdentList(fk.RefColumns))
    if fk.OnDelete != "" { def += " on delete " + strings.ToLower(fk.OnDelete) }
    return def
}

// renderConstraintDef renders the definition body of a named check/unique/
// exclude constraint. Returns "" for unknown constraint types.
func renderConstraintDef(ct *schema.Constraint) string {
    switch strings.ToLower(ct.Type) {
    case "check":
        return fmt.Sprintf("check (%s)", ct.Expression)
    case "unique":
        return fmt.Sprintf("unique (%s)", joinIdentList(ct.Columns))
    case "exclude":
        return fmt.Sprintf("exclude %s", ct.Expression)
    }
    return ""
}

func constraintDefsEqual(desired, live string) bool {
    return normalizeConstraintDef(desired) == normalizeConstraintDef(live)
}

// normalizeConstraintDef canonicalizes a constraint definition for comparison
// against pg_get_constraintdef() output: outside single-quoted strings it
// strips ::type casts, double quotes, parentheses, whitespace, and "public."
// qualifiers, and lowercases. Stripping parentheses and whitespace can make
// differently-grouped expressions compare equal; that errs toward emitting no
// SQL, same as the old name-only matching.
func normalizeConstraintDef(s string) string {
    var b strings.Builder
    inQ := false
    rs := []rune(strings.TrimSpace(s))
    for i := 0; i < len(rs); i++ {
        r := rs[i]
        if r == '\'' {
            inQ = !inQ
            b.WriteRune(r)
            continue
        }
        if inQ {
            b.WriteRune(r)
            continue
        }
        if r == ':' && i+1 < len(rs) && rs[i+1] == ':' {
            i++
            for i+1 < len(rs) && isCastTypeChar(rs[i+1]) { i++ }
            continue
        }
        switch r {
        case '"', '(', ')', ' ', '\t', '\n', '\r':
            continue
        }
        if r >= 'A' && r <= 'Z' { r += 'a' - 'A' }
        b.WriteRune(r)
    }
    return strings.ReplaceAll(b.String(), "public.", "")
}

func isCastTypeChar(r rune) bool {
    return r == '_' || r == '.' || r == '[' || r == ']' ||
        (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func renderColumn(name string, c *schema.Column) string {
    parts := []string{pqIdent(name), c.Type}
    if !c.Nullable { parts = append(parts, "not null") }
    if c.Identity != "" {
        // identity columns cannot also have a default expression
        parts = append(parts, "generated "+c.Identity+" as identity")
    } else if c.Default != "" {
        parts = append(parts, "default "+c.Default)
    }
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


