package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const crlfMigration = "create function pgytest_cli_crlf() returns int language plpgsql as $$\r\nbegin\r\n  return 1;\r\nend\r\n$$;\n"

func TestWriteMigrationChecksumMatchesCRLFBody(t *testing.T) {
	for name, body := range map[string]string{
		"crlf":       crlfMigration,
		"lf":         "create table a (id int);\n",
		"comment":    "-- pgy:no-transaction\ncreate index concurrently i on a (id);\n",
		"blank lead": "\n\ncreate table a (id int);\r\n",
		"empty":      "",
	} {
		file, err := writeMigration(t.TempDir(), 1, "m", []byte(body), time.Unix(0, 0))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(b), body) {
			t.Errorf("%s: migration body was rewritten:\n%q", name, b)
		}
		if header, sum := parseChecksumHeader(string(b)), checksumBody(b); header == "" || header != sum {
			t.Errorf("%s: header checksum %q, migrate computes %q", name, header, sum)
		}
	}
}

func TestChecksumOfEarlierMigrationsStillValid(t *testing.T) {
	body := "create table a (id int);\n\nalter table a add column b text;\n"
	raw := sha256.Sum256([]byte(body))
	file := fmt.Sprintf("-- pgy v0.56.2\n-- created 2026-01-01T00:00:00Z\n-- checksum %s\n\n%s", hex.EncodeToString(raw[:]), body)
	if header, sum := parseChecksumHeader(file), checksumBody([]byte(file)); header != sum {
		t.Errorf("checksum written by an earlier version no longer verifies: header %q, computed %q", header, sum)
	}
	converted := strings.ReplaceAll(file, "\n", "\r\n")
	if header, sum := parseChecksumHeader(converted), checksumBody([]byte(converted)); header != sum {
		t.Errorf("checksum must survive a CR LF checkout: header %q, computed %q", header, sum)
	}
}

// migrationSandbox returns a pool on PGY_TEST_DSN and a function that
// commits a buffer and migrates it, recording in a migrations table of its
// own in schema sch. It skips the test without PGY_TEST_DSN.
func migrationSandbox(t *testing.T, sch string) (*pgxpool.Pool, func(buffer string) error) {
	t.Helper()
	dsn := os.Getenv("PGY_TEST_DSN")
	if dsn == "" {
		t.Skip("PGY_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	drop := func() { pool.Exec(context.Background(), `drop schema if exists `+sch+` cascade`) } //nolint
	drop()
	t.Cleanup(func() {
		drop()
		pool.Close()
	})
	dir := t.TempDir()
	cfg := &GlobalConfig{DSN: dsn, BufferPath: filepath.Join(dir, "buffer.sql"), MigrationsDir: filepath.Join(dir, "migrations"),
		MigrationsTable: sch + ".applied", Quiet: true}
	if err := os.MkdirAll(cfg.MigrationsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return pool, func(buffer string) error {
		if err := os.WriteFile(cfg.BufferPath, []byte(buffer), 0o644); err != nil {
			t.Fatal(err)
		}
		commit, migrate := cmdCommit(), cmdMigrate()
		commit.SetArgs([]string{"step"})
		migrate.SetArgs(nil)
		migrate.SilenceUsage, migrate.SilenceErrors = true, true
		if err := commit.ExecuteContext(context.WithValue(ctx, ctxKey{}, cfg)); err != nil {
			return err
		}
		return migrate.ExecuteContext(context.WithValue(ctx, ctxKey{}, cfg))
	}
}

func TestCommitAndMigrateCRLFBuffer(t *testing.T) {
	const sch = "pgytest_cli_crlf"
	pool, commitAndMigrate := migrationSandbox(t, sch)
	buffer := "create schema " + sch + "_fn;\n" + strings.Replace(crlfMigration, "pgytest_cli_crlf()", sch+"_fn.f()", 1)
	t.Cleanup(func() { pool.Exec(context.Background(), `drop schema if exists `+sch+`_fn cascade`) }) //nolint
	if err := commitAndMigrate(buffer); err != nil {
		t.Fatalf("a migration with CR LF line endings must apply: %v", err)
	}
	var src string
	if err := pool.QueryRow(context.Background(), `select prosrc from pg_proc where pronamespace = $1::regnamespace`, sch+"_fn").Scan(&src); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src, "begin\r\n  return 1;\r\n") {
		t.Errorf("function body lost its line endings: %q", src)
	}
}
