package cli

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDiffPruneKeepExtensions(t *testing.T) {
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
	const name = "pgytest_cli_prune_ext"
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
		_, _ = admin.Exec(context.Background(), "drop database if exists "+name)
	})
	if _, err := db.Exec(ctx, `
		create schema app;
		create extension citext schema app;
		create extension hstore schema app;
		create extension pg_trgm schema app;
		create table app.t (id int primary key, email app.citext)`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.yml"), "extensions: []\nschema app:\n  table t:\n    columns:\n      id: {type: int, primaryKey: true}\n      email: {type: app.citext, nullable: true}\n")
	run := func(keep ...string) string {
		t.Helper()
		cfg := &GlobalConfig{DSN: u.String(), SchemaRoot: dir, Schemas: []string{"s.yml"}, BufferPath: "-", MigrationsTable: defaultMigrationsTable, Quiet: true, KeepExtensions: keep}
		out, err := captureStdout(t, func() error {
			c := cmdDiff()
			c.SilenceUsage, c.SilenceErrors = true, true
			return runWith(cfg, c, "--unsafe", "--prune", "--no-warnings")
		})
		if code, ok := AsExitCode(err); !ok || code != 2 {
			t.Fatalf("want exit code 2, got %v", err)
		}
		return out
	}
	if got, want := run(), "drop extension \"hstore\", \"pg_trgm\";\n"; got != want {
		t.Errorf("citext is in use and stays; want %q, got %q", want, got)
	}
	if got, want := run("hstore"), "drop extension \"pg_trgm\";\n"; got != want {
		t.Errorf("prune.keep_extensions must keep hstore; want %q, got %q", want, got)
	}
}
