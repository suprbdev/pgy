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
		create extension hstore;
		create schema x;
		create server x_remote foreign data wrapper postgres_fdw;
		create foreign table x.remote_items (id int) server x_remote;
		create function x.same(a int, b int) returns boolean language sql immutable as 'select a = b';
		create operator x.=== (leftarg = int, rightarg = int, function = x.same);
		create table x.t (id int);
		create type x.amount as (v numeric);
		create function x.amount_num(x.amount) returns numeric language sql immutable as 'select ($1).v';
		create cast (x.amount as numeric) with function x.amount_num(x.amount) as assignment;
		create type x.addr as (street text, city text);
		create table x.addresses of x.addr (primary key (street));
		create function x.rn() returns bigint language internal window as 'window_row_number';`); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(introspectAll(t, db), pull.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		"operator x.===(integer, integer) is not supported by pgy and was left out",
		"cast (x.amount as numeric) is not supported by pgy and was left out",
		"table x.addresses is a typed table (OF x.addr)",
		"function x.rn is written in internal and cannot be pulled",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "remote_items") {
		t.Errorf("foreign tables are pulled, not reported:\n%s", got)
	}
	if strings.Contains(got, "operator class") || strings.Contains(got, "gist") || strings.Contains(got, "hstore") {
		t.Errorf("extension members must not be reported:\n%s", got)
	}
}
