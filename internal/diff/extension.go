package diff

import (
    "context"
    "fmt"
    "reflect"
    "regexp"
    "sort"
    "strconv"
    "strings"

    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/suprbdev/pgy/internal/schema"
)

// contribRequires lists the extensions each of PostgreSQL's own contrib
// extensions requires. It orders CREATE EXTENSION statements when there is
// no server to ask, as in diff --from-empty.
var contribRequires = map[string][]string{
    "bool_plperl":       {"plperl"},
    "bool_plperlu":      {"plperlu"},
    "earthdistance":     {"cube"},
    "hstore_plperl":     {"hstore", "plperl"},
    "hstore_plperlu":    {"hstore", "plperlu"},
    "hstore_plpython3u": {"hstore", "plpython3u"},
    "jsonb_plperl":      {"plperl"},
    "jsonb_plperlu":     {"plperlu"},
    "jsonb_plpython3u":  {"plpython3u"},
    "ltree_plpython3u":  {"ltree", "plpython3u"},
}

// systemSchema reports whether name is a schema PostgreSQL owns, which
// CREATE SCHEMA rejects: information_schema and the reserved pg_ prefix.
func systemSchema(name string) bool {
    return name == "information_schema" || strings.HasPrefix(name, "pg_")
}

// extensionRequires returns the extensions ext has to be created after:
// its dependsOn entries naming extensions, and the ones the version being
// installed requires according to the server, or to contribRequires when
// the server's extensions are unknown.
func extensionRequires(live *Live, ext *schema.Extension) []string {
    out := []string{}
    for _, dep := range ext.DependsOn {
        if name, ok := strings.CutPrefix(strings.TrimSpace(dep), "extension "); ok {
            out = append(out, strings.Trim(strings.TrimSpace(name), `"`))
        }
    }
    if live == nil || live.AvailableExtensions == nil {
        return append(out, contribRequires[ext.Name]...)
    }
    if av := live.AvailableExtensions[ext.Name]; av != nil {
        version := ext.Version
        if version == "" { version = av.DefaultVersion }
        out = append(out, av.Requires[version]...)
    }
    return out
}

// orderedExtensions returns the declared extensions with each one after
// the declared extensions it requires, and in name order otherwise.
func orderedExtensions(live *Live, desired *schema.Database) []*schema.Extension {
    byName := map[string]*schema.Extension{}
    names := []string{}
    for _, ext := range desired.Extensions {
        if ext == nil || ext.Name == "" || byName[ext.Name] != nil { continue }
        byName[ext.Name] = ext
        names = append(names, ext.Name)
    }
    sort.Strings(names)
    out := make([]*schema.Extension, 0, len(names))
    state := map[string]int{}
    var visit func(name string)
    visit = func(name string) {
        ext := byName[name]
        if ext == nil || state[name] != 0 { return }
        state[name] = 1
        reqs := extensionRequires(live, ext)
        sort.Strings(reqs)
        for _, r := range reqs { visit(r) }
        state[name] = 2
        out = append(out, ext)
    }
    for _, name := range names { visit(name) }
    return out
}

// planExtensions emits CREATE EXTENSION for the declared extensions the
// database lacks and ALTER EXTENSION ... SET SCHEMA / UPDATE TO for live
// ones, in requirement order. Both go to plan.Creates, ahead of every
// object that may use the extension. A relocatable extension is moved to
// its declared schema; others cannot be moved and are left alone. Unset
// schema, version and comment are unmanaged.
func planExtensions(plan *PlanDiff, live *Live, desired *schema.Database) {
    warn := func(format string, args ...any) {
        plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf(format, args...)})
    }
    cascaded := false
    for _, ext := range orderedExtensions(live, desired) {
        name := pqIdent(ext.Name)
        if live.Extensions[ext.Name] {
            le := live.ExtensionDefs[ext.Name]
            if le == nil { continue }
            if ext.Schema != "" && ext.Schema != le.Schema && le.Relocatable {
                plan.Creates = append(plan.Creates, fmt.Sprintf("alter extension %s set schema %s;", name, pqIdent(ext.Schema)))
            }
            if ext.Version != "" && ext.Version != le.Version {
                if le.UpdateTargets != nil && !le.UpdateTargets[ext.Version] {
                    warn("extension %s: PostgreSQL has no update path from the installed version %s to %s (an extension cannot be downgraded), so its version is left as it is", ext.Name, le.Version, ext.Version)
                } else {
                    plan.Creates = append(plan.Creates, fmt.Sprintf("alter extension %s update to %s;", name, quoteString(ext.Version)))
                }
            }
            if ext.Comment != "" && ext.Comment != le.Comment {
                plan.Alters = append(plan.Alters, fmt.Sprintf("comment on extension %s is %s;", name, quoteString(ext.Comment)))
            }
            continue
        }
        if ext.Name == "plpgsql" && live.ServerVersion == 0 { continue }
        if live.AvailableExtensions != nil {
            if av := live.AvailableExtensions[ext.Name]; av == nil {
                warn("extension %s is not available on this server, so CREATE EXTENSION will fail until it is installed there", ext.Name)
            } else if _, ok := av.Requires[ext.Version]; ext.Version != "" && !ok {
                warn("extension %s: version %s is not available on this server (available: %s), so CREATE EXTENSION will fail", ext.Name, ext.Version, strings.Join(sortedVersions(av.Requires), ", "))
            }
        }
        create := *ext
        if cascaded { create.IfNotExists = true }
        cascaded = cascaded || ext.Cascade
        plan.Creates = append(plan.Creates, renderExtension(&create))
        if ext.Comment != "" {
            plan.Alters = append(plan.Alters, fmt.Sprintf("comment on extension %s is %s;", name, quoteString(ext.Comment)))
        }
    }
}

// sortedVersions returns the keys of m in version order: numeric parts
// compare as numbers, so 1.10 follows 1.9.
func sortedVersions(m map[string][]string) []string {
    out := sortedKeys(m)
    sort.SliceStable(out, func(i, j int) bool {
        a, b := strings.Split(out[i], "."), strings.Split(out[j], ".")
        for k := 0; k < len(a) && k < len(b); k++ {
            x, errX := strconv.Atoi(a[k])
            y, errY := strconv.Atoi(b[k])
            if errX != nil || errY != nil {
                if a[k] != b[k] { return a[k] < b[k] }
                continue
            }
            if x != y { return x < y }
        }
        return len(a) < len(b)
    })
    return out
}

// IntrospectExtensions loads what planning the declared extensions needs
// beyond Introspect, and only when it is needed: the server's available
// extensions and their requirements when one has to be created or updated,
// the update paths of the installed ones when a version differs, and the
// member names of extensions that have to move to another schema. Without
// it the plan is still valid: statements are ordered by dependsOn and the
// requirements of PostgreSQL's own extensions, nothing is checked against
// the server, and objects that follow a moved extension are compared in
// their old spelling.
func IntrospectExtensions(ctx context.Context, pool *pgxpool.Pool, live *Live, desired *schema.Database) error {
    if live == nil || desired == nil || pool == nil { return nil }
    create, update := false, false
    moving := []string{}
    for _, ext := range desired.Extensions {
        if ext == nil || ext.Name == "" { continue }
        le := live.ExtensionDefs[ext.Name]
        switch {
        case !live.Extensions[ext.Name]:
            create = true
        case le == nil:
        default:
            if ext.Version != "" && ext.Version != le.Version { update = true }
            if ext.Schema != "" && ext.Schema != le.Schema && le.Relocatable { moving = append(moving, quoteString(ext.Name)) }
        }
    }
    if !create && !update && len(moving) == 0 { return nil }
    batch := &queryBatch{}
    if create || update {
        available := map[string]*AvailableExtension{}
        batch.add(`
            select a.name::text, coalesce(a.default_version, ''), v.version, coalesce(v.requires, '{}')::text[]
            from pg_available_extensions a
            left join pg_available_extension_versions v on v.name = a.name`, func(rows pgx.Rows) error {
            for rows.Next() {
                var name, def string
                var version *string
                var requires []string
                if err := rows.Scan(&name, &def, &version, &requires); err != nil { return err }
                av := available[name]
                if av == nil {
                    av = &AvailableExtension{DefaultVersion: def, Requires: map[string][]string{}}
                    available[name] = av
                }
                if version != nil { av.Requires[*version] = requires }
            }
            live.AvailableExtensions = available
            return nil
        })
    }
    if update {
        batch.add(`
            select e.extname::text, p.target
            from pg_extension e
            cross join lateral pg_extension_update_paths(e.extname) p
            where p.source = e.extversion and p.path is not null`, func(rows pgx.Rows) error {
            for _, le := range live.ExtensionDefs {
                if le != nil { le.UpdateTargets = map[string]bool{} }
            }
            for rows.Next() {
                var name, target string
                if err := rows.Scan(&name, &target); err != nil { return err }
                if le := live.ExtensionDefs[name]; le != nil { le.UpdateTargets[target] = true }
            }
            return nil
        })
    }
    if len(moving) > 0 {
        batch.add(`
            select distinct e.extname::text, o.oprname is not null,
                   coalesce(t.typname, p.proname, c.relname, oc.opcname, o.oprname, td.dictname, tt.tmplname, tc.cfgname)::text
            from pg_depend d
            join pg_extension e on e.oid = d.refobjid and e.extname in (`+strings.Join(moving, ", ")+`)
            left join pg_type t on d.classid = 'pg_type'::regclass and t.oid = d.objid
            left join pg_proc p on d.classid = 'pg_proc'::regclass and p.oid = d.objid
            left join pg_class c on d.classid = 'pg_class'::regclass and c.oid = d.objid
            left join pg_opclass oc on d.classid = 'pg_opclass'::regclass and oc.oid = d.objid
            left join pg_operator o on d.classid = 'pg_operator'::regclass and o.oid = d.objid
            left join pg_ts_dict td on d.classid = 'pg_ts_dict'::regclass and td.oid = d.objid
            left join pg_ts_template tt on d.classid = 'pg_ts_template'::regclass and tt.oid = d.objid
            left join pg_ts_config tc on d.classid = 'pg_ts_config'::regclass and tc.oid = d.objid
            where d.deptype = 'e' and d.refclassid = 'pg_extension'::regclass
            and coalesce(t.typname, p.proname, c.relname, oc.opcname, o.oprname, td.dictname, tt.tmplname, tc.cfgname) is not null`, func(rows pgx.Rows) error {
            for rows.Next() {
                var ext, name string
                var operator bool
                if err := rows.Scan(&ext, &operator, &name); err != nil { return err }
                le := live.ExtensionDefs[ext]
                if le == nil { continue }
                if le.Members == nil { le.Members, le.Operators = map[string]bool{}, map[string]bool{} }
                if operator { le.Operators[name] = true } else { le.Members[name] = true }
            }
            return nil
        })
    }
    return batch.run(ctx, pool)
}

// extensionMove is a relocatable extension on its way from one schema to
// another, with the names of the objects that move with it.
type extensionMove struct {
    from, to           string
    members, operators map[string]bool
}

// requalifier rewrites references to the objects of moving extensions from
// the schema they move to back to the schema the live database still has
// them in.
type requalifier struct {
    moves []extensionMove
    names []*regexp.Regexp
    ops   []*regexp.Regexp
}

// newRequalifier returns a requalifier for the extensions the plan moves to
// another schema, or nil when there are none or their member names were not
// loaded (see IntrospectExtensions).
func newRequalifier(live *Live, desired *schema.Database) *requalifier {
    if live == nil || desired == nil { return nil }
    rq := &requalifier{}
    for _, ext := range desired.Extensions {
        if ext == nil || ext.Schema == "" { continue }
        le := live.ExtensionDefs[ext.Name]
        if le == nil || !le.Relocatable || le.Members == nil { continue }
        from := le.Schema
        if le.MovedFrom != "" && le.Schema == ext.Schema { from = le.MovedFrom }
        if from == ext.Schema { continue }
        schemaPattern := regexp.QuoteMeta(`"` + strings.ReplaceAll(ext.Schema, `"`, `""`) + `"`)
        if plainIdentRe.MatchString(ext.Schema) { schemaPattern = `(?i:` + regexp.QuoteMeta(ext.Schema) + `)|` + schemaPattern }
        rq.moves = append(rq.moves, extensionMove{from: from, to: ext.Schema, members: le.Members, operators: le.Operators})
        rq.names = append(rq.names, regexp.MustCompile(`(^|[^\w$."])(`+schemaPattern+`)\.("(?:[^"]|"")+"|[A-Za-z_][\w$]*)`))
        rq.ops = append(rq.ops, regexp.MustCompile(`(?i)(operator\s*\(\s*)(?:`+schemaPattern+`)(\s*\.\s*)([^\s()]+)(\s*\))`))
    }
    if len(rq.moves) == 0 { return nil }
    return rq
}

// text rewrites s, leaving string literals and dollar-quoted bodies alone.
// A qualified name is rewritten only when it names a member of the moving
// extension, so other objects of the target schema keep their spelling. An
// OPERATOR(schema.op) call becomes the bare operator when the extension
// comes from public, which is how PostgreSQL prints it there.
func (rq *requalifier) text(s string) string {
    if s == "" { return s }
    masked, parts := maskQuoted(s)
    for i, mv := range rq.moves {
        masked = rq.ops[i].ReplaceAllStringFunc(masked, func(m string) string {
            sm := rq.ops[i].FindStringSubmatch(m)
            if !mv.operators[sm[3]] { return m }
            if mv.from == "public" { return sm[3] }
            return sm[1] + printedIdent(mv.from) + sm[2] + sm[3] + sm[4]
        })
        masked = rq.names[i].ReplaceAllStringFunc(masked, func(m string) string {
            sm := rq.names[i].FindStringSubmatch(m)
            name := strings.ToLower(sm[3])
            if strings.HasPrefix(sm[3], `"`) { name = strings.ReplaceAll(sm[3][1:len(sm[3])-1], `""`, `"`) }
            if !mv.members[name] { return m }
            from := printedIdent(mv.from)
            if strings.HasPrefix(sm[2], `"`) { from = `"` + strings.ReplaceAll(mv.from, `"`, `""`) + `"` }
            return sm[1] + from + "." + sm[3]
        })
    }
    return exprPlaceholderRe.ReplaceAllStringFunc(masked, func(m string) string {
        i, _ := strconv.Atoi(exprPlaceholderRe.FindStringSubmatch(m)[1])
        return parts[i]
    })
}

var dollarTagRe = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$`)

// maskQuoted replaces each single-quoted literal and each dollar-quoted
// string in s by a \x00<n>\x00 placeholder and returns them in order.
func maskQuoted(s string) (string, []string) {
    var b strings.Builder
    parts := []string{}
    for i := 0; i < len(s); {
        end := -1
        switch s[i] {
        case '\'':
            for j := i + 1; j < len(s); j++ {
                if s[j] != '\'' { continue }
                if j+1 < len(s) && s[j+1] == '\'' { j++; continue }
                end = j + 1
                break
            }
        case '$':
            if tag := dollarTagRe.FindString(s[i:]); tag != "" {
                if k := strings.Index(s[i+len(tag):], tag); k >= 0 { end = i + len(tag) + k + len(tag) }
            }
        }
        if end < 0 {
            b.WriteByte(s[i])
            i++
            continue
        }
        fmt.Fprintf(&b, "\x00%d\x00", len(parts))
        parts = append(parts, s[i:end])
        i = end
    }
    return b.String(), parts
}

// database returns a copy of d with every type, expression and query
// rewritten by text. Routine bodies, comments and data rows are copied as
// they are: bodies are stored as text, so one that names the new schema
// really differs from the live one.
func (rq *requalifier) database(d *schema.Database) *schema.Database {
    out := reflect.New(reflect.TypeOf(*d))
    rq.copyValue(out.Elem(), reflect.ValueOf(*d), false)
    return out.Interface().(*schema.Database)
}

func (rq *requalifier) copyValue(dst, src reflect.Value, verbatim bool) {
    switch src.Kind() {
    case reflect.Pointer:
        if src.IsNil() { return }
        dst.Set(reflect.New(src.Type().Elem()))
        rq.copyValue(dst.Elem(), src.Elem(), verbatim)
    case reflect.Struct:
        dst.Set(src)
        for i := 0; i < src.NumField(); i++ {
            f := src.Type().Field(i)
            if !f.IsExported() { continue }
            rq.copyValue(dst.Field(i), src.Field(i), verbatim || strings.Contains(f.Name, "Body") || strings.Contains(f.Name, "Comment") || f.Name == "Data")
        }
    case reflect.Map:
        if src.IsNil() { return }
        dst.Set(reflect.MakeMapWithSize(src.Type(), src.Len()))
        routines := src.Type() == reflect.TypeOf(map[string]*schema.Function(nil)) || src.Type() == reflect.TypeOf(map[string]*schema.Procedure(nil)) || src.Type() == reflect.TypeOf(map[string]*schema.Aggregate(nil))
        iter := src.MapRange()
        for iter.Next() {
            key := iter.Key()
            if routines { key = reflect.ValueOf(rq.text(key.String())) }
            val := reflect.New(src.Type().Elem()).Elem()
            rq.copyValue(val, iter.Value(), verbatim)
            dst.SetMapIndex(key, val)
        }
    case reflect.Slice:
        if src.IsNil() { return }
        dst.Set(reflect.MakeSlice(src.Type(), src.Len(), src.Len()))
        for i := 0; i < src.Len(); i++ { rq.copyValue(dst.Index(i), src.Index(i), verbatim) }
    case reflect.String:
        if verbatim { dst.Set(src) } else { dst.SetString(rq.text(src.String())) }
    default:
        dst.Set(src)
    }
}

// withoutMoveEchoes drops from plan the statements and warnings that exist
// only because the YAML spells the objects of a moving extension with their
// new schema while the live definitions still have the old one. PostgreSQL
// moves those references with the extension, so nothing has to be emitted
// for them. The plan is compared with one made from the YAML in the old
// spelling: a statement stays when that plan has its counterpart. When the
// two plans don't line up, plan is returned unchanged.
func withoutMoveEchoes(plan *PlanDiff, live *Live, desired *schema.Database, opts Options) *PlanDiff {
    rq := newRequalifier(live, desired)
    if rq == nil { return plan }
    before := planWith(live, rq.database(desired), opts)
    out := &PlanDiff{}
    kept, planned := map[string]bool{}, map[string]bool{}
    for _, sec := range []struct {
        dst       *[]string
        got, want []string
    }{
        {&out.Prelude, plan.Prelude, before.Prelude},
        {&out.Creates, plan.Creates, before.Creates},
        {&out.Alters, plan.Alters, before.Alters},
        {&out.Drops, plan.Drops, before.Drops},
        {&out.Concurrent, plan.Concurrent, before.Concurrent},
    } {
        want := map[string]int{}
        for _, s := range sec.want { want[s]++ }
        left := len(sec.want)
        for _, s := range sec.got {
            planned[s] = true
            if t := rq.text(s); want[t] > 0 {
                want[t]--
                left--
                kept[s] = true
                *sec.dst = append(*sec.dst, s)
            }
        }
        if left > 0 { return plan }
    }
    messages := map[string]int{}
    for _, w := range before.Warnings { messages[w.Message]++ }
    for _, w := range plan.Warnings {
        if planned[w.Statement] {
            if kept[w.Statement] { out.Warnings = append(out.Warnings, w) }
            continue
        }
        if m := rq.text(w.Message); messages[m] > 0 {
            messages[m]--
            out.Warnings = append(out.Warnings, w)
        }
    }
    return out
}
