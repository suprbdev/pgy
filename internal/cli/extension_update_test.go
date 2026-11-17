package cli

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/group"
)

func TestCommitWritesExtensionUpdatesFirst(t *testing.T) {
	plan := &diff.PlanDiff{
		ExtensionUpdates: []string{`alter extension "pg_stat_statements" update to '1.10';`},
		Prelude:          []string{`alter type "app"."st" add value 'c';`},
		Creates:          []string{`create table "app"."t" ("id" integer);`},
	}
	dir, err := runCommit(t, group.Render([]group.Step{{Plan: plan}}), "upd")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 4 || names[1] != "0002_upd_extension_updates.sql" || names[2] != "0003_upd_enum_values.sql" || names[3] != "0004_upd.sql" {
		t.Fatalf("unexpected files: %v", names)
	}
	b, err := os.ReadFile(filepath.Join(dir, names[1]))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\n-- pgy:new-session\nalter extension \"pg_stat_statements\" update to '1.10';\n") || strings.Contains(string(b), "add value") {
		t.Errorf("the extension updates migration holds only the updates, behind the new-session directive:\n%s", b)
	}
}

func TestMigrateNewSessionFile(t *testing.T) {
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
	cleanup := func() {
		pool.Exec(ctx, `drop table if exists public.pgytest_cli_ns`)                     //nolint
		pool.Exec(ctx, `delete from pgy.migrations where version between 9301 and 9302`) //nolint
	}
	cleanup()
	t.Cleanup(cleanup)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"9301_a.sql": "-- pgy:new-session\n" +
			"create table public.pgytest_cli_ns as select pg_backend_pid() as pid, current_setting('lock_timeout') as lt;\n" +
			"insert into public.pgytest_cli_ns select pg_backend_pid(), current_setting('lock_timeout');\n",
	})
	run := func(args ...string) error {
		c := cmdMigrate()
		c.SetArgs(args)
		c.SilenceUsage, c.SilenceErrors = true, true
		return c.ExecuteContext(context.WithValue(ctx, ctxKey{}, &GlobalConfig{DSN: dsn, MigrationsDir: dir, Quiet: true}))
	}
	if err := run("--lock-timeout", "5s"); err != nil {
		t.Fatal(err)
	}
	var pids, rows int
	var lt string
	if err := pool.QueryRow(ctx, `select count(distinct pid), count(*), min(lt) from public.pgytest_cli_ns`).Scan(&pids, &rows, &lt); err != nil {
		t.Fatal(err)
	}
	if pids != 2 || rows != 2 || lt != "5s" {
		t.Errorf("each statement runs in a session of its own with the lock timeout: %d sessions, %d rows, lock_timeout %q", pids, rows, lt)
	}
	writeFiles(t, dir, map[string]string{
		"9302_b.sql": "-- pgy:new-session\ninsert into public.pgytest_cli_ns values (0, 'kept');\nselect 1/0;\n",
	})
	if err := run(); err == nil || !strings.Contains(err.Error(), "statement 2 of 2 failed") {
		t.Fatalf("want the second statement to fail, got %v", err)
	}
	var recorded, kept int
	if err := pool.QueryRow(ctx, `select (select count(*) from pgy.migrations where version between 9301 and 9302), (select count(*) from public.pgytest_cli_ns where lt = 'kept')`).Scan(&recorded, &kept); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 || kept != 1 {
		t.Errorf("only the first migration is recorded (%d), and the failed one's first statement stays applied (%d)", recorded, kept)
	}
}

func TestExtensionUpdateDiffCommitMigrate(t *testing.T) {
	dsn := os.Getenv("PGY_TEST_DSN")
	if dsn == "" {
		t.Skip("PGY_TEST_DSN not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	var n int
	if err := admin.QueryRow(ctx, `select count(*) from pg_available_extension_versions where name = 'pg_stat_statements' and version in ('1.6', '1.10')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Skip("this server cannot install pg_stat_statements 1.6 and 1.10")
	}
	const name = "pgytest_cli_ext_update"
	if _, err := admin.Exec(ctx, "drop database if exists "+name); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "create database "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		_, _ = admin.Exec(context.Background(), "drop database if exists "+name+" with (force)")
	})
	if _, err := db.Exec(ctx, `create extension pg_stat_statements version '1.6' schema public`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.yml"), "extensions:\n  - {name: pg_stat_statements, schema: public, version: \"1.10\"}\nschema app:\n  table t:\n    columns:\n      id: {type: int, primaryKey: true}\n")
	migrations := filepath.Join(dir, "migrations")
	cfg := &GlobalConfig{DSN: u.String(), SchemaRoot: dir, Schemas: []string{"s.yml"}, BufferPath: filepath.Join(dir, "buffer.sql"), MigrationsDir: migrations, MigrationsTable: defaultMigrationsTable, Quiet: true}
	run := func(c *cobra.Command, args ...string) error {
		c.SetArgs(args)
		c.SilenceUsage, c.SilenceErrors = true, true
		return c.ExecuteContext(context.WithValue(ctx, ctxKey{}, cfg))
	}
	if code, ok := AsExitCode(run(cmdDiff())); !ok || code != 2 {
		t.Fatalf("want changes, got exit code %d", code)
	}
	if err := run(cmdCommit(), "upd"); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(migrations, "0001_upd_extension_updates.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "-- pgy:new-session\nalter extension \"pg_stat_statements\" update to '1.10';") {
		t.Errorf("the update belongs in a new-session migration of its own:\n%s", first)
	}
	if err := run(cmdMigrate(), "--lock-timeout", "5s"); err != nil {
		t.Fatal(err)
	}
	var version string
	var applied int
	if err := db.QueryRow(ctx, `select (select extversion from pg_extension where extname = 'pg_stat_statements'), (select count(*) from pgy.migrations)`).Scan(&version, &applied); err != nil {
		t.Fatal(err)
	}
	if version != "1.10" || applied != 2 {
		t.Errorf("want pg_stat_statements 1.10 and two recorded migrations, got %s and %d", version, applied)
	}
	if err := run(cmdDiff()); err != nil {
		t.Errorf("the database matches the YAML after migrate: %v", err)
	}
}
