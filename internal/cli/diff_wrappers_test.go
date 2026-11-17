package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffFromEmptyPlansWrappersAndLanguages(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.yml"), `
extensions: [postgres_fdw, plperl]
languages:
  plpgsql: {grants: {public: [], app_dev: [usage]}}
  plperl: {grants: {app_dev: [usage]}}
foreignDataWrappers:
  postgres_fdw: {grants: {app_owner: [usage]}}
  pg2: {handler: postgres_fdw_handler, validator: postgres_fdw_validator}
servers:
  mirror: {wrapper: pg2}
schema app:
  function one():
    returns: integer
    language: plperl
    body: return 1;
`)
	cfg := &GlobalConfig{SchemaRoot: dir, Schemas: []string{"s.yml"}, BufferPath: "-", Quiet: true}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	out, _ := captureStdout(t, func() error {
		c := cmdDiff()
		c.SetArgs([]string{"--from-empty"})
		c.SilenceUsage, c.SilenceErrors = true, true
		return c.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, cfg))
	})
	os.Stderr = old
	_ = w.Close()
	stderr, _ := io.ReadAll(r)
	at := 0
	for _, want := range []string{
		`create extension if not exists "plperl";`,
		`create extension if not exists "postgres_fdw";`,
		`revoke usage on language "plpgsql" from public;`,
		`grant usage on language "plpgsql" to "app_dev";`,
		`do $$ begin if exists (select from pg_catalog.pg_language where lanname = 'plperl' and lanpltrusted) then grant usage on language "plperl" to "app_dev"; end if; end $$;`,
		`create foreign data wrapper "pg2" handler "postgres_fdw_handler" validator "postgres_fdw_validator";`,
		`grant usage on foreign data wrapper "postgres_fdw" to "app_owner";`,
		`create server "mirror" foreign data wrapper "pg2";`,
		`create function "app"."one"() returns integer language plperl`,
	} {
		i := strings.Index(out[at:], want)
		if i < 0 {
			t.Fatalf("expected %q after the statements before it, got:\n%s", want, out)
		}
		at += i
	}
	if strings.Contains(out, `create foreign data wrapper "postgres_fdw"`) {
		t.Errorf("the wrapper of a declared extension is not created:\n%s", out)
	}
	if strings.Contains(string(stderr), "warning") {
		t.Errorf("unexpected warnings:\n%s", stderr)
	}
}
