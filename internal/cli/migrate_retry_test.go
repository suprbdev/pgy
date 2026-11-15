package cli

import (
    "context"
    "errors"
    "fmt"
    "os"
    "strings"
    "testing"
    "time"

    "github.com/jackc/pgx/v5/pgconn"
    "github.com/jackc/pgx/v5/pgxpool"
)

func TestIsLockFailure(t *testing.T) {
    for code, want := range map[string]bool{"55P03": true, "40P01": true, "42P01": false} {
        err := fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code})
        if got := isLockFailure(err); got != want {
            t.Errorf("isLockFailure(%s) = %v, want %v", code, got, want)
        }
    }
    if isLockFailure(errors.New("plain")) {
        t.Error("plain errors are not lock failures")
    }
}

func TestMigrateRetriesOnLockTimeout(t *testing.T) {
    dsn := os.Getenv("PGY_TEST_DSN")
    if dsn == "" { t.Skip("PGY_TEST_DSN not set") }
    ctx := context.Background()
    pool, err := pgxpool.New(ctx, dsn)
    if err != nil { t.Fatal(err) }
    defer pool.Close()
    cleanup := func() {
        pool.Exec(ctx, `drop table if exists public.pgytest_cli_retry`) //nolint
        pool.Exec(ctx, `delete from pgy.migrations where version between 9301 and 9302`) //nolint
    }
    cleanup()
    t.Cleanup(cleanup)
    dir := t.TempDir()
    writeFiles(t, dir, map[string]string{"9301_a.sql": "create table public.pgytest_cli_retry (a int);\n"})
    run := func(args ...string) error {
        c := cmdMigrate()
        c.SetArgs(args)
        c.SilenceUsage, c.SilenceErrors = true, true
        return c.ExecuteContext(context.WithValue(ctx, ctxKey{}, &GlobalConfig{DSN: dsn, MigrationsDir: dir, Quiet: true}))
    }
    if err := run(); err != nil { t.Fatal(err) }
    writeFiles(t, dir, map[string]string{"9302_b.sql": "alter table public.pgytest_cli_retry add column b int;\n"})

    hold := func() func() {
        tx, err := pool.Begin(ctx)
        if err != nil { t.Fatal(err) }
        if _, err := tx.Exec(ctx, `lock table public.pgytest_cli_retry in access share mode`); err != nil { t.Fatal(err) }
        return func() { tx.Rollback(ctx) } //nolint
    }

    release := hold()
    err = run("--lock-timeout", "100ms")
    release()
    if !isLockFailure(err) {
        t.Fatalf("without --retries the migration should fail on lock_timeout, got %v", err)
    }

    release = hold()
    go func() { time.Sleep(500 * time.Millisecond); release() }()
    if err := run("--lock-timeout", "100ms", "--retries", "5", "--retry-wait", "200ms"); err != nil {
        t.Fatalf("migration should succeed once the lock is released: %v", err)
    }
    var cols string
    if err := pool.QueryRow(ctx, `select string_agg(column_name, ',' order by column_name) from information_schema.columns where table_name = 'pgytest_cli_retry'`).Scan(&cols); err != nil || !strings.Contains(cols, "b") {
        t.Fatalf("column b missing after retry: %q %v", cols, err)
    }
}
