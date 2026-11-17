package schema

import (
	"reflect"
	"testing"
)

func TestForeignDataWrapperParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
extensions: [postgres_fdw]
foreignDataWrappers:
  postgres_fdw:
    grants: {app_owner: [usage], auditor: [usage with grant option], public: []}
  pg2:
    handler: public.postgres_fdw_handler()
    validator: postgres_fdw_validator
    options: {debug: true, level: 05, note: ~}
    owner: admin
    comment: second wrapper
    renamedFrom: pg_two
  bare: {}
  none:
    handler: ""
    validator: ""
  half:
    validator: postgresql_fdw_validator
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.ForeignDataWrappers) != 5 {
		t.Fatalf("wrappers = %v", d.ForeignDataWrappers)
	}
	w := d.ForeignDataWrappers["pg2"]
	want := &ForeignDataWrapper{
		Name: "pg2", Handler: "public.postgres_fdw_handler", Validator: "postgres_fdw_validator", HandlerSet: true, ValidatorSet: true,
		Options: map[string]string{"debug": "true", "level": "05", "note": ""},
		Owner:   "admin", Comment: "second wrapper", RenamedFrom: "pg_two",
	}
	if !reflect.DeepEqual(w, want) {
		t.Errorf("pg2 = %+v, want %+v", w, want)
	}
	member := d.ForeignDataWrappers["postgres_fdw"]
	if member.ManagesHandler() || member.ManagesValidator() || member.Options != nil {
		t.Errorf("an entry with grants only declares nothing else: %+v", member)
	}
	if got := member.Grants; !reflect.DeepEqual(got, map[string][]string{"app_owner": {"usage"}, "auditor": {"usage*"}, "public": {}}) {
		t.Errorf("grants = %v", got)
	}
	if bare := d.ForeignDataWrappers["bare"]; bare == nil || bare.Name != "bare" || bare.ManagesHandler() || bare.ManagesValidator() || bare.Options != nil || bare.Grants != nil {
		t.Errorf("an empty entry manages nothing: %+v", bare)
	}
	if none := d.ForeignDataWrappers["none"]; !none.ManagesHandler() || !none.ManagesValidator() || none.Handler != "" || none.Validator != "" {
		t.Errorf("an empty handler or validator declares that the wrapper has none: %+v", none)
	}
	if half := d.ForeignDataWrappers["half"]; half.ManagesHandler() || !half.ManagesValidator() || half.Validator != "postgresql_fdw_validator" {
		t.Errorf("a key left out is unmanaged, on its own: %+v", half)
	}
	if w := (&ForeignDataWrapper{Handler: "h"}); !w.ManagesHandler() || w.ManagesValidator() {
		t.Errorf("a handler given without HandlerSet is managed: %+v", w)
	}
}

func TestForeignDataWrappersBlockPresence(t *testing.T) {
	absent, err := parseFlexibleDatabase([]byte("servers:\n  s: {wrapper: w}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if absent.ForeignDataWrappers != nil || absent.Languages != nil {
		t.Errorf("without the blocks, wrappers and languages are unmanaged: %v, %v", absent.ForeignDataWrappers, absent.Languages)
	}
	empty, err := parseFlexibleDatabase([]byte("foreignDataWrappers: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if empty.ForeignDataWrappers == nil || len(empty.ForeignDataWrappers) != 0 {
		t.Errorf("an empty block is present, and authoritative for pruning: %v", empty.ForeignDataWrappers)
	}
}

func TestLanguageParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
languages:
  plpgsql:
    grants: {public: [], app_dev: [USAGE]}
  sql:
    grants: {app_dev: [usage with grant option]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Languages) != 2 {
		t.Fatalf("languages = %v", d.Languages)
	}
	if l := d.Languages["plpgsql"]; l.Name != "plpgsql" || !reflect.DeepEqual(l.Grants, map[string][]string{"public": {}, "app_dev": {"usage"}}) {
		t.Errorf("plpgsql = %+v", l)
	}
	if l := d.Languages["sql"]; !reflect.DeepEqual(l.Grants, map[string][]string{"app_dev": {"usage*"}}) {
		t.Errorf("sql = %+v", l)
	}
}

func TestForeignDataWrappersAndLanguagesMerge(t *testing.T) {
	a, err := parseFlexibleDatabase([]byte(`
foreignDataWrappers:
  pg2: {handler: h1}
  legacy: {}
languages:
  plpgsql: {grants: {a: [usage]}}
`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseFlexibleDatabase([]byte(`
foreignDataWrappers:
  pg2: {handler: h2}
languages:
  plpgsql: {grants: {b: [usage]}}
  sql: {grants: {b: [usage]}}
`))
	if err != nil {
		t.Fatal(err)
	}
	m := Merge([]*Database{a, b})
	if len(m.ForeignDataWrappers) != 2 || m.ForeignDataWrappers["pg2"].Handler != "h2" || m.ForeignDataWrappers["legacy"] == nil {
		t.Errorf("wrappers are merged by name and a later file wins: %+v", m.ForeignDataWrappers)
	}
	if len(m.Languages) != 2 || m.Languages["plpgsql"].Grants["b"] == nil || m.Languages["plpgsql"].Grants["a"] != nil {
		t.Errorf("languages are merged by name and a later file wins: %+v", m.Languages)
	}
	if none := Merge([]*Database{{}, {}}); none.ForeignDataWrappers != nil || none.Languages != nil {
		t.Errorf("merging files without the blocks leaves them absent")
	}
}

func TestWrapperExtension(t *testing.T) {
	d := &Database{Extensions: []*Extension{{Name: "mysql_fdw"}, {Name: "hstore"}}}
	for name, want := range map[string]string{
		"postgres_fdw": "postgres_fdw",
		"file_fdw":     "file_fdw",
		"dblink_fdw":   "dblink",
		"mysql_fdw":    "mysql_fdw",
		"pg2":          "",
		"oracle_fdw":   "",
	} {
		if got := d.WrapperExtension(name); got != want {
			t.Errorf("WrapperExtension(%q) = %q, want %q", name, got, want)
		}
	}
	if !d.DeclaresExtension("hstore") || d.DeclaresExtension("dblink") {
		t.Errorf("DeclaresExtension reports the extensions block")
	}
}

func TestJSONSchemaAcceptsWrappersAndLanguages(t *testing.T) {
	s := compileEditorSchema(t)
	doc := `
extensions: [postgres_fdw, plperl]
foreignDataWrappers:
  postgres_fdw:
    grants: {app_owner: [usage]}
    comment: remote PostgreSQL
  pg2:
    handler: public.postgres_fdw_handler
    validator: public.postgres_fdw_validator
    owner: admin
    renamedFrom: pg_two
  legacy:
    handler: ""
    validator: ""
    options: {debug: true, level: 3}
  bare: {}
languages:
  plpgsql:
    grants: {public: [], app_dev: [usage]}
  plperl:
    grants: {app_dev: [usage with grant option]}
`
	if err := s.Validate(yamlToJSONValue(t, []byte(doc))); err != nil {
		t.Errorf("wrappers and languages rejected:\n%v", err)
	}
	for name, doc := range map[string]string{
		"an unknown wrapper key":            "foreignDataWrappers:\n  w: {handlr: f}\n",
		"a wrapper dependsOn":               "foreignDataWrappers:\n  w: {dependsOn: [function f]}\n",
		"a handler that is not a string":    "foreignDataWrappers:\n  w: {handler: false}\n",
		"a language without grants":         "languages:\n  plpgsql: {}\n",
		"a language with anything but that": "languages:\n  plpgsql: {grants: {a: [usage]}, owner: a}\n",
		"a language list":                   "languages: [plpgsql]\n",
	} {
		if err := s.Validate(yamlToJSONValue(t, []byte(doc))); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}
