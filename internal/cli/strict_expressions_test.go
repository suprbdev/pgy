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

// TestDiffStrictExpressions regroups the operands of a check in the
// database, which the text comparison can't see: pgy diff reports nothing,
// pgy diff --strict-expressions and policy.strict_expressions report the
// change, and --strict-expressions=false overrides the config.
func TestDiffStrictExpressions(t *testing.T) {
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
	const name = "pgytest_cli_strict_expressions"
	drop := func() {
		admin.Exec(context.Background(), `drop database if exists `+name+` with (force)`) //nolint
	}
	drop()
	t.Cleanup(drop)
	if _, err := admin.Exec(ctx, `create database `+name); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name

	dir := t.TempDir()
	schemas := filepath.Join(dir, "schemas")
	if err := os.MkdirAll(schemas, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, schemas, map[string]string{"g.yml": `
schema app:
  table g:
    columns:
      a: {type: int}
      b: {type: int}
      c: {type: int}
    constraints:
      g_abc: {type: check, expression: "(a + b) * c > 0"}
`})
	cfg := &GlobalConfig{DSN: u.String(), SchemaRoot: schemas, Schemas: []string{"g.yml"},
		BufferPath: filepath.Join(dir, "buffer.sql"), MigrationsDir: filepath.Join(dir, "migrations"), MigrationsTable: defaultMigrationsTable, Quiet: true}
	run := func(cfg *GlobalConfig, c *cobra.Command, args ...string) int {
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
	if code := run(cfg, cmdDiff()); code != 2 {
		t.Fatalf("first diff: exit %d, want 2", code)
	}
	run(cfg, cmdCommit(), "init")
	run(cfg, cmdMigrate())

	db, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ctx, `alter table app.g drop constraint g_abc; alter table app.g add constraint g_abc check (a + b * c > 0)`); err != nil {
		t.Fatal(err)
	}

	strict := *cfg
	strict.Migrate.StrictExpressions = true
	for _, c := range []struct {
		name string
		cfg  *GlobalConfig
		args []string
		want int
	}{
		{"default", cfg, []string{"--unsafe"}, 0},
		{"--strict-expressions", cfg, []string{"--unsafe", "--strict-expressions"}, 2},
		{"policy.strict_expressions", &strict, []string{"--unsafe"}, 2},
		{"--strict-expressions=false over the config", &strict, []string{"--unsafe", "--strict-expressions=false"}, 0},
	} {
		_ = os.Remove(cfg.BufferPath)
		if code := run(c.cfg, cmdDiff(), c.args...); code != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, code, c.want)
		}
		buffer, _ := os.ReadFile(cfg.BufferPath)
		if c.want == 2 && !strings.Contains(string(buffer), `add constraint "g_abc" check ((a + b) * c > 0)`) {
			t.Errorf("%s: want the check redefined, got:\n%s", c.name, buffer)
		}
	}
}
