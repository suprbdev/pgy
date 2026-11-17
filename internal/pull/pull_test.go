package pull

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

func TestBuildTable(t *testing.T) {
	lt := &diff.LiveTable{
		Columns: map[string]*diff.LiveColumn{
			"id":    {Type: "integer", Default: "nextval('app.t_id_seq'::regclass)"},
			"email": {Type: "text", Nullable: true, Comment: "@name mail"},
		},
		ConstraintDefs: map[string]string{
			"t_pkey":      "PRIMARY KEY (id)",
			"t_org_fkey":  "FOREIGN KEY (org) REFERENCES app.orgs(id) ON DELETE CASCADE DEFERRABLE",
			"t_len_check": "CHECK ((length(email) > 3))",
		},
		Constraints: map[string]bool{"t_pkey": true, "t_org_fkey": true, "t_len_check": true},
		Triggers:    map[string]string{"t_trg": "CREATE TRIGGER t_trg BEFORE INSERT OR UPDATE OF email ON app.t FOR EACH ROW EXECUTE FUNCTION app.touch()"},
		IndexDefs: map[string]*diff.LiveIndex{
			"t_pkey":  {Constraint: true},
			"t_email": {Method: "btree", Keys: []string{"lower(email)"}, Descending: []bool{true}, NullsFirst: []bool{true}, Predicate: "email IS NOT NULL"},
		},
		RLSEnabled: true,
		Policies:   map[string]*diff.LivePolicy{"t_pol": {Cmd: "select", Roles: []string{"app"}, Using: "true"}},
	}
	live := &diff.Live{
		Schemas:        map[string]bool{"app": true},
		Tables:         map[string]*diff.LiveTable{"app.t": lt},
		Sequences:      map[string]bool{"app.t_id_seq": true},
		SequenceDefs:   map[string]*diff.LiveSequence{"app.t_id_seq": {Type: "integer", Owner: "app.t.id", Increment: "1", Start: "1", MinValue: "1", MaxValue: "2147483647", Cache: "1"}},
		OwnedSequences: map[string]bool{"app.t_id_seq": true},
		RelComments:    map[string]string{"app.t": "things"},
		Details:        &diff.LiveDetails{ColumnOrder: map[string][]string{"app.t": {"id", "email"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("want one document, got %d", len(res.Files))
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{
		"schema app:",
		"table t:",
		"id: {type: serial}",
		"email: {type: text, nullable: true, comment: '@name mail'}",
		"primaryKey: [id]",
		"columns: [lower(email) desc]",
		"where: email IS NOT NULL",
		"references: {table: app.orgs, columns: [id]}",
		"onDelete: cascade",
		"deferrable: true",
		"expression: length(email) > 3",
		"events: [insert, update of email]",
		"procedure: app.touch()",
		"rowLevelSecurity: true",
		"to: [app]",
		"comment: things",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sequence t_id_seq") {
		t.Errorf("a serial column's sequence must not be pulled separately:\n%s", got)
	}
}

func TestBuildNotNullName(t *testing.T) {
	lt := &diff.LiveTable{Columns: map[string]*diff.LiveColumn{
		"id":      {Type: "integer", NotNullName: "t_id_not_null"},
		"a":       {Type: "integer", NotNullName: "a_must_exist"},
		"b":       {Type: "text", NotNullName: "b_pending", NotNullNotValid: "b_pending"},
		"c":       {Type: "text", Nullable: true},
		"unnamed": {Type: "text"},
	}}
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables:  map[string]*diff.LiveTable{"app.t": lt},
		Details: &diff.LiveDetails{ColumnOrder: map[string][]string{"app.t": {"id", "a", "b", "c", "unnamed"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{
		"id: {type: integer}",
		"a: {type: integer, notNullName: a_must_exist}",
		"b: {type: text, notValid: true, notNullName: b_pending}",
		"c: {type: text, nullable: true}",
		"unnamed: {type: text}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildQuotedTriggers(t *testing.T) {
	lt := &diff.LiveTable{
		Columns: map[string]*diff.LiveColumn{"a": {Type: "integer"}},
		Triggers: map[string]string{
			"Upd Trg": `CREATE TRIGGER "Upd Trg" BEFORE UPDATE OF a ON app."Order Items" FOR EACH ROW WHEN ((old.a IS DISTINCT FROM new.a)) EXECUTE FUNCTION app.touch()`,
			"Ref Trg": `CREATE TRIGGER "Ref Trg" AFTER UPDATE ON app."Order Items" REFERENCING OLD TABLE AS "Old Rows" NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION app.touch()`,
		},
	}
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables:  map[string]*diff.LiveTable{"app.Order Items": lt},
		Details: &diff.LiveDetails{},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) > 0 {
		t.Fatalf("quoted triggers must parse, got warnings: %v", res.Warnings)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{"Upd Trg:", "when: (old.a IS DISTINCT FROM new.a)", "referencing: {old: Old Rows, new: new_rows}"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestUnquoteIdent(t *testing.T) {
	for in, want := range map[string]string{
		`"x""y"`:      `x"y`,
		`"Weird Col"`: `Weird Col`,
		`plain`:       `plain`,
		`"a" || "b"`:  `"a" || "b"`,
	} {
		if got := unquoteIdent(in); got != want {
			t.Errorf("unquoteIdent(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestBuildExtensionsKeepTheirSchema(t *testing.T) {
	live := &diff.Live{
		Extensions:    map[string]bool{"citext": true, "pgcrypto": true},
		ExtensionDefs: map[string]*diff.LiveExtension{"citext": {Schema: "public"}, "pgcrypto": {Schema: "ext"}},
		Details:       &diff.LiveDetails{},
	}
	res, err := Build(live, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var out string
	for _, f := range res.Files {
		out += string(f.Content)
	}
	for _, want := range []string{"{name: citext, ifNotExists: true, schema: public}", "{name: pgcrypto, ifNotExists: true, schema: ext}"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestBuildExtensionComments(t *testing.T) {
	live := &diff.Live{
		Extensions: map[string]bool{"citext": true, "hstore": true, "pgcrypto": true, "mine": true},
		ExtensionDefs: map[string]*diff.LiveExtension{
			"citext":   {Schema: "public", Comment: "case-insensitive", DefaultComment: "case-insensitive"},
			"hstore":   {Schema: "public", DefaultComment: "key/value pairs"},
			"pgcrypto": {Schema: "public", Comment: "hashing", DefaultComment: "cryptographic functions"},
			"mine":     {Schema: "public"},
		},
		Details: &diff.LiveDetails{},
	}
	_, d, _ := buildSingle(t, live)
	got := map[string]string{}
	for _, ext := range d.Extensions {
		got[ext.Name] = fmt.Sprintf("%q %v", ext.Comment, ext.NoComment)
	}
	want := map[string]string{"citext": `"" false`, "hstore": `"" true`, "pgcrypto": `"hashing" false`, "mine": `"" false`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extension comments %v, want %v", got, want)
	}
}

func TestBuildEmptyGrantsWhenDefaultsWouldGrant(t *testing.T) {
	live := &diff.Live{
		Schemas: map[string]bool{"app": true},
		Tables: map[string]*diff.LiveTable{
			"app.old":    {Columns: map[string]*diff.LiveColumn{"id": {Type: "integer"}}},
			"app.shared": {Columns: map[string]*diff.LiveColumn{"id": {Type: "integer"}}},
		},
		TableGrants: map[string]map[string]map[string]bool{"app.shared": {"reader": {"select": true}}},
		DefaultACLs: map[string]map[string]map[string]bool{"owner|app|tables": {"reader": {"select": true}}},
		Details:     &diff.LiveDetails{ColumnOrder: map[string][]string{"app.old": {"id"}, "app.shared": {"id"}}},
	}
	res, err := Build(live, Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Files[0].Content)
	old := out[strings.Index(out, "table old:"):strings.Index(out, "table shared:")]
	if !strings.Contains(old, "grants: {}") {
		t.Errorf("a table without the grants its schema's default privileges give needs grants: {}:\n%s", out)
	}
	if !strings.Contains(out[strings.Index(out, "table shared:"):], "grants: {reader: [select]}") {
		t.Errorf("explicit grants must be pulled:\n%s", out)
	}
}

func TestSerialShaped(t *testing.T) {
	serial := &diff.LiveSequence{Type: "integer", Increment: "1", Start: "1", MinValue: "1", MaxValue: "2147483647", Cache: "1"}
	if !serialShaped(serial, "integer") {
		t.Error("a serial's own sequence must pull as serial")
	}
	for name, change := range map[string]func(*diff.LiveSequence){
		"bigint sequence": func(s *diff.LiveSequence) { s.Type, s.MaxValue = "bigint", "9223372036854775807" },
		"increment":       func(s *diff.LiveSequence) { s.Increment = "5" },
		"cache":           func(s *diff.LiveSequence) { s.Cache = "20" },
		"cycle":           func(s *diff.LiveSequence) { s.Cycle = true },
		"start":           func(s *diff.LiveSequence) { s.Start = "100" },
	} {
		c := *serial
		change(&c)
		if serialShaped(&c, "integer") {
			t.Errorf("%s: a sequence serial wouldn't recreate must stay explicit", name)
		}
	}
}

func TestDataNodeQuotesInexactNumbers(t *testing.T) {
	for in, quoted := range map[string]bool{
		"12.3456": false, "0.0000": false, "0.1": false, "9223372036854775807": false, "1e300": false,
		"12345678901234567890.123456789012": true, "0.12345678901234567891": true,
	} {
		n := dataNode(json.Number(in), "numeric")
		if got := n.Tag == "!!str"; got != quoted {
			t.Errorf("%s: quoted = %v, want %v (node %+v)", in, got, quoted, n)
		}
	}
	if n := dataNode([]any{json.Number("12345678901234567890.5")}, "numeric[]"); n.Content[0].Tag != "!!str" {
		t.Errorf("array elements must be quoted too: %+v", n.Content[0])
	}
	if n := dataNode(json.Number("12345678901234567890.5"), "jsonb"); n.Tag == "!!str" {
		t.Error("a jsonb number must stay a number")
	}
}
