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

// IntrospectData compares the data rows declared in desired with the rows
// of the live tables they belong to and records the result in
// l.TableData. PostgreSQL does the comparison: each declared row is turned
// into a record of the table's type with jsonb_populate_record, so values
// are compared as the column types see them (10 matches a numeric 10.00,
// enum labels, arrays and jsonb compare by value). Only the columns a row
// lists are compared. Tables that do not exist yet, or whose key columns do
// not (they are renamed or added by the plan), are skipped and get upserts,
// as are tables whose rows use enum labels the plan has yet to add.
func IntrospectData(ctx context.Context, pool *pgxpool.Pool, l *Live, desired *schema.Database) error {
    if l == nil || desired == nil { return nil }
    if l.TableData == nil { l.TableData = map[string]*LiveTableData{} }
    newLabels := addsEnumLabels(l, desired)
    for _, fq := range sortedKeys(desired.Tables) {
        dt := desired.Tables[fq]
        lt := l.Tables[fq]
        if dt == nil || dt.Data == nil || lt == nil { continue }
        key := dt.DataKey()
        if len(key) == 0 || !hasColumns(lt, key) { continue }
        rows, err := dataRowsJSON(dt.Data.Rows)
        if err != nil { return fmt.Errorf("data for %s: %w", fq, err) }
        tbl := pqIdent(fq)
        from := dataTarget(fq, lt.Partitioned || dt.PartitionBy != nil)
        join := make([]string, len(key))
        liveKey := make([]string, len(key))
        declKey := make([]string, len(key))
        textKey := make([]string, len(key))
        for i, k := range key {
            q := pqIdent(k)
            join[i] = fmt.Sprintf("t.%s = (r.v).%s", q, q)
            liveKey[i] = "t." + q
            declKey[i] = "(jsonb_populate_record(null::" + tbl + ", e.j))." + q
            textKey[i] = fmt.Sprintf("%s, t.%s::text", quoteString(k), q)
        }
        diffQ := `
            with d as (select e.j, e.i - 1 as i from jsonb_array_elements($1::jsonb) with ordinality e(j, i)),
                 r as (select d.i, d.j, jsonb_populate_record(null::` + tbl + `, d.j) as v from d)
            select r.i, t.` + pqIdent(key[0]) + ` is null,
                   coalesce((select array_agg(k order by k) from jsonb_object_keys(r.j) k where to_jsonb(t) -> k is distinct from to_jsonb(r.v) -> k), '{}')
            from r left join ` + from + ` t on ` + strings.Join(join, " and ") + `
            where t.` + pqIdent(key[0]) + ` is null
               or exists (select 1 from jsonb_object_keys(r.j) k where to_jsonb(t) -> k is distinct from to_jsonb(r.v) -> k)`
        ld := &LiveTableData{Changed: map[int][]string{}}
        missing := map[int]bool{}
        qr, err := pool.Query(ctx, diffQ, string(rows))
        if err != nil {
            if newLabels && isInvalidInput(err) { continue }
            return fmt.Errorf("data for %s: %w", fq, err)
        }
        skip := false
        for qr.Next() {
            var i int
            var isMissing bool
            var cols []string
            if err := qr.Scan(&i, &isMissing, &cols); err != nil {
                qr.Close()
                return fmt.Errorf("data for %s: %w", fq, err)
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
            if !newLabels || !isInvalidInput(err) { return fmt.Errorf("data for %s: %w", fq, err) }
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
            from ` + from + ` t
            where not exists (select 1 from jsonb_array_elements($1::jsonb) e(j)
                              where (` + strings.Join(liveKey, ", ") + `) = (` + strings.Join(declKey, ", ") + `))
            order by ` + strings.Join(liveKey, ", ")
        er, err := pool.Query(ctx, extraQ, string(rows))
        if err != nil { return fmt.Errorf("data for %s: %w", fq, err) }
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

// dataSQL renders column col's value for table fq. Values containing a map
// (composite types, json and jsonb objects) go through jsonb_populate_record,
// the same coercion the comparison uses, so a map becomes a record for a
// composite column and an object for a json column; anything else is a
// plain literal.
func dataSQL(fq, col string, v any) string {
    if !containsMap(v) { return sqlValue(v) }
    b, _ := json.Marshal(map[string]any{col: dataValue(v)})
    return fmt.Sprintf("(jsonb_populate_record(null::%s, %s::jsonb)).%s", pqIdent(fq), quoteString(string(b)), pqIdent(col))
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
        names[i], vals[i] = pqIdent(cn), dataSQL(fq, cn, row[cn])
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
// declare. A table created by this plan gets all of its rows inserted. An
// existing table without introspected data (IntrospectData was not run)
// gets an upsert of every row.
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
                for _, cn := range ld.Changed[i] {
                    if indexOf(key, cn) < 0 { set = append(set, fmt.Sprintf("%s = %s", pqIdent(cn), dataSQL(fq, cn, row[cn]))) }
                }
                if len(set) == 0 { continue }
                writes = append(writes, fmt.Sprintf("update %s set %s where %s;", dataTarget(fq, partitioned(live, dt, fq)), strings.Join(set, ", "), dataKeyWhere(key, func(k string) string { return dataSQL(fq, k, row[k]) })))
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

// TableRows returns the rows of table fq (without those of tables that
// inherit from it) as column -> value maps decoded from JSON (numbers as
// json.Number), for pgy pull --data. partitioned reads through a
// partitioned table into its partitions.
func TableRows(ctx context.Context, pool *pgxpool.Pool, fq string, partitioned bool) ([]map[string]any, error) {
    rows, err := pool.Query(ctx, "select to_jsonb(t)::text from "+dataTarget(fq, partitioned)+" t")
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
