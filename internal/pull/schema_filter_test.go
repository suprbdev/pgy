package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func filterLive() *diff.Live {
	all := []string{"insert", "update", "delete", "truncate"}
	return &diff.Live{
		Schemas: map[string]bool{"app": true, "platform": true, "extensions": true},
		Tables: map[string]*diff.LiveTable{
			"app.notes":      {Columns: map[string]*diff.LiveColumn{"id": {Type: "integer"}}},
			"platform.users": {Columns: map[string]*diff.LiveColumn{"id": {Type: "integer"}}},
		},
		Extensions: map[string]bool{"pgcrypto": true, "citext": true, "cube": true, "earthdistance": true, "pg_trgm": true, "postgres_fdw": true, "plpgsql": true},
		ExtensionDefs: map[string]*diff.LiveExtension{
			"pgcrypto":      {Schema: "extensions"},
			"citext":        {Schema: "extensions"},
			"cube":          {Schema: "extensions"},
			"earthdistance": {Schema: "extensions", Requires: []string{"cube"}},
			"pg_trgm":       {Schema: "app"},
			"postgres_fdw":  {Schema: "extensions"},
			"plpgsql":       {Schema: "pg_catalog"},
		},
		MemberExtension: map[string]string{"fdw:postgres_fdw": "postgres_fdw"},
		Servers:         map[string]*diff.LiveServer{"remote": {Wrapper: "postgres_fdw"}},
		Publications: map[string]*diff.LivePublication{
			"app_pub":  {Publish: all, Tables: map[string]diff.LivePublicationTable{"app.notes": {}}},
			"app_all":  {Publish: all, Schemas: []string{"app"}},
			"platform": {Publish: all, Tables: map[string]diff.LivePublicationTable{"platform.users": {}}},
			"mixed":    {Publish: all, Tables: map[string]diff.LivePublicationTable{"app.notes": {}, "platform.users": {}}},
			"empty":    {Publish: all},
			"every":    {Publish: all, AllTables: true},
			"seqs":     {Publish: all, AllSequences: true},
		},
		EventTriggers: map[string]*diff.LiveEventTrigger{
			"app_ddl":      {Event: "ddl_command_end", Function: "app.ddl_log", Enabled: "origin"},
			"platform_ddl": {Event: "ddl_command_end", Function: "platform.watch", Enabled: "origin"},
		},
		DatabaseConfig:     map[string]string{"app.settings.jwt_secret": "secret"},
		DatabaseRoleConfig: map[string]map[string]string{"anon": {"statement_timeout": "3s"}},
		Details: &diff.LiveDetails{
			ColumnOrder:    map[string][]string{"app.notes": {"id"}, "platform.users": {"id"}},
			ExtensionUsers: map[string]map[string]bool{"citext": {"app": true}, "earthdistance": {"app": true}, "pgcrypto": {"platform": true}},
		},
	}
}

func TestPullSchemaFilterDatabaseObjects(t *testing.T) {
	for _, opts := range []Options{{Schemas: []string{"app"}}, {ExcludeSchemas: []string{"platform", "extensions"}}} {
		opts.Single = true
		res, err := Build(filterLive(), opts)
		if err != nil {
			t.Fatal(err)
		}
		got := string(res.Files[0].Content)
		for _, want := range []string{"name: citext", "name: cube", "name: earthdistance", "name: pg_trgm", "name: postgres_fdw", "app_pub:", "app_all:", "app_ddl:", "remote:"} {
			if !strings.Contains(got, want) {
				t.Errorf("%+v: missing %q in:\n%s", opts, want, got)
			}
		}
		for _, unwanted := range []string{"pgcrypto", "platform:", "mixed:", "empty:", "every:", "seqs:", "platform_ddl", "database:", "jwt_secret", "schema platform"} {
			if strings.Contains(got, unwanted) {
				t.Errorf("%+v: %q must be left out:\n%s", opts, unwanted, got)
			}
		}
		warnings := strings.Join(res.Warnings, "\n")
		for _, want := range []string{
			"publication mixed also publishes platform.users, outside the pulled schemas, so it was left out",
			"publications: lists only the publications of the pulled schemas; pgy diff --unsafe --prune with this YAML would drop the others: empty, every, mixed, platform, seqs",
			"eventTriggers: lists only the event triggers of the pulled schemas; pgy diff --unsafe --prune with this YAML would drop the others: platform_ddl",
		} {
			if !strings.Contains(warnings, want) {
				t.Errorf("%+v: missing warning %q in:\n%s", opts, want, warnings)
			}
		}
	}

	res, err := Build(filterLive(), Options{Schemas: []string{"app"}, DatabaseSettings: true, Single: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Files[0].Content); !strings.Contains(got, "app.settings.jwt_secret: secret") || !strings.Contains(got, "anon: {statement_timeout: 3s}") {
		t.Errorf("DatabaseSettings must write the database block:\n%s", got)
	}

	res, err = Build(filterLive(), Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{"name: pgcrypto", "mixed:", "empty:", "every:", "seqs:", "platform_ddl:", "jwt_secret"} {
		if !strings.Contains(got, want) {
			t.Errorf("a pull of every schema writes everything; missing %q in:\n%s", want, got)
		}
	}
	if len(res.Warnings) > 0 {
		t.Errorf("a pull of every schema must not warn: %v", res.Warnings)
	}
}
