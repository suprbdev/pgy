package pull

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func buildDatabase(t *testing.T, live *diff.Live, opts Options) string {
	t.Helper()
	live.Details = &diff.LiveDetails{}
	opts.Single = true
	res, err := Build(live, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) == 0 {
		return ""
	}
	return string(res.Files[0].Content)
}

func TestBuildForeignDataWrappers(t *testing.T) {
	live := &diff.Live{
		CurrentUser: "me",
		Wrappers: map[string]*diff.LiveWrapper{
			"pg2": {Handler: "public.postgres_fdw_handler", Validator: "ext.postgres_fdw_validator", Options: map[string]string{}, Owner: "admin", Comment: "second wrapper",
				Grants: map[string]map[string]bool{"app": {"usage*": true}}},
			"legacy":  {Options: map[string]string{"debug": "true", "level": "3"}, Owner: "me"},
			"checked": {Validator: "pg_catalog.postgresql_fdw_validator", Options: map[string]string{}, Owner: "me"},
			"postgres_fdw": {Handler: "public.postgres_fdw_handler", Validator: "public.postgres_fdw_validator", Options: map[string]string{}, Owner: "me", ExtensionOwner: "me",
				Grants: map[string]map[string]bool{"app_owner": {"usage": true}}},
			"file_fdw": {Handler: "public.file_fdw_handler", Validator: "public.file_fdw_validator", Options: map[string]string{}, Owner: "installer", ExtensionOwner: "installer",
				Grants: map[string]map[string]bool{"ext_role": {"usage": true}}},
			"dblink_fdw": {Validator: "public.dblink_fdw_validator", Options: map[string]string{"a": "1"}, Owner: "admin", ExtensionOwner: "installer", Comment: "links",
				Grants: map[string]map[string]bool{"public": {"usage": true}}},
		},
		ExtensionMembers: map[string]bool{"fdw:postgres_fdw": true, "fdw:file_fdw": true, "fdw:dblink_fdw": true},
		InitPrivs: map[string]map[string]map[string]bool{
			"fdw:file_fdw":   {"ext_role": {"usage": true}},
			"fdw:dblink_fdw": {"ext_role": {"usage": true}},
		},
	}
	got := buildDatabase(t, live, Options{Owners: true})
	want := `foreignDataWrappers:
  checked:
    handler: ""
    validator: postgresql_fdw_validator
    options: {}
  dblink_fdw:
    options: {a: "1"}
    owner: admin
    grants: {ext_role: [], public: [usage]}
    comment: links
  legacy:
    handler: ""
    validator: ""
    options: {debug: "true", level: "3"}
  pg2:
    handler: public.postgres_fdw_handler
    validator: ext.postgres_fdw_validator
    options: {}
    owner: admin
    grants: {app: [usage with grant option]}
    comment: second wrapper
  postgres_fdw:
    grants: {app_owner: [usage]}
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if without := buildDatabase(t, live, Options{}); strings.Contains(without, "    owner:") {
		t.Errorf("owners are only written with --owners:\n%s", without)
	}
}

func TestBuildForeignDataWrapperConnection(t *testing.T) {
	live := func(version int) *diff.Live {
		return &diff.Live{
			CurrentUser:   "me",
			ServerVersion: version,
			Wrappers: map[string]*diff.LiveWrapper{
				"pg2":  {Handler: "public.postgres_fdw_handler", Connection: "public.postgres_fdw_connection", Options: map[string]string{}, Owner: "me"},
				"bare": {Options: map[string]string{}, Owner: "me"},
			},
		}
	}
	want := `foreignDataWrappers:
  bare:
    handler: ""
    validator: ""
    connection: ""
    options: {}
  pg2:
    handler: public.postgres_fdw_handler
    validator: ""
    connection: public.postgres_fdw_connection
    options: {}
`
	if got := buildDatabase(t, live(190000), Options{}); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := buildDatabase(t, live(180000), Options{}); !strings.Contains(got, "connection: public.postgres_fdw_connection") || strings.Contains(got, `connection: ""`) {
		t.Errorf("before PostgreSQL 19 only a connection function the wrapper has is written, got:\n%s", got)
	}
}

func TestBuildForeignDataWrappersOfExtensionsUntouched(t *testing.T) {
	live := &diff.Live{
		CurrentUser: "me",
		Wrappers: map[string]*diff.LiveWrapper{
			"postgres_fdw": {Handler: "public.postgres_fdw_handler", Validator: "public.postgres_fdw_validator", Options: map[string]string{}, Owner: "installer", ExtensionOwner: "installer"},
		},
		ExtensionMembers: map[string]bool{"fdw:postgres_fdw": true},
	}
	if got := buildDatabase(t, live, Options{Owners: true}); strings.Contains(got, "foreignDataWrappers") {
		t.Errorf("an extension's wrapper that is as the extension left it is not pulled, got:\n%s", got)
	}
}

func TestBuildLanguages(t *testing.T) {
	usage := func(roles ...string) map[string]map[string]bool {
		out := map[string]map[string]bool{}
		for _, r := range roles {
			priv, role := "usage", r
			if base, ok := strings.CutSuffix(r, "*"); ok {
				priv, role = "usage*", base
			}
			out[role] = map[string]bool{priv: true}
		}
		return out
	}
	live := &diff.Live{
		CurrentUser: "me",
		Languages: map[string]*diff.LiveLanguage{
			"internal":   {Owner: "me", Grants: usage("public")},
			"c":          {Owner: "me", Grants: usage("public")},
			"sql":        {Owner: "me", Trusted: true, Grants: usage("public", "app_dev*")},
			"plpgsql":    {Owner: "me", Trusted: true, Grants: usage("app_dev")},
			"plperl":     {Owner: "me", Trusted: true, Grants: usage("public")},
			"pltcl":      {Owner: "me", Trusted: true, Grants: usage("public", "ext_role", "app_dev")},
			"plsample":   {Owner: "me", Trusted: true, Grants: usage("app_dev")},
			"plpython3u": {Owner: "me", Grants: usage("app_dev")},
		},
		ExtensionMembers: map[string]bool{"language:plpgsql": true, "language:plperl": true, "language:pltcl": true, "language:plpython3u": true},
		InitPrivs:        map[string]map[string]map[string]bool{"language:pltcl": usage("public", "ext_role")},
	}
	got := buildDatabase(t, live, Options{})
	want := `languages:
  plpgsql:
    grants: {app_dev: [usage], public: []}
  plsample:
    grants: {app_dev: [usage], public: []}
  pltcl:
    grants: {app_dev: [usage]}
  sql:
    grants: {app_dev: [usage with grant option]}
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
