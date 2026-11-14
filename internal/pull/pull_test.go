package pull

import (
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
		SequenceDefs:   map[string]*diff.LiveSequence{"app.t_id_seq": {Type: "integer", Owner: "app.t.id"}},
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
