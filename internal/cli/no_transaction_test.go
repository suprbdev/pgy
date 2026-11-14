package cli

import (
    "context"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "testing"

    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/suprbdev/pgy/internal/diff"
    "github.com/suprbdev/pgy/internal/group"
)

func TestCommitSplitsConcurrentStatements(t *testing.T) {
    plan := &diff.PlanDiff{Alters: []string{"alter table t add column b int;"}, Concurrent: []string{"create index concurrently if not exists t_b on t (b);"}}
    dir, err := runCommit(t, group.Render([]group.Step{{Plan: plan}}), "add_b")
    if err != nil { t.Fatal(err) }
    entries, err := os.ReadDir(dir)
    if err != nil { t.Fatal(err) }
    names := []string{}
    for _, e := range entries { names = append(names, e.Name()) }
    sort.Strings(names)
    if len(names) != 3 || names[1] != "0002_add_b.sql" || names[2] != "0003_add_b_concurrent.sql" {
        t.Fatalf("unexpected files: %v", names)
    }
    b, err := os.ReadFile(filepath.Join(dir, names[2]))
    if err != nil { t.Fatal(err) }
    if !strings.Contains(string(b), "-- pgy:no-transaction\ncreate index concurrently") {
        t.Errorf("concurrent migration lacks the directive:\n%s", b)
    }
}

func TestMigrateNoTransactionFile(t *testing.T) {
    dsn := os.Getenv("PGY_TEST_DSN")
    if dsn == "" { t.Skip("PGY_TEST_DSN not set") }
    ctx := context.Background()
    pool, err := pgxpool.New(ctx, dsn)
    if err != nil { t.Fatal(err) }
    defer pool.Close()
    cleanup := func() {
        pool.Exec(ctx, `drop table if exists public.pgytest_cli_ntx`) //nolint
        pool.Exec(ctx, `delete from pgy.migrations where version between 9201 and 9203`) //nolint
    }
    cleanup()
    t.Cleanup(cleanup)
    dir := t.TempDir()
    writeFiles(t, dir, map[string]string{
        "9201_a.sql": "create table public.pgytest_cli_ntx (a int, b int);\n",
        "9202_b.sql": "-- pgy:no-transaction\ncreate index concurrently if not exists pgytest_cli_ntx_a on public.pgytest_cli_ntx (a);\ncreate index concurrently if not exists pgytest_cli_ntx_b on public.pgytest_cli_ntx (b);\n",
    })
    run := func(args ...string) error {
        c := cmdMigrate()
        c.SetArgs(args)
        c.SilenceUsage, c.SilenceErrors = true, true
        return c.ExecuteContext(context.WithValue(ctx, ctxKey{}, &GlobalConfig{DSN: dsn, MigrationsDir: dir, Quiet: true}))
    }
    if err := run("--lock-timeout", "5s"); err != nil { t.Fatal(err) }
    var n int
    if err := pool.QueryRow(ctx, `select count(*) from pg_indexes where tablename = 'pgytest_cli_ntx'`).Scan(&n); err != nil || n != 2 {
        t.Fatalf("expected both concurrent indexes, got %d (%v)", n, err)
    }
    writeFiles(t, dir, map[string]string{"9203_c.sql": "create index concurrently pgytest_cli_ntx_c on public.pgytest_cli_ntx (a, b);\n"})
    if err := run(); err == nil || !strings.Contains(err.Error(), "transaction block") {
        t.Errorf("CONCURRENTLY without the directive should fail inside the migration transaction, got %v", err)
    }
}
