package schema

import (
    "fmt"
    "io/fs"
    "os"
    "sort"
    "strings"
    "time"

    yaml "gopkg.in/yaml.v3"
)

// Minimal schema model: schemas -> tables -> columns
type Database struct {
    Tables map[string]*Table `yaml:"tables"`
    Extensions []*Extension `yaml:"extensions"`
    Types map[string]*TypeDef `yaml:"-"`
    Functions map[string]*Function `yaml:"-"`
    Procedures map[string]*Procedure `yaml:"-"`
    Views map[string]*View `yaml:"-"`
    Sequences map[string]*Sequence `yaml:"-"`
    Domains map[string]*Domain `yaml:"-"`
    Roles map[string]*Role `yaml:"-"`
    SchemaGrants map[string]map[string][]string `yaml:"-"` // schema name -> role -> privileges
    SchemaComments map[string]string `yaml:"-"`            // schema name -> comment
}

// Role is a cluster-level role (CREATE ROLE). Passwords are intentionally
// unsupported: secrets do not belong in schema YAML.
type Role struct {
    Name            string
    Login           bool
    Superuser       bool
    CreateDB        bool
    CreateRole      bool
    Replication     bool
    BypassRLS       bool
    NoInherit       bool     // yaml `inherit: false`
    ConnectionLimit int      // -1 = unset (no clause emitted)
    InRoles         []string // memberships: GRANT <parent> TO <role>
    Comment         string
}

type View struct {
    Schema       string
    Name         string
    Query        string
    Materialized bool
    DependsOn    []string `yaml:"dependsOn"`
    Comment      string
    Replace      bool // always emit CREATE OR REPLACE (live view definitions cannot be reliably text-compared)
    Grants       map[string][]string // role -> privileges; nil means unmanaged
}

// Sequence is an explicit CREATE SEQUENCE declaration. Numeric options are
// stored as strings so zero/negative values are distinguishable from unset.
type Sequence struct {
    Schema    string
    Name      string
    As        string // smallint | integer | bigint
    Increment string
    MinValue  string
    MaxValue  string
    Start     string
    Cache     string
    Cycle     bool
    OwnedBy   string // table.column (or schema.table.column)
    DependsOn []string `yaml:"dependsOn"`
    Comment   string
}

// Domain is a CREATE DOMAIN declaration: a named base type with optional
// default, NOT NULL, and CHECK constraint (which sees the value as VALUE).
type Domain struct {
    Schema         string
    Name           string
    Type           string // underlying data type
    Collate        string
    Default        string
    NotNull        bool
    Check          string // CHECK expression (without surrounding parens)
    ConstraintName string // optional name for the CHECK constraint
    DependsOn      []string `yaml:"dependsOn"`
    Comment        string
}

type Table struct {
    Name    string             `yaml:"name"`
    Columns map[string]*Column `yaml:"columns"`
    PrimaryKey []string        `yaml:"-"`
    Indexes    []*Index        `yaml:"-"`
    ForeignKeys []*ForeignKey  `yaml:"-"`
    Triggers   []*Trigger      `yaml:"-"`
    Constraints []*Constraint  `yaml:"-"`
    ColumnOrder []string       `yaml:"-"`
    DependsOn []string         `yaml:"dependsOn"`
    Grants map[string][]string `yaml:"-"` // role -> privileges; nil means unmanaged
    // RowLevelSecurity is tri-state: nil means unmanaged (RLS state is left
    // alone), true means ENABLE, false means DISABLE (gated behind --unsafe,
    // since disabling removes row filtering from every policy on the table).
    RowLevelSecurity *bool     `yaml:"-"`
    Policies []*Policy         `yaml:"-"` // nil means unmanaged
    Comment string             `yaml:"-"` // empty means unmanaged
    PartitionBy *PartitionBy   `yaml:"-"` // declarative partitioning key (parent tables)
    PartitionOf string         `yaml:"-"` // fully qualified parent table (partition children)
    Partition   *PartitionSpec `yaml:"-"` // bound spec for partition children
}

// PartitionBy declares a table's partitioning strategy (PARTITION BY).
// Columns may be plain column names or expressions.
type PartitionBy struct {
    Type    string   // range | list | hash
    Columns []string
}

// PartitionSpec is a partition child's bound (FOR VALUES ... / DEFAULT).
// Values are stored as raw strings; the diff layer quotes non-numeric,
// non-keyword values as SQL string literals.
type PartitionSpec struct {
    From      []string // range: FOR VALUES FROM (...)
    To        []string // range: ... TO (...)
    In        []string // list: FOR VALUES IN (...)
    Modulus   int      // hash: -1 = unset
    Remainder int      // hash: -1 = unset
    Default   bool     // DEFAULT partition (no FOR VALUES)
}

type Policy struct {
    Name      string
    For       string   // select|insert|update|delete|all (empty = all)
    To        []string // roles (empty = all roles)
    Using     string
    WithCheck string
}

type Constraint struct {
    Name       string
    Type       string   // check, unique, exclude
    Expression string   // for check or exclude
    Columns    []string // for unique
}

type Column struct {
    Type     string `yaml:"type"`
    Nullable bool   `yaml:"nullable"`
    Default  string `yaml:"default"`
    Unique   bool   `yaml:"unique"`
    PrimaryKey bool `yaml:"primaryKey"`
    Comment  string `yaml:"comment"`
    Identity string `yaml:"identity"` // "" (none), "always", or "by default"
    Using    string `yaml:"using"`    // USING expression for ALTER COLUMN ... TYPE
    Grants   map[string][]string `yaml:"-"` // role -> column privileges; nil means unmanaged
}

type Index struct {
    Name      string
    Columns   []string
    Unique    bool
    Using     string            // index method: btree (default), gist, gin, brin, hash, spgist
    Where     string            // partial index predicate
    Opclass   string            // operator class applied to every column (e.g. gin_trgm_ops)
    Opclasses map[string]string // per-column operator class overrides; key matches the columns entry
}

type ForeignKey struct {
    Name      string
    Columns   []string
    RefTable  string
    RefColumns []string
    OnDelete  string
}

type Trigger struct {
    Name    string
    Timing  string // before/after
    Events  []string
    Level   string // row/statement
    Procedure string
    When    string // WHEN (condition) guard, e.g. "old.created_at <= new.created_at"
    Constraint bool        // CREATE CONSTRAINT TRIGGER (must be AFTER ... FOR EACH ROW)
    Deferrable bool
    InitiallyDeferred bool // implies deferrable
}

type Extension struct {
    Name        string `yaml:"name"`
    IfNotExists bool   `yaml:"ifNotExists"`
    DependsOn   []string `yaml:"dependsOn"`
}

type TypeDef struct {
    Name   string
    Schema string
    Kind   string   // enum|composite
    Labels []string // enum
    Attributes map[string]string // composite: name->type
    AttributeOrder []string      // composite: YAML declaration order (filled from node tree)
    DependsOn []string `yaml:"dependsOn"`
    Comment string
}

type Function struct {
    Schema  string
    Name    string
    ArgsSig string
    Returns string
    Language string
    Security string // definer/invoker
    Volatility string // stable/volatile/immutable
    Strict bool
    Leakproof bool
    Set map[string]string
    Body string
    DependsOn []string `yaml:"dependsOn"`
    Grants map[string][]string // role -> privileges; nil means unmanaged
    RevokePublic bool          // revoke default PUBLIC execute (security-definer pattern)
    Comment string             // empty means unmanaged
}

// Procedure is a CREATE PROCEDURE declaration (PostgreSQL 11+). Unlike
// functions, procedures have no return type, volatility, or strictness;
// they are invoked with CALL and may control transactions.
type Procedure struct {
    Schema   string
    Name     string
    ArgsSig  string
    Language string
    Security string // definer/invoker
    Set      map[string]string
    Body     string
    DependsOn []string `yaml:"dependsOn"`
    Grants map[string][]string // role -> privileges; nil means unmanaged
    RevokePublic bool          // revoke default PUBLIC execute
    Comment string             // empty means unmanaged
}

func LoadAndMerge(paths []string) (*Database, error) {
    merged := &Database{Tables: map[string]*Table{}}
    for _, p := range paths {
        b, err := os.ReadFile(p)
        if err != nil {
            if errorsIsNotExist(err) {
                continue
            }
            return nil, err
        }
        d, err := parseFlexibleDatabase(b)
        if err != nil {
            return nil, fmt.Errorf("%s: %w", p, err)
        }
        for name, t := range d.Tables {
            if t != nil {
                t.Name = name
            }
            if existing, ok := merged.Tables[name]; ok {
                if existing.Columns == nil {
                    existing.Columns = map[string]*Column{}
                }
                for cn, c := range t.Columns {
                    existing.Columns[cn] = c
                }
                if t.Grants != nil {
                    existing.Grants = t.Grants
                }
                if t.RowLevelSecurity != nil {
                    existing.RowLevelSecurity = t.RowLevelSecurity
                }
                if t.Policies != nil {
                    existing.Policies = t.Policies
                }
                if t.Comment != "" {
                    existing.Comment = t.Comment
                }
                if t.PartitionBy != nil {
                    existing.PartitionBy = t.PartitionBy
                }
                if t.PartitionOf != "" {
                    existing.PartitionOf = t.PartitionOf
                }
                if t.Partition != nil {
                    existing.Partition = t.Partition
                }
                if len(existing.PrimaryKey) == 0 {
                    existing.PrimaryKey = t.PrimaryKey
                }
                existing.Indexes = mergeNamed(existing.Indexes, t.Indexes, func(x *Index) string { return x.Name })
                existing.ForeignKeys = mergeNamed(existing.ForeignKeys, t.ForeignKeys, func(x *ForeignKey) string { return x.Name })
                existing.Constraints = mergeNamed(existing.Constraints, t.Constraints, func(x *Constraint) string { return x.Name })
                existing.Triggers = mergeNamed(existing.Triggers, t.Triggers, func(x *Trigger) string { return x.Name })
                existing.DependsOn = appendUnique(existing.DependsOn, t.DependsOn)
            } else {
                merged.Tables[name] = t
            }
        }
        // merge extensions (dedupe by name; first declaration wins)
        for _, ext := range d.Extensions {
            if ext == nil { continue }
            dup := false
            for _, e := range merged.Extensions {
                if e != nil && e.Name == ext.Name { dup = true; break }
            }
            if !dup {
                merged.Extensions = append(merged.Extensions, ext)
            }
        }
        // merge types
        if len(d.Types) > 0 {
            if merged.Types == nil { merged.Types = map[string]*TypeDef{} }
            for k, v := range d.Types { merged.Types[k] = v }
        }
        // merge functions
        if len(d.Functions) > 0 {
            if merged.Functions == nil { merged.Functions = map[string]*Function{} }
            for k, v := range d.Functions { merged.Functions[k] = v }
        }
        // merge procedures
        if len(d.Procedures) > 0 {
            if merged.Procedures == nil { merged.Procedures = map[string]*Procedure{} }
            for k, v := range d.Procedures { merged.Procedures[k] = v }
        }
        // merge views
        if len(d.Views) > 0 {
            if merged.Views == nil { merged.Views = map[string]*View{} }
            for k, v := range d.Views { merged.Views[k] = v }
        }
        // merge sequences
        if len(d.Sequences) > 0 {
            if merged.Sequences == nil { merged.Sequences = map[string]*Sequence{} }
            for k, v := range d.Sequences { merged.Sequences[k] = v }
        }
        // merge domains
        if len(d.Domains) > 0 {
            if merged.Domains == nil { merged.Domains = map[string]*Domain{} }
            for k, v := range d.Domains { merged.Domains[k] = v }
        }
        // merge roles
        if len(d.Roles) > 0 {
            if merged.Roles == nil { merged.Roles = map[string]*Role{} }
            for k, v := range d.Roles { merged.Roles[k] = v }
        }
        // merge schema grants
        if len(d.SchemaGrants) > 0 {
            if merged.SchemaGrants == nil { merged.SchemaGrants = map[string]map[string][]string{} }
            for k, v := range d.SchemaGrants { merged.SchemaGrants[k] = v }
        }
        // merge schema comments
        if len(d.SchemaComments) > 0 {
            if merged.SchemaComments == nil { merged.SchemaComments = map[string]string{} }
            for k, v := range d.SchemaComments { merged.SchemaComments[k] = v }
        }
    }
    return merged, nil
}

// parseFlexibleDatabase accepts multiple structures:
// 1) { tables: { t: { columns: { c: {type,...} } } } }
// 2) { tables: [ { name: t, schema: public, columns: [ {name: c, type:...} ] } ] }
// 3) { schemas: { public: { tables: {... or [...] } } } }
func parseFlexibleDatabase(b []byte) (*Database, error) {
    // Generic map for flexible parsing
    var root map[string]any
    if err := yaml.Unmarshal(b, &root); err != nil {
        return nil, err
    }
    // Also capture YAML node tree to preserve order information
    var node yaml.Node
    _ = yaml.Unmarshal(b, &node)
    out := &Database{Tables: map[string]*Table{}}
    out.Types = map[string]*TypeDef{}
    out.Functions = map[string]*Function{}
    out.Procedures = map[string]*Procedure{}
    out.Views = map[string]*View{}
    out.Sequences = map[string]*Sequence{}
    out.Domains = map[string]*Domain{}
    // extensions top-level
    if extsRaw, ok := root["extensions"]; ok {
        if arr, ok := extsRaw.([]any); ok {
            for _, it := range arr {
                switch v := it.(type) {
                case string:
                    // shorthand: `extensions: [pg_trgm, pgcrypto]` — implies
                    // ifNotExists, matching the tool's if-not-exists style
                    if v != "" {
                        out.Extensions = append(out.Extensions, &Extension{Name: v, IfNotExists: true})
                    }
                case map[string]any:
                    name, _ := v["name"].(string)
                    if name == "" { continue }
                    ext := &Extension{Name: name}
                    if b, ok := v["ifNotExists"].(bool); ok { ext.IfNotExists = b }
                    if dep, ok := v["dependsOn"]; ok {
                        ext.DependsOn = parseStringListFromNode(dep)
                    }
                    out.Extensions = append(out.Extensions, ext)
                }
            }
        }
    }
    // Handle schemas form
    if schRaw, ok := root["schemas"]; ok {
        if m, ok := schRaw.(map[string]any); ok {
            for schemaName, v := range m {
                // intercept schema-level grants/comment so they are not parsed as tables
                if inner, ok := v.(map[string]any); ok {
                    if gRaw, ok := inner["grants"]; ok {
                        if g := parseGrants(gRaw); g != nil {
                            if out.SchemaGrants == nil { out.SchemaGrants = map[string]map[string][]string{} }
                            out.SchemaGrants[schemaName] = g
                            delete(inner, "grants")
                        }
                    }
                    if cm, ok := inner["comment"].(string); ok {
                        if cm != "" {
                            if out.SchemaComments == nil { out.SchemaComments = map[string]string{} }
                            out.SchemaComments[schemaName] = cm
                        }
                        delete(inner, "comment")
                    }
                }
                mergeTablesInto(out, schemaName, v)
            }
        }
    }
    // roles top-level (cluster-scoped, no schema qualification)
    if rolesRaw, ok := root["roles"]; ok {
        if r := parseRoles(rolesRaw); len(r) > 0 {
            out.Roles = r
        }
    }
    // Handle top-level tables
    if tblRaw, ok := root["tables"]; ok {
        mergeTablesInto(out, "", tblRaw)
    }
    // Handle keys like "schema public:" blocks
    for k, v := range root {
        if strings.HasPrefix(k, "schema ") {
            schemaName := strings.TrimSpace(strings.TrimPrefix(k, "schema "))
            mergeSchemaBlock(out, schemaName, v)
            // fill column order from node if available
            fillColumnOrderFromNode(&node, out, schemaName)
        }
    }
    return out, nil
}

// mergeNamed appends items from add to base, replacing base entries whose
// name matches (later files win). Unnamed entries are always appended.
func mergeNamed[T any](base, add []T, name func(T) string) []T {
    for _, a := range add {
        n := name(a)
        replaced := false
        if n != "" {
            for i, b := range base {
                if name(b) == n {
                    base[i] = a
                    replaced = true
                    break
                }
            }
        }
        if !replaced {
            base = append(base, a)
        }
    }
    return base
}

// appendUnique appends items from add that are not already in base.
func appendUnique(base, add []string) []string {
    for _, a := range add {
        found := false
        for _, b := range base {
            if b == a {
                found = true
                break
            }
        }
        if !found {
            base = append(base, a)
        }
    }
    return base
}

func mergeTablesInto(db *Database, defaultSchema string, v any) {
    switch tt := v.(type) {
    case map[string]any:
        // tables map: name -> spec
        for tname, tv := range tt {
            fq := qualify(defaultSchema, tname)
            t := &Table{Name: fq, Columns: map[string]*Column{}}
            if m, ok := tv.(map[string]any); ok {
                if cRaw, ok := m["columns"]; ok {
                    t.Columns = parseColumns(cRaw)
                }
                if pkRaw, ok := m["primaryKey"]; ok {
                    t.PrimaryKey = parseStringListFromNode(pkRaw)
                }
                if idxRaw, ok := m["indexes"]; ok {
                    t.Indexes = parseIndexes(idxRaw)
                }
                if fkRaw, ok := m["foreignKeys"]; ok {
                    t.ForeignKeys = parseForeignKeys(fkRaw)
                }
                if trgRaw, ok := m["triggers"]; ok {
                    t.Triggers = parseTriggers(trgRaw)
                }
                if conRaw, ok := m["constraints"]; ok {
                    t.Constraints = parseConstraints(conRaw)
                }
                if dep, ok := m["dependsOn"]; ok {
                    t.DependsOn = parseStringListFromNode(dep)
                }
                if gRaw, ok := m["grants"]; ok {
                    t.Grants = parseGrants(gRaw)
                }
                if rls, ok := m["rowLevelSecurity"].(bool); ok {
                    t.RowLevelSecurity = &rls
                }
                if polRaw, ok := m["policies"]; ok {
                    t.Policies = parsePolicies(polRaw)
                }
                if cm, ok := m["comment"].(string); ok { t.Comment = cm }
                parseTablePartitioning(t, defaultSchema, m)
            }
            db.Tables[fq] = t
        }
    case []any:
        // tables array: each element has name/schema/columns
        for _, item := range tt {
            m, ok := item.(map[string]any)
            if !ok { continue }
            name, _ := m["name"].(string)
            schemaName := defaultSchema
            if sc, ok := m["schema"].(string); ok && sc != "" {
                schemaName = sc
            }
            fq := qualify(schemaName, name)
            t := &Table{Name: fq, Columns: parseColumns(m["columns"]) }
            if pkRaw, ok := m["primaryKey"]; ok {
                t.PrimaryKey = parseStringListFromNode(pkRaw)
            }
            if idxRaw, ok := m["indexes"]; ok {
                t.Indexes = parseIndexes(idxRaw)
            }
            if fkRaw, ok := m["foreignKeys"]; ok {
                t.ForeignKeys = parseForeignKeys(fkRaw)
            }
            if trgRaw, ok := m["triggers"]; ok {
                t.Triggers = parseTriggers(trgRaw)
            }
            if conRaw, ok := m["constraints"]; ok {
                t.Constraints = parseConstraints(conRaw)
            }
            if dep, ok := m["dependsOn"]; ok {
                t.DependsOn = parseStringListFromNode(dep)
            }
            if gRaw, ok := m["grants"]; ok {
                t.Grants = parseGrants(gRaw)
            }
            if rls, ok := m["rowLevelSecurity"].(bool); ok {
                t.RowLevelSecurity = &rls
            }
            if polRaw, ok := m["policies"]; ok {
                t.Policies = parsePolicies(polRaw)
            }
            if cm, ok := m["comment"].(string); ok { t.Comment = cm }
            parseTablePartitioning(t, schemaName, m)
            db.Tables[fq] = t
        }
    }
}

// mergeSchemaBlock parses blocks of the form:
// schema <name>:
//   table <t>:
//     columns: { ... }
//   table <t2>:
//     columns: [...]
func mergeSchemaBlock(db *Database, schemaName string, v any) {
    m, ok := v.(map[string]any)
    if !ok { return }
    for key, body := range m {
        if strings.HasPrefix(key, "table ") {
            tname := strings.TrimSpace(strings.TrimPrefix(key, "table "))
            fq := qualify(schemaName, tname)
            t := &Table{Name: fq, Columns: map[string]*Column{}}
            if inner, ok := body.(map[string]any); ok {
                if cRaw, ok := inner["columns"]; ok {
                    t.Columns = parseColumns(cRaw)
                }
                if pkRaw, ok := inner["primaryKey"]; ok {
                    t.PrimaryKey = parseStringListFromNode(pkRaw)
                }
                if idxRaw, ok := inner["indexes"]; ok {
                    t.Indexes = parseIndexes(idxRaw)
                }
                if fkRaw, ok := inner["foreignKeys"]; ok {
                    t.ForeignKeys = parseForeignKeys(fkRaw)
                }
                if trgRaw, ok := inner["triggers"]; ok {
                    t.Triggers = parseTriggers(trgRaw)
                }
                if conRaw, ok := inner["constraints"]; ok {
                    t.Constraints = parseConstraints(conRaw)
                }
                if dep, ok := inner["dependsOn"]; ok {
                    t.DependsOn = parseStringListFromNode(dep)
                }
                if gRaw, ok := inner["grants"]; ok {
                    t.Grants = parseGrants(gRaw)
                }
                if rls, ok := inner["rowLevelSecurity"].(bool); ok {
                    t.RowLevelSecurity = &rls
                }
                if polRaw, ok := inner["policies"]; ok {
                    t.Policies = parsePolicies(polRaw)
                }
                if cm, ok := inner["comment"].(string); ok { t.Comment = cm }
                parseTablePartitioning(t, schemaName, inner)
            }
            db.Tables[fq] = t
        } else if strings.HasPrefix(key, "function ") {
            fn := parseFunction(schemaName, key, body)
            if fn != nil {
                full := qualify(schemaName, fn.Name)
                db.Functions[full] = fn
            }
        } else if strings.HasPrefix(key, "procedure ") {
            pr := parseProcedure(schemaName, key, body)
            if pr != nil {
                full := qualify(schemaName, pr.Name)
                db.Procedures[full] = pr
            }
        } else if strings.HasPrefix(key, "type ") {
            td := parseType(schemaName, key, body)
            if td != nil {
                full := qualify(schemaName, td.Name)
                db.Types[full] = td
            }
        } else if strings.HasPrefix(key, "view ") || strings.HasPrefix(key, "materialized view ") {
            vw := parseView(schemaName, key, body)
            if vw != nil {
                full := qualify(schemaName, vw.Name)
                db.Views[full] = vw
            }
        } else if strings.HasPrefix(key, "sequence ") {
            sq := parseSequence(schemaName, key, body)
            if sq != nil {
                full := qualify(schemaName, sq.Name)
                db.Sequences[full] = sq
            }
        } else if strings.HasPrefix(key, "domain ") {
            dm := parseDomain(schemaName, key, body)
            if dm != nil {
                full := qualify(schemaName, dm.Name)
                db.Domains[full] = dm
            }
        } else if key == "grants" {
            if g := parseGrants(body); g != nil {
                if db.SchemaGrants == nil { db.SchemaGrants = map[string]map[string][]string{} }
                db.SchemaGrants[schemaName] = g
            }
        } else if key == "comment" {
            if cm, ok := body.(string); ok && cm != "" {
                if db.SchemaComments == nil { db.SchemaComments = map[string]string{} }
                db.SchemaComments[schemaName] = cm
            }
        }
    }
}

func parseColumns(v any) map[string]*Column {
    cols := map[string]*Column{}
    switch cc := v.(type) {
    case map[string]any:
        for name, spec := range cc {
            cols[name] = parseColumnSpec(spec)
        }
    case []any:
        for _, item := range cc {
            if m, ok := item.(map[string]any); ok {
                name, _ := m["name"].(string)
                cols[name] = parseColumnSpec(m)
            }
        }
    }
    return cols
}

func parseColumnSpec(spec any) *Column {
    c := &Column{}
    if m, ok := spec.(map[string]any); ok {
        if t, ok := m["type"].(string); ok { c.Type = t }
        if n, ok := m["nullable"].(bool); ok { c.Nullable = n }
        if nn, ok := m["notNull"].(bool); ok { c.Nullable = !nn }
        if d, ok := m["default"]; ok { c.Default = defaultToString(d) }
        if u, ok := m["unique"].(bool); ok { c.Unique = u }
        if pk, ok := m["primaryKey"].(bool); ok { c.PrimaryKey = pk }
        if cm, ok := m["comment"].(string); ok { c.Comment = cm }
        if id, ok := m["identity"]; ok { c.Identity = parseIdentity(id) }
        if u, ok := m["using"].(string); ok { c.Using = u }
        if gRaw, ok := m["grants"]; ok { c.Grants = parseGrants(gRaw) }
    }
    return c
}

// parseIdentity normalizes the `identity` column property to "always" or
// "by default". Accepts `always`, `byDefault`, `by default`, `default`, and
// bare `true` (treated as ALWAYS, the stricter form). Unknown values = unset.
func parseIdentity(v any) string {
    switch x := v.(type) {
    case bool:
        if x { return "always" }
    case string:
        switch strings.ToLower(strings.TrimSpace(x)) {
        case "always":
            return "always"
        case "bydefault", "by default", "default":
            return "by default"
        }
    }
    return ""
}

// defaultToString coerces a YAML scalar default (bool, int, float) to its SQL
// literal form so `default: false` is not silently dropped.
func defaultToString(v any) string {
    switch x := v.(type) {
    case string:
        return x
    case bool, int, int64, uint64, float64:
        return fmt.Sprintf("%v", x)
    }
    return ""
}

// parsePolicies parses { name: {for, to, using, withCheck}, ... } sorted by name.
func parsePolicies(v any) []*Policy {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    out := []*Policy{}
    for name, def := range m {
        p := &Policy{Name: name}
        if dm, ok := def.(map[string]any); ok {
            if f, ok := dm["for"].(string); ok { p.For = strings.ToLower(f) }
            if to, ok := dm["to"].(string); ok {
                p.To = []string{to}
            } else if to, ok := dm["to"]; ok {
                p.To = parseStringListFromNode(to)
            }
            if u, ok := dm["using"].(string); ok { p.Using = u }
            if wc, ok := dm["withCheck"].(string); ok { p.WithCheck = wc }
        }
        out = append(out, p)
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

// parseRoles parses { name: {login, superuser, createdb, createrole,
// replication, bypassRLS, inherit, connectionLimit, inRoles, comment}, ... }.
func parseRoles(v any) map[string]*Role {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    out := map[string]*Role{}
    for name, def := range m {
        r := &Role{Name: name, ConnectionLimit: -1}
        if dm, ok := def.(map[string]any); ok {
            if b, ok := dm["login"].(bool); ok { r.Login = b }
            if b, ok := dm["superuser"].(bool); ok { r.Superuser = b }
            if b, ok := dm["createdb"].(bool); ok { r.CreateDB = b }
            if b, ok := dm["createrole"].(bool); ok { r.CreateRole = b }
            if b, ok := dm["replication"].(bool); ok { r.Replication = b }
            if b, ok := dm["bypassRLS"].(bool); ok { r.BypassRLS = b }
            if b, ok := dm["inherit"].(bool); ok { r.NoInherit = !b }
            if n, ok := dm["connectionLimit"].(int); ok { r.ConnectionLimit = n }
            if ir, ok := dm["inRoles"]; ok { r.InRoles = parseStringListFromNode(ir) }
            if cm, ok := dm["comment"].(string); ok { r.Comment = cm }
        }
        out[name] = r
    }
    return out
}

// parseGrants parses { role: [priv, ...], ... }. Privileges are lowercased.
// Returns nil (unmanaged) if the value has no valid role -> list entries.
// parseGrants builds the role -> privileges map for a grants block. A role
// mapped to an empty list is kept as an empty (non-nil) entry rather than
// dropped: it means "this role should hold no privileges here", which the diff
// layer turns into a REVOKE. Likewise a present-but-empty block returns an
// empty map, not nil — nil is reserved for "no grants block at all", i.e.
// unmanaged, where live privileges are left untouched.
func parseGrants(v any) map[string][]string {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    out := map[string][]string{}
    for role, privs := range m {
        list := parseStringListFromNode(privs)
        for i := range list { list[i] = strings.ToLower(strings.TrimSpace(list[i])) }
        sort.Strings(list)
        out[role] = list
    }
    return out
}

// BoolPtr returns a pointer to b, for setting tri-state fields such as
// Table.RowLevelSecurity where nil means unmanaged.
func BoolPtr(b bool) *bool { return &b }

func qualify(schemaName, tableName string) string {
    if tableName == "" { return tableName }
    if strings.Contains(tableName, ".") { return tableName }
    if schemaName == "" { schemaName = "public" }
    return schemaName + "." + tableName
}

func parseStringListFromNode(v any) []string {
    out := []string{}
    switch x := v.(type) {
    case []any:
        for _, it := range x {
            if s, ok := it.(string); ok { out = append(out, s) }
        }
    case map[string]any:
        // map name -> {columns:[...]}
        for _, def := range x {
            if m, ok := def.(map[string]any); ok {
                if cols, ok := m["columns"]; ok {
                    out = append(out, parseStringListFromNode(cols)...)
                }
            }
        }
    }
    return out
}

func parseIndexes(v any) []*Index {
    out := []*Index{}
    if m, ok := v.(map[string]any); ok {
        for name, def := range m {
            ix := &Index{Name: name}
            if dm, ok := def.(map[string]any); ok {
                ix.Columns = parseStringListFromNode(dm["columns"])
                if u, ok := dm["unique"].(bool); ok { ix.Unique = u }
                if m, ok := dm["using"].(string); ok { ix.Using = m }
                if w, ok := dm["where"].(string); ok { ix.Where = w }
                if oc, ok := dm["opclass"].(string); ok { ix.Opclass = oc }
                if ocm, ok := dm["opclasses"].(map[string]any); ok {
                    ix.Opclasses = map[string]string{}
                    for col, v := range ocm {
                        if s, ok := v.(string); ok { ix.Opclasses[col] = s }
                    }
                }
            }
            out = append(out, ix)
        }
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

func parseForeignKeys(v any) []*ForeignKey {
    out := []*ForeignKey{}
    if m, ok := v.(map[string]any); ok {
        for name, def := range m {
            fk := &ForeignKey{Name: name}
            if dm, ok := def.(map[string]any); ok {
                fk.Columns = parseStringListFromNode(dm["columns"])
                if ref, ok := dm["references"].(map[string]any); ok {
                    if t, ok := ref["table"].(string); ok { fk.RefTable = t }
                    fk.RefColumns = parseStringListFromNode(ref["columns"])
                }
                if od, ok := dm["onDelete"].(string); ok { fk.OnDelete = od }
            }
            out = append(out, fk)
        }
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

func parseTriggers(v any) []*Trigger {
    out := []*Trigger{}
    if m, ok := v.(map[string]any); ok {
        for name, def := range m {
            tr := &Trigger{Name: name}
            if dm, ok := def.(map[string]any); ok {
                if t, ok := dm["timing"].(string); ok { tr.Timing = t }
                tr.Events = parseStringListFromNode(dm["events"])
                if l, ok := dm["level"].(string); ok { tr.Level = l }
                if p, ok := dm["procedure"].(string); ok { tr.Procedure = p }
                if w, ok := dm["when"].(string); ok { tr.When = w }
                if c, ok := dm["constraint"].(bool); ok { tr.Constraint = c }
                if d, ok := dm["deferrable"].(bool); ok { tr.Deferrable = d }
                if id, ok := dm["initiallyDeferred"].(bool); ok { tr.InitiallyDeferred = id }
            }
            out = append(out, tr)
        }
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

func parseConstraints(v any) []*Constraint {
    out := []*Constraint{}
    if m, ok := v.(map[string]any); ok {
        for name, def := range m {
            c := &Constraint{Name: name}
            if dm, ok := def.(map[string]any); ok {
                if t, ok := dm["type"].(string); ok { c.Type = t }
                if e, ok := dm["expression"].(string); ok { c.Expression = e }
                if e, ok := dm["def"].(string); ok { c.Expression = e } // alias
                if cols, ok := dm["columns"]; ok { c.Columns = parseStringListFromNode(cols) }
            }
            out = append(out, c)
        }
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

// parseTablePartitioning fills partitioning properties from a table's YAML map:
// `partitionBy` (parent), `partitionOf` + `forValues`/`default` (child).
func parseTablePartitioning(t *Table, schemaName string, m map[string]any) {
    if pbRaw, ok := m["partitionBy"]; ok {
        t.PartitionBy = parsePartitionBy(pbRaw)
    }
    if po, ok := m["partitionOf"].(string); ok && po != "" {
        t.PartitionOf = qualify(schemaName, po)
        spec := &PartitionSpec{Modulus: -1, Remainder: -1}
        if fv, ok := m["forValues"]; ok {
            if parsed := parsePartitionSpec(fv); parsed != nil { spec = parsed }
        }
        if b, ok := m["default"].(bool); ok { spec.Default = b }
        t.Partition = spec
    }
}

// parsePartitionBy accepts either the explicit form
//   { type: range, columns: [c1, c2] }
// or the shorthand form keyed by strategy:
//   { range: [c1] } / { list: [c1] } / { hash: [c1] }
func parsePartitionBy(v any) *PartitionBy {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    pb := &PartitionBy{}
    if t, ok := m["type"].(string); ok { pb.Type = strings.ToLower(strings.TrimSpace(t)) }
    if cols, ok := m["columns"]; ok { pb.Columns = parseStringListFromNode(cols) }
    for _, k := range []string{"range", "list", "hash"} {
        if cols, ok := m[k]; ok {
            pb.Type = k
            pb.Columns = parseStringListFromNode(cols)
        }
    }
    if pb.Type == "" || len(pb.Columns) == 0 { return nil }
    return pb
}

// parsePartitionSpec parses a `forValues` block:
//   { from: [...], to: [...] }         range
//   { in: [...] }                      list
//   { modulus: 4, remainder: 0 }       hash
func parsePartitionSpec(v any) *PartitionSpec {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    ps := &PartitionSpec{Modulus: -1, Remainder: -1}
    if f, ok := m["from"]; ok { ps.From = parseScalarList(f) }
    if t, ok := m["to"]; ok { ps.To = parseScalarList(t) }
    if in, ok := m["in"]; ok { ps.In = parseScalarList(in) }
    if n, ok := m["modulus"].(int); ok { ps.Modulus = n }
    if n, ok := m["remainder"].(int); ok { ps.Remainder = n }
    return ps
}

// parseScalarList coerces a YAML list of scalars (strings, numbers, bools,
// timestamps) to strings, preserving order. Unlike parseStringListFromNode it
// keeps non-string scalars, which partition bounds routinely contain.
func parseScalarList(v any) []string {
    arr, ok := v.([]any)
    if !ok { return nil }
    out := []string{}
    for _, it := range arr {
        if s := scalarToString(it); s != "" { out = append(out, s) }
    }
    return out
}

// scalarToString renders a YAML scalar as a string. yaml.v3 resolves bare
// dates like 2024-01-01 to time.Time; format those back as date/timestamp.
func scalarToString(v any) string {
    switch x := v.(type) {
    case string:
        return x
    case bool, int, int64, uint64, float64:
        return fmt.Sprintf("%v", x)
    case time.Time:
        if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
            return x.Format("2006-01-02")
        }
        return x.Format("2006-01-02 15:04:05")
    }
    return ""
}

func parseFunction(schemaName, key string, body any) *Function {
    // key format: "function <name>(args):"
    nameAndSig := strings.TrimSpace(strings.TrimPrefix(key, "function "))
    fn := &Function{Schema: schemaName}
    if i := strings.Index(nameAndSig, "("); i >= 0 {
        fn.Name = strings.TrimSpace(nameAndSig[:i])
        fn.ArgsSig = strings.TrimSuffix(strings.TrimSpace(nameAndSig[i:]), ":")
    } else {
        fn.Name = strings.TrimSuffix(nameAndSig, ":")
        fn.ArgsSig = "()"
    }
    m, ok := body.(map[string]any)
    if !ok { return fn }
    if r, ok := m["returns"].(string); ok { fn.Returns = r }
    if l, ok := m["language"].(string); ok { fn.Language = l }
    if s, ok := m["security"].(string); ok { fn.Security = s }
    if _, ok := m["stable"].(bool); ok { fn.Volatility = "stable" }
    if _, ok := m["volatile"].(bool); ok { fn.Volatility = "volatile" }
    if _, ok := m["immutable"].(bool); ok { fn.Volatility = "immutable" }
    if st, ok := m["strict"].(bool); ok { fn.Strict = st }
    if lp, ok := m["leakproof"].(bool); ok { fn.Leakproof = lp }
    if set, ok := m["set"].(map[string]any); ok {
        fn.Set = map[string]string{}
        for k, v := range set {
            if s, ok := v.(string); ok { fn.Set[k] = s }
        }
    }
    if b, ok := m["body"].(string); ok { fn.Body = b }
    if dep, ok := m["dependsOn"]; ok {
        fn.DependsOn = parseStringListFromNode(dep)
    }
    if gRaw, ok := m["grants"]; ok { fn.Grants = parseGrants(gRaw) }
    if rp, ok := m["revokePublic"].(bool); ok { fn.RevokePublic = rp }
    if cm, ok := m["comment"].(string); ok { fn.Comment = cm }
    return fn
}

func parseProcedure(schemaName, key string, body any) *Procedure {
    // key format: "procedure <name>(args):"
    nameAndSig := strings.TrimSpace(strings.TrimPrefix(key, "procedure "))
    pr := &Procedure{Schema: schemaName}
    if i := strings.Index(nameAndSig, "("); i >= 0 {
        pr.Name = strings.TrimSpace(nameAndSig[:i])
        pr.ArgsSig = strings.TrimSuffix(strings.TrimSpace(nameAndSig[i:]), ":")
    } else {
        pr.Name = strings.TrimSuffix(nameAndSig, ":")
        pr.ArgsSig = "()"
    }
    m, ok := body.(map[string]any)
    if !ok { return pr }
    if l, ok := m["language"].(string); ok { pr.Language = l }
    if s, ok := m["security"].(string); ok { pr.Security = s }
    if set, ok := m["set"].(map[string]any); ok {
        pr.Set = map[string]string{}
        for k, v := range set {
            if s, ok := v.(string); ok { pr.Set[k] = s }
        }
    }
    if b, ok := m["body"].(string); ok { pr.Body = b }
    if dep, ok := m["dependsOn"]; ok {
        pr.DependsOn = parseStringListFromNode(dep)
    }
    if gRaw, ok := m["grants"]; ok { pr.Grants = parseGrants(gRaw) }
    if rp, ok := m["revokePublic"].(bool); ok { pr.RevokePublic = rp }
    if cm, ok := m["comment"].(string); ok { pr.Comment = cm }
    return pr
}

func parseType(schemaName, key string, body any) *TypeDef {
    name := strings.TrimSpace(strings.TrimPrefix(key, "type "))
    td := &TypeDef{Name: name, Schema: schemaName}
    m, ok := body.(map[string]any)
    if !ok { return td }
    if kind, ok := m["type"].(string); ok { td.Kind = kind }
    if labels, ok := m["labels"].([]any); ok {
        for _, it := range labels { if s, ok := it.(string); ok { td.Labels = append(td.Labels, s) } }
    }
    if attrs, ok := m["attributes"].(map[string]any); ok {
        td.Attributes = map[string]string{}
        for k, v := range attrs {
            if mm, ok := v.(map[string]any); ok {
                if t, ok := mm["type"].(string); ok { td.Attributes[k] = t }
            }
        }
    }
    if dep, ok := m["dependsOn"]; ok {
        td.DependsOn = parseStringListFromNode(dep)
    }
    if cm, ok := m["comment"].(string); ok { td.Comment = cm }
    return td
}

// fillColumnOrderFromNode walks the yaml.Node tree to extract column key order
// for tables inside a specific schema block (schema <schemaName>: ...).
func fillColumnOrderFromNode(root *yaml.Node, db *Database, schemaName string) {
    if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 { return }
    top := root.Content[0]
    if top.Kind != yaml.MappingNode { return }
    // find mapping entry key == "schema <schemaName>"
    for i := 0; i+1 < len(top.Content); i += 2 {
        k := top.Content[i]
        v := top.Content[i+1]
        if k.Value == ("schema " + schemaName) && v.Kind == yaml.MappingNode {
            // inside schema mapping, find table blocks
            for j := 0; j+1 < len(v.Content); j += 2 {
                tk := v.Content[j]
                tv := v.Content[j+1]
                if strings.HasPrefix(tk.Value, "table ") && tv.Kind == yaml.MappingNode {
                    tname := strings.TrimSpace(strings.TrimPrefix(tk.Value, "table "))
                    fq := qualify(schemaName, tname)
                    // find columns mapping
                    for k2 := 0; k2+1 < len(tv.Content); k2 += 2 {
                        ck := tv.Content[k2]
                        cv := tv.Content[k2+1]
                        if ck.Value == "columns" && cv.Kind == yaml.MappingNode {
                            order := []string{}
                            for x := 0; x+1 < len(cv.Content); x += 2 {
                                colName := cv.Content[x].Value
                                order = append(order, colName)
                            }
                            if t, ok := db.Tables[fq]; ok {
                                t.ColumnOrder = order
                            }
                        }
                    }
                }
                if strings.HasPrefix(tk.Value, "type ") && tv.Kind == yaml.MappingNode {
                    tname := strings.TrimSpace(strings.TrimPrefix(tk.Value, "type "))
                    fq := qualify(schemaName, tname)
                    for k2 := 0; k2+1 < len(tv.Content); k2 += 2 {
                        ak := tv.Content[k2]
                        av := tv.Content[k2+1]
                        if ak.Value == "attributes" && av.Kind == yaml.MappingNode {
                            order := []string{}
                            for x := 0; x+1 < len(av.Content); x += 2 {
                                order = append(order, av.Content[x].Value)
                            }
                            if td, ok := db.Types[fq]; ok {
                                td.AttributeOrder = order
                            }
                        }
                    }
                }
            }
        }
    }
}

func SortedTableNames(d *Database) []string {
    out := make([]string, 0, len(d.Tables))
    for k := range d.Tables {
        out = append(out, k)
    }
    sort.Strings(out)
    return out
}

// Entity represents any orderable entity (extension, type, function, table)
type Entity struct {
    Key      string   // fully qualified name
    Kind     string   // "extension", "type", "function", "table"
    DependsOn []string // dependencies as written in YAML
}

// TopologicalSort returns all entities in dependency order
func TopologicalSort(d *Database) ([]Entity, error) {
    entities := []Entity{}
    entityMap := map[string]Entity{}
    
    // Collect all entities
    for _, ext := range d.Extensions {
        if ext == nil { continue }
        key := ext.Name
        e := Entity{Key: key, Kind: "extension", DependsOn: ext.DependsOn}
        entities = append(entities, e)
        entityMap[key] = e
    }
    for k, td := range d.Types {
        if td == nil { continue }
        e := Entity{Key: k, Kind: "type", DependsOn: td.DependsOn}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, dm := range d.Domains {
        if dm == nil { continue }
        e := Entity{Key: k, Kind: "domain", DependsOn: dm.DependsOn}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, sq := range d.Sequences {
        if sq == nil { continue }
        e := Entity{Key: k, Kind: "sequence", DependsOn: sq.DependsOn}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, fn := range d.Functions {
        if fn == nil { continue }
        e := Entity{Key: k, Kind: "function", DependsOn: fn.DependsOn}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, pr := range d.Procedures {
        if pr == nil { continue }
        e := Entity{Key: k, Kind: "procedure", DependsOn: pr.DependsOn}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, t := range d.Tables {
        if t == nil { continue }
        deps := t.DependsOn
        if t.PartitionOf != "" {
            // partition children must be created after their parent
            deps = append(append([]string{}, deps...), "table "+t.PartitionOf)
        }
        e := Entity{Key: k, Kind: "table", DependsOn: deps}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, vw := range d.Views {
        if vw == nil { continue }
        e := Entity{Key: k, Kind: "view", DependsOn: vw.DependsOn}
        entities = append(entities, e)
        entityMap[k] = e
    }
    
    // Resolve dependencies: convert "table private.account" -> "private.account"
    // Build dependency graph
    graph := map[string][]string{} // node -> list of dependencies
    for _, e := range entities {
        graph[e.Key] = []string{}
        for _, rawDep := range e.DependsOn {
            resolvedKey := resolveDependency(rawDep, d)
            if resolvedKey == "" { continue }
            // Edges to trigger-returning functions are dropped: triggers are
            // emitted after every other create, so no CREATE statement can
            // reference such a function, and keeping the edge manufactures
            // false cycles (table -> its trigger function -> the tables the
            // function body reads).
            if fn, ok := d.Functions[resolvedKey]; ok && fn != nil && strings.EqualFold(strings.TrimSpace(fn.Returns), "trigger") {
                continue
            }
            graph[e.Key] = append(graph[e.Key], resolvedKey)
        }
    }

    // Deterministic ordering among ready nodes: kind first (matching the
    // natural create order), then name. Without this, Kahn's queue would
    // follow Go's randomized map iteration and the buffer would differ
    // between runs on identical input.
    kindRank := map[string]int{"extension": 0, "type": 1, "domain": 2, "sequence": 3, "function": 4, "procedure": 5, "table": 6, "view": 7}
    entityLess := func(a, b string) bool {
        ra, rb := kindRank[entityMap[a].Kind], kindRank[entityMap[b].Kind]
        if ra != rb { return ra < rb }
        return a < b
    }

    // Topological sort (Kahn's algorithm)
    inDegree := map[string]int{}
    for k := range graph {
        inDegree[k] = 0
    }
    for k, deps := range graph {
        for _, dep := range deps {
            if _, exists := graph[dep]; exists {
                inDegree[k]++
            }
        }
    }

    queue := []string{}
    for k, deg := range inDegree {
        if deg == 0 {
            queue = append(queue, k)
        }
    }

    result := []Entity{}
    visited := map[string]bool{}

    for len(queue) > 0 {
        sort.Slice(queue, func(i, j int) bool { return entityLess(queue[i], queue[j]) })
        node := queue[0]
        queue = queue[1:]
        if visited[node] { continue }
        visited[node] = true
        if e, ok := entityMap[node]; ok {
            result = append(result, e)
        }
        // Find nodes that depend on this one
        for k, deps := range graph {
            if visited[k] { continue }
            for _, dep := range deps {
                if dep == node {
                    inDegree[k]--
                    if inDegree[k] == 0 {
                        queue = append(queue, k)
                    }
                }
            }
        }
    }
    
    // Any unvisited node is part of a dependency cycle (or depends on one).
    // Append them deterministically so the result is still usable, but return
    // an error so callers can fail loudly — their relative order is NOT
    // dependency-correct.
    if len(visited) < len(entityMap) {
        remaining := []string{}
        for k := range entityMap {
            if !visited[k] { remaining = append(remaining, k) }
        }
        sort.Strings(remaining)
        for _, k := range remaining {
            result = append(result, entityMap[k])
        }
        cycleMsg := ""
        if cycle := findCycle(graph, remaining); len(cycle) > 0 {
            cycleMsg = strings.Join(cycle, " -> ")
        } else {
            cycleMsg = "among: " + strings.Join(remaining, ", ")
        }
        return result, fmt.Errorf("dependency cycle detected: %s — check dependsOn declarations (note: triggers, policies, and grants are all emitted after every create, so dependsOn is never needed for trigger functions or functions referenced only by policies)", cycleMsg)
    }

    return result, nil
}

// findCycle returns one concrete dependency cycle (as a path ending on its
// first node) among the given unvisited nodes, or nil if none is reachable.
// Deterministic: nodes and edges are explored in sorted order.
func findCycle(graph map[string][]string, unvisited []string) []string {
    const (
        inProgress = 1
        done       = 2
    )
    color := map[string]int{}
    path := []string{}
    var cycle []string
    var visit func(n string) bool
    visit = func(n string) bool {
        color[n] = inProgress
        path = append(path, n)
        deps := append([]string{}, graph[n]...)
        sort.Strings(deps)
        for _, dep := range deps {
            if _, ok := graph[dep]; !ok { continue }
            switch color[dep] {
            case inProgress:
                for i, p := range path {
                    if p == dep {
                        cycle = append(append([]string{}, path[i:]...), dep)
                        return true
                    }
                }
            case done:
            default:
                if visit(dep) { return true }
            }
        }
        color[n] = done
        path = path[:len(path)-1]
        return false
    }
    for _, k := range unvisited {
        if color[k] == 0 && visit(k) { return cycle }
    }
    return nil
}

// resolveDependency converts raw dependency strings like "table private.account" or "schema private"
// to the actual entity key
func resolveDependency(raw string, d *Database) string {
    raw = strings.TrimSpace(raw)
    if raw == "" { return "" }
    
    // Handle "schema <name>"
    if strings.HasPrefix(raw, "schema ") {
        // Schema dependencies are not currently resolved to specific entities
        // In practice, we might want to track schema dependencies separately
        return ""
    }
    
    // Handle "table <name>"
    if strings.HasPrefix(raw, "table ") {
        tableName := strings.TrimSpace(strings.TrimPrefix(raw, "table "))
        if !strings.Contains(tableName, ".") {
            tableName = "public." + tableName
        }
        if _, ok := d.Tables[tableName]; ok {
            return tableName
        }
        return ""
    }
    
    // Handle "function <name>(args)"
    if strings.HasPrefix(raw, "function ") {
        fnSig := strings.TrimSpace(strings.TrimPrefix(raw, "function "))
        // Function map keys carry no argument list; strip "(...)" so both
        // "function public.fn" and "function public.fn(int)" resolve.
        if i := strings.Index(fnSig, "("); i >= 0 {
            fnSig = fnSig[:i]
        }
        if _, ok := d.Functions[fnSig]; ok {
            return fnSig
        }
        for k := range d.Functions {
            if strings.Contains(k, fnSig) || strings.HasPrefix(k, fnSig) {
                return k
            }
        }
        return ""
    }

    // Handle "procedure <name>(args)"
    if strings.HasPrefix(raw, "procedure ") {
        prSig := strings.TrimSpace(strings.TrimPrefix(raw, "procedure "))
        if i := strings.Index(prSig, "("); i >= 0 {
            prSig = prSig[:i]
        }
        if _, ok := d.Procedures[prSig]; ok {
            return prSig
        }
        for k := range d.Procedures {
            if strings.Contains(k, prSig) || strings.HasPrefix(k, prSig) {
                return k
            }
        }
        return ""
    }

    // Handle "type <name>"
    if strings.HasPrefix(raw, "type ") {
        typeName := strings.TrimSpace(strings.TrimPrefix(raw, "type "))
        if !strings.Contains(typeName, ".") {
            typeName = "public." + typeName
        }
        if _, ok := d.Types[typeName]; ok {
            return typeName
        }
        return ""
    }
    
    // Handle "domain <name>"
    if strings.HasPrefix(raw, "domain ") {
        domName := strings.TrimSpace(strings.TrimPrefix(raw, "domain "))
        if !strings.Contains(domName, ".") {
            domName = "public." + domName
        }
        if _, ok := d.Domains[domName]; ok {
            return domName
        }
        return ""
    }

    // Handle "sequence <name>"
    if strings.HasPrefix(raw, "sequence ") {
        seqName := strings.TrimSpace(strings.TrimPrefix(raw, "sequence "))
        if !strings.Contains(seqName, ".") {
            seqName = "public." + seqName
        }
        if _, ok := d.Sequences[seqName]; ok {
            return seqName
        }
        return ""
    }

    // Handle "view <name>"
    if strings.HasPrefix(raw, "view ") || strings.HasPrefix(raw, "materialized view ") {
        viewName := raw
        if strings.HasPrefix(raw, "view ") {
            viewName = strings.TrimSpace(strings.TrimPrefix(raw, "view "))
        } else {
            viewName = strings.TrimSpace(strings.TrimPrefix(raw, "materialized view "))
        }
        if !strings.Contains(viewName, ".") {
            viewName = "public." + viewName
        }
        if _, ok := d.Views[viewName]; ok {
            return viewName
        }
        return ""
    }

    // Direct name match
    if _, ok := d.Tables[raw]; ok { return raw }
    if _, ok := d.Types[raw]; ok { return raw }
    if _, ok := d.Functions[raw]; ok { return raw }
    if _, ok := d.Procedures[raw]; ok { return raw }
    if _, ok := d.Views[raw]; ok { return raw }
    if _, ok := d.Sequences[raw]; ok { return raw }
    if _, ok := d.Domains[raw]; ok { return raw }

    // Try with public schema
    pub := "public." + raw
    if _, ok := d.Tables[pub]; ok { return pub }
    if _, ok := d.Types[pub]; ok { return pub }
    if _, ok := d.Functions[pub]; ok { return pub }
    if _, ok := d.Procedures[pub]; ok { return pub }
    if _, ok := d.Views[pub]; ok { return pub }
    if _, ok := d.Sequences[pub]; ok { return pub }
    if _, ok := d.Domains[pub]; ok { return pub }

    return ""
}

func parseView(schemaName, key string, body any) *View {
    materialized := strings.HasPrefix(key, "materialized view ")
    prefix := "view "
    if materialized {
        prefix = "materialized view "
    }
    name := strings.TrimSpace(strings.TrimPrefix(key, prefix))
    vw := &View{Schema: schemaName, Name: name, Materialized: materialized}
    m, ok := body.(map[string]any)
    if !ok { return vw }
    if q, ok := m["query"].(string); ok { vw.Query = q }
    if dep, ok := m["dependsOn"]; ok {
        vw.DependsOn = parseStringListFromNode(dep)
    }
    if cm, ok := m["comment"].(string); ok { vw.Comment = cm }
    if rp, ok := m["replace"].(bool); ok { vw.Replace = rp }
    if gRaw, ok := m["grants"]; ok { vw.Grants = parseGrants(gRaw) }
    return vw
}

func parseSequence(schemaName, key string, body any) *Sequence {
    name := strings.TrimSpace(strings.TrimPrefix(key, "sequence "))
    sq := &Sequence{Schema: schemaName, Name: name}
    m, ok := body.(map[string]any)
    if !ok { return sq }
    if a, ok := m["as"].(string); ok { sq.As = a }
    if v, ok := m["increment"]; ok { sq.Increment = defaultToString(v) }
    if v, ok := m["minValue"]; ok { sq.MinValue = defaultToString(v) }
    if v, ok := m["maxValue"]; ok { sq.MaxValue = defaultToString(v) }
    if v, ok := m["start"]; ok { sq.Start = defaultToString(v) }
    if v, ok := m["cache"]; ok { sq.Cache = defaultToString(v) }
    if b, ok := m["cycle"].(bool); ok { sq.Cycle = b }
    if ob, ok := m["ownedBy"].(string); ok { sq.OwnedBy = ob }
    if dep, ok := m["dependsOn"]; ok {
        sq.DependsOn = parseStringListFromNode(dep)
    }
    if cm, ok := m["comment"].(string); ok { sq.Comment = cm }
    return sq
}

func parseDomain(schemaName, key string, body any) *Domain {
    name := strings.TrimSpace(strings.TrimPrefix(key, "domain "))
    dm := &Domain{Schema: schemaName, Name: name}
    m, ok := body.(map[string]any)
    if !ok { return dm }
    if t, ok := m["type"].(string); ok { dm.Type = t }
    if t, ok := m["as"].(string); ok && t != "" { dm.Type = t } // alias
    if c, ok := m["collate"].(string); ok { dm.Collate = c }
    if v, ok := m["default"]; ok { dm.Default = defaultToString(v) }
    if b, ok := m["notNull"].(bool); ok { dm.NotNull = b }
    if b, ok := m["nullable"].(bool); ok { dm.NotNull = !b }
    if c, ok := m["check"].(string); ok { dm.Check = c }
    if cn, ok := m["constraintName"].(string); ok { dm.ConstraintName = cn }
    if dep, ok := m["dependsOn"]; ok {
        dm.DependsOn = parseStringListFromNode(dep)
    }
    if cm, ok := m["comment"].(string); ok { dm.Comment = cm }
    return dm
}

func errorsIsNotExist(err error) bool {
    return err != nil && (os.IsNotExist(err) || errorsIs(err, fs.ErrNotExist))
}

func errorsIs(err, target error) bool { // tiny helper to avoid importing errors
    type causer interface{ Is(error) bool }
    if err == nil {
        return target == nil
    }
    if err == target {
        return true
    }
    if c, ok := err.(causer); ok {
        return c.Is(target)
    }
    return false
}


