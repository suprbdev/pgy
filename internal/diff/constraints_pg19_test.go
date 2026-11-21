package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func enforcedLive(version int, check, fk string) *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	live.ServerVersion = version
	p := newLiveTable()
	p.Columns["id"] = &LiveColumn{Type: "integer"}
	p.HasPK = true
	p.Constraints["p_pkey"] = true
	p.ConstraintDefs["p_pkey"] = "PRIMARY KEY (id)"
	live.Tables["app.p"] = p
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	lt.Columns["pid"] = &LiveColumn{Type: "integer", Nullable: true}
	lt.Columns["a"] = &LiveColumn{Type: "integer", Nullable: true}
	lt.Constraints["t_ck"], lt.ConstraintDefs["t_ck"], lt.ConstraintComments["t_ck"] = true, check, "ck note"
	lt.Constraints["t_fk"], lt.ConstraintDefs["t_fk"], lt.ConstraintComments["t_fk"] = true, fk, "fk note"
	live.Tables["app.t"] = lt
	return live
}

func enforcedDesired(notEnforced, notValid bool, validate string) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{
		"app.p": {Name: "p", Columns: map[string]*schema.Column{"id": {Type: "int", PrimaryKey: true}}},
		"app.t": {
			Name:        "t",
			Validate:    validate,
			Columns:     map[string]*schema.Column{"id": {Type: "int"}, "pid": {Type: "int", Nullable: true}, "a": {Type: "int", Nullable: true}},
			Constraints: []*schema.Constraint{{Name: "t_ck", Type: "check", Expression: "a > 0", NotEnforced: notEnforced, NotValid: notValid, Comment: "ck note"}},
			ForeignKeys: []*schema.ForeignKey{{Name: "t_fk", Columns: []string{"pid"}, RefTable: "app.p", RefColumns: []string{"id"}, NotEnforced: notEnforced, NotValid: notValid, Comment: "fk note"}},
		},
	}}
}

const (
	enforcedCheck    = "CHECK ((a > 0))"
	notEnforcedCheck = "CHECK ((a > 0)) NOT ENFORCED"
	enforcedFK       = "FOREIGN KEY (pid) REFERENCES app.p(id)"
	notEnforcedFK    = "FOREIGN KEY (pid) REFERENCES app.p(id) NOT ENFORCED"
)

func lockMessages(p *PlanDiff) []string {
	out := []string{}
	for _, w := range p.Warnings {
		if w.Kind == "lock" {
			out = append(out, w.Message)
		}
	}
	return out
}

func assertPlanSQL(t *testing.T, p *PlanDiff, main, concurrent []string) {
	t.Helper()
	if got := strings.Join(append(append([]string{}, p.Alters...), p.Creates...), "\n"); got != strings.Join(main, "\n") {
		t.Errorf("main migration:\ngot  %s\nwant %s", got, strings.Join(main, "\n"))
	}
	if got := strings.Join(p.Concurrent, "\n"); got != strings.Join(concurrent, "\n") {
		t.Errorf("no-transaction migration:\ngot  %s\nwant %s", got, strings.Join(concurrent, "\n"))
	}
}

func TestEnforcedToggleNotEnforcedInPlace(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		p := Plan(enforcedLive(190000, enforcedCheck, enforcedFK), enforcedDesired(true, false, ""), unsafe)
		assertPlanSQL(t, p, []string{
			`alter table "app"."t" alter constraint "t_ck" not enforced;`,
			`alter table "app"."t" alter constraint "t_fk" not enforced;`,
		}, nil)
		if locks := lockMessages(p); len(locks) != 0 {
			t.Errorf("NOT ENFORCED checks nothing, got %v", locks)
		}
	}
}

func TestEnforcedToggleCheckBefore19(t *testing.T) {
	p := Plan(enforcedLive(180000, enforcedCheck, enforcedFK), enforcedDesired(true, false, ""), false)
	assertPlanSQL(t, p, []string{
		`alter table "app"."t" drop constraint "t_ck";`,
		`alter table "app"."t" add constraint "t_ck" check (a > 0) not enforced;`,
		`alter table "app"."t" alter constraint "t_fk" not enforced;`,
		`comment on constraint "t_ck" on "app"."t" is 'ck note';`,
	}, nil)
	if locks := lockMessages(p); len(locks) != 0 {
		t.Errorf("NOT ENFORCED checks nothing, got %v", locks)
	}
}

func TestEnforcedToggleEnforceValidatesLater(t *testing.T) {
	for _, version := range []int{180000, 190000} {
		p := Plan(enforcedLive(version, notEnforcedCheck, notEnforcedFK), enforcedDesired(false, false, ""), false)
		assertPlanSQL(t, p, []string{
			`alter table "app"."t" drop constraint "t_ck";`,
			`alter table "app"."t" add constraint "t_ck" check (a > 0) not valid;`,
			`alter table "app"."t" drop constraint "t_fk";`,
			`alter table "app"."t" add constraint "t_fk" foreign key ("pid") references "app"."p"("id") not valid;`,
			`comment on constraint "t_fk" on "app"."t" is 'fk note';`,
			`comment on constraint "t_ck" on "app"."t" is 'ck note';`,
		}, []string{
			`alter table "app"."t" validate constraint "t_fk";`,
			`alter table "app"."t" validate constraint "t_ck";`,
		})
		if locks := lockMessages(p); len(locks) != 0 {
			t.Errorf("PostgreSQL %d: adding NOT VALID locks nothing for long, got %v", version/10000, locks)
		}
	}
}

func TestEnforcedToggleEnforceNotValid(t *testing.T) {
	p := Plan(enforcedLive(190000, notEnforcedCheck, notEnforcedFK), enforcedDesired(false, true, ""), false)
	assertPlanSQL(t, p, []string{
		`alter table "app"."t" drop constraint "t_ck";`,
		`alter table "app"."t" add constraint "t_ck" check (a > 0) not valid;`,
		`alter table "app"."t" drop constraint "t_fk";`,
		`alter table "app"."t" add constraint "t_fk" foreign key ("pid") references "app"."p"("id") not valid;`,
		`comment on constraint "t_fk" on "app"."t" is 'fk note';`,
		`comment on constraint "t_ck" on "app"."t" is 'ck note';`,
	}, nil)
}

func TestEnforcedToggleEnforceImmediateInPlace(t *testing.T) {
	p := Plan(enforcedLive(190000, notEnforcedCheck, notEnforcedFK), enforcedDesired(false, false, schema.ValidateImmediate), true)
	assertPlanSQL(t, p, []string{
		`alter table "app"."t" alter constraint "t_ck" enforced;`,
		`alter table "app"."t" alter constraint "t_fk" enforced;`,
	}, nil)
	locks := strings.Join(lockMessages(p), "\n")
	for _, want := range []string{
		"enforcing check t_ck scans all of app.t under an ACCESS EXCLUSIVE lock",
		"enforcing foreign key t_fk checks every row of app.t under an ACCESS EXCLUSIVE lock",
	} {
		if !strings.Contains(locks, want) {
			t.Errorf("missing lock warning %q in:\n%s", want, locks)
		}
	}

	p = Plan(enforcedLive(180000, notEnforcedCheck, notEnforcedFK), enforcedDesired(false, false, schema.ValidateImmediate), false)
	assertPlanSQL(t, p, []string{
		`alter table "app"."t" drop constraint "t_ck";`,
		`alter table "app"."t" add constraint "t_ck" check (a > 0);`,
		`alter table "app"."t" alter constraint "t_fk" enforced;`,
		`comment on constraint "t_ck" on "app"."t" is 'ck note';`,
	}, nil)
	if locks := lockMessages(p); len(locks) != 2 {
		t.Errorf("want lock warnings for the check and the foreign key, got %v", locks)
	}
}

func TestEnforcedToggleBefore18(t *testing.T) {
	p := Plan(enforcedLive(170000, enforcedCheck, enforcedFK), enforcedDesired(true, false, ""), false)
	assertPlanSQL(t, p, nil, nil)
	warnings := []string{}
	for _, w := range p.Warnings {
		warnings = append(warnings, w.Message)
	}
	all := strings.Join(warnings, "\n")
	for _, want := range []string{
		"constraint t_ck on app.t (enforced: false) needs PostgreSQL 18",
		"foreign key t_fk on app.t (enforced: false) needs PostgreSQL 18",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
}

func TestEnforcedToggleSkippedIfMatches(t *testing.T) {
	for _, version := range []int{180000, 190000} {
		if p := Plan(enforcedLive(version, notEnforcedCheck, notEnforcedFK), enforcedDesired(true, false, ""), true); len(p.Alters)+len(p.Concurrent) != 0 {
			t.Errorf("PostgreSQL %d: matching NOT ENFORCED constraints must not diff, got %v %v", version/10000, p.Alters, p.Concurrent)
		}
	}
}

func TestEnforcedToggleWithOtherChangeNeedsUnsafe(t *testing.T) {
	d := enforcedDesired(true, false, "")
	d.Tables["app.t"].Constraints[0].Expression = "a > 1"
	p := Plan(enforcedLive(190000, enforcedCheck, enforcedFK), d, false)
	if findAlter(p, `"t_ck"`) {
		t.Errorf("a check whose expression changes too is left to --unsafe, got %v", p.Alters)
	}
	p = Plan(enforcedLive(190000, enforcedCheck, enforcedFK), d, true)
	if !findAlter(p, `alter table "app"."t" add constraint "t_ck" check (a > 1) not enforced;`) {
		t.Errorf("--unsafe drops and adds it, got %v", p.Alters)
	}
}

func TestNotEnforcedConstraintAddedWithoutLockWarning(t *testing.T) {
	live := enforcedLive(180000, enforcedCheck, enforcedFK)
	lt := live.Tables["app.t"]
	for _, name := range []string{"t_ck", "t_fk"} {
		delete(lt.Constraints, name)
		delete(lt.ConstraintDefs, name)
	}
	p := Plan(live, enforcedDesired(true, false, ""), false)
	for _, want := range []string{
		`alter table "app"."t" add constraint "t_ck" check (a > 0) not enforced;`,
		`alter table "app"."t" add constraint "t_fk" foreign key ("pid") references "app"."p"("id") not enforced;`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
	if locks := lockMessages(p); len(locks) != 0 {
		t.Errorf("a NOT ENFORCED constraint scans nothing, got %v", locks)
	}
}

func TestValidateImmediateReplacementKeepsComment(t *testing.T) {
	live := enforcedLive(170000, "CHECK ((a > 0)) NOT VALID", "FOREIGN KEY (pid) REFERENCES app.p(id) NOT VALID")
	p := Plan(live, enforcedDesired(false, false, schema.ValidateImmediate), false)
	for _, want := range []string{
		`alter table "app"."t" drop constraint "t_ck";`,
		`alter table "app"."t" drop constraint "t_fk";`,
		`comment on constraint "t_ck" on "app"."t" is 'ck note';`,
		`comment on constraint "t_fk" on "app"."t" is 'fk note';`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %v", want, p.Alters)
		}
	}
}

func virtualLive(version int) *Live {
	live := liveWithTable("app.t", map[string]*LiveColumn{
		"a": {Type: "integer", Nullable: true},
		"g": {Type: "integer", Nullable: true, Generated: "(a * 2)", Virtual: true},
	})
	live.ServerVersion = version
	live.Schemas["app"] = true
	return live
}

func virtualDesired() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{
		"a": {Type: "int", Nullable: true}, "g": {Type: "int", Nullable: true, Generated: "a * 3", Virtual: true},
	}}}}
}

func TestVirtualSetExpressionWithCheckOn18(t *testing.T) {
	live := virtualLive(180000)
	live.Tables["app.t"].Constraints["t_a_check"] = true
	live.Tables["app.t"].ConstraintDefs = map[string]string{"t_a_check": "CHECK ((a > 0)) NOT ENFORCED"}
	d := virtualDesired()
	d.Tables["app.t"].Constraints = []*schema.Constraint{{Name: "t_a_check", Type: "check", Expression: "a > 0", NotEnforced: true}}
	p := Plan(live, d, false)
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "PostgreSQL 18 rejects SET EXPRESSION on a virtual generated column while its table, or a partition or child table of it, has a check constraint (19 allows it); --unsafe drops the column and adds it again") {
		t.Errorf("without --unsafe only a warning: %+v", p.Warnings)
	}
	p = Plan(live, d, true)
	sql := Render(p)
	statementOrder(t, sql, `alter table "app"."t" drop column "g";`, `alter table "app"."t" add column "g" int generated always as (a * 3) virtual;`)
	if strings.Contains(sql, "set expression") {
		t.Errorf("PostgreSQL 18 rejects SET EXPRESSION here, got:\n%s", sql)
	}

	live.ServerVersion = 190000
	if p := Plan(live, d, true); !findAlter(p, `alter table "app"."t" alter column "g" set expression as (a * 3);`) {
		t.Errorf("PostgreSQL 19 uses SET EXPRESSION, got %v", p.Alters)
	}
}

func TestVirtualSetExpressionWithoutCheckOn18(t *testing.T) {
	live := virtualLive(180000)
	live.Tables["app.t"].Constraints["t_a_not_null"] = true
	live.Tables["app.t"].ConstraintDefs = map[string]string{"t_a_not_null": "NOT NULL a"}
	if p := Plan(live, virtualDesired(), true); !findAlter(p, `alter table "app"."t" alter column "g" set expression as (a * 3);`) {
		t.Errorf("a NOT NULL constraint doesn't stop SET EXPRESSION, got %v", p.Alters)
	}
}

func TestVirtualSetExpressionWithCheckOnPartitionOn18(t *testing.T) {
	live := virtualLive(180000)
	live.Tables["app.t"].Partitioned = true
	part := newLiveTable()
	part.Columns["a"] = &LiveColumn{Type: "integer", Nullable: true, Inherited: true}
	part.Columns["g"] = &LiveColumn{Type: "integer", Nullable: true, Generated: "(a * 2)", Virtual: true, Inherited: true}
	part.Constraints["t1_a_check"] = true
	part.ConstraintDefs["t1_a_check"] = "CHECK ((a > 0))"
	live.Tables["app.t1"] = part
	live.Partitions = map[string][]string{"app.t": {"app.t1"}}
	p := Plan(live, virtualDesired(), true)
	if sql := Render(p); strings.Contains(sql, `"g"`) {
		t.Errorf("a partitioned table keeps its column, got:\n%s", sql)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "and pgy can't drop the column and add it again, because app.t is partitioned") {
		t.Errorf("want the hand-written migration warning, got %+v", p.Warnings)
	}
}

func TestSetExpressionLockWarnings(t *testing.T) {
	live := virtualLive(190000)
	live.Tables["app.t"].Columns["s"] = &LiveColumn{Type: "integer", Nullable: true, Generated: "(a * 2)"}
	d := virtualDesired()
	d.Tables["app.t"].Columns["s"] = &schema.Column{Type: "int", Nullable: true, Generated: "a * 3"}
	locks := lockMessages(Plan(live, d, true))
	if len(locks) != 1 || locks[0] != "SET EXPRESSION on app.t.s rewrites app.t and its indexes under an ACCESS EXCLUSIVE lock" {
		t.Errorf("want only the stored column's rewrite warning, got %v", locks)
	}

	live.Tables["app.t"].Columns["g"].Nullable = false
	d.Tables["app.t"].Columns["g"].Nullable = false
	lt := live.Tables["app.t"]
	lt.ConstraintDefs = map[string]string{
		"t_g_check": "CHECK ((g > 0))",
		"t_g_later": "CHECK ((g < 100)) NOT VALID",
		"t_g_loose": "CHECK ((g < 1000)) NOT ENFORCED",
		"t_a_check": "CHECK ((a > 0))",
	}
	d.Tables["app.t"].Constraints = []*schema.Constraint{
		{Name: "t_g_check", Type: "check", Expression: "g > 0"},
		{Name: "t_g_later", Type: "check", Expression: "g < 100", NotValid: true},
		{Name: "t_g_loose", Type: "check", Expression: "g < 1000", NotEnforced: true},
		{Name: "t_a_check", Type: "check", Expression: "a > 0"},
	}
	for name := range lt.ConstraintDefs {
		lt.Constraints[name] = true
	}
	want := "SET EXPRESSION on virtual generated column app.t.g checks every row of app.t against its NOT NULL constraint, check t_g_check under an ACCESS EXCLUSIVE lock"
	if locks := strings.Join(lockMessages(Plan(live, d, true)), "\n"); !strings.Contains(locks, want) {
		t.Errorf("missing %q in:\n%s", want, locks)
	}
}

func TestGeneratedKindSwitchReadds(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{
		"a": {Type: "integer", Nullable: true},
		"s": {Type: "integer", Nullable: true, Generated: "(a * 2)"},
		"v": {Type: "integer", Nullable: true, Generated: "(a * 3)", Virtual: true},
	})
	live.ServerVersion = 190000
	live.Schemas["app"] = true
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{
		"a": {Type: "int", Nullable: true},
		"s": {Type: "int", Nullable: true, Generated: "a * 2", Virtual: true},
		"v": {Type: "int", Nullable: true, Generated: "a * 3"},
	}}}}
	msgs := []string{}
	for _, w := range Plan(live, d, false).Warnings {
		msgs = append(msgs, w.Message)
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{
		"generated column app.t.s changed, but PostgreSQL can't turn a stored generated column into a virtual one in place; --unsafe drops the column and adds it again",
		"generated column app.t.v changed, but PostgreSQL can't turn a virtual generated column into a stored one in place; --unsafe drops the column and adds it again",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	sql := Render(Plan(live, d, true))
	statementOrder(t, sql, `alter table "app"."t" drop column "s";`, `alter table "app"."t" add column "s" int generated always as (a * 2) virtual;`)
	statementOrder(t, sql, `alter table "app"."t" drop column "v";`, `alter table "app"."t" add column "v" int generated always as (a * 3) stored;`)
	if strings.Contains(sql, "set expression") {
		t.Errorf("the expressions are unchanged, got:\n%s", sql)
	}

	live.ServerVersion = 170000
	delete(live.Tables["app.t"].Columns, "v")
	delete(d.Tables["app.t"].Columns, "v")
	p := Plan(live, d, true)
	if sql := Render(p); sql != "" {
		t.Errorf("PostgreSQL 17 has no virtual columns, got:\n%s", sql)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "virtual generated column app.t.s needs PostgreSQL 18") {
		t.Errorf("want the version warning, got %+v", p.Warnings)
	}
}

func TestVirtualGeneratedToPlainWarns(t *testing.T) {
	live := virtualLive(190000)
	d := virtualDesired()
	d.Tables["app.t"].Columns["g"] = &schema.Column{Type: "int", Nullable: true}
	p := Plan(live, d, true)
	if sql := Render(p); sql != "" {
		t.Errorf("PostgreSQL has no DROP EXPRESSION for a virtual column, got:\n%s", sql)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "column app.t.g is declared plain but is a virtual generated column") {
		t.Errorf("want a warning, got %+v", p.Warnings)
	}
}
