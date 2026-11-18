package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

var pullExternal = &schema.External{
	Schemas:   []string{"platform", "extensions"},
	Roles:     []string{"plat_admin", "anon"},
	Settings:  []string{"app.settings.*"},
	Relations: []string{"*._hyper_*_chunk"},
}

func externalLive() *diff.Live {
	l := filterLive()
	l.CurrentUser = "app"
	for _, fq := range []string{"app._hyper_1_1_chunk", "app.platform_owned"} {
		l.Tables[fq] = &diff.LiveTable{Columns: map[string]*diff.LiveColumn{"id": {Type: "integer"}}}
		l.Details.ColumnOrder[fq] = []string{"id"}
	}
	l.Owners = map[string]string{"rel:app.notes": "app", "rel:app.platform_owned": "plat_admin", "rel:app._hyper_1_1_chunk": "app", "schema:app": "app"}
	l.Extensions["pg_stat_statements"] = true
	l.ExtensionDefs["pg_stat_statements"] = &diff.LiveExtension{Schema: "app", Owner: "plat_admin"}
	l.ExtensionDefs["pg_trgm"].Owner = "app"
	l.EventTriggers["owned_ddl"] = &diff.LiveEventTrigger{Event: "ddl_command_end", Function: "app.ddl_log", Enabled: "origin", Owner: "plat_admin"}
	l.Publications["owned_pub"] = &diff.LivePublication{Publish: []string{"insert", "update", "delete", "truncate"}, Owner: "plat_admin"}
	l.DatabaseConfig["search_path"] = "public"
	l.DatabaseRoleConfig["api"] = map[string]string{"app.settings.x": "1", "work_mem": "4MB"}
	l.RoleDefs = map[string]*diff.LiveRole{
		"app":        {Login: true, Inherit: true, ConnectionLimit: -1},
		"api":        {Login: true, Inherit: true, ConnectionLimit: -1, Config: map[string]string{"app.settings.y": "2", "statement_timeout": "8s"}},
		"anon":       {Inherit: true, ConnectionLimit: -1},
		"plat_admin": {Superuser: true, Inherit: true, ConnectionLimit: -1},
	}
	l.RoleMembers = map[string]map[string]bool{"api": {"anon": true}}
	l.DefaultACLRows = map[string]bool{"plat_admin|app|tables": true, "app|app|tables": true}
	l.DefaultACLs = map[string]map[string]map[string]bool{
		"plat_admin|app|tables": {"anon": {"select": true}},
		"app|app|tables":        {"anon": {"select": true}},
	}
	return l
}

func TestPullExternalLeavesOutWhatTheApplicationDoesNotOwn(t *testing.T) {
	res, err := Build(externalLive(), Options{Single: true, Roles: true, External: pullExternal})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{"table notes:", "name: pg_trgm", "app_pub:", "app_all:", "mixed:", "empty:", "every:", "app_ddl:", "search_path: public", "work_mem: 4MB", "statement_timeout: 8s", "api:", "inRoles: [anon]", "for: app"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"schema platform", "table users", "_hyper_1_1_chunk", "platform_owned", "pgcrypto", "citext", "pg_stat_statements", "platform:", "platform_ddl", "owned_ddl", "owned_pub", "app.settings", "jwt_secret", "plat_admin", "anon: {statement_timeout"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("%q must be left out:\n%s", unwanted, got)
		}
	}
	if strings.Contains(got, "\n  anon:") {
		t.Errorf("the role entry of an external role must be left out:\n%s", got)
	}
	if len(res.Warnings) > 0 {
		t.Errorf("unexpected warnings: %v", res.Warnings)
	}
}

func TestPullWithoutExternalUnchanged(t *testing.T) {
	res, err := Build(externalLive(), Options{Single: true, Roles: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{"schema platform", "_hyper_1_1_chunk", "platform_owned", "pg_stat_statements", "platform_ddl", "owned_ddl", "owned_pub", "jwt_secret", "plat_admin", "app.settings.y"} {
		if !strings.Contains(got, want) {
			t.Errorf("without an external block %q is pulled; missing in:\n%s", want, got)
		}
	}
}

func TestPullReportsExtensionConfigurationRows(t *testing.T) {
	live := externalLive()
	live.Extensions["pg_cron"] = true
	live.ExtensionDefs["pg_cron"] = &diff.LiveExtension{Schema: "pg_catalog", Owner: "app"}
	live.ExtensionDefs["pgmq"] = &diff.LiveExtension{Schema: "extensions", Owner: "app"}
	live.Extensions["pgmq"] = true
	live.MemberExtension = map[string]string{"rel:cron.job": "pg_cron", "rel:cron.job_run_details": "pg_cron", "rel:cron.empty": "pg_cron", "rel:cron.locked": "pg_cron", "rel:extensions.meta": "pgmq"}
	rows := map[string]int64{"cron.job": 3, "cron.job_run_details": 1, "cron.empty": 0, "cron.locked": -1, "extensions.meta": 2}
	res, err := Build(live, Options{Single: true, External: pullExternal, ConfigRows: rows})
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		"extension pg_cron: configuration table cron.job has 3 rows that pg_dump keeps and this YAML does not carry; a database rebuilt from it lacks them, unless the extension makes them again itself. Pull them with --data cron.job if they are settings to copy; rows that describe objects the extension's functions create at run time (hypertables, partitions, queues) come back when a migration calls those functions instead, and listing the table under external.relations leaves it out of this report",
		"extension pg_cron: configuration table cron.job_run_details has 1 row that pg_dump keeps",
		"extension pg_cron: configuration table cron.locked could not be read",
	} {
		if !strings.Contains(warnings, want) {
			t.Errorf("missing %q in:\n%s", want, warnings)
		}
	}
	if len(res.Warnings) != 3 {
		t.Errorf("want 3 warnings (no empty table, nothing of an extension the YAML doesn't create), got:\n%s", warnings)
	}

	ext := *pullExternal
	ext.Relations = append([]string{"cron.job_run_details", "cron.locked"}, ext.Relations...)
	res, err = Build(live, Options{Single: true, External: &ext, ConfigRows: rows, Data: map[string][]map[string]any{"cron.job": {}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "configuration table") {
			t.Errorf("tables pulled with --data or external are not reported: %s", w)
		}
	}
}
