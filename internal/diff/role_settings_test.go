package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestRoleSettings(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{}, Roles: map[string]*schema.Role{
		"app": {Name: "app", ConnectionLimit: -1, Managed: map[string]bool{}, Set: map[string]string{"search_path": "app, public", "statement_timeout": "5s"}},
	}}
	p := Plan(emptyLive(), d, false)
	for _, want := range []string{`alter role "app" set search_path = app, public;`, `alter role "app" set statement_timeout = '5s';`} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
	live := emptyLive()
	live.Roles["app"] = true
	live.RoleDefs = map[string]*LiveRole{"app": {Inherit: true, ConnectionLimit: -1, Config: map[string]string{"search_path": "app, public", "work_mem": "64MB"}}}
	p = Plan(live, d, false)
	if !findAlter(p, `alter role "app" set statement_timeout = '5s';`) || !findAlter(p, `alter role "app" reset work_mem;`) || findAlter(p, `alter role "app" set search_path = app, public;`) {
		t.Errorf("unexpected alters %v", p.Alters)
	}
	d.Roles["app"].Set = nil
	if p := Plan(live, d, false); len(p.Alters) != 0 {
		t.Errorf("settings are unmanaged without set, got %v", p.Alters)
	}
}
