package validate

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

const externalReferencesYAML = `
schema public:
  function handle_new_user():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table profiles:
    columns:
      id: {type: uuid}
    primaryKey: [id]
    foreignKeys:
      profiles_id_fkey: {columns: [id], references: {table: auth.identities, columns: [id]}, onDelete: cascade}
    triggers:
      notify: {timing: after, events: [insert], level: row, procedure: supabase_functions.http_request()}
    dependsOn: [function auth.uid(), table auth.identities]
  table measurements:
    columns:
      at: {type: timestamptz}
    inherits: [public._hyper_1_1_chunk]
schema storage:
  table objects:
    policies:
      avatars_read: {for: select, using: "bucket_id = 'avatars'"}
schema auth:
  table users:
    triggers:
      on_user_created: {timing: after, events: [insert], level: row, procedure: public.handle_new_user()}
eventTriggers:
  watch: {event: ddl_command_end, function: extensions.pgrst_watch()}
`

func TestValidateExternalReferences(t *testing.T) {
	p := write(t, t.TempDir(), "app.yml", externalReferencesYAML)
	ext := &schema.External{Schemas: []string{"auth", "storage", "supabase_functions", "extensions"}, Relations: []string{"*._hyper_*_chunk"}}
	if issues := FilesWith([]string{p}, ext); len(issues) > 0 {
		t.Errorf("references to external objects are not reported:\n%s", messages(issues))
	}
	got := messages(Files([]string{p}))
	for _, want := range []string{
		"foreign key profiles_id_fkey references auth.identities, which no schema file declares",
		"trigger notify calls supabase_functions.http_request, which no schema file declares",
		`dependsOn "function auth.uid()" matches nothing declared`,
		`dependsOn "table auth.identities" matches nothing declared`,
		"inherits public._hyper_1_1_chunk, which no schema file declares",
		"has triggers but no columns",
		"declares policies but not rowLevelSecurity: true",
		"function extensions.pgrst_watch() is not declared in any schema file",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("without an external block want %q in:\n%s", want, got)
		}
	}
}

func TestValidateExternalPolicy(t *testing.T) {
	d := &schema.Database{
		Tables: map[string]*schema.Table{
			"auth.users":   {Name: "users", Policies: []*schema.Policy{{Name: "own", Using: "true"}}},
			"public.todos": {Name: "todos", Columns: map[string]*schema.Column{"id": {Type: "int"}}},
		},
		External: &schema.External{Schemas: []string{"auth"}},
	}
	got := messages(CheckPolicy(d, Policy{RequireGrants: map[string]bool{"tables": true, "schemas": true}, RequireOwners: map[string]bool{"tables": true}}))
	if strings.Contains(got, "auth") {
		t.Errorf("objects in external schemas are not checked:\n%s", got)
	}
	if !strings.Contains(got, "table public.todos: policy: tables must declare a grants block") {
		t.Errorf("the application's tables are still checked:\n%s", got)
	}
}
