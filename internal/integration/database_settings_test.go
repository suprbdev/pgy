package integration

import (
	"context"
	"strings"
	"testing"
)

func TestIntegrationDatabaseSettings(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, pool, "pgytest_dbsettings")
	sch := "app"
	t.Cleanup(func() { db.Exec(context.Background(), "drop schema if exists app cascade") }) //nolint
	y := `
database:
  set: {statement_timeout: 30s, timezone: Europe/Paris, search_path: "app, public"}
schema SCH:
  table t:
    columns: {id: {type: int}}
`
	convergeYAML(t, db, sch, y)
	var tz string
	if err := db.QueryRow(ctx, `select setconfig::text from pg_db_role_setting s join pg_database d on d.oid = s.setdatabase where d.datname = current_database() and s.setrole = 0`).Scan(&tz); err != nil {
		t.Fatal(err)
	}
	if !contains(tz, "TimeZone=Europe/Paris") && !contains(tz, "timezone=Europe/Paris") {
		t.Errorf("database settings = %s", tz)
	}
	sql := convergeYAML(t, db, sch, `
database:
  set: {timezone: Europe/Paris, search_path: "app, public"}
schema SCH:
  table t:
    columns: {id: {type: int}}
`)
	if !contains(sql, "reset statement_timeout") {
		t.Errorf("expected statement_timeout to be reset:\n%s", sql)
	}
}

func TestIntegrationDatabaseRoleSettings(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_dbrolesettings")
	role := "pgytest_dbrole_settings"
	if _, err := admin.Exec(ctx, `do $$ begin if not exists (select from pg_roles where rolname = '`+role+`') then create role `+role+`; end if; end $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), `drop role if exists `+role) //nolint
	})
	y := `
database:
  roles:
    ROLE: {search_path: "app, public", statement_timeout: 10s}
schema SCH:
  table t:
    columns: {id: {type: int}}
`
	convergeYAML(t, db, "app", strings.ReplaceAll(y, "ROLE", role))
	sql := convergeYAML(t, db, "app", strings.ReplaceAll(strings.ReplaceAll(y, "ROLE", role), ", statement_timeout: 10s", ""))
	if !contains(sql, "reset statement_timeout") {
		t.Errorf("expected statement_timeout to be reset:\n%s", sql)
	}
}
