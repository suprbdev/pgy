package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestKeyIndexParametersRender(t *testing.T) {
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}, "code": {Type: "text"}, "extra": {Type: "text"}},
		PrimaryKey: []string{"id"}, PrimaryKeyName: "t_pk", PrimaryKeyInclude: []string{"code"}, PrimaryKeyWith: map[string]string{"fillfactor": "80"},
		Constraints: []*schema.Constraint{{Name: "t_code_uq", Type: "unique", Columns: []string{"code"}, Include: []string{"extra"}, With: map[string]string{"fillfactor": "70"}, Deferrable: true}},
	}}}
	sql := Render(Plan(emptyLive(), d, false))
	for _, want := range []string{
		`add constraint "t_pk" primary key ("id") include ("code") with (fillfactor = 80);`,
		`add constraint "t_code_uq" unique ("code") include ("extra") with (fillfactor = 70) deferrable;`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestUniqueConstraintParametersMatchLive(t *testing.T) {
	lt := &LiveTable{
		ConstraintDefs: map[string]string{"t_uq": "UNIQUE (code) INCLUDE (extra) WITH (fillfactor='70', deduplicate_items='off')"},
		IndexDefs:      map[string]*LiveIndex{"t_uq": {Options: map[string]string{"fillfactor": "70", "deduplicate_items": "off"}}},
	}
	ct := &schema.Constraint{Name: "t_uq", Type: "unique", Columns: []string{"code"}, Include: []string{"extra"}, With: map[string]string{"deduplicate_items": "OFF", "fillfactor": "70"}}
	if !constraintMatchesLive(lt, "t_uq", ct) {
		t.Error("equal parameters in another order and case must match")
	}
	ct.With = nil
	if !constraintMatchesLive(lt, "t_uq", ct) {
		t.Error("parameters left out are unmanaged")
	}
	ct.With = map[string]string{"fillfactor": "90"}
	if !constraintMatchesLive(lt, "t_uq", ct) {
		t.Error("changed parameters are changed in place, not by recreating the constraint")
	}
	got := strings.Join(keyIndexStorageStmts("app.t_uq", ct.With, lt.IndexDefs["t_uq"]), "\n")
	if got != "alter index \"app\".\"t_uq\" set (fillfactor = 90);\nalter index \"app\".\"t_uq\" reset (deduplicate_items);" {
		t.Errorf("in-place change: %s", got)
	}
	if got := keyIndexStorageStmts("app.t_uq", nil, lt.IndexDefs["t_uq"]); len(got) != 0 {
		t.Errorf("unmanaged parameters must not change: %v", got)
	}
	ct.With, ct.Include = nil, nil
	if constraintMatchesLive(lt, "t_uq", ct) {
		t.Error("a dropped include must not match")
	}
}

func TestIdentitySequenceName(t *testing.T) {
	o := &schema.Sequence{Name: "custom_seq", Start: "5"}
	if got := identityOptionsClause(o); got != ` (sequence name "custom_seq" start with 5)` {
		t.Errorf("clause: %s", got)
	}
	c := &schema.Column{Type: "int", Identity: "always", IdentityOptions: o}
	lc := &LiveColumn{Type: "integer", Identity: "always", IdentitySequence: &LiveSequence{Name: "app.t_id_seq", Start: "5"}}
	got := identityAlterStmts("app.t", `alter table "app"."t" alter column "id"`, c, lc, false)
	if strings.Join(got, "\n") != `alter sequence "app"."t_id_seq" rename to "custom_seq";` {
		t.Errorf("rename: %v", got)
	}
	o.Name = "other.custom_seq"
	got = identityAlterStmts("app.t", `alter table "app"."t" alter column "id"`, c, lc, false)
	if strings.Join(got, "\n") != "alter sequence \"app\".\"t_id_seq\" set schema \"other\";\nalter sequence \"other\".\"t_id_seq\" rename to \"custom_seq\";" {
		t.Errorf("move: %v", got)
	}
	lc.IdentitySequence.Name = "other.custom_seq"
	if got := identityAlterStmts("app.t", "x", c, lc, false); len(got) != 0 {
		t.Errorf("matching name must not diff: %v", got)
	}
}
