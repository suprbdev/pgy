package db

import (
    "context"
    "fmt"
    "strings"

    "github.com/jackc/pgx/v5/pgxpool"
)

func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
    cfg, err := pgxpool.ParseConfig(dsn)
    if err != nil {
        return nil, err
    }
    pool, err := pgxpool.NewWithConfig(ctx, cfg)
    if err != nil {
        return nil, err
    }
    if err := pool.Ping(ctx); err != nil {
        pool.Close()
        return nil, err
    }
    return pool, nil
}

// AdvisoryLockKey is the session-level advisory lock pgy holds while it
// initializes or migrates a database.
const AdvisoryLockKey int64 = 0x707979

// WithAdvisoryLock runs fn while holding AdvisoryLockKey on a dedicated pool
// connection, waiting for any other pgy process holding it (cancel ctx to
// stop waiting). fn may use other pool connections.
func WithAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, fn func(context.Context) error) error {
    return pool.AcquireFunc(ctx, func(conn *pgxpool.Conn) error {
        if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", AdvisoryLockKey); err != nil {
            return fmt.Errorf("acquire migration lock: %w", err)
        }
        defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", AdvisoryLockKey) }()
        return fn(ctx)
    })
}

// EnsureMigrationsTable creates the migrations table schema.table, and its
// schema, unless they exist. It looks them up in the catalog first and runs
// only the CREATE statements for what is missing, so a role without CREATE
// on the database (or on the schema) can use a migrations table that
// already exists: PostgreSQL checks that privilege before IF NOT EXISTS.
func EnsureMigrationsTable(ctx context.Context, pool *pgxpool.Pool, schema, table string) error {
    var hasSchema, hasTable bool
    err := pool.QueryRow(ctx, `
        select exists (select from pg_catalog.pg_namespace where nspname = $1),
               exists (select from pg_catalog.pg_class c join pg_catalog.pg_namespace n on n.oid = c.relnamespace
                       where n.nspname = $1 and c.relname = $2)`, schema, table).Scan(&hasSchema, &hasTable)
    if err != nil {
        return err
    }
    if hasTable {
        return nil
    }
    q := ""
    if !hasSchema {
        q = fmt.Sprintf("create schema if not exists %s;\n", pqIdent(schema))
    }
    q += fmt.Sprintf(`
        create table if not exists %s.%s (
            version integer primary key,
            name text not null,
            checksum text not null,
            applied_at timestamptz not null default now()
        );
    `, pqIdent(schema), pqIdent(table))
    _, err = pool.Exec(ctx, q)
    return err
}

func pqIdent(id string) string {
    return `"` + strings.ReplaceAll(id, `"`, `""`) + `"`
}


