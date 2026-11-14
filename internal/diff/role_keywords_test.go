package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestPolicyRoleKeywordsAndPublic(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}, RowLevelSecurity: bptr(true),
		Policies: []*schema.Policy{
			{Name: "mine", To: []string{"current_user"}, Using: "true"},
			{Name: "all", To: []string{"app", "PUBLIC"}, Using: "true"},
		},
		Grants: map[string][]string{"current_user": {"select"}},
	}}}
	sql := Render(Plan(emptyLive(), d, false))
	for _, want := range []string{`to current_user using`, `create policy "all" on "app"."t" to public using`, `grant select on table "app"."t" to current_user;`} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
	live := Simulate(&Live{CurrentUser: "deployer"}, d)
	live.CurrentUser = "deployer"
	live.TableGrants["app.t"] = map[string]map[string]bool{"deployer": {"select": true}}
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("keywords must compare as the connected role, got:\n%s", sql)
	}
}

func TestOwnerKeywordComparesAsConnectedRole(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Owner: "current_user", Columns: map[string]*schema.Column{"id": {Type: "int"}}}}}
	live := Simulate(&Live{CurrentUser: "deployer"}, d)
	live.CurrentUser = "deployer"
	if live.Owners["rel:app.t"] != "deployer" {
		t.Fatalf("simulated owner = %q", live.Owners["rel:app.t"])
	}
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("owner: current_user must match the connected role, got:\n%s", sql)
	}
}

func TestGrantPublicSpelling(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}},
		Grants: map[string][]string{"PUBLIC": {"select"}}}}}
	live := Simulate(emptyLive(), d)
	live.TableGrants["app.t"] = map[string]map[string]bool{"public": {"select": true}}
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("PUBLIC must match the live public grant, got:\n%s", sql)
	}
}
