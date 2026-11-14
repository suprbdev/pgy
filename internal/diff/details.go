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

// IntrospectDetails fills l.Details from the database.
func IntrospectDetails(ctx context.Context, pool *pgxpool.Pool, l *Live) error {
    d := &LiveDetails{
        ColumnOrder: map[string][]string{}, PartitionKeys: map[string]string{}, PartitionOf: map[string]string{},
        PartitionBounds: map[string]string{}, Routines: map[string]*LiveRoutine{}, ViewQueries: map[string]string{},
        CompositeOrder: map[string][]string{}, Ranges: map[string]*schema.RangeDef{}, DomainTypes: map[string]LiveDomainType{},
        Collations: map[string]*schema.Collation{}, TSParsers: map[string]string{}, TSDictionaries: map[string]*schema.TSDictionary{},
        ViewDeps: map[string][]string{}, Populated: map[string]bool{},
    }
    l.Details = d
    const nsFilter = `n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast') and n.nspname !~ '^pg_(toast_)?temp_'`
    queries := []struct {
        sql  string
        scan func(func(...any) error) error
    }{
        {`select n.nspname || '.' || c.relname, a.attname, c.relkind::text
          from pg_attribute a join pg_class c on c.oid = a.attrelid join pg_namespace n on n.oid = c.relnamespace
          where a.attnum > 0 and not a.attisdropped and c.relkind in ('r', 'p', 'c') and ` + nsFilter + `
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
          where c.relkind in ('r', 'p') and (c.relkind = 'p' or c.relispartition) and ` + nsFilter,
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
          where p.prokind in ('f', 'p') and ` + nsFilter + `
          and not exists (select 1 from pg_depend d where d.classid = 'pg_proc'::regclass and d.objid = p.oid and d.deptype = 'i')`,
            func(scan func(...any) error) error {
                r := &LiveRoutine{}
                var identity string
                if err := scan(&r.Schema, &r.Name, &identity, &r.Arguments, &r.Returns, &r.Language, &r.Procedure); err != nil { return err }
                d.Routines[normalizeFunctionSignature(fmt.Sprintf("%s.%s(%s)", r.Schema, r.Name, identity))] = r
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
                d.ViewDeps[view] = append(d.ViewDeps[view], ref)
                return nil
            }},
        {`select n.nspname || '.' || t.typname, format_type(r.rngsubtype, null),
                 coalesce((select o.opcname from pg_opclass o where o.oid = r.rngsubopc and not o.opcdefault), ''),
                 coalesce((select co.collname from pg_collation co where co.oid = r.rngcollation and r.rngcollation <> 100), ''),
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
                 coalesce((select co.collname from pg_collation co join pg_type b on b.oid = t.typbasetype
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
          where ` + nsFilter,
            func(scan func(...any) error) error {
                var key, tmpl, opts string
                if err := scan(&key, &tmpl, &opts); err != nil { return err }
                td := &schema.TSDictionary{Template: strings.TrimPrefix(tmpl, "pg_catalog."), Options: parseDictOptions(opts)}
                d.TSDictionaries[key] = td
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

// parseDictOptions splits pg_ts_dict.dictinitoption ("language = 'english',
// stopwords = 'english'") into name -> value, keeping the value as written.
func parseDictOptions(s string) map[string]string {
    if strings.TrimSpace(s) == "" { return nil }
    masked, lits := maskStringLiterals(s)
    out := map[string]string{}
    for _, part := range strings.Split(masked, ",") {
        k, v, ok := strings.Cut(part, "=")
        if !ok { continue }
        v = exprPlaceholderRe.ReplaceAllStringFunc(strings.TrimSpace(v), func(m string) string {
            var i int
            fmt.Sscanf(exprPlaceholderRe.FindStringSubmatch(m)[1], "%d", &i) //nolint:errcheck
            return lits[i]
        })
        out[strings.TrimSpace(k)] = v
    }
    return out
}

// queryBatch collects introspection queries and their row handlers so they
// can be sent to the server in one pipelined round trip.
type queryBatch struct {
    sqls     []string
    handlers []func(pgx.Rows) error
}

func (b *queryBatch) add(sql string, handle func(pgx.Rows) error) {
    b.sqls = append(b.sqls, sql)
    b.handlers = append(b.handlers, handle)
}

// run sends every query in one batch and feeds each result to its handler
// in the order the queries were added.
func (b *queryBatch) run(ctx context.Context, pool *pgxpool.Pool) error {
    batch := &pgx.Batch{}
    for _, q := range b.sqls { batch.Queue(q) }
    br := pool.SendBatch(ctx, batch)
    defer func() { _ = br.Close() }()
    for i, handle := range b.handlers {
        rows, err := br.Query()
        if err != nil { return fmt.Errorf("introspection query %d: %w", i+1, err) }
        err = handle(rows)
        rows.Close()
        if err == nil { err = rows.Err() }
        if err != nil { return fmt.Errorf("introspection query %d: %w", i+1, err) }
    }
    return br.Close()
}
