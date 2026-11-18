package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func cronJobDeclaration() *schema.Database {
	return &schema.Database{
		Extensions: []*schema.Extension{{Name: "pg_cron", Schema: "pg_catalog", IfNotExists: true}},
		Tables: map[string]*schema.Table{
			"cron.job": {Name: "job", RowLevelSecurity: bptr(true), Policies: []*schema.Policy{{Name: "cron_job_policy", Using: "(username = CURRENT_USER)"}}},
		},
	}
}

func TestExtensionCreatedTablePoliciesReplaced(t *testing.T) {
	for _, live := range []*Live{memberLive(), offlineLive()} {
		sql, warns := planSQL(t, live, cronJobDeclaration(), Options{})
		wantSQL(t, sql,
			`create extension if not exists "pg_cron" schema "pg_catalog";`,
			`alter table "cron"."job" enable row level security;`,
			`drop policy if exists "cron_job_policy" on "cron"."job";`,
			`create policy "cron_job_policy" on "cron"."job" using ((username = CURRENT_USER));`,
		)
		if len(warns) > 0 {
			t.Errorf("unexpected warnings: %v", warns)
		}
	}
}

func TestExtensionCreatedTablePoliciesWithoutExtensionCreate(t *testing.T) {
	d := cronJobDeclaration()
	d.Extensions = nil
	sql, warns := planSQL(t, memberLive(), d, Options{})
	wantSQL(t, sql)
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "table cron.job has no columns") {
		t.Errorf("want the missing-table warning, got %v", warns)
	}
}

func TestExtensionCreatedTableLivePolicyUnchanged(t *testing.T) {
	live := memberLive()
	live.Schemas["cron"] = true
	lt := newLiveTable()
	lt.RLSEnabled = true
	lt.Policies["cron_job_policy"] = &LivePolicy{Cmd: "all", Using: "(username = CURRENT_USER)"}
	live.Tables["cron.job"] = lt
	live.ExtensionMembers["rel:cron.job"] = true
	live.Extensions["pg_cron"] = true
	live.ExtensionDefs = map[string]*LiveExtension{"pg_cron": {Schema: "pg_catalog", Version: "1.6"}}
	sql, _ := planSQL(t, live, cronJobDeclaration(), Options{})
	wantSQL(t, sql)
	lt.Policies["cron_job_policy"].Using = "(username = SESSION_USER)"
	sql, _ = planSQL(t, live, cronJobDeclaration(), Options{})
	wantSQL(t, sql,
		`drop policy "cron_job_policy" on "cron"."job";`,
		`create policy "cron_job_policy" on "cron"."job" using ((username = CURRENT_USER));`,
	)
}

func TestExtensionCreatedTableRowLevelSecurityOff(t *testing.T) {
	d := cronJobDeclaration()
	d.Tables["cron.job"] = &schema.Table{Name: "job", RowLevelSecurity: bptr(false), ForceRowLevelSecurity: bptr(false)}
	sql, _ := planSQL(t, memberLive(), d, Options{})
	wantSQL(t, sql, `create extension if not exists "pg_cron" schema "pg_catalog";`)
	sql, _ = planSQL(t, memberLive(), d, Options{Unsafe: true})
	wantSQL(t, sql,
		`create extension if not exists "pg_cron" schema "pg_catalog";`,
		`alter table "cron"."job" disable row level security;`,
		`alter table "cron"."job" no force row level security;`,
	)
}

func TestExtensionCreatedTableTriggersReplaced(t *testing.T) {
	d := cronJobDeclaration()
	d.External = &schema.External{Schemas: []string{"cron"}}
	d.Tables["cron.job"] = &schema.Table{Name: "job", Triggers: []*schema.Trigger{
		{Name: "job_audit", Timing: "after", Events: []string{"insert"}, Level: "row", Procedure: "public.audit()", Enabled: "always"},
	}}
	sql, warns := planSQL(t, memberLive(), d, Options{})
	wantSQL(t, sql,
		`create extension if not exists "pg_cron" schema "pg_catalog";`,
		`drop trigger if exists "job_audit" on "cron"."job";`,
		`create trigger "job_audit" AFTER INSERT on "cron"."job" for each row execute procedure public.audit();`,
		`alter table "cron"."job" enable always trigger "job_audit";`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func offlineLive() *Live {
	live := emptyLive()
	live.Offline = true
	return live
}
