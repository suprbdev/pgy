package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/schema"
)

// TestPullReportsConfigurationRows pulls a database whose extension has a
// configuration table, made by hand as pg_extension_config_dump does from
// an extension's script: the rows its condition selects are reported as an
// unsupported finding, on stderr and in --json, unless --data pulls them or
// external.relations lists the table.
func TestPullReportsConfigurationRows(t *testing.T) {
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
	const name = "pgytest_cli_pull_config_rows"
	drop := func() { admin.Exec(context.Background(), `drop database if exists `+name+` with (force)`) } //nolint
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
	if _, err := db.Exec(ctx, `
		create extension hstore schema public;
		create table public.cfg (id integer primary key, name text);
		insert into public.cfg values (1, 'shipped'), (100, 'mine'), (101, 'mine too');
		alter extension hstore add table public.cfg;
		update pg_extension set extconfig = array['public.cfg'::regclass::oid], extcondition = array['WHERE id >= 100'] where extname = 'hstore';`); err != nil {
		t.Fatal(err)
	}

	type summary struct {
		Files    []string
		Warnings []struct{ Kind, Message string }
	}
	pullJSON := func(cfg *GlobalConfig, args ...string) (summary, error) {
		cfg.DSN, cfg.MigrationsTable, cfg.JSON = u.String(), defaultMigrationsTable, true
		out, err := captureStdout(t, func() error { return runWith(cfg, cmdPull(), append([]string{"--out", t.TempDir()}, args...)...) })
		var s summary
		if jerr := json.Unmarshal([]byte(out), &s); jerr != nil {
			t.Fatalf("--json output is not JSON: %v\n%s", jerr, out)
		}
		return s, err
	}
	s, err := pullJSON(&GlobalConfig{})
	if err != nil {
		t.Fatal(err)
	}
	want := "extension hstore: configuration table public.cfg has 2 rows that pg_dump keeps and this YAML does not carry"
	if len(s.Warnings) != 1 || s.Warnings[0].Kind != "unsupported" || !strings.Contains(s.Warnings[0].Message, want) || len(s.Files) == 0 {
		t.Errorf("want one unsupported warning %q and the files, got %+v", want, s)
	}
	strict, _, _ := strictSettings(&CIConfig{FailOn: []string{"unsupported"}})
	if _, err := pullJSON(&GlobalConfig{Strict: true, FailOn: strict}); err == nil || !strings.Contains(err.Error(), "kind unsupported") {
		t.Errorf("--strict with ci.fail_on: [unsupported] fails on the report, got %v", err)
	}
	for _, c := range []struct {
		cfg  *GlobalConfig
		args []string
	}{
		{&GlobalConfig{}, []string{"--data", "public.cfg"}},
		{&GlobalConfig{External: &schema.External{Relations: []string{"public.cf?"}}}, nil},
	} {
		s, err := pullJSON(c.cfg, c.args...)
		if err != nil || len(s.Warnings) != 0 {
			t.Errorf("%v: want no warning, got %+v, %v", c.args, s, err)
		}
	}
	c := cmdPull()
	c.Flags().BoolVar(new(bool), "json", false, "")
	err = runWith(&GlobalConfig{DSN: u.String(), JSON: true}, c, "--json")
	if err == nil || !strings.Contains(err.Error(), "needs --out") {
		t.Errorf("--json without --out must be refused, got %v", err)
	}
}
