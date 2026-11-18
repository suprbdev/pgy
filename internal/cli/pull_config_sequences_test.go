package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPullReportsConfigurationSequences pulls a database whose extension
// marks a table and the sequence numbering its key as configuration, as
// pg_extension_config_dump does: the report names the sequence's position
// with the table's rows, a database rebuilt from the YAML pulled with
// --data advances the sequence to it, and a configuration sequence that
// numbers no table is reported on its own.
func TestPullReportsConfigurationSequences(t *testing.T) {
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
	t.Cleanup(admin.Close)
	const name, rebuilt = "pgytest_cli_pull_config_seqs", "pgytest_cli_pull_config_seqs_b"
	drop := func() {
		admin.Exec(context.Background(), `drop database if exists `+name+` with (force)`)    //nolint
		admin.Exec(context.Background(), `drop database if exists `+rebuilt+` with (force)`) //nolint
	}
	drop()
	t.Cleanup(drop)
	if _, err := admin.Exec(ctx, `create database `+name); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const script = `
		create extension hstore schema public;
		create table public.cfg (id serial primary key, name text);
		create sequence public.counter;
		alter extension hstore add table public.cfg;
		alter extension hstore add sequence public.cfg_id_seq;
		alter extension hstore add sequence public.counter;
		update pg_extension set extconfig = array['public.cfg'::regclass::oid, 'public.cfg_id_seq'::regclass::oid, 'public.counter'::regclass::oid], extcondition = array['', '', ''] where extname = 'hstore';`
	if _, err := db.Exec(ctx, script+`
		insert into public.cfg (name) values ('a'), ('b');
		select nextval('public.counter') from generate_series(1, 4);`); err != nil {
		t.Fatal(err)
	}
	pull := func(dir string, args ...string) []string {
		t.Helper()
		cfg := &GlobalConfig{DSN: u.String(), MigrationsTable: defaultMigrationsTable, JSON: true}
		out, err := captureStdout(t, func() error { return runWith(cfg, cmdPull(), append([]string{"--out", dir, "--force"}, args...)...) })
		if err != nil {
			t.Fatal(err)
		}
		var s struct {
			Warnings []struct{ Kind, Message string }
		}
		if err := json.Unmarshal([]byte(out), &s); err != nil {
			t.Fatalf("--json output is not JSON: %v\n%s", err, out)
		}
		msgs := []string{}
		for _, w := range s.Warnings {
			msgs = append(msgs, w.Message)
		}
		return msgs
	}
	warnings := strings.Join(pull(t.TempDir()), "\n")
	for _, want := range []string{
		"configuration table public.cfg has 2 rows that pg_dump keeps and this YAML does not carry, and the position of its sequence public.cfg_id_seq (at 2)",
		"configuration sequence public.counter has reached 4, which pg_dump keeps",
	} {
		if !strings.Contains(warnings, want) {
			t.Errorf("missing %q in:\n%s", want, warnings)
		}
	}

	dir := t.TempDir()
	if warnings := pull(dir, "--data", "public.cfg"); len(warnings) != 1 || !strings.Contains(warnings[0], "public.counter") {
		t.Errorf("with --data only the sequence that numbers no table is reported, got %v", warnings)
	}
	if _, err := admin.Exec(ctx, `create database `+rebuilt); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + rebuilt
	var b []byte
	other, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec(ctx, script); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error {
		return runWith(&GlobalConfig{DSN: u.String(), SchemaRoot: dir, Schemas: discoverSchemas(dir, ""), BufferPath: dir + "/buffer.sql", MigrationsTable: defaultMigrationsTable}, cmdDiff())
	}); err == nil || !strings.Contains(err.Error(), "exit 2") {
		t.Fatalf("diff against the extension without rows must plan them, got %v", err)
	}
	if b, err = os.ReadFile(dir + "/buffer.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(ctx, string(b)); err != nil {
		t.Fatalf("rebuild: %v\n%s", err, b)
	}
	var last int
	var called bool
	if err := other.QueryRow(ctx, `select last_value, is_called from public.cfg_id_seq`).Scan(&last, &called); err != nil || last != 2 || !called {
		t.Errorf("rebuilt cfg_id_seq = (%d, %v) (%v), want (2, true)", last, called, err)
	}
}
