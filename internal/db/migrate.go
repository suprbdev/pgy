package db

import (
    "context"
    "fmt"
    "strings"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
)

// PoolShim exposes limited methods to decouple CLI from pgx import details
type PoolShim = pgxpool.Pool

func LoadApplied(ctx context.Context, pool *PoolShim, schema, table string) (map[string]string, error) {
    q := fmt.Sprintf("select name, checksum from %s.%s order by version", pqIdent(schema), pqIdent(table))
    rows, err := pool.Query(ctx, q)
    if err != nil { return nil, err }
    defer rows.Close()
    out := map[string]string{}
    for rows.Next() {
        var name, checksum string
        if err := rows.Scan(&name, &checksum); err != nil { return nil, err }
        out[name] = checksum
    }
    return out, rows.Err()
}

func ApplyInTx(ctx context.Context, pool *PoolShim, sql string) error {
    tx, err := pool.Begin(ctx)
    if err != nil { return err }
    defer func() { _ = tx.Rollback(ctx) }()
    
    statements := SplitSQLStatements(sql)
    for _, stmt := range statements {
        if strings.TrimSpace(stmt) == "" { continue }
        if _, err := tx.Exec(ctx, stmt); err != nil { return err }
    }
    return tx.Commit(ctx)
}

// SplitSQLStatements splits SQL on semicolons while respecting single-quoted
// strings ('' escape), double-quoted identifiers ("" escape), dollar-quoted
// strings ($$ or $tag$), line comments (--) and nested block comments (/* */).
// E'...' backslash-escape strings are not supported.
func SplitSQLStatements(sql string) []string {
    var statements []string
    var current strings.Builder
    n := len(sql)
    i := 0

    flush := func() {
        stmt := strings.TrimSpace(current.String())
        if stmt != "" {
            statements = append(statements, stmt)
        }
        current.Reset()
    }
    // consumeQuoted copies a quote-delimited region where a doubled delimiter
    // is an escape (covers both '...''...' and "..."".."").
    consumeQuoted := func(q byte) {
        current.WriteByte(sql[i]) // opening delimiter
        i++
        for i < n {
            current.WriteByte(sql[i])
            if sql[i] == q {
                if i+1 < n && sql[i+1] == q {
                    current.WriteByte(sql[i+1])
                    i += 2
                    continue
                }
                i++
                return
            }
            i++
        }
    }

    for i < n {
        c := sql[i]
        switch {
        case c == '\'':
            consumeQuoted('\'')
        case c == '"':
            consumeQuoted('"')
        case c == '-' && i+1 < n && sql[i+1] == '-':
            for i < n && sql[i] != '\n' {
                current.WriteByte(sql[i])
                i++
            }
        case c == '/' && i+1 < n && sql[i+1] == '*':
            depth := 0
            for i < n {
                if i+1 < n && sql[i] == '/' && sql[i+1] == '*' {
                    depth++
                    current.WriteString("/*")
                    i += 2
                } else if i+1 < n && sql[i] == '*' && sql[i+1] == '/' {
                    depth--
                    current.WriteString("*/")
                    i += 2
                    if depth == 0 { break }
                } else {
                    current.WriteByte(sql[i])
                    i++
                }
            }
        case c == '$':
            // dollar quote only if $tag$ with a valid tag (identifier chars,
            // not starting with a digit) — avoids matching positional params ($1)
            j := i + 1
            for j < n && isDollarTagChar(sql[j]) { j++ }
            validTag := j < n && sql[j] == '$' && (j == i+1 || !isDigit(sql[i+1]))
            if !validTag {
                current.WriteByte(c)
                i++
                continue
            }
            tag := sql[i : j+1]
            current.WriteString(tag)
            i = j + 1
            for i < n {
                if i+len(tag) <= n && sql[i:i+len(tag)] == tag {
                    current.WriteString(tag)
                    i += len(tag)
                    break
                }
                current.WriteByte(sql[i])
                i++
            }
        case c == ';':
            flush()
            i++
        default:
            current.WriteByte(c)
            i++
        }
    }
    flush()
    return statements
}

func isDollarTagChar(c byte) bool {
    return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func RecordApplied(ctx context.Context, pool *PoolShim, schema, table, name, checksum string, appliedAt time.Time) error {
    // version is parsed from filename prefix
    var version int
    if _, err := fmt.Sscanf(name, "%d", &version); err != nil { version = 0 }
    q := fmt.Sprintf("insert into %s.%s(version, name, checksum, applied_at) values($1,$2,$3,$4) on conflict (version) do nothing", pqIdent(schema), pqIdent(table))
    _, err := pool.Exec(ctx, q, version, name, checksum, appliedAt)
    return err
}


