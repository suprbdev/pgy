package cli

import (
    "context"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/jackc/pgx/v5/pgxpool"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
    t.Helper()
    for name, body := range files {
        if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil { t.Fatal(err) }
    }
}

func TestFilterUntil(t *testing.T) {
    files := []string{"m/0001_a.sql", "m/0002_b.sql", "m/0003_c.sql"}
    for _, until := range []string{"0002", "0002_b", "0002_b.sql", "m/0002_b.sql"} {
        got, err := filterUntil(files, until)
        if err != nil || len(got) != 2 { t.Errorf("filterUntil(%q) = %v, %v", until, got, err) }
    }
    if got, err := filterUntil(files, "0009"); err == nil {
        t.Errorf("a target matching no migration must be an error, got %v", got)
    }
}

func TestReadMigrationFilesDuplicateVersion(t *testing.T) {
    dir := t.TempDir()
    writeFiles(t, dir, map[string]string{"0001_a.sql": "", "0002_b.sql": "", "0002_c.sql": ""})
    if _, err := readMigrationFiles(dir); err == nil || !strings.Contains(err.Error(), "share version 2") {
        t.Errorf("expected duplicate version error, got %v", err)
    }
}

func TestNextMigrationNumberBeyondFourDigits(t *testing.T) {
    dir := t.TempDir()
    writeFiles(t, dir, map[string]string{"9999_a.sql": "", "10000_b.sql": ""})
    if n, err := nextMigrationNumber(dir); err != nil || n != 10001 { t.Errorf("got %d %v", n, err) }
}

func TestMigrateCommand(t *testing.T) {
    dsn := os.Getenv("PGY_TEST_DSN")
    if dsn == "" { t.Skip("PGY_TEST_DSN not set") }
    ctx := context.Background()
    pool, err := pgxpool.New(ctx, dsn)
    if err != nil { t.Fatal(err) }
    defer pool.Close()
    cleanup := func() {
        pool.Exec(ctx, `drop table if exists public.pgytest_cli_a, public.pgytest_cli_b`) //nolint
        pool.Exec(ctx, `delete from pgy.migrations where version between 9001 and 9003`) //nolint
    }
    cleanup()
    t.Cleanup(cleanup)

    dir := t.TempDir()
    writeFiles(t, dir, map[string]string{
        "9001_a.sql": "create table public.pgytest_cli_a (id int);\n",
        "9002_b.sql": "create table public.pgytest_cli_b as select current_setting('lock_timeout') as lt, current_setting('statement_timeout') as st;\n",
        "9003_c.sql": "insert into public.pgytest_cli_a values (1);\nselect this is not sql;\n",
    })
    run := func(args ...string) error {
        c := cmdMigrate()
        c.SetArgs(args)
        c.SilenceUsage, c.SilenceErrors = true, true
        return c.ExecuteContext(context.WithValue(ctx, ctxKey{}, &GlobalConfig{DSN: dsn, MigrationsDir: dir, Quiet: true}))
    }
    recorded := func() []string {
        rows, err := pool.Query(ctx, `select name from pgy.migrations where version between 9001 and 9003 order by version`)
        if err != nil { t.Fatal(err) }
        defer rows.Close()
        out := []string{}
        for rows.Next() {
            var n string
            if err := rows.Scan(&n); err != nil { t.Fatal(err) }
            out = append(out, n)
        }
        return out
    }

    if err := run("--limit", "1"); err != nil { t.Fatal(err) }
    if got := recorded(); len(got) != 1 { t.Fatalf("--limit 1: recorded %v", got) }
    if err := run("--limit", "1", "--lock-timeout", "5s", "--statement-timeout", "30s"); err != nil { t.Fatal(err) }
    if got := recorded(); len(got) != 2 { t.Fatalf("--limit 1 must count pending migrations only: recorded %v", got) }
    var lt, st string
    if err := pool.QueryRow(ctx, `select lt, st from public.pgytest_cli_b`).Scan(&lt, &st); err != nil { t.Fatal(err) }
    if lt != "5s" || st != "30s" { t.Errorf("timeouts not applied inside the migration: lock_timeout=%q statement_timeout=%q", lt, st) }

    if err := run("--until", "9999"); err == nil { t.Error("--until matching nothing must fail") }
    if err := run(); err == nil || !strings.Contains(err.Error(), "9003_c.sql") { t.Fatalf("expected 9003 to fail, got %v", err) }
    if got := recorded(); len(got) != 2 { t.Errorf("failed migration must not be recorded: %v", got) }
    var n int
    if err := pool.QueryRow(ctx, `select count(*) from public.pgytest_cli_a`).Scan(&n); err != nil || n != 0 {
        t.Errorf("failed migration must roll back: rows=%d err=%v", n, err)
    }
}
