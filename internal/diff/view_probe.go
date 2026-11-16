package diff

import (
    "context"
    "fmt"
    "strings"

    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/suprbdev/pgy/internal/schema"
)

// ViewProbe compares a declared view's query with the live view: both
// deparsed by PostgreSQL with pg_get_viewdef in one session, and each
// one's columns as "name type".
type ViewProbe struct {
    DesiredDef  string
    DesiredCols []string
    LiveDef     string
    LiveCols    []string
    // Err is why the declared query couldn't be deparsed, such as a
    // relation or column this plan creates; "" when it was.
    Err string
}

// Changed reports whether the declared query differs from the live one, or
// couldn't be compared.
func (p *ViewProbe) Changed() bool { return p.Err != "" || p.DesiredDef != p.LiveDef }

// ColumnsCompatible reports whether CREATE OR REPLACE VIEW can switch the
// live view to the declared query: the live columns, with their names and
// types, are the declared ones' prefix.
func (p *ViewProbe) ColumnsCompatible() bool {
    if p.Err != "" || len(p.DesiredCols) < len(p.LiveCols) { return false }
    for i, c := range p.LiveCols {
        if p.DesiredCols[i] != c { return false }
    }
    return true
}

const viewProbeFunc = `
create function pg_temp.pgy_viewdef(q text, OUT def text, OUT cols text[], OUT err text) language plpgsql as $f$
begin
    execute 'create temp view pgy_probe as ' || q;
    def := pg_get_viewdef('pgy_probe'::regclass, true);
    cols := array(select quote_ident(a.attname) || ' ' || format_type(a.atttypid, a.atttypmod)
                  from pg_attribute a where a.attrelid = 'pgy_probe'::regclass and a.attnum > 0 and not a.attisdropped order by a.attnum);
    execute 'drop view pgy_probe';
exception when others then
    def := null; cols := null; err := sqlerrm;
end
$f$`

// ProbeViews compares the query of each declared view and materialized view
// that exists live with the live definition, by creating it as a temporary
// view in a transaction that is always rolled back, so PostgreSQL rewrites
// both the same way (qualification, casts, aliases, * expansion). The
// results go to live.ViewProbes, and the live views that depend on each
// probed view to live.ViewDependents. A server that can't run the probe,
// such as a read-only standby, leaves both empty, and views stay
// create-only; the error is returned for the caller to report.
func ProbeViews(ctx context.Context, pool *pgxpool.Pool, live *Live, desired *schema.Database) error {
    if live == nil || desired == nil || pool == nil { return nil }
    keys, queries, schemas, names := []string{}, []string{}, []string{}, []string{}
    for _, k := range sortedKeys(desired.Views) {
        vw := desired.Views[k]
        if vw == nil || strings.TrimSpace(vw.Query) == "" { continue }
        if vw.Materialized && !live.MatViews[k] || !vw.Materialized && !live.Views[k] { continue }
        s, n := splitQualified(k)
        keys, schemas, names = append(keys, k), append(schemas, s), append(names, n)
        queries = append(queries, strings.TrimRight(strings.TrimSpace(vw.Query), "; \t\n"))
    }
    if len(keys) == 0 { return nil }
    tx, err := pool.Begin(ctx)
    if err != nil { return err }
    defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
    if _, err := tx.Exec(ctx, viewProbeFunc); err != nil { return fmt.Errorf("view probe: %w", err) }
    rows, err := tx.Query(ctx, `
        select t.k, d.def, d.cols, d.err,
               pg_get_viewdef(c.oid, true),
               array(select quote_ident(a.attname) || ' ' || format_type(a.atttypid, a.atttypmod)
                     from pg_attribute a where a.attrelid = c.oid and a.attnum > 0 and not a.attisdropped order by a.attnum)
        from unnest($1::text[], $2::text[], $3::text[], $4::text[]) with ordinality t(k, q, s, n, i)
        join pg_namespace ns on ns.nspname = t.s
        join pg_class c on c.relnamespace = ns.oid and c.relname = t.n
        cross join lateral pg_temp.pgy_viewdef(t.q) d
        order by t.i`, keys, queries, schemas, names)
    if err != nil { return fmt.Errorf("view probe: %w", err) }
    probes := map[string]*ViewProbe{}
    for rows.Next() {
        var k string
        var def, errText *string
        var cols []string
        p := &ViewProbe{}
        if err := rows.Scan(&k, &def, &cols, &errText, &p.LiveDef, &p.LiveCols); err != nil {
            rows.Close()
            return fmt.Errorf("view probe: %w", err)
        }
        if def != nil { p.DesiredDef = *def }
        if errText != nil { p.Err = *errText }
        p.DesiredCols = cols
        probes[k] = p
    }
    rows.Close()
    if err := rows.Err(); err != nil { return fmt.Errorf("view probe: %w", err) }
    deps, err := viewDependents(ctx, tx)
    if err != nil { return err }
    live.ViewProbes, live.ViewDependents = probes, deps
    return nil
}

// viewDependents returns, for each view and materialized view, the views
// and materialized views that select from it directly.
func viewDependents(ctx context.Context, tx pgx.Tx) (map[string][]string, error) {
    rows, err := tx.Query(ctx, `
        select distinct rn.nspname || '.' || r.relname, vn.nspname || '.' || v.relname
        from pg_depend d
        join pg_rewrite rw on d.classid = 'pg_rewrite'::regclass and rw.oid = d.objid
        join pg_class v on v.oid = rw.ev_class
        join pg_namespace vn on vn.oid = v.relnamespace
        join pg_class r on r.oid = d.refobjid
        join pg_namespace rn on rn.oid = r.relnamespace
        where d.refclassid = 'pg_class'::regclass and d.deptype = 'n'
        and v.oid <> r.oid and v.relkind in ('v', 'm') and r.relkind in ('v', 'm')
        order by 1, 2`)
    if err != nil { return nil, fmt.Errorf("view dependents: %w", err) }
    defer rows.Close()
    out := map[string][]string{}
    for rows.Next() {
        var ref, dep string
        if err := rows.Scan(&ref, &dep); err != nil { return nil, err }
        out[ref] = append(out[ref], dep)
    }
    return out, rows.Err()
}
