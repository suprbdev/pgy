package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestResolveMigratePolicy(t *testing.T) {
	mp, err := resolveMigratePolicy(&PolicyConfig{ConcurrentIndexes: true, PublicGrants: "authoritative", LockTimeout: "5s", StatementTimeout: " 500ms "})
	if err != nil || !mp.ConcurrentIndexes || mp.PublicGrants != diff.PublicAuthoritative || mp.LockTimeout != "5s" || mp.StatementTimeout != "500ms" {
		t.Errorf("resolved: %+v %v", mp, err)
	}
	if mp, _ := resolveMigratePolicy(&PolicyConfig{PublicGrants: "explicit"}); mp.PublicGrants != diff.PublicExplicit {
		t.Errorf("explicit: %+v", mp)
	}
	if mp, _ := resolveMigratePolicy(nil); mp != (migratePolicy{}) {
		t.Errorf("no policy block: %+v", mp)
	}
	for _, bad := range []*PolicyConfig{{PublicGrants: "all"}, {LockTimeout: "five seconds"}, {StatementTimeout: "5 parsecs"}} {
		if _, err := resolveMigratePolicy(bad); err == nil {
			t.Errorf("want an error for %+v", bad)
		}
	}
}

func TestMigratePolicyTimeouts(t *testing.T) {
	dsn := os.Getenv("PGY_TEST_DSN")
	if dsn == "" {
		t.Skip("PGY_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	cleanup := func() { _, _ = pool.Exec(context.Background(), `drop schema if exists pgytest_cli_timeouts cascade`) }
	cleanup()
	t.Cleanup(cleanup)

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"9201_a.sql": "create table pgytest_cli_timeouts.a as select current_setting('lock_timeout') as lock, current_setting('statement_timeout') as stmt;\n",
	})
	if _, err := pool.Exec(ctx, `create schema pgytest_cli_timeouts`); err != nil {
		t.Fatal(err)
	}
	cfg := &GlobalConfig{DSN: dsn, MigrationsDir: dir, MigrationsTable: "pgytest_cli_timeouts.applied", Quiet: true, Migrate: migratePolicy{LockTimeout: "4s", StatementTimeout: "40s"}}
	if err := runWith(cfg, cmdInit()); err != nil {
		t.Fatal(err)
	}
	if err := runWith(cfg, cmdMigrate()); err != nil {
		t.Fatal(err)
	}
	var lock, stmt string
	if err := pool.QueryRow(ctx, `select lock, stmt from pgytest_cli_timeouts.a`).Scan(&lock, &stmt); err != nil || lock != "4s" || stmt != "40s" {
		t.Errorf("policy timeouts: lock=%q stmt=%q err=%v", lock, stmt, err)
	}
	writeFiles(t, dir, map[string]string{
		"9202_b.sql": "create table pgytest_cli_timeouts.b as select current_setting('lock_timeout') as lock, current_setting('statement_timeout') as stmt;\n",
	})
	if err := runWith(cfg, cmdMigrate(), "--lock-timeout", "3s"); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select lock, stmt from pgytest_cli_timeouts.b`).Scan(&lock, &stmt); err != nil || lock != "3s" || stmt != "40s" {
		t.Errorf("--lock-timeout overrides the policy: lock=%q stmt=%q err=%v", lock, stmt, err)
	}
}

func TestStrictConcurrentIndexes(t *testing.T) {
	dsn := os.Getenv("PGY_TEST_DSN")
	if dsn == "" {
		t.Skip("PGY_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sch := "pgytest_strict_concurrent"
	if _, err := pool.Exec(ctx, "drop schema if exists "+sch+" cascade; create schema "+sch+"; create table "+sch+".t (id int primary key, b int, c int)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "drop schema if exists "+sch+" cascade") })
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.yml"), "schema "+sch+":\n  table t:\n    columns: {id: {type: int, primaryKey: true}, b: {type: int, nullable: true}, c: {type: int, nullable: true}}\n    indexes: {t_b: {columns: [b]}, t_c: {columns: [c], concurrently: true}}\n")
	cfg := strictConfig(t, dir, &CIConfig{FailOn: []string{"policy"}}, dsn)
	cfg.Migrate.ConcurrentIndexes = true
	if err := runWith(cfg, cmdDiff()); err == nil || !strings.Contains(err.Error(), "1 finding(s) of kind policy") {
		t.Errorf("one blocking index build must be one policy finding, got %v", err)
	}
	cfg.Migrate.ConcurrentIndexes = false
	if code, ok := AsExitCode(runWith(cfg, cmdDiff())); !ok || code != 2 {
		t.Error("without concurrent_indexes the lock warning isn't a policy finding")
	}
}
