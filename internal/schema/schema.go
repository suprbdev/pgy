package schema

import (
    "fmt"
    "io/fs"
    "os"
    "sort"
    "strings"

    yaml "gopkg.in/yaml.v3"
)

// Minimal schema model: schemas -> tables -> columns
type Database struct {
    Tables map[string]*Table `yaml:"tables"`
    Extensions []*Extension `yaml:"extensions"`
    Types map[string]*TypeDef `yaml:"-"`
    Functions map[string]*Function `yaml:"-"`
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
}

type Index struct {
    Name    string
    Columns []string
    Unique  bool
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
    DependsOn []string `yaml:"dependsOn"`
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
    Set map[string]string
    Body string
    DependsOn []string `yaml:"dependsOn"`
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
            } else {
                merged.Tables[name] = t
            }
        }
        // merge extensions
        if len(d.Extensions) > 0 {
            merged.Extensions = append(merged.Extensions, d.Extensions...)
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
    // extensions top-level
    if extsRaw, ok := root["extensions"]; ok {
        if arr, ok := extsRaw.([]any); ok {
            for _, it := range arr {
                if m, ok := it.(map[string]any); ok {
                    name, _ := m["name"].(string)
                    if name == "" { continue }
                    ext := &Extension{Name: name}
                    if b, ok := m["ifNotExists"].(bool); ok { ext.IfNotExists = b }
                    if dep, ok := m["dependsOn"]; ok {
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
                mergeTablesInto(out, schemaName, v)
            }
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
            }
            db.Tables[fq] = t
        } else if strings.HasPrefix(key, "function ") {
            fn := parseFunction(schemaName, key, body)
            if fn != nil {
                full := qualify(schemaName, fn.Name)
                db.Functions[full] = fn
            }
        } else if strings.HasPrefix(key, "type ") {
            td := parseType(schemaName, key, body)
            if td != nil {
                full := qualify(schemaName, td.Name)
                db.Types[full] = td
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
        if d, ok := m["default"].(string); ok { c.Default = d }
        if u, ok := m["unique"].(bool); ok { c.Unique = u }
        if pk, ok := m["primaryKey"].(bool); ok { c.PrimaryKey = pk }
    }
    return c
}

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
            }
            out = append(out, ix)
        }
    }
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
            }
            out = append(out, tr)
        }
    }
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
    return out
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
    if st, ok := m["strict"].(bool); ok { fn.Strict = st }
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
    return fn
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
    for k, fn := range d.Functions {
        if fn == nil { continue }
        e := Entity{Key: k, Kind: "function", DependsOn: fn.DependsOn}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, t := range d.Tables {
        if t == nil { continue }
        e := Entity{Key: k, Kind: "table", DependsOn: t.DependsOn}
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
            if resolvedKey != "" {
                graph[e.Key] = append(graph[e.Key], resolvedKey)
            }
        }
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
    
    // Add any remaining nodes (cycles or missing deps)
    for k, e := range entityMap {
        if !visited[k] {
            result = append(result, e)
        }
    }
    
    return result, nil
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
        for k := range d.Functions {
            if strings.Contains(k, fnSig) || strings.HasPrefix(k, fnSig) {
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
    
    // Direct name match
    if _, ok := d.Tables[raw]; ok { return raw }
    if _, ok := d.Types[raw]; ok { return raw }
    if _, ok := d.Functions[raw]; ok { return raw }
    
    // Try with public schema
    pub := "public." + raw
    if _, ok := d.Tables[pub]; ok { return pub }
    if _, ok := d.Types[pub]; ok { return pub }
    if _, ok := d.Functions[pub]; ok { return pub }
    
    return ""
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


