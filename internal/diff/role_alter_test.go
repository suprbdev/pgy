package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestRoleAttributesAlteredOnlyWhenManaged(t *testing.T) {
	live := emptyLive()
	live.Roles["app"] = true
	live.RoleDefs = map[string]*LiveRole{"app": {Login: false, CreateDB: true, Inherit: true, ConnectionLimit: -1, BypassRLS: true}}
	r := &schema.Role{Name: "app", Login: true, NoInherit: true, ConnectionLimit: 10,
		Managed: map[string]bool{"login": true, "inherit": true, "connectionLimit": true, "createdb": true}}
	d := &schema.Database{Tables: map[string]*schema.Table{}, Roles: map[string]*schema.Role{"app": r}}
	p := Plan(live, d, false)
	want := `alter role "app" with login nocreatedb noinherit connection limit 10;`
	if len(p.Alters) != 1 || p.Alters[0] != want {
		t.Errorf("want %q (bypassrls unmanaged), got %v", want, p.Alters)
	}
	live.RoleDefs["app"] = &LiveRole{Login: true, Inherit: false, ConnectionLimit: 10, BypassRLS: true}
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("matching role must not diff, got:\n%s", sql)
	}
	bare := &schema.Database{Tables: map[string]*schema.Table{}, Roles: map[string]*schema.Role{"app": {Name: "app", ConnectionLimit: -1, Managed: map[string]bool{}}}}
	if sql := Render(Plan(live, bare, false)); sql != "" {
		t.Errorf("a role declared without attributes leaves them unmanaged, got:\n%s", sql)
	}
}
