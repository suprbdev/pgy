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
		create function x.rn() returns bigint language internal window as 'window_row_number';
		create aggregate x.my_mode(order by anyelement) (sfunc = ordered_set_transition, stype = internal, finalfunc = mode_final, finalfunc_extra);`); err != nil {
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
		"function x.rn is written in internal and cannot be pulled",
		"ordered-set aggregate x.my_mode(ORDER BY anyelement) is not supported by pgy and was left out",
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

func TestIntegrationPullReportsTablespaces(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Exec(ctx, `drop tablespace if exists pgytest_pull_ts`)
	if _, err := conn.Exec(ctx, `set allow_in_place_tablespaces = on`); err != nil {
		conn.Release()
		t.Skipf("in-place tablespaces unavailable: %v", err)
	}
	_, err = conn.Exec(ctx, `create tablespace pgytest_pull_ts location ''`)
	_, _ = conn.Exec(ctx, `reset allow_in_place_tablespaces`)
	conn.Release()
	if err != nil {
		t.Skipf("cannot create a tablespace: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `drop tablespace if exists pgytest_pull_ts`) })
	db := freshDatabase(t, admin, "pgytest_pull_tablespaces")
	if _, err := db.Exec(ctx, `
		create schema x;
		create table x.t (id int primary key using index tablespace pgytest_pull_ts, v int) tablespace pgytest_pull_ts;
		create index t_v on x.t (v) tablespace pgytest_pull_ts;
		create table x.plain (id int);
		create materialized view x.mv tablespace pgytest_pull_ts as select 1 as one;`); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(introspectAll(t, db), pull.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		"table x.t is stored in tablespace pgytest_pull_ts, which pgy does not manage",
		"index x.t_pkey is stored in tablespace pgytest_pull_ts",
		"index x.t_v is stored in tablespace pgytest_pull_ts",
		"materialized view x.mv is stored in tablespace pgytest_pull_ts",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "x.plain") {
		t.Errorf("tables in the default tablespace aren't reported:\n%s", got)
	}
}
