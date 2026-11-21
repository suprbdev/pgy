package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
)

func TestIntegrationPullUserMappingWarns(t *testing.T) {
	admin := connect(t)
	pool := freshDatabase(t, admin, "pgytest_pull_user_mapping")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `create extension if not exists postgres_fdw`); err != nil {
		t.Skipf("postgres_fdw unavailable: %v", err)
	}
	for _, s := range []string{
		`create server loop foreign data wrapper postgres_fdw options (host 'localhost')`,
		`create user mapping for current_user server loop options (user 'someone', password 'secret')`,
		`create user mapping for public server loop`,
	} {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	res, err := pull.Build(introspectAll(t, pool), pull.Options{})
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(res.Warnings, "\n")
	for _, want := range []string{"user mapping for pgy server loop was left out", "user mapping for public server loop was left out"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "secret") {
		t.Errorf("the warning must not show the mapping's options:\n%s", all)
	}
}

func TestIntegrationDomainCheckNotValid(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	validated := func() bool {
		t.Helper()
		var v bool
		if err := pool.QueryRow(ctx, `select convalidated from pg_constraint where conname = 'pct_not_99' and contypid = $1::regtype`, sch+".pct").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	y := `
schema SCH:
  domain pct:
    type: numeric(5,2)
    checks:
      pct_min: VALUE >= 0
      pct_not_99: {check: VALUE <> 99, notValid: true}
  table t:
    dependsOn: [domain SCH.pct]
    columns:
      v: {type: SCH.pct, nullable: true}
`
	convergeYAMLWith(t, pool, sch, y, diff.Options{})
	if validated() {
		t.Fatal("notValid must create the check NOT VALID")
	}
	if _, err := pool.Exec(ctx, `alter domain `+sch+`.pct drop constraint pct_not_99`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into `+sch+`.t values (99)`); err != nil {
		t.Fatal(err)
	}
	if sql := convergeYAMLWith(t, pool, sch, y, diff.Options{}); !strings.Contains(sql, "not valid") {
		t.Fatalf("a missing notValid check is added NOT VALID, which existing values don't stop:\n%s", sql)
	}
	if _, err := pool.Exec(ctx, `delete from `+sch+`.t`); err != nil {
		t.Fatal(err)
	}
	sql := convergeYAMLWith(t, pool, sch, strings.Replace(y, ", notValid: true", "", 1), diff.Options{})
	if !strings.Contains(sql, `validate constraint "pct_not_99"`) || !validated() {
		t.Errorf("removing notValid validates the check:\n%s", sql)
	}
}

func TestIntegrationDefaultPrivilegesLargeObjects(t *testing.T) {
	admin := connect(t)
	pool := freshDatabase(t, admin, "pgytest_dp_large_objects")
	ctx := context.Background()
	var version int
	if err := pool.QueryRow(ctx, "select current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `do $$ begin
		if not exists (select from pg_roles where rolname = 'pgy_fid_reader') then create role pgy_fid_reader nologin; end if;
	end $$`); err != nil {
		t.Fatal(err)
	}
	y := `
defaultPrivileges:
  - largeObjects: {pgy_fid_reader: [all]}
schema SCH: {}
`
	if version < 180000 {
		plan := diff.PlanWith(introspectAll(t, pool), loadYAML(t, "fid", y), diff.Options{})
		warned := false
		for _, w := range plan.Warnings {
			warned = warned || strings.Contains(w.Message, "largeObjects in defaultPrivileges[0] needs PostgreSQL 18")
		}
		if !warned {
			t.Errorf("largeObjects before PostgreSQL 18 must warn, got %v", plan.Warnings)
		}
		return
	}
	sql := convergeYAMLWith(t, pool, "fid", y, diff.Options{})
	if !strings.Contains(sql, `grant select, update on large objects to "pgy_fid_reader"`) {
		t.Errorf("missing the large object defaults in:\n%s", sql)
	}
	var acl string
	if err := pool.QueryRow(ctx, `select defaclacl::text from pg_default_acl where defaclobjtype = 'L'`).Scan(&acl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(acl, "pgy_fid_reader=rw/") {
		t.Errorf("unexpected large object defaults %s", acl)
	}
}
