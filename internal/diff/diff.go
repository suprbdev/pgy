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
    Tables map[string]*LiveTable
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
    l := &Live{Tables: map[string]*LiveTable{}}
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
    return l, rows.Err()
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
    // Extensions first
    for _, ext := range desired.Extensions {
        if ext == nil || ext.Name == "" { continue }
        stmt := "create extension "
        if ext.IfNotExists { stmt += "if not exists " }
        stmt += pqIdent(ext.Name) + ";"
        plan.Creates = append(plan.Creates, stmt)
    }
    // Types
    typeNames := make([]string, 0, len(desired.Types))
    for k := range desired.Types { typeNames = append(typeNames, k) }
    sort.Strings(typeNames)
    for _, tn := range typeNames {
        td := desired.Types[tn]
        if td.Kind == "enum" {
            labels := make([]string, 0, len(td.Labels))
            for _, l := range td.Labels { labels = append(labels, quoteString(l)) }
            plan.Creates = append(plan.Creates, fmt.Sprintf("create type %s as enum (%s);", pqIdent(tn), strings.Join(labels, ", ")))
        } else if td.Kind == "composite" {
            attrs := []string{}
            keys := make([]string, 0, len(td.Attributes))
            for k := range td.Attributes { keys = append(keys, k) }
            sort.Strings(keys)
            for _, an := range keys { attrs = append(attrs, fmt.Sprintf("%s %s", pqIdent(an), td.Attributes[an])) }
            plan.Creates = append(plan.Creates, fmt.Sprintf("create type %s as (%s);", pqIdent(tn), strings.Join(attrs, ", ")))
        }
    }
    // Functions
    fnNames := make([]string, 0, len(desired.Functions))
    for k := range desired.Functions { fnNames = append(fnNames, k) }
    sort.Strings(fnNames)
    for _, fnKey := range fnNames {
        f := desired.Functions[fnKey]
        if f == nil { continue }
        setClauses := ""
        if len(f.Set) > 0 {
            // apply SET via prefixed set clauses before body
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
        // dollar-quote body
        stmt := fmt.Sprintf("create function %s%s returns %s language %s%s as $$\n%s\n$$;", pqIdent(fnKey)+f.ArgsSig, "", f.Returns, f.Language, attrsStr+setClauses, body)
        plan.Creates = append(plan.Creates, stmt)
    }
    // Only basic create table/columns; no drops unless unsafe
    // desired tables names include optional schema prefix "public.table" if user includes it
    for _, tname := range schema.SortedTableNames(desired) {
        dt := desired.Tables[tname]
        // default to public schema if no dot
        fq := tname
        if !strings.Contains(tname, ".") {
            fq = "public." + tname
        }
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
            for _, tr := range dt.Triggers {
                if tr == nil || tr.Procedure == "" { continue }
                events := strings.ToUpper(strings.Join(tr.Events, " or "))
                stmt := fmt.Sprintf("create trigger %s %s %s on %s for each %s execute procedure %s;", pqIdent(tr.Name), strings.ToUpper(tr.Timing), events, pqIdent(fq), strings.ToLower(tr.Level), tr.Procedure)
                plan.Creates = append(plan.Creates, stmt)
            }
            continue
        }
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


