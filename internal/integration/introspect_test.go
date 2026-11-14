package integration

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
)

func TestIntegrationIntrospectNoPrivileges(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	owner := freshRole(t, pool, "owner")
	reader := freshRole(t, pool, "reader")
	for _, stmt := range []string{
		fmt.Sprintf(`alter role %q login password 'pgy'`, reader),
		fmt.Sprintf(`create schema %q authorization %q`, sch+"_hidden", owner),
		fmt.Sprintf(`create table %q.t (id int)`, sch+"_hidden"),
		fmt.Sprintf(`create view %q.v as select 1 as x`, sch+"_hidden"),
		fmt.Sprintf(`alter table %q.t owner to %q`, sch+"_hidden", owner),
		fmt.Sprintf(`alter view %q.v owner to %q`, sch+"_hidden", owner),
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), fmt.Sprintf(`drop schema if exists %q cascade`, sch+"_hidden")) //nolint
	})

	u, err := url.Parse(dsn(t))
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(reader, "pgy")
	readerPool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer readerPool.Close()

	live, err := diff.Introspect(ctx, readerPool)
	if err != nil {
		t.Fatal(err)
	}
	if !live.Schemas[sch+"_hidden"] {
		t.Errorf("schema without privileges not introspected")
	}
	if live.Tables[sch+"_hidden.t"] == nil || live.Tables[sch+"_hidden.t"].Columns["id"] == nil {
		t.Errorf("table without privileges not introspected")
	}
	if !live.Views[sch+"_hidden.v"] {
		t.Errorf("view without privileges not introspected")
	}
}
