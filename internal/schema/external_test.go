package schema

import "testing"

func TestExternalGlobMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"auth", "auth", true},
		{"auth", "Auth", false},
		{"auth", "auth2", false},
		{"*_admin", "supabase_admin", true},
		{"*_admin", "_admin", true},
		{"*_admin", "admin", false},
		{"rds?", "rds1", true},
		{"rds?", "rds", false},
		{"*", "", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
		{"_hyper_*_chunk", "_hyper_1_2_chunk", true},
		{"q_*", "a_q_x", false},
	} {
		if got := globMatch(c.pattern, c.name); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestExternalMatchers(t *testing.T) {
	e := &External{
		Schemas:   []string{"auth", "storage"},
		Roles:     []string{"anon", "*_admin"},
		Settings:  []string{"pgrst.*", "app.settings.*"},
		Relations: []string{"_timescaledb_internal.*", "*._hyper_*_chunk", "pgmq.q_*"},
	}
	for _, c := range []struct {
		name string
		got  bool
		want bool
	}{
		{"schema auth", e.HasSchema("auth"), true},
		{"schema public", e.HasSchema("public"), false},
		{"role anon", e.HasRole("anon"), true},
		{"role supabase_admin", e.HasRole("supabase_admin"), true},
		{"role app", e.HasRole("app"), false},
		{"role public", (&External{Roles: []string{"*"}}).HasRole("public"), false},
		{"setting pgrst.db_schemas", e.HasSetting("pgrst.db_schemas"), true},
		{"setting in another case", e.HasSetting("APP.Settings.JWT_SECRET"), true},
		{"setting search_path", e.HasSetting("search_path"), false},
		{"relation in an external schema", e.HasRelation("auth.users"), true},
		{"relation pattern", e.HasRelation("_timescaledb_internal._hyper_1_1_chunk"), true},
		{"chunk in the application schema", e.HasRelation("public._hyper_1_1_chunk"), true},
		{"queue table", e.HasRelation("pgmq.q_jobs"), true},
		{"archive table not listed", e.HasRelation("pgmq.a_jobs"), false},
		{"application table", e.HasRelation("public.todos"), false},
		{"routine in an external schema", e.InSchema("auth.uid()"), true},
		{"routine in public", e.InSchema("public.f(integer)"), false},
		{"nil", (*External)(nil).HasRelation("auth.users") || (*External)(nil).HasRole("anon"), false},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestExternalCheck(t *testing.T) {
	if err := (&External{Relations: []string{"q_*"}}).Check(); err == nil {
		t.Error("a relation pattern without a schema was accepted")
	}
	if err := (&External{Schemas: []string{" "}}).Check(); err == nil {
		t.Error("an empty pattern was accepted")
	}
	if err := (&External{Relations: []string{"*.q_*"}, Roles: []string{"anon"}}).Check(); err != nil {
		t.Errorf("valid patterns rejected: %v", err)
	}
	if !(*External)(nil).Empty() || !(&External{}).Empty() || (&External{Roles: []string{"x"}}).Empty() {
		t.Error("Empty is wrong")
	}
}
