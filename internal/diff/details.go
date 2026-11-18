package diff

import (
    "context"
    "fmt"
    "strings"

    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/suprbdev/pgy/internal/schema"
)

// LiveDetails holds catalog details that diffing does not need but
// reconstructing YAML from a database (pgy pull) does.
type LiveDetails struct {
    ColumnOrder     map[string][]string // table -> column names by attnum
    PartitionKeys   map[string]string   // partitioned table -> pg_get_partkeydef, e.g. RANGE (at)
    PartitionOf     map[string]string   // partition -> parent table
    PartitionBounds map[string]string   // partition -> FOR VALUES ... / DEFAULT
    Routines        map[string]*LiveRoutine // normalized signature -> definition
    ViewQueries     map[string]string   // view or materialized view -> pg_get_viewdef
    CompositeOrder  map[string][]string // composite type -> attribute names by attnum
    Ranges          map[string]*schema.RangeDef
    DomainTypes     map[string]LiveDomainType
    Collations      map[string]*schema.Collation
    TSParsers       map[string]string // text search configuration -> parser
    TSDictionaries  map[string]*schema.TSDictionary
    ViewDeps        map[string][]string // view or materialized view -> views / materialized views it reads
    Populated       map[string]bool     // materialized view -> has data
    // RoutineDeps: normalized routine signature -> dependsOn entries
    // ("table x", "view x", "function x(args)", ...) for routines with a
    // SQL-standard body, whose references PostgreSQL records in pg_depend.
    // Objects an extension owns are left out, here and in ViewDeps: they
    // exist once the extension does, and are not declared.
    RoutineDeps map[string][]string
    // TransformLanguages: procedural languages that have at least one
    // transform, so their routines can take a TRANSFORM FOR TYPE clause.
    TransformLanguages map[string]bool
    // ExtensionUsers: extension -> the schemas of the objects outside every
    // extension that depend on one of its objects, as pg_depend records it:
    // columns, defaults, constraints, indexes, triggers, policies, views,
    // routines, domains and text search objects. Calls in function bodies
    // written as text are not recorded.
    ExtensionUsers map[string]map[string]bool
    // Unsupported lists objects pgy cannot express (operators, operator
    // classes and families, conversions, base types, casts, text search
    // parsers and templates, procedural languages and transforms),
    // excluding extension members and objects other objects create (such as
    // a range type's multirange casts).
    Unsupported []UnsupportedObject
}

// UnsupportedObject is a database object pgy has no YAML for. Schema is
// empty for kinds that live outside schemas (languages, transforms). Detail
// holds extra information for some kinds; it is empty for the others.
// Owner is the role that owns the object, "" for kinds without an owner
// (text search parsers and templates, casts, transforms).
type UnsupportedObject struct {
    Kind, Schema, Name, Detail, Owner string
}

// LiveRoutine is a function or procedure's declaration as pg_get_function_*
// prints it. Arguments includes defaults; Returns is empty for procedures.
type LiveRoutine struct {
    Schema    string
    Name      string
    Arguments string
    Returns   string
    Language  string
    Procedure bool
}

// LiveDomainType is a domain's base type and explicit collation.
type LiveDomainType struct {
    Type      string
    Collation string
}

// IntrospectDetails fills l.Details from the database. Like Introspect, it
// retries when another session's DDL invalidates an object mid-read.
func IntrospectDetails(ctx context.Context, pool *pgxpool.Pool, l *Live) error {
    var err error
    for attempt := 0; attempt < 3; attempt++ {
        if err = introspectDetailsOnce(ctx, pool, l); err == nil || !isCatalogRace(err) {
            return err
        }
    }
    return err
}

func introspectDetailsOnce(ctx context.Context, pool *pgxpool.Pool, l *Live) error {
    d := &LiveDetails{
        ColumnOrder: map[string][]string{}, PartitionKeys: map[string]string{}, PartitionOf: map[string]string{},
        PartitionBounds: map[string]string{}, Routines: map[string]*LiveRoutine{}, ViewQueries: map[string]string{},
        CompositeOrder: map[string][]string{}, Ranges: map[string]*schema.RangeDef{}, DomainTypes: map[string]LiveDomainType{},
        Collations: map[string]*schema.Collation{}, TSParsers: map[string]string{}, TSDictionaries: map[string]*schema.TSDictionary{},
        ViewDeps: map[string][]string{}, Populated: map[string]bool{}, RoutineDeps: map[string][]string{},
        TransformLanguages: map[string]bool{}, ExtensionUsers: map[string]map[string]bool{},
    }
    l.Details = d
    const unsupportedQ = `
        with objs(kind, nsp, name, classid, objid, detail, owner) as (
            select 'operator', o.oprnamespace, o.oprname || '(' || coalesce(format_type(o.oprleft, null), 'none') || ', ' || coalesce(format_type(o.oprright, null), 'none') || ')', 'pg_operator'::regclass, o.oid, '', o.oprowner from pg_operator o
            union all select 'operator class', c.opcnamespace, c.opcname::text, 'pg_opclass'::regclass, c.oid, '', c.opcowner from pg_opclass c
            union all select 'operator family', f.opfnamespace, f.opfname::text, 'pg_opfamily'::regclass, f.oid, '', f.opfowner from pg_opfamily f
            union all select 'conversion', c.connamespace, c.conname::text, 'pg_conversion'::regclass, c.oid, '', c.conowner from pg_conversion c
            union all select 'base type', t.typnamespace, t.typname::text, 'pg_type'::regclass, t.oid, '', t.typowner from pg_type t where t.typtype = 'b' and t.typcategory <> 'A'
            union all select 'text search parser', p.prsnamespace, p.prsname::text, 'pg_ts_parser'::regclass, p.oid, '', 0::oid from pg_ts_parser p
            union all select 'text search template', t.tmplnamespace, t.tmplname::text, 'pg_ts_template'::regclass, t.oid, '', 0::oid from pg_ts_template t
            union all select 'cast', case when st.typnamespace = 'pg_catalog'::regnamespace then tt.typnamespace else st.typnamespace end,
                   '(' || format_type(c.castsource, null) || ' as ' || format_type(c.casttarget, null) || ')', 'pg_cast'::regclass, c.oid, '', 0::oid
              from pg_cast c join pg_type st on st.oid = c.castsource join pg_type tt on tt.oid = c.casttarget where c.oid >= 16384
            union all select 'language', 0::oid, l.lanname::text, 'pg_language'::regclass, l.oid, '', l.lanowner from pg_language l where l.lanispl
            union all select 'transform', 0::oid, 'for ' || format_type(t.trftype, null) || ' language ' || l.lanname, 'pg_transform'::regclass, t.oid, '', 0::oid
              from pg_transform t join pg_language l on l.oid = t.trflang
        )
        select distinct o.kind, coalesce(n.nspname, ''), o.name, o.detail, case when o.owner = 0 then '' else coalesce(pg_get_userbyid(o.owner), '') end
        from objs o left join pg_namespace n on n.oid = o.nsp
        where (o.nsp = 0 or n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast') and n.nspname !~ '^pg_(toast_)?temp_')
        and not exists (select 1 from pg_depend d where d.classid = o.classid and d.objid = o.objid and d.deptype in ('e', 'i'))
        order by 2, 1, 3`
    unsupported := &queryBatch{}
    unsupported.add(unsupportedQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var o UnsupportedObject
            if rows.Scan(&o.Kind, &o.Schema, &o.Name, &o.Detail, &o.Owner) == nil { d.Unsupported = append(d.Unsupported, o) }
        }
        return nil
    })
    _ = unsupported.run(ctx, pool)
    const nsFilter = `n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast') and n.nspname !~ '^pg_(toast_)?temp_'`
    queries := []struct {
        sql  string
        scan func(func(...any) error) error
    }{
        {`select n.nspname || '.' || c.relname, a.attname, c.relkind::text
          from pg_attribute a join pg_class c on c.oid = a.attrelid join pg_namespace n on n.oid = c.relnamespace
          where a.attnum > 0 and not a.attisdropped and c.relkind in ('r', 'p', 'c', 'f') and ` + nsFilter + `
          order by 1, a.attnum`,
            func(scan func(...any) error) error {
                var rel, att, kind string
                if err := scan(&rel, &att, &kind); err != nil { return err }
                if kind == "c" {
                    d.CompositeOrder[rel] = append(d.CompositeOrder[rel], att)
                } else {
                    d.ColumnOrder[rel] = append(d.ColumnOrder[rel], att)
                }
                return nil
            }},
        {`select n.nspname || '.' || c.relname, coalesce(pg_get_partkeydef(c.oid), ''),
                 coalesce(pg_get_expr(c.relpartbound, c.oid), ''),
                 coalesce((select pn.nspname || '.' || p.relname from pg_inherits i join pg_class p on p.oid = i.inhparent
                           join pg_namespace pn on pn.oid = p.relnamespace where i.inhrelid = c.oid and c.relispartition), '')
          from pg_class c join pg_namespace n on n.oid = c.relnamespace
          where c.relkind in ('r', 'p', 'f') and (c.relkind = 'p' or c.relispartition) and ` + nsFilter,
            func(scan func(...any) error) error {
                var rel, key, bound, parent string
                if err := scan(&rel, &key, &bound, &parent); err != nil { return err }
                if key != "" { d.PartitionKeys[rel] = key }
                if parent != "" { d.PartitionOf[rel], d.PartitionBounds[rel] = parent, bound }
                return nil
            }},
        {`select n.nspname, p.proname, pg_get_function_identity_arguments(p.oid), pg_get_function_arguments(p.oid),
                 coalesce(pg_get_function_result(p.oid), ''), l.lanname, p.prokind = 'p'
          from pg_proc p join pg_namespace n on n.oid = p.pronamespace join pg_language l on l.oid = p.prolang
          where p.prokind in ('f', 'p', 'w') and ` + userObjects("n", "pg_proc", "p.oid") + `
          and not exists (select 1 from pg_depend d where d.classid = 'pg_proc'::regclass and d.objid = p.oid and d.deptype = 'i')`,
            func(scan func(...any) error) error {
                r := &LiveRoutine{}
                var identity string
                if err := scan(&r.Schema, &r.Name, &identity, &r.Arguments, &r.Returns, &r.Language, &r.Procedure); err != nil { return err }
                d.Routines[publicScope.functionSignature(fmt.Sprintf("%s.%s(%s)", r.Schema, r.Name, identity))] = r
                return nil
            }},
        {`select distinct l.lanname from pg_transform t join pg_language l on l.oid = t.trflang`,
            func(scan func(...any) error) error {
                var language string
                if err := scan(&language); err != nil { return err }
                d.TransformLanguages[language] = true
                return nil
            }},
        {`select n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')',
                 case when d.refclassid = 'pg_class'::regclass then
                        case c.relkind when 'v' then 'view ' when 'm' then 'materialized view ' when 'S' then 'sequence ' else 'table ' end
                        || cn.nspname || '.' || c.relname
                      else case when rp.prokind = 'p' then 'procedure ' else 'function ' end
                        || rn.nspname || '.' || rp.proname || '(' || pg_get_function_identity_arguments(rp.oid) || ')' end
          from pg_proc p
          join pg_namespace n on n.oid = p.pronamespace
          join pg_depend d on d.classid = 'pg_proc'::regclass and d.objid = p.oid and d.deptype = 'n'
          left join pg_class c on d.refclassid = 'pg_class'::regclass and c.oid = d.refobjid
          left join pg_namespace cn on cn.oid = c.relnamespace
          left join pg_proc rp on d.refclassid = 'pg_proc'::regclass and rp.oid = d.refobjid
          left join pg_namespace rn on rn.oid = rp.pronamespace
          where p.prosqlbody is not null and p.prokind in ('f', 'p') and ` + nsFilter + `
          and (c.oid is not null and c.relkind in ('r', 'p', 'f', 'v', 'm', 'S') or rp.oid is not null and rp.oid <> p.oid and rn.nspname not in ('pg_catalog', 'information_schema'))
          order by 1, 2`,
            func(scan func(...any) error) error {
                var sig, dep string
                if err := scan(&sig, &dep); err != nil { return err }
                if l.ExtensionMembers[dependencyMemberKey(dep)] { return nil }
                key := publicScope.functionSignature(sig)
                if deps := d.RoutineDeps[key]; len(deps) == 0 || deps[len(deps)-1] != dep { d.RoutineDeps[key] = append(deps, dep) }
                return nil
            }},
        {`select n.nspname || '.' || c.relname, pg_get_viewdef(c.oid, true), c.relkind = 'm' and c.relispopulated
          from pg_class c join pg_namespace n on n.oid = c.relnamespace
          where c.relkind in ('v', 'm') and ` + nsFilter,
            func(scan func(...any) error) error {
                var rel, q string
                var populated bool
                if err := scan(&rel, &q, &populated); err != nil { return err }
                d.ViewQueries[rel] = strings.TrimSuffix(strings.TrimSpace(q), ";")
                d.Populated[rel] = populated
                return nil
            }},
        {`select distinct vn.nspname || '.' || v.relname, rn.nspname || '.' || r.relname
          from pg_depend dep
          join pg_rewrite rw on rw.oid = dep.objid
          join pg_class v on v.oid = rw.ev_class join pg_namespace vn on vn.oid = v.relnamespace
          join pg_class r on r.oid = dep.refobjid join pg_namespace rn on rn.oid = r.relnamespace
          where dep.classid = 'pg_rewrite'::regclass and dep.refclassid = 'pg_class'::regclass
          and v.oid <> r.oid and v.relkind in ('v', 'm') and r.relkind in ('v', 'm')
          and vn.nspname not in ('pg_catalog', 'information_schema') and rn.nspname not in ('pg_catalog', 'information_schema')
          order by 1, 2`,
            func(scan func(...any) error) error {
                var view, ref string
                if err := scan(&view, &ref); err != nil { return err }
                if l.ExtensionMembers["rel:"+ref] { return nil }
                d.ViewDeps[view] = append(d.ViewDeps[view], ref)
                return nil
            }},
        {`select n.nspname || '.' || t.typname, format_type(r.rngsubtype, null),
                 coalesce((select o.opcname from pg_opclass o where o.oid = r.rngsubopc and not o.opcdefault), ''),
                 coalesce((select case when cn.nspname = 'pg_catalog' then '' else cn.nspname || '.' end || case when strpos(co.collname, '.') > 0 then quote_ident(co.collname) else co.collname::text end
                           from pg_collation co join pg_namespace cn on cn.oid = co.collnamespace
                           where co.oid = r.rngcollation and r.rngcollation <> 100), ''),
                 coalesce(nullif(r.rngcanonical::text, '-'), ''), coalesce(nullif(r.rngsubdiff::text, '-'), ''),
                 coalesce((select mn.nspname || '.' || m.typname from pg_type m join pg_namespace mn on mn.oid = m.typnamespace
                           where m.oid = r.rngmultitypid), '')
          from pg_range r join pg_type t on t.oid = r.rngtypid join pg_namespace n on n.oid = t.typnamespace
          where ` + nsFilter,
            func(scan func(...any) error) error {
                var key, multi string
                rd := &schema.RangeDef{}
                if err := scan(&key, &rd.Subtype, &rd.SubtypeOpclass, &rd.Collation, &rd.Canonical, &rd.SubtypeDiff, &multi); err != nil { return err }
                if multi != "" && multi != key+"_multirange" && multi != strings.Replace(key, "range", "multirange", 1) {
                    rd.MultirangeTypeName = multi
                }
                d.Ranges[key] = rd
                return nil
            }},
        {`select n.nspname || '.' || t.typname, format_type(t.typbasetype, t.typtypmod),
                 coalesce((select case when cn.nspname = 'pg_catalog' then '' else cn.nspname || '.' end || case when strpos(co.collname, '.') > 0 then quote_ident(co.collname) else co.collname::text end
                           from pg_collation co join pg_namespace cn on cn.oid = co.collnamespace join pg_type b on b.oid = t.typbasetype
                           where co.oid = t.typcollation and t.typcollation <> b.typcollation), '')
          from pg_type t join pg_namespace n on n.oid = t.typnamespace
          where t.typtype = 'd' and ` + nsFilter,
            func(scan func(...any) error) error {
                var key string
                var dt LiveDomainType
                if err := scan(&key, &dt.Type, &dt.Collation); err != nil { return err }
                d.DomainTypes[key] = dt
                return nil
            }},
        {`select n.nspname || '.' || c.collname,
                 case c.collprovider when 'i' then 'icu' when 'c' then 'libc' when 'b' then 'builtin' else '' end,
                 c.collisdeterministic,
                 coalesce(to_jsonb(c)->>'colllocale', to_jsonb(c)->>'colliculocale', ''),
                 coalesce(c.collcollate, ''), coalesce(c.collctype, ''), coalesce(to_jsonb(c)->>'collicurules', '')
          from pg_collation c join pg_namespace n on n.oid = c.collnamespace
          where ` + nsFilter,
            func(scan func(...any) error) error {
                var key string
                var det bool
                co := &schema.Collation{}
                if err := scan(&key, &co.Provider, &det, &co.Locale, &co.LcCollate, &co.LcCtype, &co.Rules); err != nil { return err }
                if !det { co.Deterministic = &det }
                if co.Locale == "" && co.LcCollate != "" && co.LcCollate == co.LcCtype { co.Locale, co.LcCollate, co.LcCtype = co.LcCollate, "", "" }
                d.Collations[key] = co
                return nil
            }},
        {`select n.nspname || '.' || c.cfgname, pn.nspname || '.' || p.prsname
          from pg_ts_config c join pg_namespace n on n.oid = c.cfgnamespace
          join pg_ts_parser p on p.oid = c.cfgparser join pg_namespace pn on pn.oid = p.prsnamespace
          where ` + nsFilter,
            func(scan func(...any) error) error {
                var key, parser string
                if err := scan(&key, &parser); err != nil { return err }
                d.TSParsers[key] = parser
                return nil
            }},
        {`select n.nspname || '.' || t.dictname, tn.nspname || '.' || tp.tmplname, coalesce(t.dictinitoption, '')
          from pg_ts_dict t join pg_namespace n on n.oid = t.dictnamespace
          join pg_ts_template tp on tp.oid = t.dicttemplate join pg_namespace tn on tn.oid = tp.tmplnamespace
          where ` + userObjects("n", "pg_ts_dict", "t.oid"),
            func(scan func(...any) error) error {
                var key, tmpl, opts string
                if err := scan(&key, &tmpl, &opts); err != nil { return err }
                options, order := parseDictOptions(opts)
                td := &schema.TSDictionary{Template: strings.TrimPrefix(tmpl, "pg_catalog."), Options: options, OptionOrder: order}
                d.TSDictionaries[key] = td
                return nil
            }},
        {`select distinct e.extname::text, n.nspname::text
          from pg_extension e
          join pg_depend m on m.refclassid = 'pg_extension'::regclass and m.refobjid = e.oid and m.deptype = 'e'
          join pg_depend d on d.refclassid = m.classid and d.refobjid = m.objid and d.deptype = 'n'
          left join pg_depend x on x.classid = d.classid and x.objid = d.objid and x.deptype = 'e'
          left join pg_class c on d.classid = 'pg_class'::regclass and c.oid = d.objid
          left join pg_type t on d.classid = 'pg_type'::regclass and t.oid = d.objid
          left join pg_proc p on d.classid = 'pg_proc'::regclass and p.oid = d.objid
          left join pg_constraint co on d.classid = 'pg_constraint'::regclass and co.oid = d.objid
          left join pg_statistic_ext st on d.classid = 'pg_statistic_ext'::regclass and st.oid = d.objid
          left join pg_ts_dict td on d.classid = 'pg_ts_dict'::regclass and td.oid = d.objid
          left join pg_ts_config tc on d.classid = 'pg_ts_config'::regclass and tc.oid = d.objid
          left join pg_attrdef ad on d.classid = 'pg_attrdef'::regclass and ad.oid = d.objid
          left join pg_trigger tg on d.classid = 'pg_trigger'::regclass and tg.oid = d.objid
          left join pg_rewrite rw on d.classid = 'pg_rewrite'::regclass and rw.oid = d.objid
          left join pg_policy po on d.classid = 'pg_policy'::regclass and po.oid = d.objid
          left join pg_class r on r.oid = coalesce(ad.adrelid, tg.tgrelid, rw.ev_class, po.polrelid)
          join pg_namespace n on n.oid = coalesce(c.relnamespace, t.typnamespace, p.pronamespace, co.connamespace, st.stxnamespace, td.dictnamespace, tc.cfgnamespace, r.relnamespace)
          where x.objid is null and ` + nsFilter,
            func(scan func(...any) error) error {
                var ext, nsp string
                if err := scan(&ext, &nsp); err != nil { return err }
                if d.ExtensionUsers[ext] == nil { d.ExtensionUsers[ext] = map[string]bool{} }
                d.ExtensionUsers[ext][nsp] = true
                return nil
            }},
    }
    batch := &queryBatch{}
    for _, q := range queries {
        scan := q.scan
        batch.add(q.sql, func(rows pgx.Rows) error {
            for rows.Next() {
                if err := scan(rows.Scan); err != nil { return err }
            }
            return nil
        })
    }
    return batch.run(ctx, pool)
}

// dependencyMemberKey returns the Live.ExtensionMembers key of the object a
// RoutineDeps entry ("table x", "function x(args)", ...) names.
func dependencyMemberKey(dep string) string {
    for _, kind := range []string{"function ", "procedure "} {
        if sig, ok := strings.CutPrefix(dep, kind); ok { return "proc:" + publicScope.functionSignature(sig) }
    }
    for _, kind := range []string{"materialized view ", "view ", "sequence ", "table "} {
        if name, ok := strings.CutPrefix(dep, kind); ok { return "rel:" + name }
    }
    return ""
}

// parseDictOptions splits pg_ts_dict.dictinitoption ("language = 'english',
// stopwords = 'english'") into name -> value, keeping the value as written,
// and returns the names in the order they are stored.
func parseDictOptions(s string) (map[string]string, []string) {
    if strings.TrimSpace(s) == "" { return nil, nil }
    masked, lits := maskStringLiterals(s)
    out := map[string]string{}
    var order []string
    for _, part := range strings.Split(masked, ",") {
        k, v, ok := strings.Cut(part, "=")
        if !ok { continue }
        v = exprPlaceholderRe.ReplaceAllStringFunc(strings.TrimSpace(v), func(m string) string {
            var i int
            fmt.Sscanf(exprPlaceholderRe.FindStringSubmatch(m)[1], "%d", &i) //nolint:errcheck
            return lits[i]
        })
        out[strings.TrimSpace(k)] = v
        order = append(order, strings.TrimSpace(k))
    }
    return out, order
}

// queryBatch collects introspection queries and their row handlers so they
// can be sent to the server in one pipelined round trip.
type queryBatch struct {
    sqls     []string
    args     [][]any
    handlers []func(pgx.Rows) error
    // searchPath is the session's search_path (see Live.SearchPath), which
    // run reads before it pins the batch's own.
    searchPath []string
    // setup holds statements that change the database, which run before
    // the queries see it; the transaction is then rolled back.
    setup []string
}

func (b *queryBatch) add(sql string, handle func(pgx.Rows) error, args ...any) {
    b.sqls = append(b.sqls, sql)
    b.args = append(b.args, args)
    b.handlers = append(b.handlers, handle)
}

// run sends every query in one batch and feeds each result to its handler
// in the order the queries were added. The batch runs in one read-only
// transaction, so every query sees the same snapshot, with JIT compilation
// off: catalog queries never gain from it, and on large catalogs their cost
// estimates cross jit_above_cost and compiling doubles introspection time.
// The transaction's search_path is pinned (see pinSearchPathQ), so what
// the queries deparse does not depend on the session. With setup
// statements the transaction is read-write, runs them on the session's
// search_path first, and is rolled back.
func (b *queryBatch) run(ctx context.Context, pool *pgxpool.Pool) error {
    batch := &pgx.Batch{}
    begin, end := "begin isolation level repeatable read read only", "commit"
    if len(b.setup) > 0 { begin, end = "begin isolation level repeatable read", "rollback" }
    batch.Queue(begin)
    batch.Queue("set local jit = off")
    for _, s := range b.setup { batch.Queue(s) }
    batch.Queue(sessionSearchPathQ)
    batch.Queue(pinSearchPathQ)
    for i, q := range b.sqls { batch.Queue(q, b.args[i]...) }
    batch.Queue(end)
    br := pool.SendBatch(ctx, batch)
    defer func() { _ = br.Close() }()
    for j := 0; j < 2; j++ {
        if _, err := br.Exec(); err != nil { return fmt.Errorf("introspection: %w", err) }
    }
    for _, s := range b.setup {
        if _, err := br.Exec(); err != nil { return &setupError{stmt: s, err: err} }
    }
    if err := br.QueryRow().Scan(&b.searchPath); err != nil { return fmt.Errorf("introspection: %w", err) }
    if _, err := br.Exec(); err != nil { return fmt.Errorf("introspection: %w", err) }
    for i, handle := range b.handlers {
        rows, err := br.Query()
        if err != nil { return fmt.Errorf("introspection query %d: %w", i+1, err) }
        err = handle(rows)
        rows.Close()
        if err == nil { err = rows.Err() }
        if err != nil && strings.Contains(err.Error(), "cannot scan NULL") {
            return fmt.Errorf("introspection query %d: %w (%v)", i+1, errObjectVanished, err)
        }
        if err != nil { return fmt.Errorf("introspection query %d: %w", i+1, err) }
    }
    if _, err := br.Exec(); err != nil { return fmt.Errorf("introspection: %w", err) }
    return br.Close()
}
