package integration

import (
	"context"
	"sort"
	"strings"
	"testing"
)

const settingValuesYAML = `
schema SCH:
  function cfg():
    returns: int
    language: sql
    set:
      plpgsql.extra_warnings: all
      application_name: "a,b"
      myapp.code: "my ($a,$b) = (1,2);"
      myapp.motto: "it's"
      myapp.literal: "'x, y'"
      myapp.tenant: ""
      myapp.ratio: 0.5
      datestyle: "'ISO', 'MDY'"
      search_path: ""
      statement_timeout: 0
      enable_seqscan: false
    body: select 1
  procedure tidy():
    language: sql
    set: {log_statement: all, work_mem: 64, jit: true, search_path: "SCH, public"}
    body: select 1
`

func TestIntegrationSettingValuesStoredAsWritten(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	convergeYAML(t, pool, sch, settingValuesYAML)
	for routine, want := range map[string][]string{
		"cfg": {
			"DateStyle=ISO, MDY", "application_name=a,b", "enable_seqscan=false", "myapp.code=my ($a,$b) = (1,2);", "myapp.literal=x, y",
			"myapp.motto=it's", "myapp.ratio=0.5", "myapp.tenant=", "plpgsql.extra_warnings=all", `search_path=""`, "statement_timeout=0",
		},
		"tidy": {"jit=true", "log_statement=all", "search_path=" + sch + ", public", "work_mem=64"},
	} {
		var config []string
		if err := pool.QueryRow(context.Background(), `select proconfig from pg_proc where proname = $1 and pronamespace = $2::regnamespace`, routine, sch).Scan(&config); err != nil {
			t.Fatal(err)
		}
		sort.Strings(config)
		if strings.Join(config, "|") != strings.Join(want, "|") {
			t.Errorf("%s: proconfig\n %q\nwant\n %q", routine, config, want)
		}
	}
}

func TestIntegrationSettingValuesIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{"values": settingValuesYAML})
}

func TestIntegrationDatabaseSettingValues(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_dbsettingvalues")
	role := "pgytest_setting_values"
	if _, err := admin.Exec(ctx, `do $$ begin if not exists (select from pg_roles where rolname = '`+role+`') then create role `+role+`; end if; end $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), `drop role if exists `+role) //nolint
	})
	convergeYAML(t, db, "app", strings.ReplaceAll(`
roles:
  ROLE:
    set: {pg_stat_statements.track: all, application_name: "a,b"}
database:
  set: {plpgsql.extra_warnings: all, myapp.tenant: "", myapp.motto: "it's, really", myapp.ratio: 0.25}
  roles:
    ROLE: {plpgsql.extra_errors: all, myapp.flag: true}
schema SCH:
  table t:
    columns: {id: {type: int}}
`, "ROLE", role))
	rows, err := db.Query(ctx, `select coalesce(r.rolname, '') || '/' || (s.setdatabase <> 0)::text || '/' || c
		from pg_db_role_setting s left join pg_roles r on r.oid = s.setrole, unnest(s.setconfig) c
		where (s.setdatabase = 0 and r.rolname = $1) or s.setdatabase = (select oid from pg_database where datname = current_database())
		order by 1`, role)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	want := []string{
		"/true/myapp.motto=it's, really", "/true/myapp.ratio=0.25", "/true/myapp.tenant=", "/true/plpgsql.extra_warnings=all",
		role + "/false/application_name=a,b", role + "/false/pg_stat_statements.track=all",
		role + "/true/myapp.flag=true", role + "/true/plpgsql.extra_errors=all",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("stored settings\n %q\nwant\n %q", got, want)
	}
}
