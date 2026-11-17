package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

var externalPlatform = &schema.External{
	Schemas:   []string{"plat"},
	Roles:     []string{"plat_admin", "plat_reader", "anon"},
	Settings:  []string{"pgrst.*"},
	Relations: []string{"*._hyper_*_chunk", "queue.q_*"},
}

// platformLive has a platform schema plat with tables users, objects and
// buckets, a function plat.uid() and a policy of the platform on
// plat.objects, and public.todos of the application.
func platformLive() *Live {
	l := emptyLive()
	l.Schemas["plat"], l.Schemas["public"] = true, true
	l.CurrentUser = "app"
	for _, name := range []string{"users", "objects", "buckets"} {
		lt := newLiveTable()
		lt.Columns["id"] = &LiveColumn{Type: "uuid"}
		lt.HasPK = true
		l.Tables["plat."+name] = lt
	}
	l.Tables["plat.users"].Columns["email"] = &LiveColumn{Type: "text", Nullable: true}
	l.Tables["plat.users"].Indexes["users_email_idx"] = true
	l.Tables["plat.users"].IndexDefs["users_email_idx"] = &LiveIndex{}
	l.Tables["plat.users"].Triggers["platform_audit"] = "CREATE TRIGGER platform_audit AFTER UPDATE ON plat.users FOR EACH ROW EXECUTE FUNCTION plat.audit()"
	l.Tables["plat.objects"].RLSEnabled = true
	l.Tables["plat.objects"].Policies["platform_policy"] = &LivePolicy{Cmd: "all", Using: "true"}
	l.Functions["plat.uid()"] = true
	l.FunctionDefs["plat.uid()"] = &LiveFunction{Signature: "plat.uid()", Kind: "f", Body: "select null::uuid", Language: "sql", Volatility: "stable", Security: "invoker", Parallel: "unsafe", Cost: 100}
	todos := newLiveTable()
	todos.Columns["id"] = &LiveColumn{Type: "integer"}
	todos.HasPK = true
	l.Tables["public.todos"] = todos
	l.Owners = map[string]string{"rel:public.todos": "app"}
	return l
}

const platformYAML = `
schema public:
  function handle_new_user():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table todos:
    columns:
      id: {type: integer}
    primaryKey: [id]
schema plat:
  table objects:
    policies:
      avatars_read: {for: select, using: "bucket_id = 'avatars'"}
  table users:
    triggers:
      on_user_created: {timing: after, events: [insert], level: row, procedure: public.handle_new_user()}
`

func TestExternalTableManagesPoliciesAndTriggers(t *testing.T) {
	d := loadYAML(t, platformYAML)
	p := PlanWith(platformLive(), d, Options{Unsafe: true, Prune: true, External: externalPlatform})
	if !findAlter(p, `create policy "avatars_read" on "plat"."objects" for select using (bucket_id = 'avatars');`) {
		t.Errorf("policy not created:\n%s", Render(p))
	}
	if !findCreate(p, `create trigger "on_user_created" AFTER INSERT on "plat"."users" for each row execute procedure public.handle_new_user();`) {
		t.Errorf("trigger not created:\n%s", Render(p))
	}
	sql := Render(p)
	for _, bad := range []string{`create schema`, `create table`, `"plat"."buckets"`, `"plat"."uid"`, `drop column`, `platform_policy`, `platform_audit`, `users_email_idx`, `drop constraint`} {
		if strings.Contains(sql, bad) {
			t.Errorf("the plan touches what the platform owns (%s):\n%s", bad, sql)
		}
	}
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", p.Warnings)
	}
}

func TestExternalBlockAbsentKeepsPruning(t *testing.T) {
	d := loadYAML(t, platformYAML)
	p := PlanWith(platformLive(), d, Options{Unsafe: true, Prune: true})
	sql := Render(p)
	for _, want := range []string{`drop table "plat"."buckets";`, `drop function "plat"."uid"();`, `drop policy "platform_policy" on "plat"."objects";`} {
		if !strings.Contains(sql, want) {
			t.Errorf("without an external block %s is planned as before; got:\n%s", want, sql)
		}
	}
}

func TestExternalTableDeclaredWithColumnsIsCutDown(t *testing.T) {
	d := loadYAML(t, `
schema plat:
  table users:
    columns:
      id: {type: uuid}
      name: {type: text}
    comment: platform users
`)
	p := PlanWith(platformLive(), d, Options{Unsafe: true, External: externalPlatform})
	if sql := Render(p); strings.Contains(sql, "add column") || strings.Contains(sql, "drop column") || !strings.Contains(sql, `comment on table "plat"."users" is 'platform users';`) {
		t.Errorf("want only the comment, got:\n%s", sql)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "table plat.users is in schema plat, which the config's external: block lists") {
		t.Errorf("want a warning about the columns, got %v", p.Warnings)
	}
}

func TestExternalTableMissingIsReported(t *testing.T) {
	d := loadYAML(t, `
schema plat:
  table sessions:
    policies:
      own: {using: "true"}
`)
	p := PlanWith(platformLive(), d, Options{External: externalPlatform})
	if sql := Render(p); sql != "" {
		t.Errorf("nothing can be planned for a missing external table, got:\n%s", sql)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "table plat.sessions has no columns") {
		t.Errorf("want the missing-table warning, got %v", p.Warnings)
	}
}

func TestExternalSchemaNeverCreated(t *testing.T) {
	d := loadYAML(t, `
schema vault:
  function secret():
    returns: text
    language: sql
    body: select 'x'
`)
	ext := &schema.External{Schemas: []string{"vault"}}
	if p := PlanWith(emptyLive(), d, Options{External: ext}); findCreate(p, "create schema") {
		t.Errorf("an external schema is never created:\n%s", Render(p))
	}
	if p := PlanWith(emptyLive(), d, Options{}); !findCreate(p, `create schema if not exists "vault";`) {
		t.Errorf("without the block the schema is created:\n%s", Render(p))
	}
}

func TestExternalRelationsNotPruned(t *testing.T) {
	live := platformLive()
	metrics := newLiveTable()
	metrics.Columns["at"] = &LiveColumn{Type: "timestamp with time zone"}
	live.Tables["public.metrics"] = metrics
	chunk := newLiveTable()
	chunk.Columns["at"] = &LiveColumn{Type: "timestamp with time zone", Inherited: true}
	chunk.Inherits = []string{"public.metrics"}
	live.Tables["public._hyper_1_1_chunk"] = chunk
	live.Schemas["queue"] = true
	live.Tables["queue.q_jobs"] = newLiveTable()
	live.Tables["queue.meta"] = newLiveTable()
	d := loadYAML(t, `
schema public:
  table todos:
    columns:
      id: {type: integer}
    primaryKey: [id]
  table metrics:
    columns:
      at: {type: timestamptz}
schema queue:
  table settings:
    columns:
      k: {type: text}
`)
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true, External: &schema.External{Relations: []string{"*._hyper_*_chunk", "queue.q_*"}}})
	sql := Render(p)
	if strings.Contains(sql, "_hyper_1_1_chunk") || strings.Contains(sql, "q_jobs") || !strings.Contains(sql, `drop table "queue"."meta";`) {
		t.Errorf("external relations are kept, others pruned; got:\n%s", sql)
	}
	for _, w := range p.Warnings {
		if strings.Contains(w.Message, "_hyper_1_1_chunk") {
			t.Errorf("an external chunk is not reported as a kept inheritance child: %s", w.Message)
		}
	}
}

func TestExternalRoleGrantsUnmanaged(t *testing.T) {
	live := platformLive()
	live.TableGrants["public.todos"] = map[string]map[string]bool{"api": {"select": true}, "plat_reader": {"select": true}, "anon": {"select": true, "insert": true}}
	d := loadYAML(t, `
schema public:
  table todos:
    columns:
      id: {type: integer}
    primaryKey: [id]
    grants: {api: [select], anon: [select]}
`)
	p := PlanWith(live, d, Options{External: externalPlatform})
	wantStatements(t, p.Alters, []string{`revoke insert on table "public"."todos" from "anon";`})
	p = PlanWith(live, d, Options{})
	if !findAlter(p, `revoke select on table "public"."todos" from "plat_reader";`) {
		t.Errorf("without the block an unlisted grantee loses its privileges:\n%s", Render(p))
	}
}

func TestExternalSchemaGrantsAdditive(t *testing.T) {
	live := platformLive()
	live.TableGrants["plat.users"] = map[string]map[string]bool{"dashboard": {"select": true}, "api": {"update": true}}
	d := loadYAML(t, `
schema plat:
  table users:
    grants: {api: [select]}
`)
	p := PlanWith(live, d, Options{External: externalPlatform})
	wantStatements(t, p.Alters, []string{
		`revoke update on table "plat"."users" from "api";`,
		`grant select on table "plat"."users" to "api";`,
	})
}

func TestExternalSettingsNeverReset(t *testing.T) {
	live := platformLive()
	live.DatabaseConfig = map[string]string{"search_path": `"$user", public`, "pgrst.db_schemas": "public", "statement_timeout": "5s"}
	live.Roles["api"] = true
	live.RoleDefs = map[string]*LiveRole{"api": {Login: true, Inherit: true, ConnectionLimit: -1, Config: map[string]string{"pgrst.jwt_aud": "x", "work_mem": "4MB"}}}
	live.DatabaseRoleConfig = map[string]map[string]string{"api": {"pgrst.db_pre_request": "f", "lock_timeout": "1s"}}
	d := loadYAML(t, `
database:
  set: {search_path: '"$user", public'}
  roles:
    api: {}
roles:
  api: {login: true, set: {}}
`)
	p := PlanWith(live, d, Options{External: externalPlatform})
	sql := Render(p)
	if strings.Contains(sql, "pgrst") {
		t.Errorf("external settings are never reset:\n%s", sql)
	}
	for _, want := range []string{"reset statement_timeout", "reset work_mem", "reset lock_timeout"} {
		if !strings.Contains(sql, want) {
			t.Errorf("want %s, got:\n%s", want, sql)
		}
	}
	d.DatabaseSettings["pgrst.db_schemas"] = "public, api"
	if p := PlanWith(live, d, Options{External: externalPlatform}); !strings.Contains(Render(p), "set pgrst.db_schemas = ''public, api''") {
		t.Errorf("a declared external setting is still set:\n%s", Render(p))
	}
}

func TestExternalRoleLeftAsItIs(t *testing.T) {
	live := platformLive()
	live.Roles["anon"], live.Roles["api"], live.Roles["authenticated"] = true, true, true
	live.RoleDefs = map[string]*LiveRole{
		"anon": {Inherit: true, ConnectionLimit: -1, Config: map[string]string{"statement_timeout": "3s"}},
		"api":  {Login: true, Inherit: true, ConnectionLimit: -1},
	}
	live.RoleMembers["api"] = map[string]bool{"anon": true, "authenticated": true}
	d := loadYAML(t, `
roles:
  anon: {login: true, set: {}}
  api: {login: true, inRoles: []}
`)
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true, External: externalPlatform})
	wantStatements(t, append(append([]string{}, p.Alters...), p.Drops...), []string{`revoke "authenticated" from "api";`})
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "role anon is external") {
		t.Errorf("want a warning about the external role, got %v", p.Warnings)
	}
}

func TestExternalOwnedDatabaseObjectsNotPruned(t *testing.T) {
	live := platformLive()
	live.ExtensionDefs = map[string]*LiveExtension{
		"pg_stat_statements": {Schema: "public", Version: "1.10", Owner: "plat_admin"},
		"citext":             {Schema: "public", Version: "1.6", Owner: "app"},
		"pg_net":             {Schema: "plat", Version: "0.1", Owner: "app"},
	}
	for name := range live.ExtensionDefs {
		live.Extensions[name] = true
	}
	live.EventTriggers = map[string]*LiveEventTrigger{
		"platform_ddl": {Event: "ddl_command_end", Function: "public.watch", Owner: "plat_admin"},
		"plat_fn":      {Event: "sql_drop", Function: "plat.on_drop", Owner: "app"},
		"app_ddl":      {Event: "ddl_command_end", Function: "public.watch", Owner: "app"},
	}
	live.Publications = map[string]*LivePublication{
		"platform_pub": {Owner: "plat_admin"},
		"plat_tables":  {Owner: "app", Tables: map[string]LivePublicationTable{"plat.users": {}}},
		"app_pub":      {Owner: "app", Tables: map[string]LivePublicationTable{"public.todos": {}}},
	}
	d := loadYAML(t, `
extensions: []
eventTriggers: {}
publications: {}
schema public:
  table todos:
    columns:
      id: {type: integer}
    primaryKey: [id]
`)
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true, External: externalPlatform})
	wantStatements(t, p.Drops, []string{
		`drop event trigger "app_ddl";`,
		`drop publication "app_pub";`,
		`drop extension "citext";`,
	})
}

func TestExternalTableRowsNotDeleted(t *testing.T) {
	live := platformLive()
	live.TableData = map[string]*LiveTableData{"plat.buckets": {Changed: map[int][]string{}, Extra: []map[string]string{{"id": "other"}}}}
	d := loadYAML(t, `
schema plat:
  table buckets:
    data:
      key: [id]
      rows:
        - {id: avatars}
`)
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true, External: externalPlatform})
	if findDrop(p, "delete from") {
		t.Errorf("rows of an external table are never deleted:\n%s", Render(p))
	}
	if p := PlanWith(live, d, Options{Unsafe: true, Prune: true}); !findDrop(p, `delete from only "plat"."buckets"`) {
		t.Errorf("without the block undeclared rows are deleted:\n%s", Render(p))
	}
}

func TestExternalOwnerIsNotANotOwnerFinding(t *testing.T) {
	live := platformLive()
	live.OwnerRoles = map[string]bool{"app": true}
	live.ExtensionDefs = map[string]*LiveExtension{"pg_stat_statements": {Schema: "public", Version: "1.10", Owner: "plat_admin"}}
	live.Extensions["pg_stat_statements"] = true
	live.EventTriggers = map[string]*LiveEventTrigger{"platform_ddl": {Event: "ddl_command_end", Function: "public.watch", Owner: "plat_admin"}}
	live.Publications = map[string]*LivePublication{"platform_pub": {Owner: "plat_admin"}}
	live.Functions["public.platform_fn()"] = true
	live.FunctionDefs["public.platform_fn()"] = &LiveFunction{Signature: "public.platform_fn()", Kind: "f", Body: "select 1", Language: "sql", Volatility: "volatile", Security: "invoker", Parallel: "unsafe", Cost: 100}
	live.Owners["proc:public.platform_fn()"] = "plat_admin"
	live.Tables["public.platform_log"] = newLiveTable()
	live.Owners["rel:public.platform_log"] = "plat_admin"
	d := loadYAML(t, `
extensions: []
eventTriggers: {}
publications: {}
schema public:
  table todos:
    columns:
      id: {type: integer}
    primaryKey: [id]
`)
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true, External: externalPlatform})
	if len(p.Drops) != 0 || len(p.Warnings) != 0 {
		t.Errorf("objects an external role owns are left out without a warning, got drops %v, warnings %v", p.Drops, p.Warnings)
	}
	p = PlanWith(live, d, Options{Unsafe: true, Prune: true})
	notOwner := 0
	for _, w := range p.Warnings {
		if w.Kind == "not_owner" {
			notOwner++
		}
	}
	if len(p.Drops) != 0 || notOwner != 5 {
		t.Errorf("without the block each is a not_owner finding, got drops %v, warnings %v", p.Drops, p.Warnings)
	}
}
