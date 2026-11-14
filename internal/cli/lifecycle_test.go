package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// TestLifecycleDiffCommitMigrate drives diff, commit, migrate and status
// through three schema versions: renames, a concurrent index on a renamed
// table, a concurrent index on a partitioned table, and a foreign key added
// NOT VALID and validated later.
func TestLifecycleDiffCommitMigrate(t *testing.T) {
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
	const sch = "pgytest_cli_lifecycle"
	cleanup := func() { pool.Exec(context.Background(), `drop schema if exists `+sch+` cascade`) } //nolint
	cleanup()
	t.Cleanup(cleanup)
	var version int
	if err := pool.QueryRow(ctx, "select current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	schemas, migrations := filepath.Join(dir, "schemas"), filepath.Join(dir, "migrations")
	for _, d := range []string{schemas, migrations} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &GlobalConfig{DSN: dsn, SchemaRoot: schemas, Schemas: []string{"schema.yml"}, BufferPath: filepath.Join(dir, "buffer.sql"),
		MigrationsDir: migrations, MigrationsTable: sch + ".applied", Quiet: true}
	run := func(c *cobra.Command, args ...string) error {
		c.SetArgs(args)
		c.SilenceUsage, c.SilenceErrors = true, true
		return c.ExecuteContext(context.WithValue(ctx, ctxKey{}, cfg))
	}
	exitCode := func(err error) int {
		var code *exitCodeError
		if errors.As(err, &code) {
			return code.code
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0
	}

	orders := `
  table orders:
    columns:
      id: {type: bigint, identity: always}
      customer_id: {type: bigint}
      placed_at: {type: timestamptz}
      total: {type: "numeric(12,2)"}
    primaryKey: [id, placed_at]
    partitionBy: {type: range, columns: [placed_at]}`
	part := `
  table orders_2026:
    partitionOf: SCH.orders
    forValues: {from: ["2026-01-01"], to: ["2027-01-01"]}`
	v2Orders := orders + `
    indexes:
      orders_customer_idx: {columns: [customer_id], concurrently: true}
    foreignKeys:
      orders_client_fkey: {columns: [customer_id], references: {table: SCH.client, columns: [id]}NOTVALID}`
	client := `
  table client:
    renamedFrom: customer
    columns:
      id: {type: bigint, identity: always}
      email_address: {type: text, renamedFrom: email}
    primaryKey: [id]
    indexes:
      client_email_idx: {columns: [email_address], concurrently: true}`
	notValid := ", notValid: true"
	if version < 180000 {
		notValid = ""
	}
	versions := []string{
		`
schema SCH:
  table customer:
    columns:
      id: {type: bigint, identity: always}
      email: {type: text}
    primaryKey: [id]` + orders + part,
		"\nschema SCH:" + client + strings.ReplaceAll(v2Orders, "NOTVALID", notValid) + part,
		"\nschema SCH:" + client + strings.ReplaceAll(v2Orders, "NOTVALID", "") + part,
	}
	for i, y := range versions {
		if err := os.WriteFile(filepath.Join(schemas, "schema.yml"), []byte(strings.ReplaceAll(y, "SCH", sch)), 0o644); err != nil {
			t.Fatal(err)
		}
		want := 2
		if i == 2 && version < 180000 {
			want = 0
		}
		if code := exitCode(run(cmdDiff(), "--no-warnings")); code != want {
			t.Fatalf("version %d: diff exit code %d, want %d", i+1, code, want)
		}
		if want == 0 {
			continue
		}
		if err := run(cmdCommit(), "v"+string(rune('1'+i))); err != nil {
			t.Fatalf("version %d: commit: %v", i+1, err)
		}
		if err := run(cmdMigrate()); err != nil {
			t.Fatalf("version %d: migrate: %v", i+1, err)
		}
		if i == 0 {
			if _, err := pool.Exec(ctx, `insert into `+sch+`.customer (email) values ('a@b.c'); insert into `+sch+`.orders (customer_id, placed_at, total) values (1, '2026-05-01', 9.99)`); err != nil {
				t.Fatal(err)
			}
		}
	}
	if code := exitCode(run(cmdStatus())); code != 0 {
		t.Errorf("status exit code %d after migrating everything", code)
	}
	if code := exitCode(run(cmdDiff(), "--unsafe", "--prune", "--no-warnings")); code != 0 {
		b, _ := os.ReadFile(cfg.BufferPath)
		t.Errorf("final diff not empty:\n%s", b)
	}
	files, _ := filepath.Glob(filepath.Join(migrations, "*_concurrent.sql"))
	if len(files) == 0 {
		t.Error("expected a no-transaction migration for the concurrent indexes")
	}
	var invalid, unvalidated int
	var email string
	if err := pool.QueryRow(ctx, `select count(*) from pg_index x join pg_class c on c.oid = x.indexrelid where c.relnamespace = $1::regnamespace and not x.indisvalid`, sch).Scan(&invalid); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from pg_constraint where connamespace = $1::regnamespace and not convalidated`, sch).Scan(&unvalidated); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select email_address from `+sch+`.client`).Scan(&email); err != nil {
		t.Fatal(err)
	}
	if invalid != 0 || unvalidated != 0 || email != "a@b.c" {
		t.Errorf("invalid indexes=%d unvalidated constraints=%d email=%q", invalid, unvalidated, email)
	}
}
