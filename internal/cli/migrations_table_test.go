package cli

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationsTableParsing(t *testing.T) {
    cases := map[string][2]string{
        "":               {"pgy", "migrations"},
        "pgy.migrations": {"pgy", "migrations"},
        "ops.applied":    {"ops", "applied"},
        "applied":        {"pgy", "applied"},
    }
    for in, want := range cases {
        s, tb := (&GlobalConfig{MigrationsTable: in}).migrationsTable()
        if s != want[0] || tb != want[1] {
            t.Errorf("migrationsTable(%q) = %s.%s, want %s.%s", in, s, tb, want[0], want[1])
        }
    }
    dir := t.TempDir()
    writeFile(t, filepath.Join(dir, "pgy.yaml"), "migrations_table: ops.applied\n")
    fc, _, err := loadFileConfig(dir, "")
    if err != nil || fc.MigrationsTable != "ops.applied" {
        t.Errorf("config file migrations_table not read: %+v %v", fc, err)
    }
}

func TestMigrateWithCustomMigrationsTable(t *testing.T) {
    dsn := os.Getenv("PGY_TEST_DSN")
    if dsn == "" { t.Skip("PGY_TEST_DSN not set") }
    ctx := context.Background()
    pool, err := pgxpool.New(ctx, dsn)
    if err != nil { t.Fatal(err) }
    defer pool.Close()
    cleanup := func() {
        pool.Exec(ctx, `drop schema if exists pgytest_cli_ops cascade`) //nolint
    }
    cleanup()
    t.Cleanup(cleanup)

    dir := t.TempDir()
    writeFiles(t, dir, map[string]string{"9101_a.sql": "create schema if not exists pgytest_cli_ops;\n"})
    cfg := &GlobalConfig{DSN: dsn, MigrationsDir: dir, MigrationsTable: "pgytest_cli_ops.applied", Quiet: true}
    run := func(c interface {
        SetArgs([]string)
        ExecuteContext(context.Context) error
    }, args ...string) error {
        c.SetArgs(args)
        return c.ExecuteContext(context.WithValue(ctx, ctxKey{}, cfg))
    }
    if err := run(cmdInit()); err != nil { t.Fatal(err) }
    if err := run(cmdMigrate()); err != nil { t.Fatal(err) }
    var n int
    if err := pool.QueryRow(ctx, `select count(*) from pgytest_cli_ops.applied where name = '9101_a.sql'`).Scan(&n); err != nil || n != 1 {
        t.Fatalf("migration should be recorded in the custom table: n=%d err=%v", n, err)
    }
    if err := pool.QueryRow(ctx, `select count(*) from pgy.migrations where name = '9101_a.sql'`).Scan(&n); err == nil && n != 0 {
        t.Errorf("migration must not be recorded in pgy.migrations")
    }
    status := cmdStatus()
    if err := run(status); err != nil { t.Errorf("status with everything applied should exit 0, got %v", err) }
}
