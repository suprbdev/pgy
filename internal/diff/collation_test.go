package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestCollationCreateAndSkip(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{
		"app.users": {Name: "users", Columns: map[string]*schema.Column{"email": {Type: "text", Collate: "app.ci"}}, DependsOn: []string{"collation app.ci"}},
	}, Collations: map[string]*schema.Collation{
		"app.ci":   {Name: "ci", Provider: "icu", Locale: "und-u-ks-level2", Deterministic: schema.BoolPtr(false), Comment: "case-insensitive"},
		"app.copy": {Name: "copy", From: "pg_catalog.C"},
	}}
	p := Plan(emptyLive(), d, false)
	want := []string{
		`create schema if not exists "app";`,
		`create collation if not exists "app"."ci" (provider = icu, locale = 'und-u-ks-level2', deterministic = false);`,
		`create collation if not exists "app"."copy" from "pg_catalog"."C";`,
		`create table if not exists "app"."users" ("email" text collate "app"."ci" not null);`,
	}
	for i, w := range want {
		if i >= len(p.Creates) || p.Creates[i] != w {
			t.Fatalf("creates[%d]: want %q, got %+v", i, w, p.Creates)
		}
	}
	if !findAlter(p, `comment on collation "app"."ci" is 'case-insensitive';`) {
		t.Errorf("expected collation comment, got %+v", p.Alters)
	}
	live := emptyLive()
	live.Schemas["app"] = true
	live.Collations = map[string]bool{"app.ci": true, "app.copy": true}
	live.CollationComments = map[string]string{"app.ci": "case-insensitive"}
	d.Tables = map[string]*schema.Table{}
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("existing collations must not diff, got:\n%s", sql)
	}
}
