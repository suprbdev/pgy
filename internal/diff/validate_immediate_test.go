package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func validateImmediateLive() *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	live.ServerVersion = 180000
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	lt.Columns["ref"] = &LiveColumn{Type: "integer", Nullable: true}
	lt.Columns["v"] = &LiveColumn{Type: "integer", Nullable: true}
	lt.HasPK = true
	lt.Constraints["t_pkey"] = true
	lt.ConstraintDefs["t_pkey"] = "PRIMARY KEY (id)"
	live.Tables["app.t"] = lt
	return live
}

func validateImmediateDesired(table, check, fk, column string) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name:        "t",
		Validate:    table,
		PrimaryKey:  []string{"id"},
		Columns:     map[string]*schema.Column{"id": {Type: "int"}, "ref": {Type: "int", Validate: column}, "v": {Type: "int", Nullable: true}},
		Constraints: []*schema.Constraint{{Name: "t_v_check", Type: "check", Expression: "v > 0", Validate: check}},
		ForeignKeys: []*schema.ForeignKey{{Name: "t_ref_fkey", Columns: []string{"ref"}, RefTable: "app.t", RefColumns: []string{"id"}, Validate: fk}},
	}}}
}

func TestValidateImmediateAddsConstraintsInOneStep(t *testing.T) {
	for _, d := range []*schema.Database{
		validateImmediateDesired(schema.ValidateImmediate, "", "", ""),
		validateImmediateDesired("", schema.ValidateImmediate, schema.ValidateImmediate, schema.ValidateImmediate),
	} {
		p := Plan(validateImmediateLive(), d, false)
		sql := Render(p)
		for _, want := range []string{
			`alter table "app"."t" alter column "ref" set not null;`,
			`alter table "app"."t" add constraint "t_v_check" check (v > 0);`,
			`alter table "app"."t" add constraint "t_ref_fkey" foreign key ("ref") references "app"."t"("id");`,
		} {
			if !strings.Contains(sql, want) {
				t.Errorf("missing %q in:\n%s", want, sql)
			}
		}
		if strings.Contains(sql, "not valid") || len(p.Concurrent) != 0 {
			t.Errorf("validate: immediate adds the constraints validated, got:\n%s%v", sql, p.Concurrent)
		}
		locks := 0
		for _, w := range p.Warnings {
			if w.Kind == "lock" {
				locks++
			}
		}
		if locks != 3 {
			t.Errorf("want a lock warning for each one-step addition: %+v", p.Warnings)
		}
	}
}

func TestValidateLaterOverridesTable(t *testing.T) {
	d := validateImmediateDesired(schema.ValidateImmediate, schema.ValidateLater, schema.ValidateLater, schema.ValidateLater)
	p := Plan(validateImmediateLive(), d, false)
	wantStatements(t, p.Concurrent, []string{
		`alter table "app"."t" validate constraint "t_ref_not_null";`,
		`alter table "app"."t" validate constraint "t_ref_fkey";`,
		`alter table "app"."t" validate constraint "t_v_check";`,
	})
	if p := Plan(validateImmediateLive(), validateImmediateDesired("", "", "", ""), false); len(p.Concurrent) != 3 {
		t.Errorf("without validate the constraints are validated later: %v", p.Concurrent)
	}
}

func TestValidateImmediateReplacesNotValidConstraints(t *testing.T) {
	live := validateImmediateLive()
	lt := live.Tables["app.t"]
	lt.Columns["ref"].Nullable = false
	lt.Columns["ref"].NotNullName = "t_ref_not_null"
	lt.Columns["ref"].NotNullNotValid = "t_ref_not_null"
	for name, def := range map[string]string{
		"t_ref_not_null": "NOT NULL ref NOT VALID",
		"t_v_check":      "CHECK ((v > 0)) NOT VALID",
		"t_ref_fkey":     "FOREIGN KEY (ref) REFERENCES app.t(id) NOT VALID",
	} {
		lt.Constraints[name] = true
		lt.ConstraintDefs[name] = def
	}
	p := Plan(live, validateImmediateDesired(schema.ValidateImmediate, "", "", ""), false)
	if len(p.Concurrent) != 0 {
		t.Errorf("no VALIDATE for constraints validated immediately: %v", p.Concurrent)
	}
	sql := Render(p)
	for _, want := range []string{
		`alter table "app"."t" alter column "ref" set not null;`,
		"alter table \"app\".\"t\" drop constraint \"t_v_check\";\nalter table \"app\".\"t\" add constraint \"t_v_check\" check (v > 0);",
		"alter table \"app\".\"t\" drop constraint \"t_ref_fkey\";\nalter table \"app\".\"t\" add constraint \"t_ref_fkey\" foreign key (\"ref\") references \"app\".\"t\"(\"id\");",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}
