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
    // split on semicolons while keeping simple; does not handle dollar-quoted blocks
    // for a minimal tool, assume statements separated by ';' and not multiline function bodies
    tx, err := pool.Begin(ctx)
    if err != nil { return err }
    defer func() { _ = tx.Rollback(ctx) }()
    for _, stmt := range strings.Split(sql, ";") {
        if strings.TrimSpace(stmt) == "" { continue }
        if _, err := tx.Exec(ctx, stmt); err != nil { return err }
    }
    return tx.Commit(ctx)
}

func RecordApplied(ctx context.Context, pool *PoolShim, schema, table, name, checksum string, appliedAt time.Time) error {
    // version is parsed from filename prefix
    var version int
    if _, err := fmt.Sscanf(name, "%d", &version); err != nil { version = 0 }
    q := fmt.Sprintf("insert into %s.%s(version, name, checksum, applied_at) values($1,$2,$3,$4) on conflict (version) do nothing", pqIdent(schema), pqIdent(table))
    _, err := pool.Exec(ctx, q, version, name, checksum, appliedAt)
    return err
}


