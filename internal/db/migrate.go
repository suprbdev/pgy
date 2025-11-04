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
    
    statements := splitSQLStatements(sql)
    for _, stmt := range statements {
        if strings.TrimSpace(stmt) == "" { continue }
        if _, err := tx.Exec(ctx, stmt); err != nil { return err }
    }
    return tx.Commit(ctx)
}

// splitSQLStatements splits SQL on semicolons while respecting dollar-quoted strings
func splitSQLStatements(sql string) []string {
    var statements []string
    var current strings.Builder
    inDollarQuote := false
    dollarTag := ""
    i := 0
    
    for i < len(sql) {
        if !inDollarQuote {
            // Look for dollar-quote start: $tag$ or $$
            if sql[i] == '$' {
                // Find the matching closing $
                tagEnd := i + 1
                // Handle $$ (empty tag)
                if tagEnd < len(sql) && sql[tagEnd] == '$' {
                    dollarTag = "$$"
                    inDollarQuote = true
                    current.WriteString(dollarTag)
                    i = tagEnd + 1
                    continue
                }
                // Handle $tag$ (non-empty tag)
                for tagEnd < len(sql) && sql[tagEnd] != '$' {
                    tagEnd++
                }
                if tagEnd < len(sql) {
                    dollarTag = sql[i : tagEnd+1] // includes both $ chars
                    inDollarQuote = true
                    current.WriteString(dollarTag)
                    i = tagEnd + 1
                    continue
                }
            }
            // Check for statement terminator
            if sql[i] == ';' {
                stmt := strings.TrimSpace(current.String())
                if stmt != "" {
                    statements = append(statements, stmt)
                }
                current.Reset()
                i++
                continue
            }
            current.WriteByte(sql[i])
            i++
        } else {
            // Inside dollar-quoted string - look for closing tag
            if i+len(dollarTag)-1 < len(sql) {
                if sql[i:i+len(dollarTag)] == dollarTag {
                    // Found closing tag
                    current.WriteString(dollarTag)
                    i += len(dollarTag)
                    inDollarQuote = false
                    dollarTag = ""
                } else {
                    current.WriteByte(sql[i])
                    i++
                }
            } else {
                // End of input while in quote - treat rest as part of quote
                current.WriteByte(sql[i])
                i++
            }
        }
    }
    
    // Add final statement if any
    stmt := strings.TrimSpace(current.String())
    if stmt != "" {
        statements = append(statements, stmt)
    }
    
    return statements
}

func RecordApplied(ctx context.Context, pool *PoolShim, schema, table, name, checksum string, appliedAt time.Time) error {
    // version is parsed from filename prefix
    var version int
    if _, err := fmt.Sscanf(name, "%d", &version); err != nil { version = 0 }
    q := fmt.Sprintf("insert into %s.%s(version, name, checksum, applied_at) values($1,$2,$3,$4) on conflict (version) do nothing", pqIdent(schema), pqIdent(table))
    _, err := pool.Exec(ctx, q, version, name, checksum, appliedAt)
    return err
}


