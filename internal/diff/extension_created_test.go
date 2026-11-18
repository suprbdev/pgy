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

// probed returns live with the ExtensionObjects that ProbeExtensionObjects
// reads for a plan creating pg_cron: its schema cron and its table cron.job
// with row level security and cron_job_policy, and for one creating
// postgis: its table public.spatial_ref_sys with a primary key and a
// check, both with the grants their scripts give.
func probed(live *Live, extensions ...string) *Live {
	objs := emptyLive()
	objs.ExtensionMembers, objs.MemberExtension = map[string]bool{}, map[string]string{}
	objs.Owners, objs.RelComments = map[string]string{}, map[string]string{}
	member := func(ext, key string) {
		objs.ExtensionMembers[key], objs.MemberExtension[key] = true, ext
		objs.Owners[key] = "owner"
	}
	for _, ext := range extensions {
		switch ext {
		case "pg_cron":
			objs.Schemas["cron"] = true
			member(ext, "schema:cron")
			lt := newLiveTable()
			lt.Columns["jobid"] = &LiveColumn{Type: "bigint"}
			lt.Columns["username"] = &LiveColumn{Type: "text"}
			lt.HasPK, lt.Constraints["job_pkey"] = true, true
			lt.RLSEnabled = true
			lt.Policies["cron_job_policy"] = &LivePolicy{Cmd: "all", Using: "(username = CURRENT_USER)"}
			objs.Tables["cron.job"] = lt
			objs.TableGrants["cron.job"] = map[string]map[string]bool{"public": {"select": true}}
			member(ext, "rel:cron.job")
		case "postgis":
			lt := newLiveTable()
			for _, c := range []string{"srid", "auth_srid"} {
				lt.Columns[c] = &LiveColumn{Type: "integer"}
			}
			lt.Columns["srtext"] = &LiveColumn{Type: "character varying(2048)", Nullable: true}
			lt.HasPK, lt.Constraints["spatial_ref_sys_pkey"] = true, true
			lt.Constraints["spatial_ref_sys_srid_check"] = true
			lt.ConstraintDefs["spatial_ref_sys_srid_check"] = "CHECK (((srid > 0) AND (srid <= 998999)))"
			objs.Tables["public.spatial_ref_sys"] = lt
			objs.TableGrants["public.spatial_ref_sys"] = map[string]map[string]bool{"public": {"select": true}}
			member(ext, "rel:public.spatial_ref_sys")
		}
	}
	names := map[string]bool{}
	for _, ext := range extensions {
		names[ext] = true
	}
	live.ExtensionObjects = &ExtensionObjects{Declared: names, Extensions: names, Objects: objs}
	return live
}

func TestExtensionObjectsPoliciesOfCreatedExtension(t *testing.T) {
	sql, warns := planSQL(t, probed(memberLive(), "pg_cron"), cronJobDeclaration(), Options{Unsafe: true, Prune: true})
	wantSQL(t, sql, `create extension if not exists "pg_cron" schema "pg_catalog";`)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	d := cronJobDeclaration()
	d.Tables["cron.job"].Policies = []*schema.Policy{{Name: "job_readers", For: "select", Using: "true"}}
	d.Tables["cron.job"].Grants = map[string][]string{"public": {}}
	sql, _ = planSQL(t, probed(memberLive(), "pg_cron"), d, Options{})
	wantSQL(t, sql,
		`create extension if not exists "pg_cron" schema "pg_catalog";`,
		`create policy "job_readers" on "cron"."job" for select using (true);`,
		`revoke select on table "cron"."job" from public;`,
		`drop policy "cron_job_policy" on "cron"."job";`,
	)
}

func TestExtensionObjectsTableDeclaredWithColumns(t *testing.T) {
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "postgis", Schema: "public"}},
		Tables: map[string]*schema.Table{
			"public.spatial_ref_sys": {Name: "spatial_ref_sys",
				Columns: map[string]*schema.Column{
					"srid":      {Type: "integer"},
					"auth_srid": {Type: "integer", Nullable: true},
					"srtext":    {Type: "varchar(2048)", Nullable: true},
				},
				PrimaryKey:  []string{"srid"},
				Constraints: []*schema.Constraint{{Name: "spatial_ref_sys_srid_check", Type: "check", Expression: "srid > 0 AND srid <= 998999"}},
				Data:        &schema.TableData{Rows: []map[string]any{{"srid": 990001, "auth_srid": 990001, "srtext": "LOCAL_CS[\"x\"]"}}},
			},
		},
	}
	sql, warns := planSQL(t, probed(memberLive(), "postgis"), d, Options{})
	wantSQL(t, sql,
		`create extension "postgis" schema "public";`,
		`insert into "public"."spatial_ref_sys" ("auth_srid", "srid", "srtext") values (990001, 990001, 'LOCAL_CS["x"]') on conflict ("srid") do update set "auth_srid" = excluded."auth_srid", "srtext" = excluded."srtext";`,
	)
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "table public.spatial_ref_sys belongs to extension postgis") {
		t.Errorf("want the warning about the extension's table, got %v", warns)
	}

	sql, warns = planSQL(t, offlineLive(), d, Options{})
	if !strings.Contains(sql, `create table if not exists "public"."spatial_ref_sys"`) {
		t.Errorf("without a database the table is planned as declared, got:\n%s", sql)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "diff --from-empty cannot tell which tables extension postgis creates, and the YAML declares public.spatial_ref_sys with columns and data rows") {
		t.Errorf("want the --from-empty warning, got %v", warns)
	}
	d.Tables["public.spatial_ref_sys"].Data = nil
	if _, warns := planSQL(t, offlineLive(), d, Options{}); len(warns) > 0 {
		t.Errorf("a table without rows gets no warning, got %v", warns)
	}
}

func TestExtensionObjectsOnlyForTheirPlan(t *testing.T) {
	d := cronJobDeclaration()
	d.Extensions = nil
	sql, warns := planSQL(t, probed(memberLive(), "pg_cron"), d, Options{})
	wantSQL(t, sql)
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "table cron.job has no columns") {
		t.Errorf("a plan that doesn't create the extension doesn't see its objects, got %v", warns)
	}

	prev := &schema.Database{Extensions: cronJobDeclaration().Extensions}
	live := Simulate(probed(memberLive(), "pg_cron"), prev)
	sql, warns = planSQL(t, live, cronJobDeclaration(), Options{})
	wantSQL(t, sql)
	if len(warns) > 0 {
		t.Errorf("a step after the one that creates the extension sees its objects, got %v", warns)
	}
	if live := probed(memberLive(), "pg_cron"); withExtensionObjects(live, cronJobDeclaration()) == live || live.Tables["cron.job"] != nil || live.ExtensionMembers["rel:cron.job"] {
		t.Error("withExtensionObjects must return a copy and leave live as it is")
	}
}

func TestExtensionObjectsDeclarationOfNoObject(t *testing.T) {
	d := cronJobDeclaration()
	d.Tables["cron.jobs"] = &schema.Table{Name: "jobs", RowLevelSecurity: bptr(true)}
	sql, warns := planSQL(t, probed(memberLive(), "pg_cron"), d, Options{})
	wantSQL(t, sql, `create extension if not exists "pg_cron" schema "pg_catalog";`)
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "table cron.jobs has no columns") {
		t.Errorf("a declaration of an object the extension doesn't create is reported, got %v", warns)
	}
	d.Extensions = append(d.Extensions, &schema.Extension{Name: "citext"})
	if _, warns := planSQL(t, probed(memberLive(), "pg_cron"), d, Options{}); len(warns) > 0 {
		t.Errorf("with an extension the probe didn't create, the declaration may be about one of its objects, got %v", warns)
	}
}

func TestExtensionProbeSetup(t *testing.T) {
	live := memberLive()
	d := &schema.Database{Extensions: []*schema.Extension{
		{Name: "postgis_topology", Schema: "topology", DependsOn: []string{"extension postgis"}},
		{Name: "postgis", Schema: "gis"},
		{Name: "pgcrypto"},
	}}
	live.Extensions["pgcrypto"] = true
	setup, declared, schemas := extensionProbeSetup(live, d)
	want := []string{`create schema if not exists "gis";`, `create schema if not exists "topology";`, `create extension "postgis" schema "gis";`, `create extension "postgis_topology" schema "topology";`}
	if strings.Join(setup, "\n") != strings.Join(want, "\n") || len(declared) != 2 || !declared["postgis"] || !schemas["gis"] || !schemas["topology"] {
		t.Errorf("got %v %v %v", setup, declared, schemas)
	}
	live.AvailableExtensions = map[string]*AvailableExtension{"postgis": {DefaultVersion: "3.6.4", Requires: map[string][]string{"3.6.4": nil}}}
	if setup, _, _ := extensionProbeSetup(live, d); setup != nil {
		t.Errorf("an extension the server lacks leaves nothing to probe, got %v", setup)
	}

	before, after := memberLive(), memberLive()
	after.Schemas["gis"], after.Schemas["topology"] = true, true
	after.SchemaGrants["topology"] = map[string]map[string]bool{"public": {"usage": true}}
	after.Tables["topology.topology"] = newLiveTable()
	objs := &Live{}
	mergeObjects(objs, after, before, map[string]bool{"gis": true})
	if objs.Schemas["gis"] || !objs.Schemas["topology"] || objs.SchemaGrants["topology"] == nil || objs.Tables["topology.topology"] == nil || objs.Schemas["public"] {
		t.Errorf("the objects the probe found are what the extensions created, without the schemas it created itself: %+v", objs)
	}
}
