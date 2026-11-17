package diff

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "sort"
    "strconv"
    "strings"
    "time"

    "github.com/jackc/pgx/v5/pgconn"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/suprbdev/pgy/internal/schema"
)

// LiveTableData is the comparison of a table's declared data rows with its
// live rows: the declared rows (by index) that are missing, the declared
// rows whose listed columns differ (by index, with those columns), and the
// key values (as text) of live rows the YAML does not declare.
type LiveTableData struct {
    Missing []int
    Changed map[int][]string
    Extra   []map[string]string
}

// dataValue converts a decoded YAML value to the form it is sent to
// PostgreSQL in: dates and timestamps become ISO strings, lists and maps are
// converted recursively.
func dataValue(v any) any {
    switch x := v.(type) {
    case time.Time:
        if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 && x.Location() == time.UTC {
            return x.Format("2006-01-02")
        }
        return x.Format(time.RFC3339Nano)
    case []any:
        out := make([]any, len(x))
        for i, e := range x { out[i] = dataValue(e) }
        return out
    case map[string]any:
        out := make(map[string]any, len(x))
        for k, e := range x { out[k] = dataValue(e) }
        return out
    }
    return v
}

func dataRowsJSON(rows []map[string]any) ([]byte, error) {
    conv := make([]map[string]any, len(rows))
    for i, r := range rows {
        conv[i] = map[string]any{}
        for k, v := range r { conv[i][k] = dataValue(v) }
    }
    return json.Marshal(conv)
}

// dataColumn describes a column for comparing data rows: typ is its type,
// cmp the type declared values are converted to, and json whether the type
// takes a YAML map (json, jsonb, a composite type, or an array or domain of
// one).
type dataColumn struct {
    typ  string
    cmp  string
    json bool
}

// dataTypeChainQ resolves columns (name, type text, type oid, type
// modifier, false, 0) selected by its %s to their comparison types: domains
// are replaced by their base type, also as array elements, keeping the
// domain's type modifier. jsonb_to_record runs domain_check on every domain
// column, which fails for a domain whose base type has no binary input
// function (seg, the isn types) and rejects a null in a NOT NULL domain;
// the base type converts and compares the same values without it.
const dataTypeChainQ = `
    with recursive chain(name, orig, typ, mod, arr, depth) as (
        %s
      union all
        select c.name, c.orig,
               case when t.typtype = 'd' then t.typbasetype else t.typelem end,
               case when t.typtype = 'd' then t.typtypmod else c.mod end,
               c.arr or t.typtype <> 'd', c.depth + 1
        from chain c join pg_type t on t.oid = c.typ
        where t.typtype = 'd' or (not c.arr and t.typcategory = 'A' and t.typelem <> 0 and t.typlen = -1)
    )
    select distinct on (c.name) c.name, c.orig,
           case when not c.arr then format_type(c.typ, c.mod) when t.typarray <> 0 then format_type(t.typarray, c.mod) else c.orig end,
           t.typtype = 'c' or t.oid in ('json'::regtype, 'jsonb'::regtype)
    from chain c join pg_type t on t.oid = c.typ
    order by c.name, c.depth desc`

// dataLiveColumnsQ takes a table ($1) and resolves its columns.
var dataLiveColumnsQ = fmt.Sprintf(dataTypeChainQ, `
        select a.attname::text, format_type(a.atttypid, a.atttypmod), a.atttypid, a.atttypmod, false, 0
        from pg_attribute a
        where a.attrelid = $1::text::regclass and a.attnum > 0 and not a.attisdropped`)

// dataDeclaredColumnsQ takes column names ($1) and their declared types
// ($2) and resolves those whose type exists in the database.
var dataDeclaredColumnsQ = fmt.Sprintf(dataTypeChainQ, `
        select c.name, c.typ, to_regtype(c.typ)::oid, -1, false, 0
        from unnest($1::text[], $2::text[]) as c(name, typ)
        where to_regtype(c.typ) is not null`)

func dataColumnTypes(ctx context.Context, pool *pgxpool.Pool, q string, args ...any) (map[string]dataColumn, error) {
    rows, err := pool.Query(ctx, q, args...)
    if err != nil { return nil, err }
    defer rows.Close()
    out := map[string]dataColumn{}
    for rows.Next() {
        var name string
        var c dataColumn
        if err := rows.Scan(&name, &c.typ, &c.cmp, &c.json); err != nil { return nil, err }
        out[name] = c
    }
    return out, rows.Err()
}

// dataColumnDefs renders cols as a column definition list for
// jsonb_to_record, with their comparison types.
func dataColumnDefs(cols []string, types map[string]dataColumn) string {
    defs := make([]string, len(cols))
    for i, c := range cols { defs[i] = quoteIdent(c) + " " + types[c].cmp }
    return strings.Join(defs, ", ")
}

// dataMapHint names the first declared value that is a map (or holds one)
// in a column whose type takes no map, or returns "".
func dataMapHint(rows []map[string]any, types map[string]dataColumn) string {
    for i, row := range rows {
        for _, col := range sortedKeys(row) {
            if c, ok := types[col]; ok && !c.json && containsMap(row[col]) {
                return fmt.Sprintf("row %d gives column %s a map, but its type %s is not json, jsonb or a composite type; write the value as text, the way PostgreSQL prints it", i+1, col, c.typ)
            }
        }
    }
    return ""
}

func dataError(fq string, err error, rows []map[string]any, types map[string]dataColumn) error {
    if hint := dataMapHint(rows, types); hint != "" { return fmt.Errorf("data for %s: %s: %w", fq, hint, err) }
    return fmt.Errorf("data for %s: %w", fq, err)
}

func dataSameKeyError(fq string, key []string, rows []map[string]any, a, b int) error {
    return fmt.Errorf("data for %s: rows %d and %d identify the same row: their keys (%s) are equal in the key's column types", fq, a+1, b+1, dataKeyText(key, rows[a], rows[b]))
}

// dataUsedColumns returns the key columns and every column a row sets.
func dataUsedColumns(key []string, rows []map[string]any) map[string]bool {
    used := map[string]bool{}
    for _, k := range key { used[k] = true }
    for _, row := range rows {
        for col := range row { used[col] = true }
    }
    return used
}

// checkNewTableData checks the rows declared for a table the plan creates,
// as far as the database already has the declared column types: two rows
// whose keys are equal in the key types, and a map given to a column whose
// type cannot take one, are errors here instead of when the migration runs.
// Anything else is left to the migration.
func checkNewTableData(ctx context.Context, pool *pgxpool.Pool, fq string, dt *schema.Table, key []string) error {
    names, typs := []string{}, []string{}
    for _, col := range sortedKeys(dataUsedColumns(key, dt.Data.Rows)) {
        if t := dataColumnType(dt, col); t != "" { names, typs = append(names, col), append(typs, t) }
    }
    types, err := dataColumnTypes(ctx, pool, dataDeclaredColumnsQ, names, typs)
    if err != nil || len(types) == 0 { return nil }
    rows, err := dataRowsJSON(dt.Data.Rows)
    if err != nil { return fmt.Errorf("data for %s: %w", fq, err) }
    first := "d.i"
    if hasDataColumns(types, key) {
        part := make([]string, len(key))
        for i, k := range key { part[i] = "v." + quoteIdent(k) }
        first = "min(d.i) over (partition by " + strings.Join(part, ", ") + ")"
    }
    q := `
        with d as (select e.j, e.i - 1 as i from jsonb_array_elements($1::jsonb) with ordinality e(j, i)),
             r as (select d.i, ` + first + ` as first
                   from d cross join lateral jsonb_to_record(d.j) as v(` + dataColumnDefs(sortedKeys(types), types) + `))
        select r.first, r.i from r where r.first <> r.i order by r.i limit 1`
    var a, b int
    switch err := pool.QueryRow(ctx, q, string(rows)).Scan(&a, &b); {
    case err == nil:
        return dataSameKeyError(fq, key, dt.Data.Rows, a, b)
    case dataMapHint(dt.Data.Rows, types) != "":
        var pgErr *pgconn.PgError
        if errors.As(err, &pgErr) { return dataError(fq, err, dt.Data.Rows, types) }
    }
    return nil
}

// IntrospectData compares the data rows declared in desired with the rows
// of the live tables they belong to and records the result in
// l.TableData. PostgreSQL does the comparison: each declared row is turned
// into a record of the columns it uses with jsonb_to_record, so values are
// compared as the column types see them (10 matches a numeric 10.00, enum
// labels, arrays and jsonb compare by value). Only the columns a row lists
// are compared. Rows are matched on the key with the key types' equality;
// a key that matches but is written differently (a citext key in another
// case) is reported as changed, and two declared rows with equal keys are
// an error. Tables whose key columns do not exist yet (they are renamed or
// added by the plan) are skipped and get upserts, as are tables whose rows
// use enum labels the plan has yet to add. The rows of tables that do not
// exist yet are only checked (see checkNewTableData).
func IntrospectData(ctx context.Context, pool *pgxpool.Pool, l *Live, desired *schema.Database) error {
    if l == nil || desired == nil { return nil }
    if l.TableData == nil { l.TableData = map[string]*LiveTableData{} }
    newLabels := addsEnumLabels(l, desired)
    for _, fq := range sortedKeys(desired.Tables) {
        dt := desired.Tables[fq]
        lt := l.Tables[fq]
        if dt == nil || dt.Data == nil { continue }
        key := dt.DataKey()
        if len(key) == 0 { continue }
        if lt == nil {
            if err := checkNewTableData(ctx, pool, fq, dt, key); err != nil { return err }
            continue
        }
        if !hasColumns(lt, key) { continue }
        rows, err := dataRowsJSON(dt.Data.Rows)
        if err != nil { return fmt.Errorf("data for %s: %w", fq, err) }
        types, err := dataColumnTypes(ctx, pool, dataLiveColumnsQ, pqIdent(fq))
        if err != nil { return fmt.Errorf("data for %s: %w", fq, err) }
        if !hasDataColumns(types, key) { continue }
        used := dataUsedColumns(key, dt.Data.Rows)
        for col := range used {
            if _, ok := types[col]; !ok { delete(used, col) }
        }
        from := dataTarget(fq, lt.Partitioned || dt.PartitionBy != nil)
        join := make([]string, len(key))
        liveKey := make([]string, len(key))
        declKey := make([]string, len(key))
        textKey := make([]string, len(key))
        for i, k := range key {
            q := quoteIdent(k)
            join[i] = fmt.Sprintf("pgy_row.%s = (r.v).%s", q, q)
            liveKey[i] = "pgy_row." + q
            declKey[i] = "v." + q
            textKey[i] = fmt.Sprintf("%s, pgy_row.%s::text", quoteString(k), q)
        }
        diffQ := `
            with d as (select e.j, e.i - 1 as i from jsonb_array_elements($1::jsonb) with ordinality e(j, i)),
                 r as (select d.i, d.j, v, min(d.i) over (partition by ` + strings.Join(declKey, ", ") + `) as first
                       from d cross join lateral jsonb_to_record(d.j) as v(` + dataColumnDefs(sortedKeys(used), types) + `))
            select r.i, pgy_row.` + quoteIdent(key[0]) + ` is null, r.first,
                   coalesce((select array_agg(k order by k) from jsonb_object_keys(r.j) k where to_jsonb(pgy_row) -> k is distinct from to_jsonb(r.v) -> k), '{}')
            from r left join ` + from + ` pgy_row on ` + strings.Join(join, " and ") + `
            where pgy_row.` + quoteIdent(key[0]) + ` is null or r.first <> r.i
               or exists (select 1 from jsonb_object_keys(r.j) k where to_jsonb(pgy_row) -> k is distinct from to_jsonb(r.v) -> k)`
        ld := &LiveTableData{Changed: map[int][]string{}}
        missing := map[int]bool{}
        qr, err := pool.Query(ctx, diffQ, string(rows))
        if err != nil {
            if newLabels && isInvalidInput(err) { continue }
            return dataError(fq, err, dt.Data.Rows, types)
        }
        skip := false
        for qr.Next() {
            var i, first int
            var isMissing bool
            var cols []string
            if err := qr.Scan(&i, &isMissing, &first, &cols); err != nil {
                qr.Close()
                return fmt.Errorf("data for %s: %w", fq, err)
            }
            if first != i {
                qr.Close()
                return dataSameKeyError(fq, key, dt.Data.Rows, first, i)
            }
            if isMissing {
                ld.Missing = append(ld.Missing, i)
                missing[i] = true
            } else if len(cols) > 0 {
                ld.Changed[i] = cols
            }
        }
        qr.Close()
        if err := qr.Err(); err != nil {
            if !newLabels || !isInvalidInput(err) { return dataError(fq, err, dt.Data.Rows, types) }
            skip = true
        }
        if skip { continue }
        for i, row := range dt.Data.Rows {
            if missing[i] { continue }
            for col := range row {
                if lt.Columns[col] == nil && indexOf(ld.Changed[i], col) < 0 { ld.Changed[i] = append(ld.Changed[i], col) }
            }
            sort.Strings(ld.Changed[i])
            if len(ld.Changed[i]) == 0 { delete(ld.Changed, i) }
        }
        extraQ := `
            select jsonb_build_object(` + strings.Join(textKey, ", ") + `)
            from ` + from + ` pgy_row
            where not exists (select 1 from jsonb_array_elements($1::jsonb) e(j)
                              cross join lateral jsonb_to_record(e.j) as v(` + dataColumnDefs(key, types) + `)
                              where (` + strings.Join(liveKey, ", ") + `) = (` + strings.Join(declKey, ", ") + `))
            order by ` + strings.Join(liveKey, ", ")
        er, err := pool.Query(ctx, extraQ, string(rows))
        if err != nil { return dataError(fq, err, dt.Data.Rows, types) }
        for er.Next() {
            var keys map[string]string
            if err := er.Scan(&keys); err != nil {
                er.Close()
                return fmt.Errorf("data for %s: %w", fq, err)
            }
            ld.Extra = append(ld.Extra, keys)
        }
        er.Close()
        if err := er.Err(); err != nil { return fmt.Errorf("data for %s: %w", fq, err) }
        sort.Ints(ld.Missing)
        l.TableData[fq] = ld
    }
    return nil
}

// dataTarget names table fq for reading or changing its own rows: ONLY for
// a regular table, so the rows of tables inheriting from it are left out;
// a partitioned table as is, since its rows live in its partitions.
func dataTarget(fq string, partitioned bool) string {
    if partitioned { return pqIdent(fq) }
    return "only " + pqIdent(fq)
}

func hasColumns(lt *LiveTable, cols []string) bool {
    for _, c := range cols {
        if lt.Columns[c] == nil { return false }
    }
    return true
}

func hasDataColumns(types map[string]dataColumn, cols []string) bool {
    for _, c := range cols {
        if _, ok := types[c]; !ok { return false }
    }
    return true
}

// dataKeyText renders the key values of two declared rows side by side
// ("code: free / FREE").
func dataKeyText(key []string, a, b map[string]any) string {
    parts := make([]string, len(key))
    for i, k := range key { parts[i] = fmt.Sprintf("%s: %v / %v", k, dataValue(a[k]), dataValue(b[k])) }
    return strings.Join(parts, ", ")
}

// sqlValue renders a decoded YAML value as a SQL literal that PostgreSQL
// coerces to the column's type: strings, dates, lists (as array literals)
// and maps (as JSON) are quoted, numbers and booleans are bare.
func sqlValue(v any) string {
    switch x := dataValue(v).(type) {
    case nil:
        return "null"
    case bool:
        if x { return "true" }
        return "false"
    case int:
        return strconv.Itoa(x)
    case int64:
        return strconv.FormatInt(x, 10)
    case uint64:
        return strconv.FormatUint(x, 10)
    case float64:
        return strconv.FormatFloat(x, 'g', -1, 64)
    case string:
        return quoteString(x)
    case []any:
        return quoteString(arrayLiteral(x))
    case map[string]any:
        b, _ := json.Marshal(x)
        return quoteString(string(b))
    }
    return quoteString(fmt.Sprint(v))
}

// arrayLiteral renders a list as a PostgreSQL array literal ({"a","b"}).
func arrayLiteral(list []any) string {
    parts := make([]string, len(list))
    for i, e := range list {
        switch x := e.(type) {
        case nil:
            parts[i] = "NULL"
        case []any:
            parts[i] = arrayLiteral(x)
        case bool, int, int64, uint64, float64:
            parts[i] = strings.Trim(sqlValue(x), "'")
        default:
            s := fmt.Sprint(dataValue(x))
            if m, ok := x.(map[string]any); ok {
                b, _ := json.Marshal(m)
                s = string(b)
            }
            parts[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
        }
    }
    return "{" + strings.Join(parts, ",") + "}"
}

// dataSQL renders column col's value for table fq. Every value of a json or
// jsonb column is a JSON document (a list or string is not an array or text
// literal) and is written as a jsonb literal, cast on to json where the
// column is json. Other values containing a map (composite types and their
// arrays) are converted by jsonb_to_record to the column's declared type,
// the conversion the comparison uses; a column the YAML gives no type
// (an inherited or partition column) is converted through the table's row
// type instead. Anything else is a plain literal.
func dataSQL(fq string, dt *schema.Table, col string, v any) string {
    typ := dataColumnType(dt, col)
    if kind := jsonTypeKind(typ); kind != "" {
        if v == nil { return "null" }
        b, _ := json.Marshal(dataValue(v))
        if kind == "json" { return quoteString(string(b)) + "::jsonb::json" }
        return quoteString(string(b)) + "::jsonb"
    }
    if !containsMap(v) { return sqlValue(v) }
    if typ != "" {
        b, _ := json.Marshal(map[string]any{"v": dataValue(v)})
        return fmt.Sprintf("(select v from jsonb_to_record(%s::jsonb) as x(v %s))", quoteString(string(b)), typ)
    }
    b, _ := json.Marshal(map[string]any{col: dataValue(v)})
    tbl := pqIdent(fq)
    // The base record is not null, so jsonb_populate_record skips the columns
    // the JSON lacks; with null::tbl it runs each of them through domain_check.
    return fmt.Sprintf("(jsonb_populate_record((select row(x.*)::%s from (select (null::%s).*) x), %s::jsonb)).%s", tbl, tbl, quoteString(string(b)), quoteIdent(col))
}

// dataColumnType returns the type dt declares for col, or "".
func dataColumnType(dt *schema.Table, col string) string {
    if dt == nil || dt.Columns[col] == nil { return "" }
    return strings.TrimSpace(dt.Columns[col].Type)
}

// jsonTypeKind returns "json" or "jsonb" when typ names that type, else "".
func jsonTypeKind(typ string) string {
    switch t := strings.TrimPrefix(strings.ToLower(typ), "pg_catalog."); t {
    case "json", "jsonb":
        return t
    }
    return ""
}

func containsMap(v any) bool {
    switch x := v.(type) {
    case map[string]any:
        return true
    case []any:
        for _, e := range x {
            if containsMap(e) { return true }
        }
    }
    return false
}

// dataColumns returns the columns a row lists, in the table's column order.
func dataColumns(dt *schema.Table, row map[string]any) []string {
    out := []string{}
    seen := map[string]bool{}
    for _, cn := range dt.ColumnOrder {
        if _, ok := row[cn]; ok {
            out = append(out, cn)
            seen[cn] = true
        }
    }
    rest := []string{}
    for cn := range row {
        if !seen[cn] { rest = append(rest, cn) }
    }
    sort.Strings(rest)
    return append(out, rest...)
}

func dataInsert(fq string, dt *schema.Table, row map[string]any) string {
    cols := dataColumns(dt, row)
    names, vals := make([]string, len(cols)), make([]string, len(cols))
    overriding := ""
    for i, cn := range cols {
        names[i], vals[i] = pqIdent(cn), dataSQL(fq, dt, cn, row[cn])
        if c := dt.Columns[cn]; c != nil && c.Identity == "always" { overriding = " overriding system value" }
    }
    return fmt.Sprintf("insert into %s (%s)%s values (%s);", pqIdent(fq), strings.Join(names, ", "), overriding, strings.Join(vals, ", "))
}

func dataKeyWhere(key []string, value func(string) string) string {
    conds := make([]string, len(key))
    for i, k := range key { conds[i] = fmt.Sprintf("%s = %s", pqIdent(k), value(k)) }
    return strings.Join(conds, " and ")
}

// dataTableOrder returns the tables of desired that declare data, ordered so
// that a table comes after the data tables its foreign keys reference.
func dataTableOrder(desired *schema.Database) []string {
    out := []string{}
    state := map[string]int{}
    var visit func(fq string)
    visit = func(fq string) {
        dt := desired.Tables[fq]
        if dt == nil || dt.Data == nil || state[fq] != 0 { return }
        state[fq] = 1
        for _, fk := range dt.ForeignKeys {
            if fk != nil { visit(qualifyName(fk.RefTable)) }
        }
        state[fq] = 2
        out = append(out, fq)
    }
    for _, fq := range sortedKeys(desired.Tables) { visit(fq) }
    return out
}

// planData returns the statements that bring the data rows declared in
// desired into the live tables: INSERT for missing rows and UPDATE of the
// differing columns for changed ones, in foreign key order, and, when
// prune is set, DELETE (in reverse order) for live rows the YAML does not
// declare. A changed key column is one whose live value equals the declared
// one in the column's type but is written differently, and is rewritten
// like any other column. A table created by this plan gets all of its rows
// inserted. An existing table without introspected data (IntrospectData was
// not run) gets an upsert of every row.
func planData(live *Live, desired *schema.Database, prune bool) (writes, deletes []string) {
    order := dataTableOrder(desired)
    for _, fq := range order {
        dt := desired.Tables[fq]
        key := dt.DataKey()
        if len(key) == 0 { continue }
        ld := live.TableData[fq]
        switch {
        case live.Tables[fq] == nil:
            for _, row := range dt.Data.Rows { writes = append(writes, dataInsert(fq, dt, row)) }
        case ld == nil:
            for _, row := range dt.Data.Rows {
                set := []string{}
                for _, cn := range dataColumns(dt, row) {
                    if indexOf(key, cn) < 0 { set = append(set, fmt.Sprintf("%s = excluded.%s", pqIdent(cn), pqIdent(cn))) }
                }
                stmt := strings.TrimSuffix(dataInsert(fq, dt, row), ";") + " on conflict (" + joinIdentList(key) + ") do "
                if len(set) == 0 { stmt += "nothing;" } else { stmt += "update set " + strings.Join(set, ", ") + ";" }
                writes = append(writes, stmt)
            }
        default:
            for _, i := range ld.Missing {
                if i < len(dt.Data.Rows) { writes = append(writes, dataInsert(fq, dt, dt.Data.Rows[i])) }
            }
            idx := make([]int, 0, len(ld.Changed))
            for i := range ld.Changed { idx = append(idx, i) }
            sort.Ints(idx)
            for _, i := range idx {
                if i >= len(dt.Data.Rows) { continue }
                row := dt.Data.Rows[i]
                set := []string{}
                for _, cn := range ld.Changed[i] { set = append(set, fmt.Sprintf("%s = %s", pqIdent(cn), dataSQL(fq, dt, cn, row[cn]))) }
                if len(set) == 0 { continue }
                writes = append(writes, fmt.Sprintf("update %s set %s where %s;", dataTarget(fq, partitioned(live, dt, fq)), strings.Join(set, ", "), dataKeyWhere(key, func(k string) string { return dataSQL(fq, dt, k, row[k]) })))
            }
        }
    }
    if prune {
        for i := len(order) - 1; i >= 0; i-- {
            fq := order[i]
            ld := live.TableData[fq]
            if ld == nil { continue }
            key := desired.Tables[fq].DataKey()
            for _, ex := range ld.Extra {
                deletes = append(deletes, fmt.Sprintf("delete from %s where %s;", dataTarget(fq, partitioned(live, desired.Tables[fq], fq)), dataKeyWhere(key, func(k string) string { return quoteString(ex[k]) })))
            }
        }
    }
    return writes, deletes
}

func partitioned(live *Live, dt *schema.Table, fq string) bool {
    if dt != nil && dt.PartitionBy != nil { return true }
    lt := live.Tables[fq]
    return lt != nil && lt.Partitioned
}

// dataTextColumnsQ lists the columns of a table whose to_jsonb form does not
// load back into the column: to_jsonb renders a type that is not built in
// and has a cast to json (hstore) with that cast, wherever the type occurs
// (as the column's type, under a domain, as an array element or inside a
// composite type). The second result column is true when the column is an
// array of such a type, whose elements can be read as text one by one.
const dataTextColumnsQ = `
    with recursive reach(attnum, typ, kind) as (
        select a.attnum, a.atttypid, 0
        from pg_attribute a
        where a.attrelid = $1::text::regclass and a.attnum > 0 and not a.attisdropped
      union
        select r.attnum, n.typ, n.kind
        from reach r
        join pg_type t on t.oid = r.typ
        cross join lateral (
            select t.typbasetype as typ, r.kind as kind where t.typtype = 'd'
            union all
            select t.typelem, case when r.kind = 0 then 1 else 2 end
            where t.typtype <> 'd' and t.typcategory = 'A' and t.typelem <> 0 and t.typlen = -1
            union all
            select ca.atttypid, 2 from pg_attribute ca
            where t.typtype = 'c' and ca.attrelid = t.typrelid and ca.attnum > 0 and not ca.attisdropped
        ) n
    )
    select a.attname, min(r.kind) = 1 and max(r.kind) = 1
    from reach r
    join pg_attribute a on a.attrelid = $1::text::regclass and a.attnum = r.attnum
    join pg_type t on t.oid = r.typ
    where t.oid >= 16384 and t.typtype not in ('c', 'd')
      and not (t.typcategory = 'A' and t.typelem <> 0 and t.typlen = -1)
      and exists (select 1 from pg_cast c where c.castsource = t.oid and c.casttarget = 'json'::regtype and c.castmethod = 'f')
    group by a.attnum, a.attname
    order by a.attnum`

// TableRows returns the rows of table fq (without those of tables that
// inherit from it) as column -> value maps decoded from JSON (numbers as
// json.Number), for pgy pull --data. partitioned reads through a
// partitioned table into its partitions. A column whose JSON form would not
// load back (see dataTextColumnsQ) is returned in its text form instead, or
// as a list of its elements' text forms for an array.
func TableRows(ctx context.Context, pool *pgxpool.Pool, fq string, partitioned bool) ([]map[string]any, error) {
    tr, err := pool.Query(ctx, dataTextColumnsQ, pqIdent(fq))
    if err != nil { return nil, fmt.Errorf("data for %s: %w", fq, err) }
    text := []string{}
    for tr.Next() {
        var name string
        var elements bool
        if err := tr.Scan(&name, &elements); err != nil {
            tr.Close()
            return nil, fmt.Errorf("data for %s: %w", fq, err)
        }
        cast := "::text"
        if elements { cast = "::text[]" }
        text = append(text, quoteString(name)+", pgy_row."+quoteIdent(name)+cast)
    }
    tr.Close()
    if err := tr.Err(); err != nil { return nil, fmt.Errorf("data for %s: %w", fq, err) }
    row := "to_jsonb(pgy_row)"
    if len(text) > 0 { row = "(to_jsonb(pgy_row) || jsonb_build_object(" + strings.Join(text, ", ") + "))" }
    rows, err := pool.Query(ctx, "select "+row+"::text from "+dataTarget(fq, partitioned)+" pgy_row")
    if err != nil { return nil, fmt.Errorf("data for %s: %w", fq, err) }
    defer rows.Close()
    out := []map[string]any{}
    for rows.Next() {
        var raw string
        if err := rows.Scan(&raw); err != nil { return nil, err }
        dec := json.NewDecoder(strings.NewReader(raw))
        dec.UseNumber()
        var m map[string]any
        if err := dec.Decode(&m); err != nil { return nil, err }
        out = append(out, m)
    }
    return out, rows.Err()
}

// addsEnumLabels reports whether desired declares enum labels that live
// lacks, which the plan adds in its prelude.
func addsEnumLabels(l *Live, desired *schema.Database) bool {
    for k, td := range desired.Types {
        if td == nil || td.Kind != "enum" || !l.Types[k] { continue }
        have := map[string]bool{}
        for _, lbl := range l.EnumLabels[k] { have[lbl] = true }
        for _, lbl := range td.Labels {
            if !have[lbl] { return true }
        }
    }
    return false
}

// isInvalidInput reports whether err is PostgreSQL's invalid input syntax
// error (22P02), raised for example by an unknown enum label.
func isInvalidInput(err error) bool {
    var pgErr *pgconn.PgError
    return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}
