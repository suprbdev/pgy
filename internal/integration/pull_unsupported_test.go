package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/pull"
)

func TestIntegrationPullReportsUnsupportedObjects(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_pull_unsupported")
	if _, err := db.Exec(ctx, `
		create extension btree_gist;
		create extension postgres_fdw;
		create schema x;
		create server x_remote foreign data wrapper postgres_fdw;
		create foreign table x.remote_items (id int) server x_remote;
		create function x.same(a int, b int) returns boolean language sql immutable as 'select a = b';
		create operator x.=== (leftarg = int, rightarg = int, function = x.same);
		create table x.t (id int);`); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(introspectAll(t, db), pull.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		"operator x.===(integer, integer) is not supported by pgy and was left out",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "remote_items") {
		t.Errorf("foreign tables are pulled, not reported:\n%s", got)
	}
	if strings.Contains(got, "operator class") || strings.Contains(got, "gist") {
		t.Errorf("extension members must not be reported:\n%s", got)
	}
}
