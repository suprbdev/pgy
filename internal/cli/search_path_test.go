package cli

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// TestMigrateDatabaseSearchPath builds a fresh database from two schema
// files with diff --split, commit and one migrate run. The table file
// spells an extension type and its operators without their schema and
// relies on the search_path the other file sets for the database, so every
// migration of the run has to see that search_path.
func TestMigrateDatabaseSearchPath(t *testing.T) {
	dsn := os.Getenv("PGY_TEST_DSN")
	if dsn == "" {
		t.Skip("PGY_TEST_DSN not set")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Skip("PGY_TEST_DSN is not a URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	const name = "pgytest_cli_search_path"
	drop := func() { admin.Exec(context.Background(), `drop database if exists `+name+` with (force)`) } //nolint
	drop()
	t.Cleanup(drop)
	if _, err := admin.Exec(ctx, `create database `+name); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name

	dir := t.TempDir()
	schemas, migrations := filepath.Join(dir, "schemas"), filepath.Join(dir, "migrations")
	if err := os.MkdirAll(schemas, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, schemas, map[string]string{
		"accounts.yml": `
schema app:
  table accounts:
    columns:
      id: {type: int}
      email: {type: citext}
    primaryKey: [id]
    constraints:
      accounts_email_chk: {type: check, expression: "email <> ''"}
    indexes:
      accounts_email_trgm: {columns: ["(email::text)"], using: gin, opclass: gin_trgm_ops}
`,
		"database.yml": `
extensions:
  - {name: citext, schema: extensions}
  - {name: pg_trgm, schema: extensions}
database:
  set:
    search_path: '"$user", public, extensions'
`,
		"rooms.yml": `
schema app:
  table rooms:
    columns:
      id: {type: int}
      name: {type: citext, default: "''::citext"}
    primaryKey: [id]
  view room_names:
    query: select id, similarity(name::text, 'x') as sim from app.rooms where name <> ''
`,
	})
	cfg := &GlobalConfig{DSN: u.String(), SchemaRoot: schemas, Schemas: []string{"accounts.yml", "database.yml", "rooms.yml"},
		BufferPath: filepath.Join(dir, "buffer.sql"), MigrationsDir: migrations, MigrationsTable: defaultMigrationsTable, Quiet: true}
	run := func(c *cobra.Command, args ...string) int {
		t.Helper()
		c.SetArgs(args)
		c.SilenceUsage, c.SilenceErrors = true, true
		err := c.ExecuteContext(context.WithValue(ctx, ctxKey{}, cfg))
		var code *exitCodeError
		if errors.As(err, &code) {
			return code.code
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0
	}
	if code := run(cmdDiff(), "--split"); code != 2 {
		t.Fatalf("diff --split: exit %d, want 2", code)
	}
	run(cmdCommit(), "init")
	files, err := filepath.Glob(filepath.Join(migrations, "*.sql"))
	if err != nil || len(files) != 2 {
		t.Fatalf("expected one migration for accounts.yml and one for rooms.yml, got %v (%v)", files, err)
	}
	first, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "\nset search_path = \"$user\", public, extensions;\n") {
		t.Fatalf("the first migration must set the session's search_path:\n%s", first)
	}
	run(cmdMigrate())
	for _, flags := range [][]string{nil, {"--split"}, {"--unsafe", "--prune"}} {
		if code := run(cmdDiff(), flags...); code != 0 {
			buffer, _ := os.ReadFile(cfg.BufferPath)
			t.Fatalf("diff %v after migrate: exit %d, want 0:\n%s", flags, code, buffer)
		}
	}
}
