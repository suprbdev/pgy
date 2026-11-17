package cli

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/schema"
	"github.com/suprbdev/pgy/internal/validate"
)

// TestPullOwnersSatisfiesRequireOwners pulls a database with --owners under
// a policy that requires owners and grants everywhere: the YAML must pass
// require_owners, and the declaration of an extension's function whose
// grants changed must pass both rules.
func TestPullOwnersSatisfiesRequireOwners(t *testing.T) {
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
	const name, role = "pgytest_cli_pull_policy", "pgytest_cli_pull_policy_reader"
	drop := func() {
		admin.Exec(context.Background(), `drop database if exists `+name+` with (force)`) //nolint
		admin.Exec(context.Background(), `drop role if exists `+role)                     //nolint
	}
	drop()
	t.Cleanup(drop)
	for _, stmt := range []string{`create database ` + name, `create role ` + role} {
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
		create extension pgcrypto with schema public;
		grant execute on function public.digest(text, text) to `+role+`;
		create table public.t (id bigint generated always as identity primary key);
		grant usage on sequence public.t_id_seq to `+role+`;
		create function public.on_t() returns trigger language plpgsql as $$ begin return new; end $$;
		create trigger t_ins after insert on public.t for each row execute function public.on_t();`); err != nil {
		t.Fatal(err)
	}

	all := map[string]bool{}
	for _, k := range validate.PolicyKinds {
		all[k] = true
	}
	policy := validate.Policy{RequireOwners: all, RequireGrants: map[string]bool{"functions": true}}
	out := t.TempDir()
	cfg := &GlobalConfig{DSN: u.String(), MigrationsTable: defaultMigrationsTable, Quiet: true, Policy: policy}
	if err := runWith(cfg, cmdPull(), "--owners", "--out", out); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(out, "*.yml"))
	d, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}
	pulled := ""
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		pulled += string(b)
	}
	if !strings.Contains(pulled, "function digest(text, text):") || !strings.Contains(pulled, "sequence t_id_seq:") {
		t.Fatalf("the member's and the identity sequence's grants must be pulled:\n%s", pulled)
	}
	got := []string{}
	for _, issue := range validate.CheckPolicy(d, policy) {
		got = append(got, issue.String())
	}
	findings := strings.Join(got, "\n")
	if strings.Contains(findings, "must declare an owner") || strings.Contains(findings, "digest") || strings.Contains(findings, "t_id_seq") {
		t.Errorf("pulled YAML must pass require_owners, and extension members are exempt:\n%s\n%s", findings, pulled)
	}
	if !strings.Contains(findings, "function public.on_t(): policy: functions must declare a grants block") {
		t.Errorf("require_grants must still apply to the application's function:\n%s", findings)
	}
}
