package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/validate"
)

// wrapperConnections returns the connection function of every foreign-data
// wrapper of db, "-" for none.
func wrapperConnections(t *testing.T, db *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := db.Query(context.Background(), `select fdwname::text, fdwconnection::regproc::text from pg_foreign_data_wrapper`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, fn string
		if err := rows.Scan(&name, &fn); err != nil {
			t.Fatal(err)
		}
		out[name] = fn
	}
	return out
}

func TestIntegrationPostgres19WrapperConnection(t *testing.T) {
	requireServerVersion(t, 190000)
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_fdw_connection")
	opts := diff.Options{Unsafe: true, Prune: true}

	sql := convergeLive(t, db, `
extensions:
  - {name: postgres_fdw, schema: public}
foreignDataWrappers:
  pg2:
    handler: public.postgres_fdw_handler
    validator: public.postgres_fdw_validator
    connection: public.postgres_fdw_connection
  bare:
    connection: ""
`, opts)
	if !strings.Contains(sql, `create foreign data wrapper "pg2" handler "public"."postgres_fdw_handler" validator "public"."postgres_fdw_validator" connection "public"."postgres_fdw_connection";`) {
		t.Errorf("want the wrapper created with its connection function:\n%s", sql)
	}
	want := map[string]string{"postgres_fdw": "postgres_fdw_connection", "pg2": "postgres_fdw_connection", "bare": "-"}
	if got := wrapperConnections(t, db); len(got) != len(want) || got["pg2"] != want["pg2"] || got["bare"] != want["bare"] || got["postgres_fdw"] != want["postgres_fdw"] {
		t.Fatalf("connection functions after create: %v, want %v", got, want)
	}

	sql = convergeLive(t, db, `
extensions:
  - {name: postgres_fdw, schema: public}
foreignDataWrappers:
  pg2:
    connection: ""
  bare:
    connection: public.postgres_fdw_connection
`, opts)
	for _, stmt := range []string{
		`alter foreign data wrapper "bare" connection "public"."postgres_fdw_connection";`,
		`alter foreign data wrapper "pg2" no connection;`,
	} {
		if !strings.Contains(sql, stmt) {
			t.Errorf("missing %q in:\n%s", stmt, sql)
		}
	}
	if got := wrapperConnections(t, db); got["pg2"] != "-" || got["bare"] != "postgres_fdw_connection" {
		t.Fatalf("connection functions after alter: %v", got)
	}
}

func TestIntegrationPostgres19WrapperConnectionPulled(t *testing.T) {
	requireServerVersion(t, 190000)
	admin := connect(t)
	ctx := context.Background()
	src := freshDatabase(t, admin, "pgytest_fdw_conn_pull")
	dst := freshDatabase(t, admin, "pgytest_fdw_conn_pull_copy")
	if _, err := src.Exec(ctx, `
		create extension postgres_fdw;
		create foreign data wrapper pg2 handler postgres_fdw_handler validator postgres_fdw_validator connection postgres_fdw_connection;
		create foreign data wrapper bare;`); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(introspectAll(t, src), pull.Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	y := string(res.Files[0].Content)
	for _, want := range []string{
		"  bare:\n    handler: \"\"\n    validator: \"\"\n    connection: \"\"\n",
		"  pg2:\n    handler: public.postgres_fdw_handler\n    validator: public.postgres_fdw_validator\n    connection: public.postgres_fdw_connection\n",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("want:\n%s\nin the pulled YAML:\n%s", want, y)
		}
	}
	for _, issue := range validate.Files([]string{writeYAML(t, y)}) {
		if issue.Severity == validate.Error {
			t.Errorf("pgy validate rejects the pulled YAML: %s", issue)
		}
	}
	opts := diff.Options{Unsafe: true, Prune: true}
	if sql := diff.Render(planExtensionsLive(t, src, y, opts)); sql != "" {
		t.Fatalf("pulled YAML must diff clean against its source:\n%s\n%s", sql, y)
	}
	applyPlan(t, dst, planExtensionsLive(t, dst, y, diff.Options{}))
	if sql := diff.Render(planExtensionsLive(t, dst, y, opts)); sql != "" {
		t.Errorf("rebuilt database differs from the pulled YAML:\n%s", sql)
	}
	if got, want := wrapperConnections(t, dst), wrapperConnections(t, src); got["pg2"] != want["pg2"] || got["bare"] != want["bare"] {
		t.Errorf("connection functions were rebuilt as %v, want %v", got, want)
	}
	if a := pgDump(t, "pgytest_fdw_conn_pull"); a != "" {
		if d := dumpDifferences(a, pgDump(t, "pgytest_fdw_conn_pull_copy")); len(d) > 0 {
			t.Errorf("pg_dump of the rebuilt database differs from the original:\n%s\n%s", strings.Join(d, "\n"), y)
		}
	}

	if _, err := dst.Exec(ctx, `
		alter foreign data wrapper pg2 no connection;
		alter foreign data wrapper bare connection postgres_fdw_connection;`); err != nil {
		t.Fatal(err)
	}
	repair := strings.TrimSpace(diff.Render(planExtensionsLive(t, dst, y, opts)))
	if repair != "alter foreign data wrapper \"bare\" no connection;\nalter foreign data wrapper \"pg2\" connection \"public\".\"postgres_fdw_connection\";" {
		t.Errorf("the pulled YAML undoes drift on the connection function, got:\n%s", repair)
	}
}

func TestIntegrationPostgres19AggregateSupportIdempotent(t *testing.T) {
	requireServerVersion(t, 190000)
	assertYAMLIdempotent(t, map[string]string{
		"support": `
schema SCH:
  aggregate total(int8):
    sfunc: int8pl
    stype: int8
    support: int8inc_support
  aggregate counted(*):
    sfunc: int8inc
    stype: int8
    initcond: "0"
    support: pg_catalog.int8inc_support
  aggregate plain(int8):
    sfunc: int8pl
    stype: int8
    support: ""
`,
	})
}

func TestIntegrationPostgres19AggregateSupportChanged(t *testing.T) {
	requireServerVersion(t, 190000)
	pool := connect(t)
	sch := freshSchema(t, pool)
	y := func(support string) string {
		return strings.ReplaceAll(`
schema SCH:
  function total_support(internal):
    returns: internal
    language: internal
    body: int8inc_support
  aggregate total(int8):
    sfunc: int8pl
    stype: int8
    support: `+support+`
`, "SCH", sch)
	}
	supportOf := func() string {
		var s string
		if err := pool.QueryRow(context.Background(), `select prosupport::regproc::text from pg_proc where oid = $1::regprocedure`, sch+".total(int8)").Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, c := range []struct{ support, want string }{
		{`""`, "-"},
		{sch + ".total_support", sch + ".total_support"},
		{"int8inc_support", "int8inc_support"},
		{`""`, "-"},
	} {
		applyPlan(t, pool, planExtensionsLive(t, pool, y(c.support), diff.Options{}))
		if sql := diff.Render(planExtensionsLive(t, pool, y(c.support), diff.Options{})); sql != "" {
			t.Errorf("support %s: the second plan is not empty:\n%s", c.support, sql)
		}
		if got := supportOf(); got != c.want {
			t.Errorf("support %s: have %s, want %s", c.support, got, c.want)
		}
	}
}
