package diff

import (
    "cmp"
    "context"
    "fmt"
    "reflect"
    "regexp"
    "sort"
    "strconv"
    "strings"
    "unicode"

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
// schema, version and comment are unmanaged. Dropping undeclared
// extensions is part of pruning; see planExtensionDrops.
func planExtensions(plan *PlanDiff, live *Live, desired *schema.Database) {
    warn := func(format string, args ...any) {
        plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf(format, args...)})
    }
    cascaded := false
    for _, ext := range orderedExtensions(live, desired) {
        name := quoteIdent(ext.Name)
        if live.Extensions[ext.Name] {
            le := live.ExtensionDefs[ext.Name]
            if le == nil { continue }
            if ext.Schema != "" && ext.Schema != le.Schema && le.Relocatable {
                plan.Creates = append(plan.Creates, fmt.Sprintf("alter extension %s set schema %s;", name, quoteIdent(ext.Schema)))
                if !onSearchPath(desired, ext.Schema) {
                    uses := unqualifiedMemberUses(desired, le)
                    if len(uses) > 10 { uses = append(uses[:10], fmt.Sprintf("%d more", len(uses)-10)) }
                    if len(uses) > 0 { warn("extension %s moves to schema %s, which is not on the search_path, while the YAML still names its objects without a schema in %s; those names will not resolve once it has moved, so qualify them with the schema", ext.Name, ext.Schema, strings.Join(uses, ", ")) }
                }
            } else if ext.Schema != "" && ext.Schema != le.Schema {
                refs := ""
                if le.Members != nil { refs = fmt.Sprintf(", and references to them qualified with %s are planned as references to %s", ext.Schema, le.Schema) }
                warn("extension %s is not relocatable, so it stays in schema %s with its objects instead of moving to schema %s%s; declare schema: %s, or drop the extension and create it in %s by hand", ext.Name, le.Schema, ext.Schema, refs, le.Schema, ext.Schema)
            }
            if ext.Version != "" && ext.Version != le.Version {
                if le.UpdateTargets != nil && !le.UpdateTargets[ext.Version] {
                    av, available := live.AvailableExtensions[ext.Name], true
                    if av != nil { _, available = av.Requires[ext.Version] }
                    switch {
                    case versionLess(ext.Version, le.Version):
                        warn("extension %s: PostgreSQL has no update path from the installed version %s to %s (an extension cannot be downgraded), so its version is left as it is", ext.Name, le.Version, ext.Version)
                    case !available:
                        warn("extension %s: version %s is not available on this server (available: %s), so the installed version %s is left as it is", ext.Name, ext.Version, strings.Join(sortedVersions(av.Requires), ", "), le.Version)
                    default:
                        warn("extension %s: PostgreSQL has no update path from the installed version %s to %s, so its version is left as it is", ext.Name, le.Version, ext.Version)
                    }
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
            } else if version := cmp.Or(ext.Version, av.DefaultVersion); ext.Schema != "" && av.Schemas[version] != "" && av.Schemas[version] != ext.Schema {
                warn("extension %s can only be installed in schema %s, which its control file names, so CREATE EXTENSION with schema %s will fail; declare schema: %s, or leave schema out", ext.Name, av.Schemas[version], ext.Schema, av.Schemas[version])
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

// onSearchPath reports whether an unqualified name finds the objects of
// schema s in the sessions after the migration: s is pg_catalog, public or
// on the search_path of the session that read the snapshot (see
// nameScope), or desired sets a database search_path that lists it.
func onSearchPath(desired *schema.Database, s string) bool {
    if s == "pg_catalog" || activeScope().schemas[s] { return true }
    for k, v := range desired.DatabaseSettings {
        if !strings.EqualFold(k, "search_path") { continue }
        for _, p := range strings.Split(v, ",") {
            if strings.Trim(strings.TrimSpace(p), `"`) == s { return true }
        }
    }
    return false
}

var (
    identTokenRe = regexp.MustCompile(`"(?:[^"]|"")+"|[A-Za-z_][\w$]*`)
    wholeIdentRe = regexp.MustCompile(`^(?:` + identTokenRe.String() + `)$`)
)

// identName returns the name an identifier token spells: unquoted, or
// folded to lower case.
func identName(tok string) string {
    if strings.HasPrefix(tok, `"`) { return strings.ReplaceAll(tok[1:len(tok)-1], `""`, `"`) }
    return strings.ToLower(tok)
}

// unqualifiedTypeName returns the name of type t when it is a single
// identifier without a schema ("citext", "citext[]", "setof citext"), and
// "" otherwise.
func unqualifiedTypeName(t string) string {
    s := strings.TrimSpace(t)
    if len(s) > 6 && strings.EqualFold(s[:6], "setof ") { s = s[6:] }
    if i := strings.IndexAny(s, "(["); i >= 0 { s = s[:i] }
    if s = strings.TrimSpace(s); !wholeIdentRe.MatchString(s) { return "" }
    return identName(s)
}

// unqualifiedMemberUses names the declarations of desired that refer to an
// object of extension le without a schema: one of its types as the type of
// a column, domain, composite type attribute or routine argument or
// result, or in a cast, and one of its routines called, in a column
// default, generated column, check constraint, domain or argument default,
// or as a trigger function. A call is not counted when pg_catalog has a
// routine of that name too, since it may mean the built-in one. It is nil
// while the extension's member names are unknown.
func unqualifiedMemberUses(desired *schema.Database, le *LiveExtension) []string {
    if le.Members == nil { return nil }
    found := map[string]bool{}
    typ := func(what, t string) {
        if name := unqualifiedTypeName(t); name != "" && le.Members[name] { found[what] = true }
    }
    expr := func(what, s string) {
        masked, _ := maskQuoted(s)
        for _, loc := range identTokenRe.FindAllStringIndex(masked, -1) {
            i, j := loc[0], loc[1]
            for i > 0 && unicode.IsSpace(rune(masked[i-1])) { i-- }
            for j < len(masked) && unicode.IsSpace(rune(masked[j])) { j++ }
            before, after := masked[:i], masked[j:]
            if strings.HasSuffix(before, ".") || strings.HasPrefix(after, ".") { continue }
            name := identName(masked[loc[0]:loc[1]])
            if strings.HasPrefix(after, "(") && le.Members[name] && !le.Builtins[name] || strings.HasSuffix(before, "::") && le.Members[name] { found[what] = true }
        }
    }
    args := func(what, sig string) {
        for _, a := range splitTopLevelQuotedParens(innerArgs(sig)) {
            decl, def := splitArgDefault(a)
            words := strings.Fields(decl)
            if len(words) > 1 {
                switch strings.ToLower(words[0]) {
                case "in", "out", "inout", "variadic": words = words[1:]
                }
            }
            if len(words) > 1 && !isMultiWordTypeStart(strings.ToLower(words[0]), strings.ToLower(words[1])) { words = words[1:] }
            typ(what, strings.Join(words, " "))
            expr(what, def)
        }
    }
    for _, fq := range sortedKeys(desired.Tables) {
        t := desired.Tables[fq]
        if t == nil { continue }
        for _, cn := range sortedKeys(t.Columns) {
            if c := t.Columns[cn]; c != nil {
                what := "column " + fq + "." + cn
                typ(what, c.Type)
                expr(what, c.Default)
                expr(what, c.Generated)
            }
        }
        for _, ct := range t.Constraints {
            if ct != nil && ct.Type == "check" { expr("constraint "+ct.Name+" on "+fq, ct.Expression) }
        }
        for _, tg := range t.Triggers {
            if tg != nil { expr("trigger "+tg.Name+" on "+fq, tg.Procedure) }
        }
    }
    for _, fq := range sortedKeys(desired.Domains) {
        if dm := desired.Domains[fq]; dm != nil {
            typ("domain "+fq, dm.Type)
            expr("domain "+fq, dm.Default)
            expr("domain "+fq, dm.Check)
            for _, c := range dm.Checks { expr("domain "+fq, c) }
        }
    }
    for _, fq := range sortedKeys(desired.Types) {
        if td := desired.Types[fq]; td != nil {
            for _, at := range td.Attributes { typ("type "+fq, at) }
        }
    }
    for _, k := range sortedKeys(desired.Functions) {
        if f := desired.Functions[k]; f != nil {
            what := "function " + schema.RoutineName(k) + f.ArgsSig
            args(what, f.ArgsSig)
            typ(what, f.Returns)
        }
    }
    for _, k := range sortedKeys(desired.Procedures) {
        if p := desired.Procedures[k]; p != nil { args("procedure "+schema.RoutineName(k)+p.ArgsSig, p.ArgsSig) }
    }
    return sortedKeys(found)
}

// sortedVersions returns the keys of m in version order (see versionLess).
func sortedVersions(m map[string][]string) []string {
    out := sortedKeys(m)
    sort.SliceStable(out, func(i, j int) bool { return versionLess(out[i], out[j]) })
    return out
}

// versionLess reports whether extension version a comes before b: the
// dot-separated parts compare in turn, numeric ones as numbers, so 1.10
// follows 1.9, and a version comes before the longer ones it starts (1.1
// before 1.1.1).
func versionLess(a, b string) bool {
    x, y := strings.Split(a, "."), strings.Split(b, ".")
    for k := 0; k < len(x) && k < len(y); k++ {
        m, errM := strconv.Atoi(x[k])
        n, errN := strconv.Atoi(y[k])
        if errM != nil || errN != nil {
            if x[k] != y[k] { return x[k] < y[k] }
            continue
        }
        if m != n { return m < n }
    }
    return len(x) < len(y)
}

// IntrospectExtensions loads what planning the extensions needs beyond
// Introspect, and only when it is needed: the server's available
// extensions and their requirements when one has to be created or updated,
// the update paths of the installed ones and the views and routines that
// use them when a version differs, the member names of extensions declared
// in another schema than they are in, and the objects that use an extension
// opts lets pruning drop. Without it the plan is still valid: statements
// are ordered by dependsOn and the requirements of PostgreSQL's own
// extensions, nothing is checked against the server, objects that follow a
// moved extension are compared in their old spelling, and ALTER EXTENSION
// ... UPDATE and DROP EXTENSION are planned without looking for
// dependents.
func IntrospectExtensions(ctx context.Context, pool *pgxpool.Pool, live *Live, desired *schema.Database, opts Options) error {
    if live == nil || desired == nil || pool == nil { return nil }
    create, update := false, false
    moving, updating := []string{}, []string{}
    for _, ext := range desired.Extensions {
        if ext == nil || ext.Name == "" { continue }
        le := live.ExtensionDefs[ext.Name]
        switch {
        case !live.Extensions[ext.Name]:
            create = true
        case le == nil:
        default:
            if ext.Version != "" && ext.Version != le.Version { update, updating = true, append(updating, ext.Name) }
            if ext.Schema != "" && ext.Schema != le.Schema { moving = append(moving, quoteString(ext.Name)) }
        }
    }
    prunable := sortedKeys(prunableExtensions(live, desired, opts))
    if !create && !update && len(moving) == 0 && len(prunable) == 0 { return nil }
    batch := &queryBatch{}
    if create || update {
        available := map[string]*AvailableExtension{}
        batch.add(`
            select a.name::text, coalesce(a.default_version, ''), v.version, coalesce(v.requires, '{}')::text[], coalesce(v.schema::text, '')
            from pg_available_extensions a
            left join pg_available_extension_versions v on v.name = a.name`, func(rows pgx.Rows) error {
            for rows.Next() {
                var name, def, fixed string
                var version *string
                var requires []string
                if err := rows.Scan(&name, &def, &version, &requires, &fixed); err != nil { return err }
                av := available[name]
                if av == nil {
                    av = &AvailableExtension{DefaultVersion: def, Requires: map[string][]string{}, Schemas: map[string]string{}}
                    available[name] = av
                }
                if version != nil { av.Requires[*version] = requires }
                if version != nil && fixed != "" { av.Schemas[*version] = fixed }
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
        queryUpdateDependents(batch, live, updating)
    }
    if len(moving) > 0 {
        batch.add(`
            select distinct e.extname::text, o.oprname is not null,
                   coalesce(t.typname, p.proname, c.relname, oc.opcname, o.oprname, td.dictname, tt.tmplname, tc.cfgname)::text,
                   b.oid is not null
            from pg_depend d
            join pg_extension e on e.oid = d.refobjid and e.extname in (`+strings.Join(moving, ", ")+`)
            left join pg_type t on d.classid = 'pg_type'::regclass and t.oid = d.objid
            left join pg_proc p on d.classid = 'pg_proc'::regclass and p.oid = d.objid
            left join pg_proc b on b.proname = p.proname and b.pronamespace = 'pg_catalog'::regnamespace
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
                var operator, builtin bool
                if err := rows.Scan(&ext, &operator, &name, &builtin); err != nil { return err }
                le := live.ExtensionDefs[ext]
                if le == nil { continue }
                if le.Members == nil { le.Members, le.Operators, le.Builtins = map[string]bool{}, map[string]bool{}, map[string]bool{} }
                if operator { le.Operators[name] = true } else { le.Members[name] = true }
                if builtin { le.Builtins[name] = true }
            }
            return nil
        })
    }
    if len(prunable) > 0 { queryExtensionDependents(batch, live, prunable) }
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
func newRequalifier(live *Live, desired *schema.Database) *requalifier { return requalifierFor(live, desired, true) }

// withUnmovedExtensions returns desired with the references to the objects
// of each extension declared in a schema it cannot move to (it is not
// relocatable, see planExtensions) qualified with the schema it stays in,
// so that the plan uses the objects the extension has instead of objects a
// move would have made; desired itself when there are none or their member
// names were not loaded.
func withUnmovedExtensions(live *Live, desired *schema.Database) *schema.Database {
    rq := requalifierFor(live, desired, false)
    if rq == nil { return desired }
    return rq.database(desired)
}

// requalifierFor returns a requalifier for the declared extensions whose
// declared schema differs from the live one and whose Relocatable is
// relocatable, or nil.
func requalifierFor(live *Live, desired *schema.Database, relocatable bool) *requalifier {
    if live == nil || desired == nil { return nil }
    rq := &requalifier{}
    for _, ext := range desired.Extensions {
        if ext == nil || ext.Schema == "" { continue }
        le := live.ExtensionDefs[ext.Name]
        if le == nil || le.Relocatable != relocatable || le.Members == nil { continue }
        from := le.Schema
        if le.MovedFrom != "" && le.Schema == ext.Schema { from = le.MovedFrom }
        if from == ext.Schema { continue }
        pattern := schemaPattern(ext.Schema)
        rq.moves = append(rq.moves, extensionMove{from: from, to: ext.Schema, members: le.Members, operators: le.Operators})
        rq.names = append(rq.names, regexp.MustCompile(`(^|[^\w$."])(`+pattern+`)\.("(?:[^"]|"")+"|[A-Za-z_][\w$]*)`))
        rq.ops = append(rq.ops, regexp.MustCompile(`(?i)(operator\s*\(\s*)(?:`+pattern+`)(\s*\.\s*)([^\s()]+)(\s*\))`))
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
func (rq *requalifier) database(d *schema.Database) *schema.Database { return requalified(rq, d) }

// requalified returns a copy of v rewritten the way database rewrites a
// whole schema.
func requalified[T any](rq *requalifier, v *T) *T {
    out := reflect.New(reflect.TypeOf(*v))
    rq.copyValue(out.Elem(), reflect.ValueOf(*v), false)
    return out.Interface().(*T)
}

// routinesAfterMoves returns live with each routine that desired declares
// in the new spelling of a moving extension's objects (an argument type of
// a moved extension qualified with its new schema, say) described as
// PostgreSQL prints it once the extension has moved: keyed by the declared
// signature, with the declared return type, argument defaults and
// transform types wherever the declaration spelled the old way matches the
// live ones. The plan then replaces or alters such a routine in place
// instead of creating it a second time. live itself is returned when no
// declaration needs it.
func routinesAfterMoves(live *Live, desired *schema.Database) *Live {
    rq := newRequalifier(live, desired)
    if rq == nil { return live }
    r := &renamer{l: live}
    move := func(old, norm string) {
        if r.l == live {
            r.l = cloneLive(live)
            r.l.MemberExtension, r.l.InitPrivs = cloneMap(live.MemberExtension, nil), cloneMap(live.InitPrivs, cloneACL)
        }
        moveKey(r.l.ExtensionMembers, "proc:"+old, "proc:"+norm)
        moveKey(r.l.MemberExtension, "proc:"+old, "proc:"+norm)
        moveKey(r.l.InitPrivs, "proc:"+old, "proc:"+norm)
    }
    for _, k := range sortedKeys(desired.Functions) {
        f := desired.Functions[k]
        if f == nil { continue }
        name := schema.RoutineName(k)
        norm, old := normalizeFunctionSignature(name+f.ArgsSig), normalizeFunctionSignature(rq.text(name+f.ArgsSig))
        if old == norm || r.l.FunctionDefs[old] == nil || r.l.FunctionDefs[norm] != nil { continue }
        move(old, norm)
        r.moveFunction(old, norm, name+grantFuncArgs(f.ArgsSig))
        lf, was := r.l.FunctionDefs[norm], requalified(rq, f)
        if f.Returns != "" && !returnsChanged(was.Returns, lf.Returns) { lf.Returns = f.Returns }
        if changed, removed := argDefaultsDiff(was.ArgsSig, lf.Args); lf.Args != "" && !changed && !removed { lf.Args = innerArgs(f.ArgsSig) }
        if t := schema.RoutineTransform(f.Language, f.Transform); t != nil && !transformChanged(schema.RoutineTransform(was.Language, was.Transform), lf.Transform) { lf.Transform = t }
    }
    for _, k := range sortedKeys(desired.Procedures) {
        p := desired.Procedures[k]
        if p == nil { continue }
        name := schema.RoutineName(k)
        norm, old := normalizeFunctionSignature(name+p.ArgsSig), normalizeFunctionSignature(rq.text(name+p.ArgsSig))
        if old == norm || r.l.ProcedureDefs[old] == nil || r.l.Procedures[norm] { continue }
        move(old, norm)
        r.moveProcedure(old, norm, name+grantFuncArgs(p.ArgsSig))
        lp, was := r.l.ProcedureDefs[norm], requalified(rq, p)
        if changed, removed := argDefaultsDiff(was.ArgsSig, lp.Args); lp.Args != "" && !changed && !removed { lp.Args = innerArgs(p.ArgsSig) }
        if t := schema.RoutineTransform(p.Language, p.Transform); t != nil && !transformChanged(schema.RoutineTransform(was.Language, was.Transform), lp.Transform) { lp.Transform = t }
    }
    return r.l
}

// innerArgs returns an argument list without its enclosing parentheses.
func innerArgs(args string) string {
    return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(args), "("), ")")
}

// copyValue copies src to dst, rewriting strings with text unless verbatim
// is set. Map keys are rewritten where they hold signatures or
// expressions: the keys of the routine maps, and those of an index's
// per-column maps, which match its rewritten columns entries.
func (rq *requalifier) copyValue(dst, src reflect.Value, verbatim bool) { rq.copyKeyed(dst, src, verbatim, false) }

func (rq *requalifier) copyKeyed(dst, src reflect.Value, verbatim, keyed bool) {
    switch src.Kind() {
    case reflect.Pointer:
        if src.IsNil() { return }
        dst.Set(reflect.New(src.Type().Elem()))
        rq.copyValue(dst.Elem(), src.Elem(), verbatim)
    case reflect.Struct:
        dst.Set(src)
        index := src.Type() == reflect.TypeOf(schema.Index{})
        for i := 0; i < src.NumField(); i++ {
            f := src.Type().Field(i)
            if !f.IsExported() { continue }
            keyed := index && (f.Name == "Opclasses" || f.Name == "Collations" || f.Name == "StatisticsTargets")
            rq.copyKeyed(dst.Field(i), src.Field(i), verbatim || strings.Contains(f.Name, "Body") || strings.Contains(f.Name, "Comment") || f.Name == "Data", keyed)
        }
    case reflect.Map:
        if src.IsNil() { return }
        dst.Set(reflect.MakeMapWithSize(src.Type(), src.Len()))
        routines := src.Type() == reflect.TypeOf(map[string]*schema.Function(nil)) || src.Type() == reflect.TypeOf(map[string]*schema.Procedure(nil)) || src.Type() == reflect.TypeOf(map[string]*schema.Aggregate(nil))
        iter := src.MapRange()
        for iter.Next() {
            key := iter.Key()
            if routines || keyed && !verbatim { key = reflect.ValueOf(rq.text(key.String())) }
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
// spelling: a statement stays, where plan has it, when that plan has its
// counterpart anywhere. When the two plans don't line up, plan is returned
// unchanged.
func withoutMoveEchoes(plan *PlanDiff, live *Live, desired *schema.Database, opts Options) *PlanDiff {
    rq := newRequalifier(live, desired)
    if rq == nil { return plan }
    before := planWith(live, rq.database(desired), opts)
    want, left := map[string]int{}, 0
    for _, sec := range [][]string{before.ExtensionUpdates, before.Prelude, before.Creates, before.Alters, before.Drops, before.Concurrent} {
        for _, s := range sec {
            want[s]++
            left++
        }
    }
    out := &PlanDiff{}
    kept, planned := map[string]bool{}, map[string]bool{}
    for _, sec := range []struct {
        dst *[]string
        got []string
    }{
        {&out.ExtensionUpdates, plan.ExtensionUpdates},
        {&out.Prelude, plan.Prelude},
        {&out.Creates, plan.Creates},
        {&out.Alters, plan.Alters},
        {&out.Drops, plan.Drops},
        {&out.Concurrent, plan.Concurrent},
    } {
        for _, s := range sec.got {
            planned[s] = true
            if t := rq.text(s); want[t] > 0 {
                want[t]--
                left--
                kept[s] = true
                *sec.dst = append(*sec.dst, s)
            }
        }
    }
    if left > 0 { return plan }
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
