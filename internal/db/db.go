package db

import (
    "context"
    "fmt"

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

// WithAdvisoryLock acquires a session-level advisory lock for the duration of fn.
// Returns true if the lock was acquired.
func WithAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, fn func(context.Context) error) (bool, error) {
    // single lock key for tool, could be customized later
    const key int64 = 0x707979 // "pyy" arbitrary
    var acquired bool
    err := pool.AcquireFunc(ctx, func(conn *pgxpool.Conn) error {
        if err := conn.Conn().QueryRow(ctx, "select pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
            return err
        }
        if !acquired {
            // run without lock but indicate false
            return fn(ctx)
        }
        defer conn.Conn().Exec(ctx, "select pg_advisory_unlock($1)", key)
        return fn(ctx)
    })
    return acquired, err
}

func EnsureMigrationsTable(ctx context.Context, pool *pgxpool.Pool, schema, table string) error {
    q := fmt.Sprintf(`
        create schema if not exists %s;
        create table if not exists %s.%s (
            version integer primary key,
            name text not null,
            checksum text not null,
            applied_at timestamptz not null default now()
        );
    `, pqIdent(schema), pqIdent(schema), pqIdent(table))
    _, err := pool.Exec(ctx, q)
    return err
}

func pqIdent(id string) string {
    // very basic quote identifier
    return `"` + id + `"`
}


