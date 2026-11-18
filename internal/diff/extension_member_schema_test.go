package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestExtensionSchemaGrants(t *testing.T) {
	live := memberLive()
	live.Schemas["pdb"] = true
	live.ExtensionMembers["schema:pdb"] = true
	live.MemberExtension = map[string]string{"schema:pdb": "pg_search"}
	live.InitPrivs["schema:pdb"] = map[string]map[string]bool{"public": {"usage": true}}
	live.SchemaGrants["pdb"] = map[string]map[string]bool{"public": {"usage": true}, "old": {"usage": true}}
	d := &schema.Database{SchemaGrants: map[string]map[string][]string{"pdb": {"app": {"usage"}}}}
	sql, _ := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, sql,
		`revoke usage on schema "pdb" from "old";`,
		`grant usage on schema "pdb" to "app";`,
	)
}

func TestExtensionSchemaPending(t *testing.T) {
	live := memberLive()
	d := &schema.Database{
		Extensions:   []*schema.Extension{{Name: "pg_search", Schema: "public"}},
		SchemaGrants: map[string]map[string][]string{"pdb": {"app": {"usage"}, "public": {}}},
		Functions: map[string]*schema.Function{
			"pdb.score(anyelement)": {Schema: "pdb", Name: "score", ArgsSig: "(anyelement)", Grants: map[string][]string{"app": {"execute"}}},
		},
		Tables: map[string]*schema.Table{
			"pdb.settings": {Name: "pdb.settings", Grants: map[string][]string{"app": {"select"}}},
		},
	}
	sql, _ := planSQL(t, live, d, Options{})
	if at := strings.Index(sql, `create schema if not exists "pdb";`); at < 0 || at < strings.Index(sql, "create extension") {
		t.Errorf("a schema that only a schema block and declarations of existing objects name may be the extension's to create, so it is created after it, where it is still missing:\n%s", sql)
	}
	for _, want := range []string{
		`revoke all on schema "pdb" from public;`,
		`grant usage on schema "pdb" to "app";`,
		`grant execute on function "pdb"."score"(anyelement) to "app";`,
		`grant select on table "pdb"."settings" to "app";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Index(sql, "create extension") > strings.Index(sql, "grant usage on schema") {
		t.Errorf("the grants follow the extension:\n%s", sql)
	}

	d.Tables["pdb.own"] = &schema.Table{Name: "pdb.own", ColumnsSet: true, Columns: map[string]*schema.Column{"id": {Type: "int"}}}
	if sql, _ := planSQL(t, live, d, Options{}); strings.Count(sql, `create schema if not exists "pdb";`) != 1 || strings.Index(sql, "create schema") > strings.Index(sql, "create extension") {
		t.Errorf("a table pgy creates still needs its schema, ahead of everything:\n%s", sql)
	}
}
