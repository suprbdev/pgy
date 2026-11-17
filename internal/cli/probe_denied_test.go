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
)

const deniedWarning = "lacks a privilege to compare these"

// TestDiffReportsDeniedComparisonsOnce runs pgy diff as a role without
// USAGE on a schema whose view and table the YAML spells differently from
// PostgreSQL: one warning names both, the view is not replaced, and the
// default is planned as the text comparison finds it.
func TestDiffReportsDeniedComparisonsOnce(t *testing.T) {
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
	const name, role = "pgytest_cli_probe_denied", "pgytest_cli_probe_denied_app"
	drop := func() {
		admin.Exec(context.Background(), `drop database if exists `+name+` with (force)`) //nolint
		admin.Exec(context.Background(), `drop role if exists `+role)                     //nolint
	}
	drop()
	t.Cleanup(drop)
	for _, stmt := range []string{`create database ` + name, `create role ` + role + ` login password 'pgy'`} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	u.Path = "/" + name
	db, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ctx, `
		create schema hidden;
		revoke all on schema hidden from public;
		create table hidden.beat (ts timestamptz);
		create view hidden.last_beat as select max(ts) as ts from hidden.beat;
		create table hidden.p (id int primary key, ttl interval default '1 hour');`); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"hidden.yml": `
schema hidden:
  table beat:
    columns:
      ts: {type: timestamptz, nullable: true}
  view last_beat:
    query: select max(beat.ts) as ts from hidden.beat
  table p:
    columns:
      id: {type: integer}
      ttl: {type: interval, nullable: true, default: "'1 hour'::interval"}
    primaryKey: [id]
`})
	limited := *u
	limited.User = url.UserPassword(role, "pgy")
	cfg := &GlobalConfig{DSN: limited.String(), SchemaRoot: dir, Schemas: []string{"hidden.yml"},
		BufferPath: filepath.Join(dir, "buffer.sql"), MigrationsDir: filepath.Join(dir, "migrations"), MigrationsTable: defaultMigrationsTable, Quiet: true}
	run := func(args ...string) (int, string) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stderr
		os.Stderr = w
		c := cmdDiff()
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
	for _, flags := range [][]string{nil, {"--unsafe"}} {
		code, stderr := run(flags...)
		buffer, _ := os.ReadFile(cfg.BufferPath)
		if code != 2 || strings.Count(stderr, deniedWarning) != 1 || !strings.Contains(stderr, "permission denied for schema hidden") || !strings.Contains(stderr, ": hidden.last_beat, hidden.p\n") {
			t.Errorf("diff %v: want exit 2 and one warning naming both, got exit %d:\n%s", flags, code, stderr)
		}
		if want := "alter table \"hidden\".\"p\" alter column \"ttl\" set default '1 hour'::interval;\n"; string(buffer) != want {
			t.Errorf("diff %v: want only the default the text comparison finds changed, got:\n%s", flags, buffer)
		}
	}
	if code, stderr := run("--no-warnings"); code != 2 || strings.Contains(stderr, deniedWarning) {
		t.Errorf("--no-warnings: want exit 2 and no warning, got exit %d:\n%s", code, stderr)
	}
}
