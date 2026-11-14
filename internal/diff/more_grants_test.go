package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestSequenceTypeDomainGrants(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{},
		Sequences: map[string]*schema.Sequence{"app.s": {Name: "s", Grants: map[string][]string{"app_rw": {"select", "usage"}}}},
		Types:     map[string]*schema.TypeDef{"app.mood": {Name: "mood", Kind: "enum", Labels: []string{"ok"}, Grants: map[string][]string{"app_rw": {"usage"}}}},
		Domains:   map[string]*schema.Domain{"app.email": {Name: "email", Type: "text", Grants: map[string][]string{"app_rw": {"usage"}, "old": {}}}},
	}
	live := emptyLive()
	live.Schemas["app"] = true
	live.Sequences["app.s"] = true
	live.Types["app.mood"] = true
	live.Domains["app.email"] = true
	live.TableGrants["app.s"] = map[string]map[string]bool{"app_rw": {"usage": true, "update": true}}
	live.TypeGrants = map[string]map[string]map[string]bool{"app.email": {"old": {"usage": true}}}
	p := Plan(live, d, false)
	for _, want := range []string{
		`revoke update on sequence "app"."s" from "app_rw";`,
		`grant select on sequence "app"."s" to "app_rw";`,
		`grant usage on type "app"."mood" to "app_rw";`,
		`grant usage on domain "app"."email" to "app_rw";`,
		`revoke usage on domain "app"."email" from "old";`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
}
