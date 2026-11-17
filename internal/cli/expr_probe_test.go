package cli

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

const exprProbeWarning = "compared as text only"

// TestDiffComparesExpressionsThroughServer builds a database from YAML that
// spells constants another way than PostgreSQL prints them. Every later
// diff has to be empty, also with --split and --unsafe --prune. A role
// without the TEMPORARY privilege gets the diff the text comparison makes
// and one warning, and no warning for YAML that is in sync.
func TestDiffComparesExpressionsThroughServer(t *testing.T) {
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
	const name, role = "pgytest_cli_expr_probe", "pgytest_cli_expr_probe_notemp"
	drop := func() {
		admin.Exec(context.Background(), `drop database if exists `+name+` with (force)`) //nolint
		admin.Exec(context.Background(), `drop role if exists `+role)                     //nolint
	}
	drop()
	t.Cleanup(drop)
	for _, stmt := range []string{
		`create database ` + name,
		`create role ` + role + ` login password 'pgy'`,
		`revoke temporary on database ` + name + ` from public`,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	u.Path = "/" + name

	dir := t.TempDir()
	schemas, migrations, pulled := filepath.Join(dir, "schemas"), filepath.Join(dir, "migrations"), filepath.Join(dir, "pulled")
	if err := os.MkdirAll(schemas, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, schemas, map[string]string{
		"events.yml": `
schema app:
  function touch():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table events:
    columns:
      id: {type: bigint}
      ttl: {type: interval, default: "'1 hour'"}
      at: {type: date, default: "'2020-1-1'"}
      meta: {type: jsonb, default: "'{ \"a\" : 1 }'"}
      late: {type: boolean, nullable: true, generated: "ttl > '90 minutes'"}
    primaryKey: [id]
    constraints:
      events_ttl: {type: check, expression: "ttl > '0 seconds'"}
    indexes:
      events_recent: {columns: [id], where: "at > '2020-1-1'"}
    rowLevelSecurity: true
    policies:
      events_p: {using: "at >= '2020-1-1'"}
    triggers:
      events_t:
        timing: before
        events: [update]
        level: row
        when: "new.ttl > '1 hour'"
        procedure: app.touch()
`,
		"rooms.yml": `
schema app:
  table rooms:
    columns:
      id: {type: bigint}
      open: {type: tstzrange, nullable: true, default: "'[2020-1-1 00:00+00,)'"}
    primaryKey: [id]
`,
	})
	cfg := &GlobalConfig{DSN: u.String(), SchemaRoot: schemas, Schemas: []string{"events.yml", "rooms.yml"},
		BufferPath: filepath.Join(dir, "buffer.sql"), MigrationsDir: migrations, MigrationsTable: defaultMigrationsTable, Quiet: true}
	run := func(cfg *GlobalConfig, c *cobra.Command, args ...string) (int, string) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stderr
		os.Stderr = w
		c.SetArgs(args)
		c.SilenceUsage, c.SilenceErrors = true, true
		err = c.ExecuteContext(context.WithValue(ctx, ctxKey{}, cfg))
		os.Stderr = old
		_ = w.Close()
		stderr, _ := io.ReadAll(r)
		var code *exitCodeError
		if errors.As(err, &code) {
			return code.code, string(stderr)
		}
		if err != nil {
			t.Fatalf("%v\n%s", err, stderr)
		}
		return 0, string(stderr)
	}
	if code, _ := run(cfg, cmdDiff()); code != 2 {
		t.Fatalf("first diff: exit %d, want 2", code)
	}
	run(cfg, cmdCommit(), "init")
	run(cfg, cmdMigrate())
	for _, flags := range [][]string{nil, {"--split"}, {"--unsafe"}, {"--unsafe", "--prune"}} {
		code, stderr := run(cfg, cmdDiff(), flags...)
		if code != 0 || strings.Contains(stderr, "warning") {
			buffer, _ := os.ReadFile(cfg.BufferPath)
			t.Errorf("diff %v after migrate: exit %d, want 0 and no warnings:\n%s%s", flags, code, buffer, stderr)
		}
	}

	db, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`grant usage on schema app, pgy to ` + role,
		`grant select on all tables in schema app, pgy to ` + role,
	} {
		if _, err := db.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	limited := *u
	limited.User = url.UserPassword(role, "pgy")
	noTemp := *cfg
	noTemp.DSN = limited.String()
	for _, flags := range [][]string{nil, {"--split"}} {
		code, stderr := run(&noTemp, cmdDiff(), flags...)
		if code != 2 || strings.Count(stderr, exprProbeWarning) != 1 || !strings.Contains(stderr, "permission denied") {
			t.Errorf("diff %v without TEMPORARY: want exit 2 and one warning that says why, got exit %d:\n%s", flags, code, stderr)
		}
	}
	if code, stderr := run(&noTemp, cmdDiff(), "--no-warnings"); code != 2 || strings.Contains(stderr, exprProbeWarning) {
		t.Errorf("--no-warnings: want exit 2 and no warning, got exit %d:\n%s", code, stderr)
	}

	run(cfg, cmdPull(), "--out", pulled, "--schema", "app")
	inSync := noTemp
	inSync.SchemaRoot, inSync.Schemas = pulled, []string{"app.yml"}
	if code, stderr := run(&inSync, cmdDiff()); code != 0 || strings.Contains(stderr, exprProbeWarning) {
		buffer, _ := os.ReadFile(cfg.BufferPath)
		t.Errorf("pulled YAML needs no probe, so nothing can warn: exit %d\n%s%s", code, buffer, stderr)
	}
}
