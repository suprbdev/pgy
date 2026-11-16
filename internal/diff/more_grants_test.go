package diff

import (
	"strings"
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

func TestNewObjectsStartFromDefaultPrivileges(t *testing.T) {
	live := emptyLive()
	live.CurrentUser = "deployer"
	d := &schema.Database{
		DefaultPrivileges: []*schema.DefaultPrivilege{{ForRole: "deployer", Schemas: []string{"app"}, Grants: map[string]map[string][]string{
			"tables":    {"reader": {"select"}},
			"sequences": {"reader": {"usage"}},
		}}},
		Tables: map[string]*schema.Table{
			"app.private": {Name: "app.private", Columns: map[string]*schema.Column{"id": {Type: "int"}}, Grants: map[string][]string{}},
			"app.shared":  {Name: "app.shared", Columns: map[string]*schema.Column{"id": {Type: "int"}}, Grants: map[string][]string{"reader": {"select"}, "writer": {"insert"}}},
			"app.loose":   {Name: "app.loose", Columns: map[string]*schema.Column{"id": {Type: "int"}}},
		},
		Sequences: map[string]*schema.Sequence{"app.s": {Name: "s", Grants: map[string][]string{}}},
	}
	sql := Render(Plan(live, d, false))
	for _, want := range []string{
		`revoke select on table "app"."private" from "reader";`,
		`revoke usage on sequence "app"."s" from "reader";`,
		`grant insert on table "app"."shared" to "writer";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
	for _, unwanted := range []string{`grant select on table "app"."shared"`, `"app"."loose" from`} {
		if strings.Contains(sql, unwanted) {
			t.Errorf("unexpected %q in\n%s", unwanted, sql)
		}
	}
}

func TestGrantOption(t *testing.T) {
	target := `table "app"."t"`
	for _, c := range []struct {
		desired  []string
		live     map[string]bool
		revokes  string
		grants   string
	}{
		{[]string{"select", "insert*"}, nil, "", `grant select on table "app"."t" to "r";|grant insert on table "app"."t" to "r" with grant option;`},
		{[]string{"insert*"}, map[string]bool{"insert": true}, "", `grant insert on table "app"."t" to "r" with grant option;`},
		{[]string{"insert"}, map[string]bool{"insert*": true}, `revoke grant option for insert on table "app"."t" from "r";`, ""},
		{[]string{}, map[string]bool{"insert*": true, "select": true}, `revoke insert, select on table "app"."t" from "r";`, ""},
		{[]string{"insert*"}, map[string]bool{"insert*": true}, "", ""},
	} {
		live := map[string]map[string]bool{}
		if c.live != nil {
			live["r"] = c.live
		}
		rv, gr := grantDiffStmts(target, map[string][]string{"r": c.desired}, live)
		if strings.Join(rv, "|") != c.revokes || strings.Join(gr, "|") != c.grants {
			t.Errorf("desired %v live %v: revokes %q grants %q", c.desired, c.live, rv, gr)
		}
	}
	_, gr := columnGrantDiffStmts("app.t", "a", map[string][]string{"r": {"update*"}}, nil, true)
	if strings.Join(gr, "") != `grant update ("a") on table "app"."t" to "r" with grant option;` {
		t.Errorf("column grant = %q", gr)
	}
}
