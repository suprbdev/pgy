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
    return ApplyMigration(ctx, pool, Migration{SQL: sql})
}

// Migration is one migration file to apply with ApplyMigration.
type Migration struct {
    SQL              string
    Name             string // file name; recorded when RecordIn is set
    Checksum         string
    LockTimeout      string // transaction-local lock_timeout, e.g. "5s"; empty = server default
    StatementTimeout string // transaction-local statement_timeout; empty = server default
    RecordIn         string // "schema.table" migrations table to record the migration in; empty = not recorded
    NoTransaction    bool   // run statements one by one outside a transaction (see NoTransactionDirective)
}

// NoTransactionDirective, as a comment line before a migration's first
// statement, makes migrate run it without a transaction, as statements such
// as CREATE INDEX CONCURRENTLY require.
const NoTransactionDirective = "-- pgy:no-transaction"

// HasNoTransactionDirective reports whether the comment lines leading a
// migration contain NoTransactionDirective.
func HasNoTransactionDirective(sql string) bool {
    for _, ln := range strings.Split(sql, "\n") {
        t := strings.TrimSpace(ln)
        if t == NoTransactionDirective { return true }
        if t != "" && !strings.HasPrefix(t, "--") { return false }
    }
    return false
}

// ApplyMigration runs m.SQL statement by statement in a single transaction.
// Timeouts are set transaction-locally before the first statement, and when
// RecordIn is set the migration is recorded in the same transaction, so it
// can never be applied without being recorded (or vice versa). Recording
// fails if the version parsed from Name is already taken.
func ApplyMigration(ctx context.Context, pool *PoolShim, m Migration) error {
    if m.NoTransaction { return applyWithoutTransaction(ctx, pool, m) }
    tx, err := pool.Begin(ctx)
    if err != nil { return err }
    defer func() { _ = tx.Rollback(ctx) }()

    for _, setting := range [][2]string{{"lock_timeout", m.LockTimeout}, {"statement_timeout", m.StatementTimeout}} {
        if setting[1] == "" { continue }
        if _, err := tx.Exec(ctx, "select set_config($1, $2, true)", setting[0], setting[1]); err != nil {
            return fmt.Errorf("set %s: %w", setting[0], err)
        }
    }
    for _, stmt := range SplitSQLStatements(m.SQL) {
        if strings.TrimSpace(stmt) == "" { continue }
        if _, err := tx.Exec(ctx, stmt); err != nil { return err }
    }
    if m.RecordIn != "" {
        schema, table, _ := strings.Cut(m.RecordIn, ".")
        q := fmt.Sprintf("insert into %s.%s(version, name, checksum, applied_at) values($1, $2, $3, now())", pqIdent(schema), pqIdent(table))
        if _, err := tx.Exec(ctx, q, MigrationVersion(m.Name), m.Name, m.Checksum); err != nil {
            return fmt.Errorf("record %s: %w", m.Name, err)
        }
    }
    return tx.Commit(ctx)
}

// applyWithoutTransaction runs a no-transaction migration statement by
// statement on one connection (timeouts set for the session and reset
// afterwards) and records it only once every statement succeeded. A failure
// leaves the earlier statements applied.
func applyWithoutTransaction(ctx context.Context, pool *PoolShim, m Migration) error {
    conn, err := pool.Acquire(ctx)
    if err != nil { return err }
    defer conn.Release()
    for _, setting := range [][2]string{{"lock_timeout", m.LockTimeout}, {"statement_timeout", m.StatementTimeout}} {
        if setting[1] == "" { continue }
        if _, err := conn.Exec(ctx, "select set_config($1, $2, false)", setting[0], setting[1]); err != nil {
            return fmt.Errorf("set %s: %w", setting[0], err)
        }
        defer func(name string) { _, _ = conn.Exec(context.WithoutCancel(ctx), "reset "+name) }(setting[0])
    }
    stmts := []string{}
    for _, stmt := range SplitSQLStatements(m.SQL) {
        if strings.TrimSpace(stmt) != "" { stmts = append(stmts, stmt) }
    }
    for i, stmt := range stmts {
        if _, err := conn.Exec(ctx, stmt); err != nil {
            return fmt.Errorf("statement %d of %d failed; this migration runs without a transaction, so statements before it stay applied: %w", i+1, len(stmts), err)
        }
    }
    if m.RecordIn != "" {
        schema, table, _ := strings.Cut(m.RecordIn, ".")
        q := fmt.Sprintf("insert into %s.%s(version, name, checksum, applied_at) values($1, $2, $3, now())", pqIdent(schema), pqIdent(table))
        if _, err := conn.Exec(ctx, q, MigrationVersion(m.Name), m.Name, m.Checksum); err != nil {
            return fmt.Errorf("record %s: %w", m.Name, err)
        }
    }
    return nil
}

// MigrationVersion returns the number a migration file name starts with
// ("0003_users.sql" -> 3), or 0 when it has none.
func MigrationVersion(name string) int {
    n := 0
    for i := 0; i < len(name) && isDigit(name[i]); i++ {
        n = n*10 + int(name[i]-'0')
    }
    return n
}

// SplitSQLStatements splits SQL on semicolons while respecting single-quoted
// strings ('' escape), double-quoted identifiers ("" escape), dollar-quoted
// strings ($$ or $tag$), line comments (--), nested block comments (/* */)
// and parentheses (a multi-action CREATE RULE ... DO (a; b) is one
// statement). E'...' backslash-escape strings are not supported.
func SplitSQLStatements(sql string) []string {
    var statements []string
    var current strings.Builder
    n := len(sql)
    i := 0
    depth := 0

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
        case c == '(' || c == ')':
            if c == '(' { depth++ } else if depth > 0 { depth-- }
            current.WriteByte(c)
            i++
        case c == ';' && depth == 0:
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
    q := fmt.Sprintf("insert into %s.%s(version, name, checksum, applied_at) values($1,$2,$3,$4) on conflict (version) do nothing", pqIdent(schema), pqIdent(table))
    _, err := pool.Exec(ctx, q, MigrationVersion(name), name, checksum, appliedAt)
    return err
}


