package diff

import (
    "context"
    "fmt"
    "strings"

    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/suprbdev/pgy/internal/schema"
)

// ViewProbe compares a declared view's query with the live view: both
// deparsed by PostgreSQL with pg_get_viewdef under the pinned search_path
// (see pinSearchPathQ), and each one's columns as "name type".
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

// viewProbeFuncs creates the probe's two functions. pgy_viewdef parses the
// declared query on the session's own search_path, which is how the
// migration will run it; pgy_deparse prints a view with the pinned one.
const viewProbeFuncs = `
create function pg_temp.pgy_deparse(v oid, OUT def text, OUT cols text[]) language sql stable set search_path = public as $f$
    select pg_get_viewdef(v, true),
           array(select quote_ident(a.attname) || ' ' || format_type(a.atttypid, a.atttypmod)
                 from pg_attribute a where a.attrelid = v and a.attnum > 0 and not a.attisdropped order by a.attnum)
$f$;
create function pg_temp.pgy_viewdef(q text, OUT def text, OUT cols text[], OUT err text) language plpgsql as $f$
begin
    execute 'create temp view pgy_probe as ' || q;
    select d.def, d.cols into def, cols from pg_temp.pgy_deparse('pgy_probe'::regclass) d;
    execute 'drop view pgy_probe';
exception when others then
    def := null; cols := null; err := sqlerrm;
end
$f$`

// ProbeViews compares the query of each declared view and materialized view
// that exists live with the live definition, by creating it as a temporary
// view in a transaction that is always rolled back, so PostgreSQL rewrites
// both the same way (qualification, casts, aliases, * expansion). The
// declared query is parsed on the session's search_path and both are
// printed with the pinned one. A query that names the objects of an
// extension the plan moves to another schema is probed in the schema they
// are still in. The
// results go to live.ViewProbes. A server that can't run the probe, such as
// a read-only standby, leaves it empty, and views stay create-only; the
// error is returned for the caller to report.
func ProbeViews(ctx context.Context, pool *pgxpool.Pool, live *Live, desired *schema.Database) error {
    if live == nil || desired == nil || pool == nil { return nil }
    keys, queries, schemas, names := []string{}, []string{}, []string{}, []string{}
    rq := newRequalifier(live, desired)
    for _, k := range sortedKeys(desired.Views) {
        vw := desired.Views[k]
        if vw == nil || strings.TrimSpace(vw.Query) == "" { continue }
        if vw.Materialized && !live.MatViews[k] || !vw.Materialized && !live.Views[k] { continue }
        s, n := splitQualified(k)
        keys, schemas, names = append(keys, k), append(schemas, s), append(names, n)
        query := strings.TrimRight(strings.TrimSpace(vw.Query), "; \t\n")
        if rq != nil { query = rq.text(query) }
        queries = append(queries, query)
    }
    if len(keys) == 0 { return nil }
    workers := min(viewProbeWorkers, (len(keys)+viewProbeChunk-1)/viewProbeChunk)
    type result struct {
        probes map[string]*ViewProbe
        err    error
    }
    results := make(chan result, workers)
    for w := 0; w < workers; w++ {
        go func(w int) {
            var ck, cq, cs, cn []string
            for i := w; i < len(keys); i += workers {
                ck, cq, cs, cn = append(ck, keys[i]), append(cq, queries[i]), append(cs, schemas[i]), append(cn, names[i])
            }
            probes, err := probeViewChunk(ctx, pool, ck, cq, cs, cn)
            results <- result{probes, err}
        }(w)
    }
    probes := map[string]*ViewProbe{}
    var firstErr error
    for w := 0; w < workers; w++ {
        r := <-results
        if r.err != nil && firstErr == nil { firstErr = r.err }
        for k, p := range r.probes { probes[k] = p }
    }
    if firstErr != nil { return firstErr }
    live.ViewProbes = probes
    return nil
}

// viewProbeWorkers is how many connections ProbeViews splits the views
// across, and viewProbeChunk the fewest views worth a connection of their
// own; each probe creates and drops a temporary view, about a millisecond.
const (
    viewProbeWorkers = 4
    viewProbeChunk   = 50
)

// probeViewChunk probes the given views in a transaction of its own, which
// it always rolls back.
func probeViewChunk(ctx context.Context, pool *pgxpool.Pool, keys, queries, schemas, names []string) (map[string]*ViewProbe, error) {
    tx, err := pool.Begin(ctx)
    if err != nil { return nil, err }
    defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
    if _, err := tx.Exec(ctx, viewProbeFuncs); err != nil { return nil, fmt.Errorf("view probe: %w", err) }
    rows, err := tx.Query(ctx, `
        select t.k, d.def, d.cols, d.err, l.def, l.cols
        from unnest($1::text[], $2::text[], $3::text[], $4::text[]) with ordinality t(k, q, s, n, i)
        join pg_namespace ns on ns.nspname = t.s
        join pg_class c on c.relnamespace = ns.oid and c.relname = t.n
        cross join lateral pg_temp.pgy_viewdef(t.q) d
        cross join lateral pg_temp.pgy_deparse(c.oid) l
        order by t.i`, keys, queries, schemas, names)
    if err != nil { return nil, fmt.Errorf("view probe: %w", err) }
    probes := map[string]*ViewProbe{}
    for rows.Next() {
        var k string
        var def, errText *string
        var cols []string
        p := &ViewProbe{}
        if err := rows.Scan(&k, &def, &cols, &errText, &p.LiveDef, &p.LiveCols); err != nil {
            rows.Close()
            return nil, fmt.Errorf("view probe: %w", err)
        }
        if def != nil { p.DesiredDef = *def }
        if errText != nil { p.Err = *errText }
        p.DesiredCols = cols
        probes[k] = p
    }
    rows.Close()
    if err := rows.Err(); err != nil { return nil, fmt.Errorf("view probe: %w", err) }
    return probes, nil
}
