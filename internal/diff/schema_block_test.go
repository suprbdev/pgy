package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestSchemaBlockCreate(t *testing.T) {
	live := memberLive()
	d := &schema.Database{Schemas: map[string]bool{"reports": true}}
	sql, warns := planSQL(t, live, d, Options{})
	wantSQL(t, sql, `create schema if not exists "reports";`)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	live.Offline = true
	sql, _ = planSQL(t, live, d, Options{})
	wantSQL(t, sql, `create schema if not exists "reports";`)
}

func TestSchemaBlockSkippedIfExists(t *testing.T) {
	live := memberLive()
	live.Schemas["reports"] = true
	d := &schema.Database{Schemas: map[string]bool{"reports": true}}
	if sql, _ := planSQL(t, live, d, Options{Unsafe: true, Prune: true}); strings.TrimSpace(sql) != "" {
		t.Errorf("an existing schema needs nothing:\n%s", sql)
	}
}

func TestSchemaBlockAttributesCreate(t *testing.T) {
	live := memberLive()
	live.Roles["reader"], live.Roles["app_owner"] = true, true
	d := &schema.Database{
		SchemaGrants:   map[string]map[string][]string{"reports": {"reader": {"usage"}}},
		SchemaComments: map[string]string{"audit": "Audit trail."},
		SchemaOwners:   map[string]string{"billing": "app_owner"},
	}
	sql, _ := planSQL(t, live, d, Options{})
	for _, s := range []string{"audit", "billing", "reports"} {
		create := strings.Index(sql, `create schema if not exists "`+s+`";`)
		if create < 0 {
			t.Fatalf("schema %s is declared, so it is created:\n%s", s, sql)
		}
		if use := strings.Index(sql, `schema "`+s+`" `); use >= 0 && use < create {
			t.Errorf("schema %s is created before its attributes are set:\n%s", s, sql)
		}
	}
	for _, want := range []string{
		`grant usage on schema "reports" to "reader";`,
		`comment on schema "audit" is 'Audit trail.';`,
		`alter schema "billing" owner to "app_owner";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestSchemaBlockSystemAndExternalNotCreated(t *testing.T) {
	live := memberLive()
	d := &schema.Database{
		Schemas:      map[string]bool{"public": true, "pg_catalog": true, "information_schema": true, "auth": true, "pg_temp_x": true},
		SchemaGrants: map[string]map[string][]string{"storage": {"app": {"usage"}}},
	}
	opts := Options{External: &schema.External{Schemas: []string{"auth", "storage"}}}
	if sql, _ := planSQL(t, live, d, opts); strings.Contains(sql, "create schema") {
		t.Errorf("system, public and external schemas are never created:\n%s", sql)
	}
}

func TestSchemaBlockAfterPendingExtension(t *testing.T) {
	live := memberLive()
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "pg_cron"}},
		Schemas:    map[string]bool{"cron": true, "reports": true},
		Tables: map[string]*schema.Table{
			"app.t": {Name: "app.t", ColumnsSet: true, Columns: map[string]*schema.Column{"id": {Type: "int"}}},
		},
	}
	sql, _ := planSQL(t, live, d, Options{})
	ext := strings.Index(sql, "create extension")
	if app := strings.Index(sql, `create schema if not exists "app";`); app < 0 || app > ext {
		t.Errorf("a schema of objects pgy creates comes first:\n%s", sql)
	}
	for _, s := range []string{"cron", "reports"} {
		if at := strings.Index(sql, `create schema if not exists "`+s+`";`); at < ext {
			t.Errorf("an extension the plan creates may create schema %s itself, so the schema block's CREATE SCHEMA IF NOT EXISTS follows it:\n%s", s, sql)
		}
	}
}

func TestSchemaBlockPruneScope(t *testing.T) {
	live := memberLive()
	live.CurrentUser = "pgy"
	for _, s := range []string{"reports", "app"} {
		live.Schemas[s] = true
	}
	for _, fq := range []string{"reports.old", "public.old", "app.old", "app.kept"} {
		live.Tables[fq] = &LiveTable{Columns: map[string]*LiveColumn{"id": {Type: "integer"}}}
	}
	d := &schema.Database{
		Schemas:      map[string]bool{"reports": true, "app": true},
		SchemaGrants: map[string]map[string][]string{"public": {"pgy": {"usage"}}},
		Tables: map[string]*schema.Table{
			"app.kept": {Name: "app.kept", ColumnsSet: true, Columns: map[string]*schema.Column{"id": {Type: "integer"}}},
		},
	}
	sql, _ := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	if !strings.Contains(sql, `drop table "app"."old";`) {
		t.Errorf("a schema the YAML declares objects in is pruned:\n%s", sql)
	}
	if strings.Contains(sql, `"reports"."old"`) || strings.Contains(sql, `"public"."old"`) {
		t.Errorf("an empty or grants-only schema block does not put the schema's objects in reach of --prune:\n%s", sql)
	}
}

func TestSchemaBlockSimulatedAsCreated(t *testing.T) {
	live := memberLive()
	d := &schema.Database{Schemas: map[string]bool{"reports": true}, SchemaComments: map[string]string{"audit": "Audit trail."}}
	next := Simulate(live, d)
	if !next.Schemas["reports"] || !next.Schemas["audit"] {
		t.Fatalf("a step that plans a schema block leaves the schema behind: %v", next.Schemas)
	}
	if sql, _ := planSQL(t, next, d, Options{}); strings.Contains(sql, "create schema") {
		t.Errorf("the next step doesn't create it again:\n%s", sql)
	}
}
