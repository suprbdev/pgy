package integration

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
)

// TestIntegrationMembershipOptionsPerGrantor changes the options of
// memberships that other roles granted. On PostgreSQL 16 and later, where
// each grantor's grant has options of its own, a superuser revokes an
// option from every grant that has it and sets one in a grant that
// exists, without adding a grant. A role with CREATEROLE and the ADMIN
// option changes its own grant, leaves out the statements on the grant of
// the bootstrap superuser with a not_owner warning, and sets an option the
// membership lacks in a grant of its own that keeps SET off. On
// PostgreSQL 15 a membership has one grant, and the ADMIN option is
// revoked and granted without GRANTED BY. Every plan applies and the
// second diff plans nothing.
func TestIntegrationMembershipOptionsPerGrantor(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	const creator, lead, parent = "pgytest_mg_creator", "pgytest_mg_lead", "pgytest_mg_parent"
	const member, member2, member3 = "pgytest_mg_member", "pgytest_mg_member2", "pgytest_mg_member3"
	dropRoles := func() {
		for _, role := range []string{member, member2, member3, lead, creator, parent} {
			admin.Exec(context.Background(), `drop role if exists `+role) //nolint
		}
	}
	dropRoles()
	t.Cleanup(dropRoles)
	var version int
	var bootstrap string
	if err := admin.QueryRow(ctx, `select current_setting('server_version_num')::int, (select rolname::text from pg_roles where oid = 10)`).Scan(&version, &bootstrap); err != nil {
		t.Fatal(err)
	}
	pg16 := version >= 160000
	setup := []string{
		`create role ` + creator + ` login createrole password 'pgy'`,
		`create role ` + lead,
		`create role ` + parent,
		`create role ` + member,
		`create role ` + member2,
		`create role ` + member3,
		`grant ` + parent + ` to ` + lead + ` with admin option`,
		`grant ` + parent + ` to ` + creator + ` with admin option`,
		`grant ` + parent + ` to ` + member + ` with admin option granted by ` + lead,
		`grant ` + parent + ` to ` + member2 + ` with admin option`,
		`grant ` + parent + ` to ` + member3,
	}
	if pg16 {
		setup = append(setup,
			`grant `+parent+` to `+member+` with inherit false`,
			`grant `+parent+` to `+member2+` granted by `+creator,
			`grant `+parent+` to `+member3+` with inherit false, set false`)
	}
	for _, stmt := range setup {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db := freshDatabase(t, admin, "pgytest_membership_grantors")
	cfg := db.Config().Copy()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = creator, "pgy"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	grants := func(m string) map[string]string {
		t.Helper()
		rows, err := admin.Query(ctx, `select pg_get_userbyid(grantor)::text, format('%s:%s:%s', admin_option::text, coalesce(to_jsonb(am) ->> 'inherit_option', '-'), coalesce(to_jsonb(am) ->> 'set_option', '-'))
			from pg_auth_members am where member = $1::regrole and roleid = $2::regrole`, m, parent)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var grantor, options string
			if err := rows.Scan(&grantor, &options); err != nil {
				t.Fatal(err)
			}
			out[grantor] = options
		}
		return out
	}
	wantGrants := func(m string, want map[string]string, why string) {
		t.Helper()
		if got := grants(m); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %s has the grants %v, want %v", why, m, got, want)
		}
	}
	step := func(pool *pgxpool.Pool, y string, want []string, warned int) {
		t.Helper()
		y = strings.NewReplacer("PARENT", parent, "MEMBER2", member2, "MEMBER3", member3, "MEMBER", member).Replace(y)
		plan := planExtensionsLive(t, pool, y, diff.Options{})
		sql := diff.Render(plan)
		for _, w := range want {
			if !strings.Contains(sql, w) {
				t.Errorf("missing %q in:\n%s", w, sql)
			}
		}
		if got := notOwnerMessages(plan); len(got) != warned {
			t.Errorf("want %d not_owner warnings, got %q", warned, got)
		}
		applyPlan(t, pool, plan)
		again := planExtensionsLive(t, pool, y, diff.Options{})
		if sql := diff.Render(again); sql != "" {
			t.Errorf("a second diff plans nothing, got:\n%s", sql)
		}
		if got := notOwnerMessages(again); len(got) != warned {
			t.Errorf("and warns the same, got %q", got)
		}
	}
	q := func(s string) string { return `"` + s + `"` }

	if !pg16 {
		step(db, `
roles:
  MEMBER: {inRoles: [{role: PARENT, admin: false}]}
`, []string{`revoke admin option for ` + q(parent) + ` from ` + q(member) + `;`}, 0)
		step(pool, `
roles:
  MEMBER2: {inRoles: [{role: PARENT, admin: false}]}
  MEMBER3: {inRoles: [{role: PARENT, admin: true}]}
`, []string{`revoke admin option for ` + q(parent) + ` from ` + q(member2) + `;`, `grant ` + q(parent) + ` to ` + q(member3) + ` with admin option;`}, 0)
		for m, admin := range map[string]bool{member: false, member2: false, member3: true} {
			for grantor, options := range grants(m) {
				if strings.HasPrefix(options, "true") != admin {
					t.Errorf("%s: the grant of %s has %s, want the ADMIN option %t", m, grantor, options, admin)
				}
			}
		}
		return
	}

	step(db, `
roles:
  MEMBER: {inRoles: [{role: PARENT, admin: false, inherit: false, set: false}]}
`, []string{
		`revoke admin option for ` + q(parent) + ` from ` + q(member) + ` granted by ` + q(lead) + `;`,
		`revoke inherit option for ` + q(parent) + ` from ` + q(member) + ` granted by ` + q(lead) + `;`,
		`revoke set option for ` + q(parent) + ` from ` + q(member) + ` granted by ` + q(bootstrap) + `;`,
		`revoke set option for ` + q(parent) + ` from ` + q(member) + ` granted by ` + q(lead) + `;`,
	}, 0)
	wantGrants(member, map[string]string{bootstrap: "false:false:false", lead: "false:false:false"}, "every grant loses the options")
	step(db, `
roles:
  MEMBER: {inRoles: [{role: PARENT, admin: true, inherit: true, set: true}]}
`, []string{`grant ` + q(parent) + ` to ` + q(member) + ` with admin true, inherit true, set true granted by `}, 0)
	if g := grants(member); len(g) != 2 || g[bootstrap] != "true:true:true" && g[lead] != "true:true:true" {
		t.Errorf("an existing grant gets the options, without a new one: %v", g)
	}

	step(pool, `
roles:
  MEMBER2: {inRoles: [{role: PARENT, admin: false, inherit: false}]}
  MEMBER3: {inRoles: [{role: PARENT, inherit: true}]}
`, []string{
		`revoke inherit option for ` + q(parent) + ` from ` + q(member2) + ` granted by ` + q(creator) + `;`,
		`grant ` + q(parent) + ` to ` + q(member3) + ` with inherit true, set false;`,
	}, 1)
	wantGrants(member2, map[string]string{bootstrap: "true:true:true", creator: "false:false:true"}, "only the connecting role's grant changes")
	wantGrants(member3, map[string]string{bootstrap: "false:false:false", creator: "false:true:false"}, "a new grant of the connecting role sets INHERIT and keeps SET off")
}
