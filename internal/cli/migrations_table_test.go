package cli

import (
    "context"
    "errors"
    "net/url"
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

// TestMigrationsTableWithoutCreateOnDatabase runs init, status and migrate
// as a role that owns its schema but has no CREATE on the database: init
// creates only the table, and the other commands create nothing.
func TestMigrationsTableWithoutCreateOnDatabase(t *testing.T) {
    dsn := os.Getenv("PGY_TEST_DSN")
    if dsn == "" { t.Skip("PGY_TEST_DSN not set") }
    u, err := url.Parse(dsn)
    if err != nil || u.Scheme == "" { t.Skip("PGY_TEST_DSN is not a URL") }
    ctx := context.Background()
    admin, err := pgxpool.New(ctx, dsn)
    if err != nil { t.Fatal(err) }
    defer admin.Close()
    const name, role = "pgytest_cli_nocreate", "pgytest_cli_nocreate_owner"
    drop := func() {
        admin.Exec(context.Background(), `drop database if exists `+name+` with (force)`) //nolint
        admin.Exec(context.Background(), `drop role if exists `+role)                     //nolint
    }
    drop()
    t.Cleanup(drop)
    for _, stmt := range []string{`create database ` + name, `create role ` + role + ` login password 'pgy'`} {
        if _, err := admin.Exec(ctx, stmt); err != nil { t.Fatalf("%s: %v", stmt, err) }
    }
    u.Path = "/" + name
    db, err := pgxpool.New(ctx, u.String())
    if err != nil { t.Fatal(err) }
    defer db.Close()
    if _, err := db.Exec(ctx, `create schema app authorization `+role); err != nil { t.Fatal(err) }

    limited := *u
    limited.User = url.UserPassword(role, "pgy")
    dir := t.TempDir()
    writeFiles(t, dir, map[string]string{"9301_a.sql": "create table app.t (id int);\n"})
    cfg := &GlobalConfig{DSN: limited.String(), MigrationsDir: dir, MigrationsTable: "app.pgy_migrations", Quiet: true}
    run := func(c interface {
        SetArgs([]string)
        ExecuteContext(context.Context) error
    }, args ...string) error {
        c.SetArgs(args)
        return c.ExecuteContext(context.WithValue(ctx, ctxKey{}, cfg))
    }
    if err := run(cmdInit()); err != nil { t.Fatalf("init in a schema the role owns: %v", err) }
    var owner string
    if err := db.QueryRow(ctx, `select tableowner from pg_tables where schemaname = 'app' and tablename = 'pgy_migrations'`).Scan(&owner); err != nil || owner != role {
        t.Fatalf("init should create app.pgy_migrations as %s: owner %q, err %v", role, owner, err)
    }
    var code *exitCodeError
    if err := run(cmdStatus()); !errors.As(err, &code) || code.code != 2 { t.Errorf("status with a pending migration: want exit 2, got %v", err) }
    if err := run(cmdMigrate()); err != nil { t.Fatalf("migrate: %v", err) }
    if err := run(cmdStatus()); err != nil { t.Errorf("status with everything applied: want exit 0, got %v", err) }
    if err := run(cmdInit()); err != nil { t.Errorf("init with the table in place: %v", err) }
}
