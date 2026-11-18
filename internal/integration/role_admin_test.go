package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
)

// TestIntegrationRoleAdminStatementsLeftOut plans role changes as a role
// with CREATEROLE that is not a superuser. A superuser role's settings,
// and an attribute the connecting role may not change, are left out on
// every version; on PostgreSQL 16 and later so are the settings, comment
// and membership of a role it holds no ADMIN option on, and the revoke of a
// membership a superuser granted. The roles it administers, the one it
// created included, get their changes; the plan applies, the second diff
// plans nothing and warns the same, and a superuser plans everything.
func TestIntegrationRoleAdminStatementsLeftOut(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	const creator, plat, super, managed, own, member, old = "pgytest_ra_creator", "pgytest_ra_plat", "pgytest_ra_super", "pgytest_ra_managed", "pgytest_ra_own", "pgytest_ra_member", "pgytest_ra_old"
	dropRoles := func() {
		for _, role := range []string{own, plat, super, managed, member, old, creator} {
			admin.Exec(context.Background(), `drop role if exists `+role) //nolint
		}
	}
	dropRoles()
	t.Cleanup(dropRoles)
	var version int
	if err := admin.QueryRow(ctx, `select current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`create role ` + creator + ` login createrole password 'pgy'`,
		`create role ` + plat,
		`create role ` + super + ` superuser`,
		`create role ` + managed,
		`create role ` + member,
		`create role ` + old,
		`grant ` + managed + ` to ` + creator + ` with admin option`,
		`grant ` + old + ` to ` + creator + ` with admin option`,
		`grant ` + old + ` to ` + member,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db := freshDatabase(t, admin, "pgytest_role_admin")
	cfg := db.Config().Copy()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = creator, "pgy"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `create role `+own); err != nil {
		t.Fatal(err)
	}

	y := strings.NewReplacer("PLAT", plat, "SUPER", super, "MANAGED", managed, "OWN", own, "MEMBER", member).Replace(`
roles:
  PLAT: {set: {statement_timeout: 8s}, comment: the platform's}
  SUPER: {set: {work_mem: 8MB}}
  MANAGED: {login: true, set: {statement_timeout: 8s}, comment: managed}
  OWN: {bypassRLS: true, set: {work_mem: 8MB}}
  MEMBER: {inRoles: [PLAT, MANAGED]}
`)
	opts := diff.Options{Unsafe: true, Prune: true}
	plan := planExtensionsLive(t, pool, y, opts)
	sql := diff.Render(plan)
	want := []string{
		`alter role "` + managed + `" with login;`,
		`alter role "` + managed + `" set statement_timeout = '8s';`,
		`comment on role "` + managed + `" is 'managed';`,
		`alter role "` + own + `" set work_mem = '8MB';`,
		`grant "` + managed + `" to "` + member + `";`,
	}
	unwanted := []string{`"` + super + `"`, "bypassrls"}
	warned := []string{"role " + own + ": ", "role " + super + ": "}
	if version >= 160000 {
		unwanted = append(unwanted, `"`+plat+`"`, `revoke "`+old+`"`)
		warned = []string{"role " + old + ": ", "role " + own + ": ", "role " + plat + ": ", "role " + super + ": "}
	} else {
		want = append(want, `alter role "`+plat+`" set statement_timeout = '8s';`, `comment on role "`+plat+`"`, `grant "`+plat+`" to "`+member+`";`, `revoke "`+old+`" from "`+member+`";`)
	}
	for _, w := range want {
		if !strings.Contains(sql, w) {
			t.Errorf("missing %q in:\n%s", w, sql)
		}
	}
	for _, u := range unwanted {
		if strings.Contains(sql, u) {
			t.Errorf("%s is left out, got:\n%s", u, sql)
		}
	}
	got := notOwnerMessages(plan)
	if len(got) != len(warned) {
		t.Fatalf("want not_owner warnings for %q, got %q", warned, got)
	}
	for i, w := range warned {
		if !strings.HasPrefix(got[i], w+creator+", the connecting role, ") {
			t.Errorf("want a warning starting %q, got %q", w, got[i])
		}
	}
	applyPlan(t, pool, plan)
	again := planExtensionsLive(t, pool, y, opts)
	if sql := diff.Render(again); sql != "" {
		t.Errorf("a second diff plans nothing, got:\n%s", sql)
	}
	if w := notOwnerMessages(again); len(w) != len(warned) {
		t.Errorf("and warns about the same roles, got %q", w)
	}

	sql = diff.Render(planExtensionsLive(t, db, y, opts))
	for _, w := range []string{`alter role "` + super + `" set work_mem = '8MB';`, `alter role "` + own + `" with bypassrls;`} {
		if !strings.Contains(sql, w) {
			t.Errorf("a superuser plans %q too, got:\n%s", w, sql)
		}
	}
	if version >= 160000 && !strings.Contains(sql, `grant "`+plat+`" to "`+member+`";`) {
		t.Errorf("and the grant, got:\n%s", sql)
	}
}
