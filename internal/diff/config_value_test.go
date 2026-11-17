package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func checkConfigValues(t *testing.T, cases [][3]string) {
	t.Helper()
	for _, c := range cases {
		if got := configValueSQL(c[0], c[1]); got != c[2] {
			t.Errorf("configValueSQL(%s, %q) = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestConfigValueSQLKeepsCase(t *testing.T) {
	checkConfigValues(t, [][3]string{
		{"timezone", "UTC", "'UTC'"},
		{"app.mode", "Prod", "'Prod'"},
		{"timezone", "utc", "utc"},
		{"enable_seqscan", "on", "on"},
		{"work_mem", "64MB", "'64MB'"},
		{"statement_timeout", "30", "30"},
		{"timezone", "America/New_York", "'America/New_York'"},
		{"search_path", `"$user", public`, `"$user", public`},
		{"search_path", `"MySchema", public`, `"MySchema", public`},
		{"datestyle", "'ISO, MDY'", "'ISO, MDY'"},
	})
}

func TestConfigValueSQLReservedWords(t *testing.T) {
	checkConfigValues(t, [][3]string{
		{"plpgsql.extra_warnings", "all", "'all'"},
		{"pg_stat_statements.track", "all", "'all'"},
		{"log_statement", "'all'", "'all'"},
		{"app.mode", "null", "'null'"},
		{"app.mode", "default", "'default'"},
		{"app.mode", "user", "'user'"},
		{"enable_seqscan", "off", "off"},
		{"enable_seqscan", "true", "true"},
		{"enable_seqscan", "false", "false"},
		{"pg_stat_statements.track", "top", "top"},
		{"search_path", "all, public", "'all', public"},
	})
}

func TestConfigValueSQLSingleValueKeepsCommas(t *testing.T) {
	checkConfigValues(t, [][3]string{
		{"application_name", "a,b", "'a,b'"},
		{"plperl.on_plperl_init", "my ($a,$b) = (1,2);", "'my ($a,$b) = (1,2);'"},
		{"plperl.on_init", `my ($a,$b) = "x";`, `'my ($a,$b) = "x";'`},
		{"datestyle", "iso, mdy", "'iso, mdy'"},
		{"log_destination", "'stderr', 'csvlog'", "'stderr, csvlog'"},
		{"log_destination", `"stderr", csvlog`, "'stderr, csvlog'"},
		{"timezone", `"UTC"`, "'UTC'"},
		{"app.motto", "it's", "'it''s'"},
		{"app.motto", "'it''s, really'", "'it''s, really'"},
		{"pg_trgm.similarity_threshold", "0.5", "0.5"},
		{"statement_timeout", "-1", "-1"},
	})
}

func TestConfigValueSQLEmpty(t *testing.T) {
	checkConfigValues(t, [][3]string{
		{"search_path", "", "''"},
		{"search_path", `""`, "''"},
		{"search_path", "''", "''"},
		{"myapp.tenant", "", "''"},
		{"myapp.tenant", "  ", "''"},
	})
}

func TestConfigValueSQLListParameters(t *testing.T) {
	long := strings.Repeat("s", 64)
	checkConfigValues(t, [][3]string{
		{"search_path", "App, public", "'App', public"},
		{"Search_Path", "app,public", "app, public"},
		{"search_path", "'app', \"public\"", "'app', \"public\""},
		{"search_path", `"` + long + `", public`, "'" + long + "', public"},
		{"temp_tablespaces", "fast, slow", "fast, slow"},
		{"session_preload_libraries", "auto_explain, pg_stat_statements", "auto_explain, pg_stat_statements"},
		{"local_preload_libraries", "$libdir/plugins/my_lib", "'$libdir/plugins/my_lib'"},
	})
}

func TestConfigValueYAML(t *testing.T) {
	for _, c := range [][3]string{
		{"timezone", "UTC", "UTC"},
		{"pg_stat_statements.track", "all", "all"},
		{"application_name", "a,b", "a,b"},
		{"search_path", `"$user", public`, `"$user", public`},
		{"search_path", `""`, `""`},
		{"app.motto", "it's", "'it''s'"},
		{"app.motto", "'quoted'", "'''quoted'''"},
		{"app.motto", `say "hi", twice`, `'say "hi", twice'`},
		{"app.motto", " padded ", "' padded '"},
		{"app.motto", "", ""},
	} {
		if got := ConfigValueYAML(c[0], c[1]); got != c[2] {
			t.Errorf("ConfigValueYAML(%s, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestFunctionSetValuesAreQuoted(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{}, Functions: map[string]*schema.Function{
		"public.f()": {Schema: "public", Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select 1", Set: map[string]string{
			"plpgsql.extra_warnings":       "all",
			"application_name":             "a,b",
			"search_path":                  "",
			"pg_trgm.similarity_threshold": "0.5",
		}},
	}, Procedures: map[string]*schema.Procedure{
		"public.p()": {Schema: "public", Name: "p", ArgsSig: "()", Language: "sql", Body: "select 1", Set: map[string]string{"log_statement": "all"}},
	}}
	sql := Render(Plan(emptyLive(), d, false))
	for _, want := range []string{
		"set application_name = 'a,b' set pg_trgm.similarity_threshold = 0.5 set plpgsql.extra_warnings = 'all' set search_path = ''",
		`create procedure "public"."p"() language sql set log_statement = 'all'`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
}

func TestFunctionSetValuesMatchLive(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{}, Functions: map[string]*schema.Function{
		"public.f()": {Schema: "public", Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select 1", Set: map[string]string{
			"plpgsql.extra_warnings": "all",
			"application_name":       "a,b",
			"search_path":            "",
			"datestyle":              "'ISO, MDY'",
			"app.motto":              "'it''s'",
		}},
	}}
	live := emptyLive()
	live.Functions["public.f()"] = true
	live.FunctionDefs = map[string]*LiveFunction{"public.f()": {Signature: "public.f()", Body: "select 1", Returns: "integer", Security: "invoker", Volatility: "volatile", Config: map[string]string{
		"plpgsql.extra_warnings": "all",
		"application_name":       "a,b",
		"search_path":            `""`,
		"datestyle":              "ISO, MDY",
		"app.motto":              "it's",
	}}}
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("settings match the live function, got:\n%s", sql)
	}
	live.FunctionDefs["public.f()"].Config["plpgsql.extra_warnings"] = "none"
	want := `alter function "public"."f"() set plpgsql.extra_warnings = 'all';`
	if sql := Render(Plan(live, d, false)); !strings.Contains(sql, want) {
		t.Errorf("missing %q in\n%s", want, sql)
	}
}

func TestRoleAndDatabaseSetValuesAreQuoted(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{},
		Roles: map[string]*schema.Role{
			"app": {Name: "app", ConnectionLimit: -1, Managed: map[string]bool{}, Set: map[string]string{"pg_stat_statements.track": "all", "application_name": "a,b"}},
		},
		DatabaseSettings:     map[string]string{"log_statement": "all", "myapp.tenant": ""},
		DatabaseRoleSettings: map[string]map[string]string{"app": {"plpgsql.extra_errors": "all"}},
	}
	live := emptyLive()
	live.Roles["app"] = true
	live.RoleDefs = map[string]*LiveRole{"app": {Inherit: true, ConnectionLimit: -1}}
	sql := Render(Plan(live, d, false))
	for _, want := range []string{
		`alter role "app" set pg_stat_statements.track = 'all';`,
		`alter role "app" set application_name = 'a,b';`,
		`execute format('alter database %I set log_statement = ''all''', current_database())`,
		`execute format('alter database %I set myapp.tenant = ''''', current_database())`,
		`execute format('alter role "app" in database %I set plpgsql.extra_errors = ''all''', current_database())`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
}
