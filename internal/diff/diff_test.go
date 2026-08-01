package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// --- helpers ---

func emptyLive() *Live {
	return &Live{
		Schemas:            map[string]bool{},
		Tables:             map[string]*LiveTable{},
		Types:              map[string]bool{},
		Functions:          map[string]bool{},
		Extensions:         map[string]bool{},
		Views:              map[string]bool{},
		MatViews:           map[string]bool{},
		Sequences:          map[string]bool{},
		Domains:            map[string]bool{},
		Roles:              map[string]bool{},
		RoleMembers:        map[string]map[string]bool{},
		RoleComments:       map[string]string{},
		TableGrants:        map[string]map[string]map[string]bool{},
		FunctionGrants:     map[string]map[string]map[string]bool{},
		SchemaGrants:       map[string]map[string]map[string]bool{},
		ColumnGrants:       map[string]map[string]map[string]map[string]bool{},
		FunctionPublicExec: map[string]bool{},
		FunctionDefs:       map[string]*LiveFunction{},
		FunctionComments:   map[string]string{},
		Procedures:         map[string]bool{},
		ProcedureDefs:      map[string]*LiveProcedure{},
		EnumLabels:         map[string][]string{},
	}
}

func liveWithTable(fq string, cols map[string]*LiveColumn) *Live {
	l := emptyLive()
	l.Tables[fq] = &LiveTable{
		Columns:     cols,
		Constraints: map[string]bool{},
		Indexes:     map[string]bool{},
		Triggers:    map[string]string{},
	}
	return l
}

func liveWithTablePK(fq string, cols map[string]*LiveColumn) *Live {
	l := liveWithTable(fq, cols)
	l.Tables[fq].HasPK = true
	return l
}

func findCreate(p *PlanDiff, substr string) bool {
	for _, s := range p.Creates {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func findAlter(p *PlanDiff, substr string) bool {
	for _, s := range p.Alters {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func findDrop(p *PlanDiff, substr string) bool {
	for _, s := range p.Drops {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// --- create table ---

func TestPlanCreateAndAddColumn(t *testing.T) {
	live := &Live{Tables: map[string]*LiveTable{}}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {Name: "users", Columns: map[string]*schema.Column{
			"id":    {Type: "int", Nullable: false},
			"email": {Type: "text", Nullable: false},
		}},
	}}
	p := Plan(live, desired, false)
	if len(p.Creates) != 1 {
		t.Fatalf("want 1 create, got %d", len(p.Creates))
	}
	// now live has table with only id
	live = &Live{Tables: map[string]*LiveTable{"public.users": {Columns: map[string]*LiveColumn{"id": {Type: "int"}}}}}
	p = Plan(live, desired, false)
	if len(p.Creates) != 1 || !findCreate(p, "add column") {
		t.Fatalf("want 1 add column create, got %v", p.Creates)
	}
}

func TestCreateTableSQL(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.products": {
			Name: "products",
			Columns: map[string]*schema.Column{
				"id":    {Type: "int", Nullable: false},
				"price": {Type: "numeric", Nullable: true, Default: "0"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "create table if not exists") {
		t.Error("expected CREATE TABLE")
	}
	if !findCreate(p, "public") {
		t.Error("expected schema in CREATE TABLE")
	}
}

// --- column order ---

func TestColumnOrderPreservedInSQL(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name: "t",
			Columns: map[string]*schema.Column{
				"z": {Type: "text"},
				"a": {Type: "int"},
			},
			ColumnOrder: []string{"z", "a"},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if len(p.Creates) == 0 {
		t.Fatal("no creates")
	}
	sql := p.Creates[0]
	zIdx := strings.Index(sql, `"z"`)
	aIdx := strings.Index(sql, `"a"`)
	if zIdx == -1 || aIdx == -1 {
		t.Fatalf("columns not found in SQL: %s", sql)
	}
	if zIdx > aIdx {
		t.Errorf("z should appear before a: %s", sql)
	}
}

// --- primary key ---

func TestPrimaryKeyTableLevel(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:       "t",
			Columns:    map[string]*schema.Column{"id": {Type: "int"}},
			PrimaryKey: []string{"id"},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, "primary key") {
		t.Error("expected primary key alter")
	}
}

func TestPrimaryKeyColumnLevel(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name: "t",
			Columns: map[string]*schema.Column{
				"id": {Type: "int", PrimaryKey: true},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, "primary key") {
		t.Error("expected primary key alter")
	}
}

// --- foreign keys ---

func TestForeignKey(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.orders": {
			Name:    "orders",
			Columns: map[string]*schema.Column{"user_id": {Type: "int"}},
			ForeignKeys: []*schema.ForeignKey{
				{Name: "fk_user", Columns: []string{"user_id"}, RefTable: "public.users", RefColumns: []string{"id"}, OnDelete: "cascade"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, "foreign key") {
		t.Error("expected foreign key alter")
	}
	if !findAlter(p, "on delete cascade") {
		t.Error("expected on delete cascade")
	}
}

// --- indexes ---

func TestUniqueIndex(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"email": {Type: "text"}},
			Indexes: []*schema.Index{
				{Name: "idx_email", Columns: []string{"email"}, Unique: true},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "create unique index") {
		t.Error("expected CREATE UNIQUE INDEX")
	}
	if !findCreate(p, "idx_email") {
		t.Error("expected index name")
	}
}

func TestNonUniqueIndex(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"name": {Type: "text"}},
			Indexes: []*schema.Index{
				{Name: "idx_name", Columns: []string{"name"}, Unique: false},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "create index if not exists") {
		t.Error("expected non-unique CREATE INDEX")
	}
}

func TestIndexAutoName(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"col": {Type: "text"}},
			Indexes: []*schema.Index{
				{Columns: []string{"col"}}, // no name
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	// auto name includes table and column
	if !findCreate(p, "public_t_col") {
		t.Errorf("expected auto-generated index name containing table+col; creates: %v", p.Creates)
	}
}

func TestIndexUsingGist(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"geom": {Type: "geometry(Point, 4326)"}},
			Indexes: []*schema.Index{
				{Name: "idx_geom", Columns: []string{"geom"}, Using: "gist"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create index if not exists "idx_geom" on "public"."t" using gist("geom");`) {
		t.Errorf("expected USING gist index; creates: %v", p.Creates)
	}
}

func TestIndexUsingBtreeOmitted(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"name": {Type: "text"}},
			Indexes: []*schema.Index{
				{Name: "idx_name", Columns: []string{"name"}, Using: "btree"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if findCreate(p, "using btree") {
		t.Errorf("btree is default, should be omitted; creates: %v", p.Creates)
	}
	if !findCreate(p, `create index if not exists "idx_name"`) {
		t.Errorf("expected index create; creates: %v", p.Creates)
	}
}

func TestIndexPartial(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"email": {Type: "text"}, "deleted_at": {Type: "timestamptz", Nullable: true}},
			Indexes: []*schema.Index{
				{Name: "idx_active_email", Columns: []string{"email"}, Unique: true, Where: "deleted_at is null"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create unique index if not exists "idx_active_email" on "public"."t"("email") where (deleted_at is null);`) {
		t.Errorf("expected partial unique index; creates: %v", p.Creates)
	}
}

func TestIndexGistPartialCombined(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"geom": {Type: "geometry"}, "active": {Type: "boolean"}},
			Indexes: []*schema.Index{
				{Name: "idx_geom_active", Columns: []string{"geom"}, Using: "GIST", Where: "active"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `using gist("geom") where (active);`) {
		t.Errorf("expected combined gist+partial index; creates: %v", p.Creates)
	}
}

// --- constraints ---

func TestCheckConstraint(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"age": {Type: "int"}},
			Constraints: []*schema.Constraint{
				{Name: "chk_age", Type: "check", Expression: "age > 0"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, "check (age > 0)") {
		t.Errorf("expected check constraint; alters: %v", p.Alters)
	}
}

func TestUniqueConstraint(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"a": {Type: "text"}, "b": {Type: "text"}},
			Constraints: []*schema.Constraint{
				{Name: "uq_ab", Type: "unique", Columns: []string{"a", "b"}},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, "unique") {
		t.Errorf("expected unique constraint; alters: %v", p.Alters)
	}
}

func TestExcludeConstraint(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"range": {Type: "tstzrange"}},
			Constraints: []*schema.Constraint{
				{Name: "excl_r", Type: "exclude", Expression: "using gist (range with &&)"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, "exclude using gist") {
		t.Errorf("expected exclude constraint; alters: %v", p.Alters)
	}
}

// --- triggers ---

func TestTrigger(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Triggers: []*schema.Trigger{
				{Name: "trg_audit", Timing: "after", Events: []string{"insert", "update"}, Level: "row", Procedure: "audit_fn()"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "create trigger") {
		t.Error("expected CREATE TRIGGER")
	}
	if !findCreate(p, "AFTER") {
		t.Error("expected AFTER timing")
	}
	if !findCreate(p, "INSERT OR UPDATE") {
		t.Errorf("expected INSERT OR UPDATE events; creates: %v", p.Creates)
	}
}

func TestTriggerWhen(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"created_at": {Type: "timestamptz"}},
			Triggers: []*schema.Trigger{
				{Name: "trg_supersede", Timing: "after", Events: []string{"insert"}, Level: "row", Procedure: "supersede_fn()", When: "old.created_at <= new.created_at"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `for each row when (old.created_at <= new.created_at) execute procedure supersede_fn();`) {
		t.Errorf("expected WHEN guard before EXECUTE; creates: %v", p.Creates)
	}
}

func TestConstraintTriggerWhen(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Triggers: []*schema.Trigger{
				{Name: "trg_chk", Events: []string{"insert"}, Procedure: "chk_fn()", Constraint: true, InitiallyDeferred: true, When: "new.id > 0"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `deferrable initially deferred for each row when (new.id > 0) execute procedure chk_fn();`) {
		t.Errorf("expected WHEN guard on constraint trigger; creates: %v", p.Creates)
	}
}

func TestTriggerEmittedAfterDependentFunction(t *testing.T) {
	// SQL-language trigger function body references its own table, so it
	// declares dependsOn the table. Order must be: table -> function -> trigger.
	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			"public.t": {
				Name:    "t",
				Columns: map[string]*schema.Column{"id": {Type: "int"}},
				Triggers: []*schema.Trigger{
					{Name: "trg_t", Timing: "after", Events: []string{"insert"}, Level: "row", Procedure: "public.trg_fn()"},
				},
			},
		},
		Functions: map[string]*schema.Function{
			"public.trg_fn": {
				Name: "trg_fn", Schema: "public", ArgsSig: "()",
				Returns: "trigger", Language: "plpgsql",
				Body:      "begin return new; end",
				DependsOn: []string{"table public.t"},
			},
		},
	}
	p := Plan(emptyLive(), desired, false)
	tableIdx, fnIdx, trgIdx := -1, -1, -1
	for i, s := range p.Creates {
		if strings.Contains(s, "create table") { tableIdx = i }
		if strings.Contains(s, "create function") { fnIdx = i }
		if strings.Contains(s, "create trigger") { trgIdx = i }
	}
	if tableIdx < 0 || fnIdx < 0 || trgIdx < 0 {
		t.Fatalf("missing statements; creates: %v", p.Creates)
	}
	if !(tableIdx < fnIdx && fnIdx < trgIdx) {
		t.Errorf("want table < function < trigger, got table=%d function=%d trigger=%d; creates: %v", tableIdx, fnIdx, trgIdx, p.Creates)
	}
}

// --- grants ---

func TestGrantTableCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Grants:  map[string][]string{"kickly_member": {"insert", "select"}},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `grant insert, select on table "public"."t" to "kickly_member";`) {
		t.Errorf("expected table grant; alters: %v", p.Alters)
	}
}

func TestGrantSkippedIfLive(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	live.TableGrants["public.t"] = map[string]map[string]bool{
		"kickly_member": {"select": true, "insert": true},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Grants:  map[string][]string{"kickly_member": {"insert", "select"}},
		},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "grant") {
		t.Errorf("grants already live, should not re-grant; alters: %v", p.Alters)
	}
}

func TestGrantPartialMissing(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	live.TableGrants["public.t"] = map[string]map[string]bool{
		"kickly_member": {"select": true},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Grants:  map[string][]string{"kickly_member": {"insert", "select"}},
		},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, `grant insert on table "public"."t" to "kickly_member";`) {
		t.Errorf("expected grant of only missing priv; alters: %v", p.Alters)
	}
}

func TestGrantRevokeOnRemoval(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	live.TableGrants["public.t"] = map[string]map[string]bool{
		"kickly_member": {"select": true, "delete": true},
		"old_role":      {"select": true},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Grants:  map[string][]string{"kickly_member": {"select"}},
		},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, `revoke delete on table "public"."t" from "kickly_member";`) {
		t.Errorf("expected revoke of removed priv; alters: %v", p.Alters)
	}
	if !findAlter(p, `revoke select on table "public"."t" from "old_role";`) {
		t.Errorf("expected revoke of removed role; alters: %v", p.Alters)
	}
}

func TestGrantNoRevokeWithoutGrantsBlock(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	live.TableGrants["public.t"] = map[string]map[string]bool{
		"some_role": {"select": true},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
		},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "revoke") {
		t.Errorf("no grants block, grants unmanaged, should not revoke; alters: %v", p.Alters)
	}
}

// An explicitly empty privilege list (`role: []`) means "this role holds
// nothing here" and must revoke, not be silently dropped as unmanaged.
func TestGrantEmptyListRevokes(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	live.TableGrants["public.t"] = map[string]map[string]bool{
		"anon": {"select": true},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Grants:  map[string][]string{"anon": {}},
		},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, `revoke select on table "public"."t" from "anon";`) {
		t.Errorf("expected revoke for emptied grant list; alters: %v", p.Alters)
	}
}

// A grants block whose every role is empty is still a present block, so it
// stays authoritative rather than degrading to unmanaged.
func TestGrantAllRolesEmptyStillAuthoritative(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	live.TableGrants["public.t"] = map[string]map[string]bool{
		"anon": {"select": true, "insert": true},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Grants:  map[string][]string{"anon": {}},
		},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, `revoke insert, select on table "public"."t" from "anon";`) {
		t.Errorf("expected all privileges revoked; alters: %v", p.Alters)
	}
}

// Narrowing a table-wide grant to per-column grants: REVOKE ... ON TABLE also
// strips column privileges, so the revoke must be emitted before the column
// grant or it destroys it.
func TestGrantRevokeEmittedBeforeColumnGrant(t *testing.T) {
	live := liveWithTable("public.person", map[string]*LiveColumn{
		"id": {Type: "bigint"}, "email": {Type: "text"},
	})
	live.TableGrants["public.person"] = map[string]map[string]bool{
		"anon": {"select": true},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.person": {
			Name: "person",
			Columns: map[string]*schema.Column{
				"id":    {Type: "bigint", Grants: map[string][]string{"anon": {"select"}}},
				"email": {Type: "text"},
			},
			Grants: map[string][]string{"anon": {}},
		},
	}}
	p := Plan(live, desired, false)
	rv := alterIndex(p, `revoke select on table "public"."person" from "anon";`)
	gr := alterIndex(p, `grant select ("id") on table "public"."person" to "anon";`)
	if rv == -1 {
		t.Fatalf("expected table revoke; alters: %v", p.Alters)
	}
	if gr == -1 {
		t.Fatalf("expected column grant; alters: %v", p.Alters)
	}
	if rv > gr {
		t.Errorf("revoke must precede column grant or it strips it; alters: %v", p.Alters)
	}
}

// Every revoke precedes every grant across all objects, not just within one
// table — a later table's revoke must not land after an earlier table's grant.
func TestGrantAllRevokesPrecedeAllGrants(t *testing.T) {
	live := liveWithTable("public.aaa", map[string]*LiveColumn{"id": {Type: "int"}})
	live.Tables["public.zzz"] = &LiveTable{
		Columns:     map[string]*LiveColumn{"id": {Type: "int"}},
		Constraints: map[string]bool{}, ConstraintDefs: map[string]string{},
		Indexes: map[string]bool{}, Triggers: map[string]string{},
		Policies: map[string]*LivePolicy{},
	}
	live.TableGrants["public.zzz"] = map[string]map[string]bool{"anon": {"select": true}}

	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.aaa": {
			Name:    "aaa",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Grants:  map[string][]string{"anon": {"select"}},
		},
		"public.zzz": {
			Name:    "zzz",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Grants:  map[string][]string{"anon": {}},
		},
	}}
	p := Plan(live, desired, false)
	lastRevoke, firstGrant := -1, -1
	for i, s := range p.Alters {
		if strings.HasPrefix(s, "revoke ") {
			lastRevoke = i
		}
		if strings.HasPrefix(s, "grant ") && firstGrant == -1 {
			firstGrant = i
		}
	}
	if lastRevoke == -1 || firstGrant == -1 {
		t.Fatalf("expected both a revoke and a grant; alters: %v", p.Alters)
	}
	if lastRevoke > firstGrant {
		t.Errorf("all revokes must precede all grants; alters: %v", p.Alters)
	}
}

// --- column-level grants ---

func TestColumnGrantCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name: "users",
			Columns: map[string]*schema.Column{
				"id":    {Type: "int"},
				"email": {Type: "text", Grants: map[string][]string{"reporting": {"select", "update"}}},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `grant select ("email"), update ("email") on table "public"."users" to "reporting";`) {
		t.Errorf("expected column grant; alters: %v", p.Alters)
	}
}

func TestColumnGrantSkippedIfLive(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{"email": {Type: "text"}})
	live.ColumnGrants["public.users"] = map[string]map[string]map[string]bool{
		"email": {"reporting": {"select": true}},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name: "users",
			Columns: map[string]*schema.Column{
				"email": {Type: "text", Grants: map[string][]string{"reporting": {"select"}}},
			},
		},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "grant") {
		t.Errorf("column grant already live, should not re-grant; alters: %v", p.Alters)
	}
}

func TestColumnGrantRevokeOnRemoval(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{"email": {Type: "text"}})
	live.ColumnGrants["public.users"] = map[string]map[string]map[string]bool{
		"email": {
			"reporting": {"select": true, "update": true},
			"old_role":  {"select": true},
		},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name: "users",
			Columns: map[string]*schema.Column{
				"email": {Type: "text", Grants: map[string][]string{"reporting": {"select"}}},
			},
		},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, `revoke update ("email") on table "public"."users" from "reporting";`) {
		t.Errorf("expected revoke of removed column priv; alters: %v", p.Alters)
	}
	if !findAlter(p, `revoke select ("email") on table "public"."users" from "old_role";`) {
		t.Errorf("expected revoke of removed role; alters: %v", p.Alters)
	}
}

func TestColumnGrantUnmanagedWithoutBlock(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{"email": {Type: "text"}})
	live.ColumnGrants["public.users"] = map[string]map[string]map[string]bool{
		"email": {"some_role": {"select": true}},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name: "users",
			Columns: map[string]*schema.Column{
				"email": {Type: "text"}, // no grants block
			},
		},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "revoke") {
		t.Errorf("no column grants block, unmanaged, should not revoke; alters: %v", p.Alters)
	}
}

// --- view grants ---

func TestViewGrantCreate(t *testing.T) {
	desired := viewDesired("public.active_users", "select id from users", false)
	desired.Views["public.active_users"].Grants = map[string][]string{"reporting": {"select"}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `grant select on table "public"."active_users" to "reporting";`) {
		t.Errorf("expected view grant; alters: %v", p.Alters)
	}
}

func TestViewGrantSkippedIfLive(t *testing.T) {
	live := emptyLive()
	live.Views["public.active_users"] = true
	live.TableGrants["public.active_users"] = map[string]map[string]bool{
		"reporting": {"select": true},
	}
	desired := viewDesired("public.active_users", "select id from users", false)
	desired.Views["public.active_users"].Grants = map[string][]string{"reporting": {"select"}}
	p := Plan(live, desired, false)
	if findAlter(p, "grant select") {
		t.Errorf("view grant already live, should not re-grant; alters: %v", p.Alters)
	}
}

func TestViewGrantRevokeOnRemoval(t *testing.T) {
	live := emptyLive()
	live.Views["public.active_users"] = true
	live.TableGrants["public.active_users"] = map[string]map[string]bool{
		"reporting": {"select": true, "update": true},
	}
	desired := viewDesired("public.active_users", "select id from users", false)
	desired.Views["public.active_users"].Grants = map[string][]string{"reporting": {"select"}}
	p := Plan(live, desired, false)
	if !findAlter(p, `revoke update on table "public"."active_users" from "reporting";`) {
		t.Errorf("expected revoke of removed view priv; alters: %v", p.Alters)
	}
}

func TestMatViewGrantCreate(t *testing.T) {
	desired := viewDesired("public.user_stats", "select count(*) from users", true)
	desired.Views["public.user_stats"].Grants = map[string][]string{"reporting": {"select"}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `grant select on table "public"."user_stats" to "reporting";`) {
		t.Errorf("expected matview grant; alters: %v", p.Alters)
	}
}

func TestColumnGrantPublicNotAutoRevoked(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{"email": {Type: "text"}})
	live.ColumnGrants["public.users"] = map[string]map[string]map[string]bool{
		"email": {"public": {"select": true}},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name: "users",
			Columns: map[string]*schema.Column{
				"email": {Type: "text", Grants: map[string][]string{"reporting": {"select"}}},
			},
		},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "revoke") {
		t.Errorf("PUBLIC column grant must not be auto-revoked; alters: %v", p.Alters)
	}
}

func TestFunctionRevokePublicNewFunction(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.secret_fn": {
				Schema: "public", Name: "secret_fn", ArgsSig: "()",
				Returns: "int", Language: "sql", Security: "definer",
				Body:         "select 1",
				RevokePublic: true,
				Grants:       map[string][]string{"kickly_member": {"execute"}},
			},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `revoke all on function "public"."secret_fn"() from public;`) {
		t.Errorf("expected revoke public on new function; alters: %v", p.Alters)
	}
	if !findAlter(p, `grant execute on function "public"."secret_fn"() to "kickly_member";`) {
		t.Errorf("expected execute grant; alters: %v", p.Alters)
	}
}

func TestFunctionRevokePublicSkippedIfAlreadyRevoked(t *testing.T) {
	live := emptyLive()
	live.Functions["public.secret_fn()"] = true
	// FunctionPublicExec absent -> PUBLIC execute already revoked
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.secret_fn": {
				Schema: "public", Name: "secret_fn", ArgsSig: "()",
				Returns: "int", Language: "sql", Body: "select 1",
				RevokePublic: true,
			},
		},
	}
	p := Plan(live, desired, false)
	if findAlter(p, "revoke all") {
		t.Errorf("PUBLIC execute already revoked, should not re-revoke; alters: %v", p.Alters)
	}
}

func TestFunctionGrantSigStripsDefaults(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.get_setting": {
				Schema: "public", Name: "get_setting",
				ArgsSig: "(key text, default_value jsonb default null)",
				Returns: "jsonb", Language: "sql", Body: "select null::jsonb",
				Grants: map[string][]string{"app": {"execute"}},
			},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `grant execute on function "public"."get_setting"(key text, default_value jsonb) to "app";`) {
		t.Errorf("expected grant with defaults stripped from signature; alters: %v", p.Alters)
	}
}

func TestSchemaGrant(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		SchemaGrants: map[string]map[string][]string{
			"app": {"kickly_member": {"usage"}},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `grant usage on schema "app" to "kickly_member";`) {
		t.Errorf("expected schema grant; alters: %v", p.Alters)
	}
}

// --- row level security ---

func rlsTable(policies []*schema.Policy) *schema.Table {
	return &schema.Table{
		Name:             "t",
		Columns:          map[string]*schema.Column{"id": {Type: "bigint"}},
		RowLevelSecurity: schema.BoolPtr(true),
		Policies:         policies,
	}
}

func TestRLSEnable(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable(nil)}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `alter table "public"."t" enable row level security;`) {
		t.Errorf("expected enable RLS; alters: %v", p.Alters)
	}
}

func TestRLSSkippedIfEnabled(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["public.t"].RLSEnabled = true
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable(nil)}}
	p := Plan(live, desired, false)
	if findAlter(p, "row level security") {
		t.Errorf("RLS already enabled, should not re-enable; alters: %v", p.Alters)
	}
}

func TestRLSDisableUnsafe(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["public.t"].RLSEnabled = true
	tbl := rlsTable(nil)
	tbl.RowLevelSecurity = schema.BoolPtr(false)
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": tbl}}
	p := Plan(live, desired, true)
	if !findAlter(p, `alter table "public"."t" disable row level security;`) {
		t.Errorf("expected disable RLS; alters: %v", p.Alters)
	}
}

func TestRLSDisableSkippedWithoutUnsafe(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["public.t"].RLSEnabled = true
	tbl := rlsTable(nil)
	tbl.RowLevelSecurity = schema.BoolPtr(false)
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": tbl}}
	p := Plan(live, desired, false)
	if findAlter(p, "disable row level security") {
		t.Errorf("disable RLS must be gated behind --unsafe; alters: %v", p.Alters)
	}
}

func TestRLSDisableSkippedIfAlreadyDisabled(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	tbl := rlsTable(nil)
	tbl.RowLevelSecurity = schema.BoolPtr(false)
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": tbl}}
	p := Plan(live, desired, true)
	if findAlter(p, "row level security") {
		t.Errorf("RLS already disabled, should emit nothing; alters: %v", p.Alters)
	}
}

// A table with no rowLevelSecurity key is unmanaged: live RLS is left alone
// even under --unsafe.
func TestRLSUnmanagedLeavesLiveEnabled(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["public.t"].RLSEnabled = true
	tbl := rlsTable(nil)
	tbl.RowLevelSecurity = nil
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": tbl}}
	p := Plan(live, desired, true)
	if findAlter(p, "row level security") {
		t.Errorf("unmanaged RLS should emit nothing; alters: %v", p.Alters)
	}
}

func TestPolicyCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "member_select", For: "select", To: []string{"kickly_member"},
			Using: "member_id = current_setting('app.member_id')::bigint"},
	})}}
	p := Plan(emptyLive(), desired, false)
	want := `create policy "member_select" on "public"."t" for select to "kickly_member" using (member_id = current_setting('app.member_id')::bigint);`
	if !findAlter(p, want) {
		t.Errorf("expected %s; alters: %v", want, p.Alters)
	}
}

func TestPolicyWithCheck(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "member_insert", For: "insert", WithCheck: "member_id = 1"},
	})}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `create policy "member_insert" on "public"."t" for insert with check (member_id = 1);`) {
		t.Errorf("expected with check policy; alters: %v", p.Alters)
	}
}

func TestPolicySkippedIfExists(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["public.t"].RLSEnabled = true
	live.Tables["public.t"].Policies = map[string]*LivePolicy{
		"member_select": {Cmd: "select", Using: "true"},
	}
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "member_select", For: "select", Using: "true"},
	})}}
	p := Plan(live, desired, false)
	if findCreate(p, "create policy") || findAlter(p, "create policy") {
		t.Errorf("policy already live, should not re-create; creates: %v alters: %v", p.Creates, p.Alters)
	}
}

// Regression: existing live table + new policy referencing a function created in
// the same plan. Policy must come after the function create (previously the
// policy was emitted at the table's position in Creates -> SQLSTATE 42883).
func TestPolicyAfterFunctionCreate(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			"public.t": {
				Name:             "t",
				Columns:          map[string]*schema.Column{"id": {Type: "bigint"}},
				RowLevelSecurity: schema.BoolPtr(true),
				Policies: []*schema.Policy{
					{Name: "admin_all", Using: "public.is_organisation_admin(id)"},
				},
			},
		},
		Functions: map[string]*schema.Function{
			"public.is_organisation_admin": {
				Schema: "public", Name: "is_organisation_admin", ArgsSig: "(org uuid)",
				Returns: "boolean", Language: "sql", Body: "select true",
			},
		},
	}
	p := Plan(live, desired, false)
	if findCreate(p, "create policy") {
		t.Errorf("policy must not be in Creates; creates: %v", p.Creates)
	}
	if !findAlter(p, "create policy") {
		t.Fatalf("expected policy in Alters; alters: %v", p.Alters)
	}
	if !findCreate(p, "create function") {
		t.Fatalf("expected function create; creates: %v", p.Creates)
	}
	// Render order: all Creates (function) precede all Alters (policy)
	rendered := Render(p)
	fnIdx := strings.Index(rendered, "create function")
	polIdx := strings.Index(rendered, "create policy")
	if fnIdx == -1 || polIdx == -1 || polIdx < fnIdx {
		t.Errorf("policy must come after function create; rendered:\n%s", rendered)
	}
}

func TestPolicyDroppedOnRemoval(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["public.t"].RLSEnabled = true
	live.Tables["public.t"].Policies = map[string]*LivePolicy{"old_policy": {Cmd: "all"}}
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "member_select", For: "select", Using: "true"},
	})}}
	p := Plan(live, desired, false)
	found := false
	for _, s := range p.Drops {
		if s == `drop policy "old_policy" on "public"."t";` {
			found = true
		}
	}
	if !found {
		t.Errorf("expected drop of removed policy; drops: %v", p.Drops)
	}
}

// livePolicyTable returns a live table with RLS on and one policy installed.
func livePolicyTable(name string, lp *LivePolicy) *Live {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["public.t"].RLSEnabled = true
	live.Tables["public.t"].Policies = map[string]*LivePolicy{name: lp}
	return live
}

// assertPolicyReplaced checks the plan drops then recreates the policy, in
// that order, within Alters (Drops renders last, so a drop there would run
// after the recreate).
func assertPolicyReplaced(t *testing.T, p *PlanDiff, name string) {
	t.Helper()
	drop := `drop policy "` + name + `" on "public"."t";`
	dropAt, createAt := -1, -1
	for i, s := range p.Alters {
		if s == drop && dropAt == -1 {
			dropAt = i
		}
		if strings.HasPrefix(s, `create policy "`+name+`"`) && createAt == -1 {
			createAt = i
		}
	}
	if dropAt == -1 || createAt == -1 {
		t.Fatalf("expected drop+create of %q; alters: %v", name, p.Alters)
	}
	if dropAt > createAt {
		t.Errorf("drop must precede create; alters: %v", p.Alters)
	}
	for _, s := range p.Drops {
		if s == drop {
			t.Errorf("replace drop must live in Alters, not Drops (Drops renders last); drops: %v", p.Drops)
		}
	}
}

func TestPolicyReplacedOnChangedUsing(t *testing.T) {
	live := livePolicyTable("member_select", &LivePolicy{Cmd: "select", Using: "member_id = 1"})
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "member_select", For: "select", Using: "member_id = 2"},
	})}}
	p := Plan(live, desired, false)
	assertPolicyReplaced(t, p, "member_select")
	if !findAlter(p, `create policy "member_select" on "public"."t" for select using (member_id = 2);`) {
		t.Errorf("expected recreate with new using; alters: %v", p.Alters)
	}
}

func TestPolicyReplacedOnChangedWithCheck(t *testing.T) {
	live := livePolicyTable("member_insert", &LivePolicy{Cmd: "insert", WithCheck: "member_id = 1"})
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "member_insert", For: "insert", WithCheck: "member_id = 2"},
	})}}
	assertPolicyReplaced(t, Plan(live, desired, false), "member_insert")
}

func TestPolicyReplacedOnChangedCommand(t *testing.T) {
	live := livePolicyTable("p", &LivePolicy{Cmd: "select", Using: "true"})
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "p", For: "delete", Using: "true"},
	})}}
	assertPolicyReplaced(t, Plan(live, desired, false), "p")
}

func TestPolicyReplacedOnChangedRoles(t *testing.T) {
	live := livePolicyTable("p", &LivePolicy{Cmd: "all", Roles: []string{"alice"}, Using: "true"})
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "p", To: []string{"bob"}, Using: "true"},
	})}}
	assertPolicyReplaced(t, Plan(live, desired, false), "p")
}

// Narrowing PUBLIC (no TO clause) to a named role must be detected.
func TestPolicyReplacedWhenRolesNarrowed(t *testing.T) {
	live := livePolicyTable("p", &LivePolicy{Cmd: "all", Using: "true"})
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "p", To: []string{"app"}, Using: "true"},
	})}}
	assertPolicyReplaced(t, Plan(live, desired, false), "p")
}

// An omitted `for:` means ALL, matching a live polcmd of '*'.
func TestPolicyOmittedForMatchesLiveAll(t *testing.T) {
	live := livePolicyTable("p", &LivePolicy{Cmd: "all", Using: "true"})
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "p", Using: "true"},
	})}}
	p := Plan(live, desired, false)
	if findAlter(p, "policy") {
		t.Errorf("unchanged policy should emit nothing; alters: %v", p.Alters)
	}
}

// PostgreSQL echoes expressions back with added casts, parens and quoting.
// Those must not read as a change, or every plan would churn the policy.
func TestPolicyNotReplacedOnEquivalentExpression(t *testing.T) {
	live := livePolicyTable("p", &LivePolicy{
		Cmd:   "select",
		Roles: []string{"app"},
		Using: "(member_id = (current_setting('app.member_id'::text))::bigint)",
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "p", For: "select", To: []string{"app"},
			Using: "member_id = current_setting('app.member_id')::bigint"},
	})}}
	p := Plan(live, desired, false)
	if findAlter(p, "policy") {
		t.Errorf("equivalent expression should emit nothing; alters: %v", p.Alters)
	}
}

// Role comparison is case- and order-insensitive.
func TestPolicyRoleOrderAndCaseIgnored(t *testing.T) {
	live := livePolicyTable("p", &LivePolicy{Cmd: "all", Roles: []string{"alice", "bob"}, Using: "true"})
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "p", To: []string{"BOB", "alice"}, Using: "true"},
	})}}
	p := Plan(live, desired, false)
	if findAlter(p, "policy") {
		t.Errorf("same roles reordered should emit nothing; alters: %v", p.Alters)
	}
}

func TestPolicyNoDropWithoutPoliciesBlock(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["public.t"].Policies = map[string]*LivePolicy{"some_policy": {Cmd: "all"}}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "bigint"}}},
	}}
	p := Plan(live, desired, false)
	if len(p.Drops) != 0 {
		t.Errorf("no policies block, policies unmanaged, should not drop; drops: %v", p.Drops)
	}
}

// --- replace-on-change ---

func loginFn(body string) *schema.Function {
	return &schema.Function{
		Schema: "public", Name: "login", ArgsSig: "(email text)",
		Returns: "int", Language: "sql", Body: body,
	}
}

func liveWithLoginFn(body string) *Live {
	l := emptyLive()
	l.Functions["public.login(email text)"] = true
	l.FunctionDefs[normalizeFunctionSignature("public.login(email text)")] = &LiveFunction{
		Body: body, Volatility: "volatile", Security: "invoker",
	}
	return l
}

func TestFunctionReplaceOnBodyChange(t *testing.T) {
	live := liveWithLoginFn("select 1")
	desired := &schema.Database{
		Tables:    map[string]*schema.Table{},
		Functions: map[string]*schema.Function{"public.login": loginFn("select 2")},
	}
	p := Plan(live, desired, false)
	if !findCreate(p, "create or replace function") {
		t.Errorf("expected CREATE OR REPLACE on body change; creates: %v", p.Creates)
	}
	if !findCreate(p, "select 2") {
		t.Errorf("expected new body; creates: %v", p.Creates)
	}
}

func TestFunctionSkippedIfBodySame(t *testing.T) {
	live := liveWithLoginFn("select 1")
	desired := &schema.Database{
		Tables:    map[string]*schema.Table{},
		Functions: map[string]*schema.Function{"public.login": loginFn("select 1")},
	}
	p := Plan(live, desired, false)
	if findCreate(p, "function") {
		t.Errorf("body unchanged, should skip; creates: %v", p.Creates)
	}
}

func TestFunctionBodyCompareTrimsWhitespace(t *testing.T) {
	live := liveWithLoginFn("\nselect 1\n")
	desired := &schema.Database{
		Tables:    map[string]*schema.Table{},
		Functions: map[string]*schema.Function{"public.login": loginFn("select 1")},
	}
	p := Plan(live, desired, false)
	if findCreate(p, "function") {
		t.Errorf("whitespace-only diff, should skip; creates: %v", p.Creates)
	}
}

func TestFunctionReplaceOnVolatilityChange(t *testing.T) {
	live := liveWithLoginFn("select 1")
	f := loginFn("select 1")
	f.Volatility = "stable"
	desired := &schema.Database{
		Tables:    map[string]*schema.Table{},
		Functions: map[string]*schema.Function{"public.login": f},
	}
	p := Plan(live, desired, false)
	if !findCreate(p, "create or replace function") {
		t.Errorf("expected replace on volatility change; creates: %v", p.Creates)
	}
}

func TestViewReplaceFlag(t *testing.T) {
	live := emptyLive()
	live.Views["public.v"] = true
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Views: map[string]*schema.View{
			"public.v": {Schema: "public", Name: "v", Query: "select 2", Replace: true},
		},
	}
	p := Plan(live, desired, false)
	if !findCreate(p, `create or replace view "public"."v" as select 2;`) {
		t.Errorf("expected replace with flag; creates: %v", p.Creates)
	}
	// without flag: skipped
	desired.Views["public.v"].Replace = false
	p = Plan(live, desired, false)
	if findCreate(p, "view") {
		t.Errorf("no replace flag, should skip; creates: %v", p.Creates)
	}
}

func TestEnumAddValue(t *testing.T) {
	live := emptyLive()
	live.Types["public.status"] = true
	live.EnumLabels["public.status"] = []string{"active", "closed"}
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Types: map[string]*schema.TypeDef{
			"public.status": {Schema: "public", Name: "status", Kind: "enum",
				Labels: []string{"active", "pending", "closed", "archived"}},
		},
	}
	p := Plan(live, desired, false)
	if !findAlter(p, `alter type "public"."status" add value 'pending' before 'closed';`) {
		t.Errorf("expected positioned add value; alters: %v", p.Alters)
	}
	if !findAlter(p, `alter type "public"."status" add value 'archived';`) {
		t.Errorf("expected appended add value; alters: %v", p.Alters)
	}
	if findCreate(p, "create type") {
		t.Errorf("type exists, should not re-create; creates: %v", p.Creates)
	}
}

func TestEnumSkippedIfSame(t *testing.T) {
	live := emptyLive()
	live.Types["public.status"] = true
	live.EnumLabels["public.status"] = []string{"a", "b"}
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Types: map[string]*schema.TypeDef{
			"public.status": {Schema: "public", Name: "status", Kind: "enum", Labels: []string{"a", "b"}},
		},
	}
	p := Plan(live, desired, false)
	if len(p.Alters) != 0 {
		t.Errorf("labels unchanged, expected no alters; got %v", p.Alters)
	}
}

// --- comments ---

func TestCommentTableAndColumn(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Comment: "@behavior +list",
			Columns: map[string]*schema.Column{"id": {Type: "bigint", Comment: "@name theId"}},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `comment on table "public"."t" is '@behavior +list';`) {
		t.Errorf("expected table comment; alters: %v", p.Alters)
	}
	if !findAlter(p, `comment on column "public"."t"."id" is '@name theId';`) {
		t.Errorf("expected column comment; alters: %v", p.Alters)
	}
}

func TestCommentSkippedIfSame(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint", Comment: "same"}})
	live.RelComments = map[string]string{"public.t": "tbl"}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Comment: "tbl",
			Columns: map[string]*schema.Column{"id": {Type: "bigint", Comment: "same"}},
		},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "comment on") {
		t.Errorf("comments unchanged, should not re-emit; alters: %v", p.Alters)
	}
}

func TestCommentUpdatedIfChanged(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.RelComments = map[string]string{"public.t": "old"}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Comment: "new", Columns: map[string]*schema.Column{"id": {Type: "bigint"}}},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, `comment on table "public"."t" is 'new';`) {
		t.Errorf("expected updated comment; alters: %v", p.Alters)
	}
}

func TestCommentEscapesQuotes(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Comment: "it's quoted", Columns: map[string]*schema.Column{"id": {Type: "int"}}},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `comment on table "public"."t" is 'it''s quoted';`) {
		t.Errorf("expected escaped quote; alters: %v", p.Alters)
	}
}

func TestCommentFunctionViewSchemaType(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.fn": {Schema: "public", Name: "fn", ArgsSig: "(a int default 0)",
				Returns: "int", Language: "sql", Body: "select a", Comment: "fn doc"},
		},
		Views: map[string]*schema.View{
			"public.v":  {Schema: "public", Name: "v", Query: "select 1", Comment: "view doc"},
			"public.mv": {Schema: "public", Name: "mv", Query: "select 1", Materialized: true, Comment: "mv doc"},
		},
		Types: map[string]*schema.TypeDef{
			"public.status": {Schema: "public", Name: "status", Kind: "enum", Labels: []string{"a"}, Comment: "type doc"},
		},
		SchemaComments: map[string]string{"public": "schema doc"},
	}
	p := Plan(emptyLive(), desired, false)
	for _, want := range []string{
		`comment on function "public"."fn"(a int) is 'fn doc';`,
		`comment on view "public"."v" is 'view doc';`,
		`comment on materialized view "public"."mv" is 'mv doc';`,
		`comment on type "public"."status" is 'type doc';`,
		`comment on schema "public" is 'schema doc';`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %s; alters: %v", want, p.Alters)
		}
	}
}

// --- column defaults ---

func TestColumnDefaultBoolRendered(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"active": {Type: "boolean", Default: "false"}},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "default false") {
		t.Errorf("expected default false in CREATE TABLE; creates: %v", p.Creates)
	}
}

// --- FK ordering ---

func TestCircularFKAltersAfterAllPKs(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.person": {
			Name:       "person",
			Columns:    map[string]*schema.Column{"id": {Type: "bigint"}, "asset_id": {Type: "bigint"}},
			PrimaryKey: []string{"id"},
			ForeignKeys: []*schema.ForeignKey{
				{Name: "fk_person_asset", Columns: []string{"asset_id"}, RefTable: "public.asset", RefColumns: []string{"id"}},
			},
		},
		"public.asset": {
			Name:       "asset",
			Columns:    map[string]*schema.Column{"id": {Type: "bigint"}, "owner_id": {Type: "bigint"}},
			PrimaryKey: []string{"id"},
			ForeignKeys: []*schema.ForeignKey{
				{Name: "fk_asset_owner", Columns: []string{"owner_id"}, RefTable: "public.person", RefColumns: []string{"id"}},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	lastPK, firstFK := -1, -1
	for i, s := range p.Alters {
		if strings.Contains(s, "add primary key") && i > lastPK {
			lastPK = i
		}
		if strings.Contains(s, "foreign key") && firstFK == -1 {
			firstFK = i
		}
	}
	if lastPK == -1 || firstFK == -1 {
		t.Fatalf("expected both PK and FK alters; alters: %v", p.Alters)
	}
	if firstFK < lastPK {
		t.Errorf("FK alter at %d precedes PK alter at %d; alters: %v", firstFK, lastPK, p.Alters)
	}
}

func TestConstraintTriggerCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"app.entry": {
			Name:    "entry",
			Columns: map[string]*schema.Column{"id": {Type: "bigint"}},
			Triggers: []*schema.Trigger{
				{Name: "trg_check", Constraint: true, InitiallyDeferred: true,
					Events: []string{"insert", "update"}, Procedure: "app.check_entry_requirements()"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	want := `create constraint trigger "trg_check" AFTER INSERT OR UPDATE on "app"."entry" deferrable initially deferred for each row execute procedure app.check_entry_requirements();`
	if !findCreate(p, want) {
		t.Errorf("expected %s; creates: %v", want, p.Creates)
	}
}

func TestConstraintTriggerDeferrableOnly(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"app.entry": {
			Name:    "entry",
			Columns: map[string]*schema.Column{"id": {Type: "bigint"}},
			Triggers: []*schema.Trigger{
				{Name: "trg_check", Constraint: true, Deferrable: true,
					Events: []string{"insert"}, Procedure: "f()"},
			},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `on "app"."entry" deferrable for each row`) {
		t.Errorf("expected deferrable without initially deferred; creates: %v", p.Creates)
	}
}

func TestConstraintTriggerSkippedIfExists(t *testing.T) {
	live := liveWithTable("app.entry", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["app.entry"].Triggers = map[string]string{"trg_check": `CREATE CONSTRAINT TRIGGER trg_check AFTER INSERT ON app.entry DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION f()`}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"app.entry": {
			Name:    "entry",
			Columns: map[string]*schema.Column{"id": {Type: "bigint"}},
			Triggers: []*schema.Trigger{
				{Name: "trg_check", Constraint: true, InitiallyDeferred: true,
					Events: []string{"insert"}, Procedure: "f()"},
			},
		},
	}}
	p := Plan(live, desired, false)
	if findCreate(p, "trg_check") {
		t.Errorf("constraint trigger already live, should skip; creates: %v", p.Creates)
	}
}

// --- extensions ---

func TestExtensionCreate(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Extensions: []*schema.Extension{
			{Name: "pgcrypto", IfNotExists: true},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "create extension if not exists") {
		t.Errorf("expected CREATE EXTENSION IF NOT EXISTS; creates: %v", p.Creates)
	}
	if !findCreate(p, "pgcrypto") {
		t.Error("expected extension name")
	}
}

func TestExtensionSkippedIfExists(t *testing.T) {
	l := emptyLive()
	l.Extensions["pgcrypto"] = true
	desired := &schema.Database{
		Tables:     map[string]*schema.Table{},
		Extensions: []*schema.Extension{{Name: "pgcrypto"}},
	}
	p := Plan(l, desired, false)
	if findCreate(p, "pgcrypto") {
		t.Error("extension already live, should not create")
	}
}

// --- enum types ---

func TestEnumTypeCreate(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Types: map[string]*schema.TypeDef{
			"public.status": {Name: "status", Schema: "public", Kind: "enum", Labels: []string{"active", "inactive"}},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "create type") {
		t.Error("expected CREATE TYPE")
	}
	if !findCreate(p, "as enum") {
		t.Error("expected AS ENUM")
	}
	if !findCreate(p, "'active'") {
		t.Error("expected 'active' label")
	}
}

func TestEnumTypeSkippedIfExists(t *testing.T) {
	l := emptyLive()
	l.Types["public.status"] = true
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Types: map[string]*schema.TypeDef{
			"public.status": {Name: "status", Schema: "public", Kind: "enum", Labels: []string{"active"}},
		},
	}
	p := Plan(l, desired, false)
	if findCreate(p, "create type") {
		t.Error("type already live, should not create")
	}
}

// --- composite types ---

func TestCompositeTypeCreate(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Types: map[string]*schema.TypeDef{
			"public.address": {
				Name: "address", Schema: "public", Kind: "composite",
				Attributes: map[string]string{"street": "text", "city": "text"},
			},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "create type") {
		t.Error("expected CREATE TYPE")
	}
	if !findCreate(p, "as (") {
		t.Errorf("expected composite AS (...); creates: %v", p.Creates)
	}
}

func TestCompositeTypeAttributeOrder(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Types: map[string]*schema.TypeDef{
			"public.jwt": {
				Name: "jwt", Schema: "public", Kind: "composite",
				Attributes:     map[string]string{"role": "text", "person_id": "uuid", "exp": "bigint"},
				AttributeOrder: []string{"role", "person_id", "exp"},
			},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create type "public"."jwt" as ("role" text, "person_id" uuid, "exp" bigint);`) {
		t.Errorf("expected YAML-ordered attributes; creates: %v", p.Creates)
	}
}

func TestCompositeTypeAttributeOrderFallbackSorted(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Types: map[string]*schema.TypeDef{
			"public.jwt": {
				Name: "jwt", Schema: "public", Kind: "composite",
				Attributes: map[string]string{"role": "text", "exp": "bigint"},
			},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `as ("exp" bigint, "role" text);`) {
		t.Errorf("expected sorted fallback without order info; creates: %v", p.Creates)
	}
}

// --- functions ---

func TestFunctionCreate(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.hello": {
				Name: "hello", Schema: "public", ArgsSig: "()",
				Returns: "text", Language: "sql",
				Body: "select 'hello'",
			},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "create function") {
		t.Error("expected CREATE FUNCTION")
	}
	if !findCreate(p, "returns text") {
		t.Errorf("expected returns text; creates: %v", p.Creates)
	}
}

func TestFunctionSkippedIfExists(t *testing.T) {
	l := emptyLive()
	l.Functions["public.hello()"] = true
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.hello": {
				Name: "hello", Schema: "public", ArgsSig: "()",
				Returns: "text", Language: "sql", Body: "select 'hello'",
			},
		},
	}
	p := Plan(l, desired, false)
	if findCreate(p, "create function") {
		t.Error("function already live, should not create")
	}
}

func TestFunctionQualifiedArgSkippedIfExists(t *testing.T) {
	l := emptyLive()
	l.Functions["public.hello(t my_type)"] = true
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.hello": {
				Name: "hello", Schema: "public", ArgsSig: "(t public.my_type)",
				Returns: "text", Language: "sql", Body: "select 'hello'",
			},
		},
	}
	p := Plan(l, desired, false)
	if findCreate(p, "create function") {
		t.Errorf("qualified arg type should match unqualified live signature; creates: %v", p.Creates)
	}
}

func TestFunctionQualifiedArgCreate(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.hello": {
				Name: "hello", Schema: "public", ArgsSig: "(t public.my_type)",
				Returns: "text", Language: "sql", Body: "select 'hello'",
			},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create function "public"."hello"(t public.my_type)`) {
		t.Errorf("expected qualified arg type verbatim in CREATE; creates: %v", p.Creates)
	}
}

func TestFunctionSecurityDefiner(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.fn": {
				Name: "fn", Schema: "public", ArgsSig: "()",
				Returns: "void", Language: "plpgsql",
				Security: "definer", Body: "begin end;",
			},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "security definer") {
		t.Errorf("expected security definer; creates: %v", p.Creates)
	}
}

func TestFunctionVolatility(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.fn": {
				Name: "fn", Schema: "public", ArgsSig: "()",
				Returns: "void", Language: "sql",
				Volatility: "stable", Body: "select null",
			},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, " stable") {
		t.Errorf("expected stable volatility; creates: %v", p.Creates)
	}
}

func TestFunctionImmutable(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.fn": {
				Name: "fn", Schema: "public", ArgsSig: "()",
				Returns: "int", Language: "sql",
				Volatility: "immutable", Body: "select 1",
			},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, " immutable") {
		t.Errorf("expected immutable volatility; creates: %v", p.Creates)
	}
}

// --- schemas ---

func TestCustomSchemaCreate(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			"private.accounts": {Name: "accounts", Columns: map[string]*schema.Column{"id": {Type: "int"}}},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "create schema if not exists") {
		t.Errorf("expected CREATE SCHEMA; creates: %v", p.Creates)
	}
	if !findCreate(p, "private") {
		t.Error("expected schema name private")
	}
}

func TestPublicSchemaNotCreated(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			"public.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}},
		},
	}
	p := Plan(emptyLive(), desired, false)
	if findCreate(p, "create schema") {
		t.Error("public schema should never be created")
	}
}

// --- add column ---

func TestAddColumn(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{
		"id": {Type: "int"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {Name: "users", Columns: map[string]*schema.Column{
			"id":    {Type: "int"},
			"email": {Type: "text"},
		}},
	}}
	p := Plan(live, desired, false)
	if !findCreate(p, "add column") {
		t.Errorf("expected add column; creates: %v", p.Creates)
	}
	if !findCreate(p, "email") {
		t.Error("expected column name email")
	}
}

// --- drop column (unsafe) ---

func TestDropColumnUnsafe(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{
		"id":   {Type: "int"},
		"junk": {Type: "text"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {Name: "users", Columns: map[string]*schema.Column{
			"id": {Type: "int"},
		}},
	}}
	p := Plan(live, desired, true) // unsafe=true
	if !findDrop(p, "drop column") {
		t.Errorf("expected drop column in unsafe mode; drops: %v", p.Drops)
	}
	if !findDrop(p, "junk") {
		t.Error("expected junk column dropped")
	}
}

func TestDropColumnSafe(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{
		"id":   {Type: "int"},
		"junk": {Type: "text"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {Name: "users", Columns: map[string]*schema.Column{
			"id": {Type: "int"},
		}},
	}}
	p := Plan(live, desired, false) // unsafe=false
	if findDrop(p, "drop column") {
		t.Error("should not drop column in safe mode")
	}
}

// --- render ---

func TestRender(t *testing.T) {
	p := &PlanDiff{
		Creates: []string{"create table t (id int not null);"},
		Alters:  []string{"alter table t add primary key (id);"},
	}
	out := Render(p)
	if !strings.Contains(out, "create table") {
		t.Error("missing CREATE TABLE in render")
	}
	if !strings.Contains(out, "add primary key") {
		t.Error("missing ALTER TABLE in render")
	}
}

func TestRenderEmpty(t *testing.T) {
	p := &PlanDiff{}
	if Render(p) != "" {
		t.Error("empty plan should render empty string")
	}
}

// --- pqIdent ---

func TestPqIdent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"public.users", `"public"."users"`},
		{"id", `"id"`},
	}
	for _, c := range cases {
		got := pqIdent(c.in)
		if got != c.want {
			t.Errorf("pqIdent(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// --- normalizeFunctionSignature ---

func TestNormalizeFunctionSignature(t *testing.T) {
	cases := []struct{ in, want string }{
		{"public.fn(key text, val jsonb default null)", "public.fn(key text, val jsonb)"},
		{"public.fn(a integer, b boolean)", "public.fn(a int, b bool)"},
		{"public.fn()", "public.fn()"},
		{"public.fn(t public.my_type)", "public.fn(t my_type)"},
		{"public.fn(public.my_type)", "public.fn(my_type)"},
		{"public.fn(t app.my_type)", "public.fn(t app.my_type)"},
		{"public.fn(t public.my_type default null)", "public.fn(t my_type)"},
	}
	for _, c := range cases {
		got := normalizeFunctionSignature(c.in)
		if got != c.want {
			t.Errorf("normalize(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// --- PlanDiff summary ---

func TestPlanDiffSummary(t *testing.T) {
	p := &PlanDiff{
		Creates: []string{"a", "b"},
		Alters:  []string{"c"},
		Drops:   []string{},
	}
	s := p.Summary()
	if s["creates"] != 2 || s["alters"] != 1 || s["drops"] != 0 {
		t.Errorf("unexpected summary: %v", s)
	}
}

// --- column unique flag ---

func TestColumnUniqueEmitsConstraint(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name:    "users",
			Columns: map[string]*schema.Column{"email": {Type: "text", Unique: true}},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, "unique") {
		t.Errorf("expected unique constraint from column.Unique; alters: %v", p.Alters)
	}
	if !findAlter(p, "email") {
		t.Errorf("expected email in unique constraint; alters: %v", p.Alters)
	}
}

// --- constraints/indexes on existing tables ---

func TestConstraintsAppliedToExistingTable(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{
		"id":    {Type: "int"},
		"email": {Type: "text"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name: "users",
			Columns: map[string]*schema.Column{
				"id":    {Type: "int"},
				"email": {Type: "text"},
			},
			Constraints: []*schema.Constraint{
				{Name: "chk_email_nonempty", Type: "check", Expression: "email <> ''"},
			},
		},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, "chk_email_nonempty") {
		t.Errorf("expected check constraint on existing table; alters: %v", p.Alters)
	}
}

func TestIndexAppliedToExistingTable(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{
		"id":    {Type: "int"},
		"email": {Type: "text"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name: "users",
			Columns: map[string]*schema.Column{
				"id":    {Type: "int"},
				"email": {Type: "text"},
			},
			Indexes: []*schema.Index{
				{Name: "idx_email", Columns: []string{"email"}, Unique: true},
			},
		},
	}}
	p := Plan(live, desired, false)
	if !findCreate(p, "idx_email") {
		t.Errorf("expected index on existing table; creates: %v", p.Creates)
	}
}

func TestConstraintSkippedIfAlreadyLive(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{
		"id":    {Type: "int"},
		"email": {Type: "text"},
	})
	live.Tables["public.users"].Constraints = map[string]bool{"chk_email_nonempty": true}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name: "users",
			Columns: map[string]*schema.Column{
				"id":    {Type: "int"},
				"email": {Type: "text"},
			},
			Constraints: []*schema.Constraint{
				{Name: "chk_email_nonempty", Type: "check", Expression: "email <> ''"},
			},
		},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "chk_email_nonempty") {
		t.Errorf("constraint already live, should not re-add; alters: %v", p.Alters)
	}
}

// --- constraint alterations (changed definitions -> drop + re-add, --unsafe) ---

func constraintAlterDesired(cts []*schema.Constraint, fks []*schema.ForeignKey) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name: "users",
			Columns: map[string]*schema.Column{
				"id":    {Type: "int"},
				"email": {Type: "text"},
			},
			Constraints: cts,
			ForeignKeys: fks,
		},
	}}
}

func constraintAlterLive(defs map[string]string) *Live {
	live := liveWithTable("public.users", map[string]*LiveColumn{
		"id":    {Type: "integer"},
		"email": {Type: "text"},
	})
	lt := live.Tables["public.users"]
	lt.ConstraintDefs = defs
	for name := range defs {
		lt.Constraints[name] = true
	}
	return live
}

func alterIndex(p *PlanDiff, substr string) int {
	for i, s := range p.Alters {
		if strings.Contains(s, substr) {
			return i
		}
	}
	return -1
}

func TestConstraintAlterCheckChanged(t *testing.T) {
	live := constraintAlterLive(map[string]string{
		"chk_email": "CHECK ((email <> ''::text))",
	})
	desired := constraintAlterDesired([]*schema.Constraint{
		{Name: "chk_email", Type: "check", Expression: "length(email) > 3"},
	}, nil)
	p := Plan(live, desired, true)
	di := alterIndex(p, `drop constraint "chk_email"`)
	ai := alterIndex(p, `add constraint "chk_email" check (length(email) > 3)`)
	if di < 0 || ai < 0 {
		t.Fatalf("expected drop+add for changed check; alters: %v", p.Alters)
	}
	if di > ai {
		t.Errorf("drop must precede add; alters: %v", p.Alters)
	}
}

func TestConstraintAlterSkippedWhenSafe(t *testing.T) {
	live := constraintAlterLive(map[string]string{
		"chk_email": "CHECK ((email <> ''::text))",
	})
	desired := constraintAlterDesired([]*schema.Constraint{
		{Name: "chk_email", Type: "check", Expression: "length(email) > 3"},
	}, nil)
	p := Plan(live, desired, false)
	if findAlter(p, "chk_email") {
		t.Errorf("changed constraint must be unsafe-gated; alters: %v", p.Alters)
	}
}

func TestConstraintAlterSkippedIfEquivalent(t *testing.T) {
	// live defs come back with extra parens and ::casts — must not churn
	live := constraintAlterLive(map[string]string{
		"chk_email": "CHECK (((email)::text <> ''::text))",
		"uq_email":  "UNIQUE (email)",
	})
	desired := constraintAlterDesired([]*schema.Constraint{
		{Name: "chk_email", Type: "check", Expression: "email <> ''"},
		{Name: "uq_email", Type: "unique", Columns: []string{"email"}},
	}, nil)
	p := Plan(live, desired, true)
	if findAlter(p, "chk_email") || findAlter(p, "uq_email") {
		t.Errorf("equivalent constraints must emit no SQL; alters: %v", p.Alters)
	}
}

func TestConstraintAlterUniqueChanged(t *testing.T) {
	live := constraintAlterLive(map[string]string{
		"uq_users": "UNIQUE (email)",
	})
	desired := constraintAlterDesired([]*schema.Constraint{
		{Name: "uq_users", Type: "unique", Columns: []string{"id", "email"}},
	}, nil)
	p := Plan(live, desired, true)
	if alterIndex(p, `drop constraint "uq_users"`) < 0 || alterIndex(p, `add constraint "uq_users" unique ("id", "email")`) < 0 {
		t.Errorf("expected drop+add for changed unique; alters: %v", p.Alters)
	}
}

func TestConstraintAlterExcludeChanged(t *testing.T) {
	live := constraintAlterLive(map[string]string{
		"excl_room": "EXCLUDE USING gist (room WITH =)",
	})
	desired := constraintAlterDesired([]*schema.Constraint{
		{Name: "excl_room", Type: "exclude", Expression: "using gist (room with =, during with &&)"},
	}, nil)
	p := Plan(live, desired, true)
	if alterIndex(p, `drop constraint "excl_room"`) < 0 || alterIndex(p, `add constraint "excl_room" exclude using gist`) < 0 {
		t.Errorf("expected drop+add for changed exclude; alters: %v", p.Alters)
	}
}

func TestConstraintAlterFKChanged(t *testing.T) {
	live := constraintAlterLive(map[string]string{
		"fk_org": "FOREIGN KEY (id) REFERENCES orgs(id)",
	})
	desired := constraintAlterDesired(nil, []*schema.ForeignKey{
		{Name: "fk_org", Columns: []string{"id"}, RefTable: "orgs", RefColumns: []string{"id"}, OnDelete: "cascade"},
	})
	p := Plan(live, desired, true)
	di := alterIndex(p, `drop constraint "fk_org"`)
	ai := alterIndex(p, `add constraint "fk_org" foreign key ("id") references "orgs"("id") on delete cascade`)
	if di < 0 || ai < 0 {
		t.Fatalf("expected drop+add for changed FK; alters: %v", p.Alters)
	}
	if di > ai {
		t.Errorf("drop must precede add; alters: %v", p.Alters)
	}
}

func TestConstraintAlterFKEquivalentSkipped(t *testing.T) {
	// live def: unquoted idents, uppercase keywords, schema omitted for public
	live := constraintAlterLive(map[string]string{
		"fk_org": "FOREIGN KEY (id) REFERENCES orgs(id) ON DELETE CASCADE",
	})
	desired := constraintAlterDesired(nil, []*schema.ForeignKey{
		{Name: "fk_org", Columns: []string{"id"}, RefTable: "public.orgs", RefColumns: []string{"id"}, OnDelete: "CASCADE"},
	})
	p := Plan(live, desired, true)
	if findAlter(p, "fk_org") {
		t.Errorf("equivalent FK must emit no SQL; alters: %v", p.Alters)
	}
}

func TestConstraintAlterNoDefRecordedNoChange(t *testing.T) {
	// live constraint known by name only (no definition captured) — keep old
	// name-only behavior and emit nothing even under --unsafe
	live := liveWithTable("public.users", map[string]*LiveColumn{
		"id":    {Type: "integer"},
		"email": {Type: "text"},
	})
	live.Tables["public.users"].Constraints["chk_email"] = true
	desired := constraintAlterDesired([]*schema.Constraint{
		{Name: "chk_email", Type: "check", Expression: "length(email) > 3"},
	}, nil)
	p := Plan(live, desired, true)
	if findAlter(p, "chk_email") {
		t.Errorf("no live def recorded, should not drop+add; alters: %v", p.Alters)
	}
}

func TestNormalizeConstraintDef(t *testing.T) {
	cases := [][2]string{
		{"CHECK ((price > (0)::numeric))", "check (price > 0)"},
		{"UNIQUE (a, b)", "unique (\"a\",\"b\")"},
		{"FOREIGN KEY (uid) REFERENCES public.users(id) ON DELETE SET NULL", `foreign key ("uid") references "users"("id") on delete set null`},
		{"CHECK ((status <> 'del'::text))", "check (status <> 'del')"},
	}
	for _, c := range cases {
		if normalizeConstraintDef(c[0]) != normalizeConstraintDef(c[1]) {
			t.Errorf("expected %q == %q after normalization (%q vs %q)", c[0], c[1], normalizeConstraintDef(c[0]), normalizeConstraintDef(c[1]))
		}
	}
	if normalizeConstraintDef("CHECK (a > 1)") == normalizeConstraintDef("check (a > 2)") {
		t.Errorf("different expressions must not normalize equal")
	}
	if normalizeConstraintDef("CHECK (s <> 'A')") == normalizeConstraintDef("check (s <> 'a')") {
		t.Errorf("string literal case must be preserved")
	}
}

func TestIndexSkippedIfAlreadyLive(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{
		"email": {Type: "text"},
	})
	live.Tables["public.users"].Indexes = map[string]bool{"idx_email": true}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name:    "users",
			Columns: map[string]*schema.Column{"email": {Type: "text"}},
			Indexes: []*schema.Index{{Name: "idx_email", Columns: []string{"email"}}},
		},
	}}
	p := Plan(live, desired, false)
	if findCreate(p, "idx_email") {
		t.Errorf("index already live, should not re-create; creates: %v", p.Creates)
	}
}

// --- views ---

func viewDesired(key, query string, materialized bool) *schema.Database {
	return &schema.Database{
		Tables: map[string]*schema.Table{},
		Views: map[string]*schema.View{
			key: {Schema: "public", Name: key, Query: query, Materialized: materialized},
		},
	}
}

func TestViewCreate(t *testing.T) {
	desired := viewDesired("public.active_users", "select id from users where active", false)
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "create or replace view") {
		t.Errorf("expected create or replace view, creates=%v", p.Creates)
	}
	if !findCreate(p, "active_users") {
		t.Error("expected active_users in create")
	}
}

func TestViewSkippedIfExists(t *testing.T) {
	live := emptyLive()
	live.Views["public.active_users"] = true
	desired := viewDesired("public.active_users", "select id from users", false)
	p := Plan(live, desired, false)
	if findCreate(p, "active_users") {
		t.Error("should skip view that already exists")
	}
}

func TestMaterializedViewCreate(t *testing.T) {
	desired := viewDesired("public.user_stats", "select count(*) from users", true)
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "create materialized view if not exists") {
		t.Errorf("expected create materialized view, creates=%v", p.Creates)
	}
	if !findCreate(p, "user_stats") {
		t.Error("expected user_stats in create")
	}
}

func TestMaterializedViewSkippedIfExists(t *testing.T) {
	live := emptyLive()
	live.MatViews["public.user_stats"] = true
	desired := viewDesired("public.user_stats", "select count(*) from users", true)
	p := Plan(live, desired, false)
	if findCreate(p, "user_stats") {
		t.Error("should skip materialized view that already exists")
	}
}

// --- primary key on existing table ---

func TestPrimaryKeyTableLevelOnExistingTable(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}, PrimaryKey: []string{"id"}},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, "primary key") {
		t.Errorf("expected PK alter on existing table; alters: %v", p.Alters)
	}
}

func TestPrimaryKeyColumnLevelOnExistingTable(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int", PrimaryKey: true}}},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, "primary key") {
		t.Errorf("expected PK alter on existing table; alters: %v", p.Alters)
	}
}

func TestPrimaryKeySkippedIfAlreadyLive(t *testing.T) {
	live := liveWithTablePK("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}, PrimaryKey: []string{"id"}},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "primary key") {
		t.Errorf("PK already live, should not re-add; alters: %v", p.Alters)
	}
}

// --- foreign key on existing table ---

func TestForeignKeyOnExistingTable(t *testing.T) {
	live := liveWithTable("public.orders", map[string]*LiveColumn{"user_id": {Type: "int"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.orders": {
			Name:    "orders",
			Columns: map[string]*schema.Column{"user_id": {Type: "int"}},
			ForeignKeys: []*schema.ForeignKey{
				{Name: "fk_user", Columns: []string{"user_id"}, RefTable: "public.users", RefColumns: []string{"id"}, OnDelete: "cascade"},
			},
		},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, "foreign key") {
		t.Errorf("expected FK on existing table; alters: %v", p.Alters)
	}
	if !findAlter(p, "on delete cascade") {
		t.Errorf("expected on delete cascade; alters: %v", p.Alters)
	}
}

func TestForeignKeySkippedIfAlreadyLive(t *testing.T) {
	live := liveWithTable("public.orders", map[string]*LiveColumn{"user_id": {Type: "int"}})
	live.Tables["public.orders"].Constraints["fk_user"] = true
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.orders": {
			Name:    "orders",
			Columns: map[string]*schema.Column{"user_id": {Type: "int"}},
			ForeignKeys: []*schema.ForeignKey{
				{Name: "fk_user", Columns: []string{"user_id"}, RefTable: "public.users", RefColumns: []string{"id"}},
			},
		},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "fk_user") {
		t.Errorf("FK already live, should not re-add; alters: %v", p.Alters)
	}
}

// --- unique constraint on existing table ---

func TestUniqueConstraintOnExistingTable(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"a": {Type: "text"}, "b": {Type: "text"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"a": {Type: "text"}, "b": {Type: "text"}},
			Constraints: []*schema.Constraint{
				{Name: "uq_ab", Type: "unique", Columns: []string{"a", "b"}},
			},
		},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, "uq_ab") {
		t.Errorf("expected unique constraint on existing table; alters: %v", p.Alters)
	}
}

// --- exclude constraint on existing table ---

func TestExcludeConstraintOnExistingTable(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"range": {Type: "tstzrange"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"range": {Type: "tstzrange"}},
			Constraints: []*schema.Constraint{
				{Name: "excl_r", Type: "exclude", Expression: "using gist (range with &&)"},
			},
		},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, "excl_r") {
		t.Errorf("expected exclude constraint on existing table; alters: %v", p.Alters)
	}
}

// --- trigger on existing table ---

func TestTriggerOnExistingTable(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Triggers: []*schema.Trigger{
				{Name: "trg_audit", Timing: "after", Events: []string{"insert"}, Level: "row", Procedure: "audit_fn()"},
			},
		},
	}}
	p := Plan(live, desired, false)
	if !findCreate(p, "trg_audit") {
		t.Errorf("expected trigger create on existing table; creates: %v", p.Creates)
	}
}

func TestTriggerSkippedIfExists(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	live.Tables["public.t"].Triggers["trg_audit"] = `CREATE TRIGGER trg_audit AFTER INSERT ON public.t FOR EACH ROW EXECUTE FUNCTION audit_fn()`
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Triggers: []*schema.Trigger{
				{Name: "trg_audit", Timing: "after", Events: []string{"insert"}, Level: "row", Procedure: "audit_fn()"},
			},
		},
	}}
	p := Plan(live, desired, false)
	if findCreate(p, "trg_audit") {
		t.Errorf("trigger already live, should not create; creates: %v", p.Creates)
	}
}

func TestTriggerRecreateOnChange(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	// live trigger fires on INSERT only; desired adds UPDATE
	live.Tables["public.t"].Triggers["trg_audit"] = `CREATE TRIGGER trg_audit AFTER INSERT ON public.t FOR EACH ROW EXECUTE FUNCTION audit_fn()`
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Triggers: []*schema.Trigger{
				{Name: "trg_audit", Timing: "after", Events: []string{"insert", "update"}, Level: "row", Procedure: "audit_fn()"},
			},
		},
	}}
	p := Plan(live, desired, false)
	dropIdx, createIdx := -1, -1
	for i, s := range p.Creates {
		if strings.Contains(s, `drop trigger "trg_audit"`) { dropIdx = i }
		if strings.Contains(s, `create trigger "trg_audit"`) { createIdx = i }
	}
	if dropIdx < 0 || createIdx < 0 {
		t.Fatalf("expected drop+recreate of changed trigger; creates: %v", p.Creates)
	}
	if dropIdx > createIdx {
		t.Errorf("drop at %d must precede create at %d; creates: %v", dropIdx, createIdx, p.Creates)
	}
}

func TestTriggerSkippedIfDefinitionEquivalent(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"created_at": {Type: "timestamptz"}})
	// pg_get_triggerdef output: EXECUTE FUNCTION, reordered events, extra
	// parens and casts in the WHEN clause, schema-qualified function
	live.Tables["public.t"].Triggers["trg_sup"] = `CREATE TRIGGER trg_sup AFTER INSERT OR UPDATE ON public.t FOR EACH ROW WHEN ((old.created_at <= new.created_at)) EXECUTE FUNCTION public.supersede_fn()`
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"created_at": {Type: "timestamptz"}},
			Triggers: []*schema.Trigger{
				{Name: "trg_sup", Timing: "after", Events: []string{"update", "insert"}, Level: "row", Procedure: "supersede_fn()", When: "old.created_at <= new.created_at"},
			},
		},
	}}
	p := Plan(live, desired, false)
	if findCreate(p, "trg_sup") {
		t.Errorf("equivalent trigger definition should emit no SQL; creates: %v", p.Creates)
	}
}

func TestTriggerNameOnlyLiveSkipped(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "int"}})
	// unknown live definition ("") falls back to name-only matching: skip
	live.Tables["public.t"].Triggers["trg_audit"] = ""
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"id": {Type: "int"}},
			Triggers: []*schema.Trigger{
				{Name: "trg_audit", Timing: "after", Events: []string{"insert"}, Level: "row", Procedure: "audit_fn()"},
			},
		},
	}}
	p := Plan(live, desired, false)
	if findCreate(p, "trg_audit") {
		t.Errorf("unknown live definition should skip, not churn; creates: %v", p.Creates)
	}
}

// --- column unique flag on existing table ---

func TestColumnUniqueOnExistingTable(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"email": {Type: "text"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"email": {Type: "text", Unique: true}},
		},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, "unique") {
		t.Errorf("expected unique constraint from column.Unique on existing table; alters: %v", p.Alters)
	}
	if !findAlter(p, "email") {
		t.Errorf("expected email in unique constraint; alters: %v", p.Alters)
	}
}

func TestColumnUniqueSkippedIfAlreadyLive(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"email": {Type: "text"}})
	live.Tables["public.t"].Constraints["public_t_email_key"] = true
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"email": {Type: "text", Unique: true}},
		},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "public_t_email_key") {
		t.Errorf("unique constraint already live, should not re-add; alters: %v", p.Alters)
	}
}

// --- composite type skip-if-live ---

func TestCompositeTypeSkippedIfExists(t *testing.T) {
	l := emptyLive()
	l.Types["public.address"] = true
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Types: map[string]*schema.TypeDef{
			"public.address": {Name: "address", Schema: "public", Kind: "composite", Attributes: map[string]string{"street": "text"}},
		},
	}
	p := Plan(l, desired, false)
	if findCreate(p, "create type") {
		t.Error("composite type already live, should not create")
	}
}

// --- index auto-name on existing table ---

func TestIndexAutoNameOnExistingTable(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"col": {Type: "text"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"col": {Type: "text"}},
			Indexes: []*schema.Index{{Columns: []string{"col"}}}, // no name
		},
	}}
	p := Plan(live, desired, false)
	if !findCreate(p, "public_t_col") {
		t.Errorf("expected auto-named index on existing table; creates: %v", p.Creates)
	}
}

// --- non-unique index on existing table ---

func TestNonUniqueIndexOnExistingTable(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"name": {Type: "text"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:    "t",
			Columns: map[string]*schema.Column{"name": {Type: "text"}},
			Indexes: []*schema.Index{{Name: "idx_name", Columns: []string{"name"}, Unique: false}},
		},
	}}
	p := Plan(live, desired, false)
	if !findCreate(p, "idx_name") {
		t.Errorf("expected non-unique index on existing table; creates: %v", p.Creates)
	}
}

// --- drop column safe (no drop) ---

func TestDropColumnSafeOnExistingTable(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{
		"id":   {Type: "int"},
		"junk": {Type: "text"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}},
	}}
	p := Plan(live, desired, false)
	if findDrop(p, "junk") {
		t.Error("safe mode must not drop columns")
	}
}

// --- custom schema not recreated if already live ---

func TestCustomSchemaSkippedIfAlreadyLive(t *testing.T) {
	live := emptyLive()
	live.Schemas["private"] = true
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"private.accounts": {Name: "accounts", Columns: map[string]*schema.Column{"id": {Type: "int"}}},
	}}
	p := Plan(live, desired, false)
	if findCreate(p, "create schema") {
		t.Errorf("schema already live, should not re-create; creates: %v", p.Creates)
	}
}

// --- roles ---

func TestRoleCreate(t *testing.T) {
	desired := &schema.Database{Roles: map[string]*schema.Role{
		"app_user": {Name: "app_user", Login: true, ConnectionLimit: -1},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create role "app_user" with login;`) {
		t.Errorf("expected CREATE ROLE; creates: %v", p.Creates)
	}
}

func TestRoleCreateGuardedAgainstPreexisting(t *testing.T) {
	// Roles are cluster-level: with --from-empty the Live snapshot is empty
	// but the role may still exist, so CREATE ROLE must be wrapped in an
	// IF NOT EXISTS guard.
	desired := &schema.Database{Roles: map[string]*schema.Role{
		"app_user": {Name: "app_user", Login: true, ConnectionLimit: -1},
	}}
	p := Plan(emptyLive(), desired, false)
	want := `do $$ begin if not exists (select from pg_catalog.pg_roles where rolname = 'app_user') then create role "app_user" with login; end if; end $$;`
	if !findCreate(p, want) {
		t.Errorf("expected guarded CREATE ROLE; creates: %v", p.Creates)
	}
}

func TestRoleSkippedIfExists(t *testing.T) {
	live := emptyLive()
	live.Roles["app_user"] = true
	desired := &schema.Database{Roles: map[string]*schema.Role{
		"app_user": {Name: "app_user", Login: true, ConnectionLimit: -1},
	}}
	p := Plan(live, desired, false)
	if findCreate(p, "create role") {
		t.Errorf("role already live, should not re-create; creates: %v", p.Creates)
	}
}

func TestRoleCreateNoOptions(t *testing.T) {
	desired := &schema.Database{Roles: map[string]*schema.Role{
		"readonly": {Name: "readonly", ConnectionLimit: -1},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create role "readonly";`) {
		t.Errorf("expected bare CREATE ROLE without with clause; creates: %v", p.Creates)
	}
}

func TestRoleOptions(t *testing.T) {
	desired := &schema.Database{Roles: map[string]*schema.Role{
		"admin": {
			Name: "admin", Login: true, Superuser: true, CreateDB: true,
			CreateRole: true, Replication: true, BypassRLS: true,
			NoInherit: true, ConnectionLimit: 5,
		},
	}}
	p := Plan(emptyLive(), desired, false)
	want := `create role "admin" with login superuser createdb createrole replication bypassrls noinherit connection limit 5;`
	if !findCreate(p, want) {
		t.Errorf("expected all role options; creates: %v", p.Creates)
	}
}

func TestRoleCreatedBeforeSchema(t *testing.T) {
	desired := &schema.Database{
		Roles: map[string]*schema.Role{"app_user": {Name: "app_user", ConnectionLimit: -1}},
		Tables: map[string]*schema.Table{
			"private.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}},
		},
	}
	p := Plan(emptyLive(), desired, false)
	roleIdx, schemaIdx := -1, -1
	for i, s := range p.Creates {
		if strings.Contains(s, "create role") {
			roleIdx = i
		}
		if strings.Contains(s, "create schema") {
			schemaIdx = i
		}
	}
	if roleIdx < 0 || schemaIdx < 0 || roleIdx > schemaIdx {
		t.Errorf("role create must precede schema create; creates: %v", p.Creates)
	}
}

func TestRoleMembershipGrant(t *testing.T) {
	desired := &schema.Database{Roles: map[string]*schema.Role{
		"app_user": {Name: "app_user", Login: true, ConnectionLimit: -1, InRoles: []string{"readonly"}},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `grant "readonly" to "app_user";`) {
		t.Errorf("expected membership grant; alters: %v", p.Alters)
	}
}

func TestRoleMembershipSkippedIfExists(t *testing.T) {
	live := emptyLive()
	live.Roles["app_user"] = true
	live.RoleMembers["app_user"] = map[string]bool{"readonly": true}
	desired := &schema.Database{Roles: map[string]*schema.Role{
		"app_user": {Name: "app_user", ConnectionLimit: -1, InRoles: []string{"readonly"}},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, `grant "readonly"`) {
		t.Errorf("membership already live, should not re-grant; alters: %v", p.Alters)
	}
}

func TestRoleNotDroppedIfRemoved(t *testing.T) {
	live := emptyLive()
	live.Roles["old_role"] = true
	desired := &schema.Database{Roles: map[string]*schema.Role{}}
	p := Plan(live, desired, false)
	if findDrop(p, "role") {
		t.Errorf("forward-only tool must never drop roles; drops: %v", p.Drops)
	}
}

func TestRoleComment(t *testing.T) {
	desired := &schema.Database{Roles: map[string]*schema.Role{
		"app_user": {Name: "app_user", ConnectionLimit: -1, Comment: "application login role"},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `comment on role "app_user" is 'application login role';`) {
		t.Errorf("expected role comment; alters: %v", p.Alters)
	}
}

func TestRoleCommentSkippedIfSame(t *testing.T) {
	live := emptyLive()
	live.Roles["app_user"] = true
	live.RoleComments["app_user"] = "application login role"
	desired := &schema.Database{Roles: map[string]*schema.Role{
		"app_user": {Name: "app_user", ConnectionLimit: -1, Comment: "application login role"},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "comment on role") {
		t.Errorf("comment unchanged, should not re-emit; alters: %v", p.Alters)
	}
}

// --- sequences ---

func seqDesired(key string, sq *schema.Sequence) *schema.Database {
	return &schema.Database{
		Tables:    map[string]*schema.Table{},
		Sequences: map[string]*schema.Sequence{key: sq},
	}
}

func TestSequenceCreate(t *testing.T) {
	desired := seqDesired("public.order_seq", &schema.Sequence{Schema: "public", Name: "order_seq"})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create sequence if not exists "public"."order_seq";`) {
		t.Errorf("expected create sequence, creates=%v", p.Creates)
	}
}

func TestSequenceSkippedIfExists(t *testing.T) {
	live := emptyLive()
	live.Sequences["public.order_seq"] = true
	desired := seqDesired("public.order_seq", &schema.Sequence{Schema: "public", Name: "order_seq"})
	p := Plan(live, desired, false)
	if findCreate(p, "order_seq") {
		t.Errorf("should skip sequence that already exists; creates=%v", p.Creates)
	}
}

func TestSequenceOptions(t *testing.T) {
	desired := seqDesired("public.order_seq", &schema.Sequence{
		Schema: "public", Name: "order_seq",
		As: "bigint", Increment: "5", MinValue: "10", MaxValue: "99999",
		Start: "100", Cache: "20", Cycle: true,
	})
	p := Plan(emptyLive(), desired, false)
	for _, want := range []string{
		"as bigint",
		"increment by 5",
		"minvalue 10",
		"maxvalue 99999",
		"start with 100",
		"cache 20",
		"cycle",
	} {
		if !findCreate(p, want) {
			t.Errorf("expected %q in create; creates=%v", want, p.Creates)
		}
	}
}

func TestSequenceOwnedBy(t *testing.T) {
	desired := seqDesired("public.order_seq", &schema.Sequence{
		Schema: "public", Name: "order_seq", OwnedBy: "public.orders.order_number",
	})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `owned by "public"."orders"."order_number"`) {
		t.Errorf("expected owned by clause; creates=%v", p.Creates)
	}
}

func TestSequenceCreatesSchema(t *testing.T) {
	desired := seqDesired("billing.invoice_seq", &schema.Sequence{Schema: "billing", Name: "invoice_seq"})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create schema if not exists "billing";`) {
		t.Errorf("expected schema create for sequence-only schema; creates=%v", p.Creates)
	}
}

func TestSequenceComment(t *testing.T) {
	desired := seqDesired("public.order_seq", &schema.Sequence{
		Schema: "public", Name: "order_seq", Comment: "order numbers",
	})
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `comment on sequence "public"."order_seq" is 'order numbers';`) {
		t.Errorf("expected sequence comment; alters=%v", p.Alters)
	}
}

func TestSequenceCommentSkippedIfSame(t *testing.T) {
	live := emptyLive()
	live.Sequences["public.order_seq"] = true
	live.RelComments = map[string]string{"public.order_seq": "order numbers"}
	desired := seqDesired("public.order_seq", &schema.Sequence{
		Schema: "public", Name: "order_seq", Comment: "order numbers",
	})
	p := Plan(live, desired, false)
	if findAlter(p, "comment on sequence") {
		t.Errorf("comment unchanged, should not re-emit; alters=%v", p.Alters)
	}
}

func TestSequenceBeforeDependentTable(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			"public.orders": {
				Name:      "public.orders",
				Columns:   map[string]*schema.Column{"id": {Type: "bigint", Default: "nextval('order_seq')"}},
				DependsOn: []string{"sequence public.order_seq"},
			},
		},
		Sequences: map[string]*schema.Sequence{
			"public.order_seq": {Schema: "public", Name: "order_seq"},
		},
	}
	p := Plan(emptyLive(), desired, false)
	seqIdx, tblIdx := -1, -1
	for i, s := range p.Creates {
		if strings.Contains(s, "create sequence") {
			seqIdx = i
		}
		if strings.Contains(s, "create table") {
			tblIdx = i
		}
	}
	if seqIdx < 0 || tblIdx < 0 {
		t.Fatalf("missing creates: %v", p.Creates)
	}
	if seqIdx > tblIdx {
		t.Errorf("sequence must be created before dependent table; creates=%v", p.Creates)
	}
}

// --- identity columns ---

func TestIdentityColumnCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.orders": {Name: "public.orders", Columns: map[string]*schema.Column{
			"id": {Type: "bigint", Identity: "always"},
		}},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `"id" bigint not null generated always as identity`) {
		t.Errorf("expected identity column in create table; creates=%v", p.Creates)
	}
}

func TestIdentityColumnByDefaultCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.orders": {Name: "public.orders", Columns: map[string]*schema.Column{
			"id": {Type: "bigint", Identity: "by default"},
		}},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `"id" bigint not null generated by default as identity`) {
		t.Errorf("expected by-default identity column; creates=%v", p.Creates)
	}
}

func TestIdentityColumnSkippedIfExists(t *testing.T) {
	live := liveWithTablePK("public.orders", map[string]*LiveColumn{
		"id": {Type: "bigint", Identity: "always"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.orders": {Name: "public.orders", Columns: map[string]*schema.Column{
			"id": {Type: "bigint", Identity: "always"},
		}},
	}}
	p := Plan(live, desired, false)
	if len(p.Creates) != 0 || len(p.Alters) != 0 {
		t.Errorf("existing identity column should emit nothing; creates=%v alters=%v", p.Creates, p.Alters)
	}
}

func TestIdentityColumnAddColumn(t *testing.T) {
	live := liveWithTablePK("public.orders", map[string]*LiveColumn{
		"id": {Type: "bigint"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.orders": {Name: "public.orders", Columns: map[string]*schema.Column{
			"id":     {Type: "bigint"},
			"seq_no": {Type: "int", Identity: "by default"},
		}},
	}}
	p := Plan(live, desired, false)
	if !findCreate(p, `alter table "public"."orders" add column "seq_no" int not null generated by default as identity;`) {
		t.Errorf("expected add column with identity; creates=%v", p.Creates)
	}
}

func TestIdentityColumnOverridesDefault(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.orders": {Name: "public.orders", Columns: map[string]*schema.Column{
			"id": {Type: "bigint", Identity: "always", Default: "nextval('some_seq')"},
		}},
	}}
	p := Plan(emptyLive(), desired, false)
	if findCreate(p, "default nextval") {
		t.Errorf("identity column must not also emit default; creates=%v", p.Creates)
	}
	if !findCreate(p, "generated always as identity") {
		t.Errorf("expected identity clause; creates=%v", p.Creates)
	}
}

// --- domains ---

func domainDesired(key string, dm *schema.Domain) *schema.Database {
	return &schema.Database{
		Tables:  map[string]*schema.Table{},
		Domains: map[string]*schema.Domain{key: dm},
	}
}

func TestDomainCreate(t *testing.T) {
	desired := domainDesired("public.email", &schema.Domain{Schema: "public", Name: "email", Type: "text"})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create domain "public"."email" as text;`) {
		t.Errorf("expected CREATE DOMAIN, got %v", p.Creates)
	}
}

func TestDomainSkippedIfExists(t *testing.T) {
	live := emptyLive()
	live.Domains["public.email"] = true
	desired := domainDesired("public.email", &schema.Domain{Schema: "public", Name: "email", Type: "text"})
	p := Plan(live, desired, false)
	if findCreate(p, "create domain") {
		t.Errorf("existing domain should be skipped, got %v", p.Creates)
	}
}

func TestDomainOptions(t *testing.T) {
	desired := domainDesired("public.email", &schema.Domain{
		Schema:  "public",
		Name:    "email",
		Type:    "text",
		Default: "'unknown@example.com'",
		NotNull: true,
		Check:   "value ~ '^[^@]+@[^@]+$'",
	})
	p := Plan(emptyLive(), desired, false)
	want := `create domain "public"."email" as text default 'unknown@example.com' not null check (value ~ '^[^@]+@[^@]+$');`
	if !findCreate(p, want) {
		t.Errorf("want %q, got %v", want, p.Creates)
	}
}

func TestDomainNamedCheckConstraint(t *testing.T) {
	desired := domainDesired("public.email", &schema.Domain{
		Schema:         "public",
		Name:           "email",
		Type:           "text",
		Check:          "value <> ''",
		ConstraintName: "email_not_empty",
	})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `constraint "email_not_empty" check (value <> '')`) {
		t.Errorf("expected named check constraint, got %v", p.Creates)
	}
}

func TestDomainCollate(t *testing.T) {
	desired := domainDesired("public.name_ci", &schema.Domain{
		Schema:  "public",
		Name:    "name_ci",
		Type:    "text",
		Collate: "en_US",
	})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `as text collate "en_US"`) {
		t.Errorf("expected COLLATE clause, got %v", p.Creates)
	}
}

func TestDomainCreatesSchema(t *testing.T) {
	desired := domainDesired("billing.money_amount", &schema.Domain{Schema: "billing", Name: "money_amount", Type: "numeric(12,2)"})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create schema if not exists "billing";`) {
		t.Errorf("expected CREATE SCHEMA, got %v", p.Creates)
	}
	if !findCreate(p, `create domain "billing"."money_amount" as numeric(12,2);`) {
		t.Errorf("expected CREATE DOMAIN, got %v", p.Creates)
	}
}

func TestDomainWithoutTypeSkipped(t *testing.T) {
	desired := domainDesired("public.broken", &schema.Domain{Schema: "public", Name: "broken"})
	p := Plan(emptyLive(), desired, false)
	if findCreate(p, "create domain") {
		t.Errorf("domain without type should emit no SQL, got %v", p.Creates)
	}
}

func TestDomainComment(t *testing.T) {
	desired := domainDesired("public.email", &schema.Domain{
		Schema: "public", Name: "email", Type: "text", Comment: "validated email",
	})
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `comment on domain "public"."email" is 'validated email';`) {
		t.Errorf("expected COMMENT ON DOMAIN, got %v", p.Alters)
	}
}

func TestDomainCommentSkippedIfSame(t *testing.T) {
	live := emptyLive()
	live.Domains["public.email"] = true
	live.TypeComments = map[string]string{"public.email": "validated email"}
	desired := domainDesired("public.email", &schema.Domain{
		Schema: "public", Name: "email", Type: "text", Comment: "validated email",
	})
	p := Plan(live, desired, false)
	if findAlter(p, "comment on domain") {
		t.Errorf("unchanged comment should be skipped, got %v", p.Alters)
	}
}

func TestDomainBeforeDependentTable(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			"public.users": {
				Name:      "public.users",
				Columns:   map[string]*schema.Column{"email": {Type: "public.email", Nullable: false}},
				DependsOn: []string{"domain public.email"},
			},
		},
		Domains: map[string]*schema.Domain{
			"public.email": {Schema: "public", Name: "email", Type: "text"},
		},
	}
	p := Plan(emptyLive(), desired, false)
	domIdx, tblIdx := -1, -1
	for i, s := range p.Creates {
		if strings.Contains(s, "create domain") {
			domIdx = i
		}
		if strings.Contains(s, "create table") {
			tblIdx = i
		}
	}
	if domIdx < 0 || tblIdx < 0 {
		t.Fatalf("expected both domain and table creates, got %v", p.Creates)
	}
	if domIdx > tblIdx {
		t.Errorf("domain must be created before dependent table: %v", p.Creates)
	}
}

// --- procedures ---

func procDesired(key string, pr *schema.Procedure) *schema.Database {
	return &schema.Database{
		Tables:     map[string]*schema.Table{},
		Procedures: map[string]*schema.Procedure{key: pr},
	}
}

func TestProcedureCreate(t *testing.T) {
	desired := procDesired("public.cleanup", &schema.Procedure{
		Schema: "public", Name: "cleanup", ArgsSig: "()",
		Language: "plpgsql", Body: "begin delete from public.audit; end;",
	})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create procedure "public"."cleanup"() language plpgsql as $$`) {
		t.Errorf("expected CREATE PROCEDURE, got %v", p.Creates)
	}
}

func TestProcedureSkippedIfExists(t *testing.T) {
	live := emptyLive()
	live.Procedures["public.cleanup()"] = true
	live.ProcedureDefs["public.cleanup()"] = &LiveProcedure{Body: "begin delete from public.audit; end;", Security: "invoker"}
	desired := procDesired("public.cleanup", &schema.Procedure{
		Schema: "public", Name: "cleanup", ArgsSig: "()",
		Language: "plpgsql", Body: "begin delete from public.audit; end;",
	})
	p := Plan(live, desired, false)
	if findCreate(p, "procedure") {
		t.Errorf("unchanged procedure should be skipped, got %v", p.Creates)
	}
}

func TestProcedureReplaceOnBodyChange(t *testing.T) {
	live := emptyLive()
	live.Procedures["public.cleanup()"] = true
	live.ProcedureDefs["public.cleanup()"] = &LiveProcedure{Body: "begin null; end;", Security: "invoker"}
	desired := procDesired("public.cleanup", &schema.Procedure{
		Schema: "public", Name: "cleanup", ArgsSig: "()",
		Language: "plpgsql", Body: "begin delete from public.audit; end;",
	})
	p := Plan(live, desired, false)
	if !findCreate(p, "create or replace procedure") {
		t.Errorf("changed body should emit CREATE OR REPLACE, got %v", p.Creates)
	}
}

func TestProcedureReplaceOnSecurityChange(t *testing.T) {
	live := emptyLive()
	live.Procedures["public.cleanup()"] = true
	live.ProcedureDefs["public.cleanup()"] = &LiveProcedure{Body: "begin null; end;", Security: "invoker"}
	desired := procDesired("public.cleanup", &schema.Procedure{
		Schema: "public", Name: "cleanup", ArgsSig: "()",
		Language: "plpgsql", Security: "definer", Body: "begin null; end;",
	})
	p := Plan(live, desired, false)
	if !findCreate(p, "create or replace procedure") {
		t.Errorf("changed security should emit CREATE OR REPLACE, got %v", p.Creates)
	}
}

func TestProcedureSecurityDefiner(t *testing.T) {
	desired := procDesired("public.cleanup", &schema.Procedure{
		Schema: "public", Name: "cleanup", ArgsSig: "()",
		Language: "plpgsql", Security: "definer", Body: "begin null; end;",
	})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "language plpgsql security definer as $$") {
		t.Errorf("expected SECURITY DEFINER clause, got %v", p.Creates)
	}
}

func TestProcedureSetClause(t *testing.T) {
	desired := procDesired("public.cleanup", &schema.Procedure{
		Schema: "public", Name: "cleanup", ArgsSig: "()",
		Language: "plpgsql", Set: map[string]string{"search_path": "public"},
		Body: "begin null; end;",
	})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "set search_path = public as $$") {
		t.Errorf("expected SET clause, got %v", p.Creates)
	}
}

func TestProcedureWithArgs(t *testing.T) {
	desired := procDesired("public.archive_user", &schema.Procedure{
		Schema: "public", Name: "archive_user", ArgsSig: "(user_id bigint)",
		Language: "plpgsql", Body: "begin null; end;",
	})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create procedure "public"."archive_user"(user_id bigint) language plpgsql`) {
		t.Errorf("expected args in CREATE PROCEDURE, got %v", p.Creates)
	}
}

func TestProcedureQualifiedArgSkippedIfExists(t *testing.T) {
	live := emptyLive()
	live.Procedures["public.archive_user(t my_type)"] = true
	live.ProcedureDefs["public.archive_user(t my_type)"] = &LiveProcedure{Body: "begin null; end;", Security: "invoker"}
	desired := procDesired("public.archive_user", &schema.Procedure{
		Schema: "public", Name: "archive_user", ArgsSig: "(t public.my_type)",
		Language: "plpgsql", Body: "begin null; end;",
	})
	p := Plan(live, desired, false)
	if findCreate(p, "procedure") {
		t.Errorf("qualified arg type should match unqualified live signature, got %v", p.Creates)
	}
}

func TestProcedureCreatesSchema(t *testing.T) {
	desired := procDesired("jobs.run_all", &schema.Procedure{
		Schema: "jobs", Name: "run_all", ArgsSig: "()",
		Language: "plpgsql", Body: "begin null; end;",
	})
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create schema if not exists "jobs";`) {
		t.Errorf("expected CREATE SCHEMA, got %v", p.Creates)
	}
}

func TestProcedureComment(t *testing.T) {
	desired := procDesired("public.cleanup", &schema.Procedure{
		Schema: "public", Name: "cleanup", ArgsSig: "()",
		Language: "plpgsql", Body: "begin null; end;", Comment: "nightly cleanup",
	})
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `comment on procedure "public"."cleanup"() is 'nightly cleanup';`) {
		t.Errorf("expected COMMENT ON PROCEDURE, got %v", p.Alters)
	}
}

func TestProcedureCommentSkippedIfSame(t *testing.T) {
	live := emptyLive()
	live.Procedures["public.cleanup()"] = true
	live.ProcedureDefs["public.cleanup()"] = &LiveProcedure{Body: "begin null; end;", Security: "invoker"}
	live.FunctionComments["public.cleanup()"] = "nightly cleanup"
	desired := procDesired("public.cleanup", &schema.Procedure{
		Schema: "public", Name: "cleanup", ArgsSig: "()",
		Language: "plpgsql", Body: "begin null; end;", Comment: "nightly cleanup",
	})
	p := Plan(live, desired, false)
	if findAlter(p, "comment on procedure") {
		t.Errorf("unchanged comment should be skipped, got %v", p.Alters)
	}
}

func TestProcedureRevokePublic(t *testing.T) {
	desired := procDesired("public.cleanup", &schema.Procedure{
		Schema: "public", Name: "cleanup", ArgsSig: "()",
		Language: "plpgsql", Body: "begin null; end;", RevokePublic: true,
	})
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `revoke all on procedure "public"."cleanup"() from public;`) {
		t.Errorf("expected REVOKE from PUBLIC, got %v", p.Alters)
	}
}

func TestProcedureGrants(t *testing.T) {
	desired := procDesired("public.cleanup", &schema.Procedure{
		Schema: "public", Name: "cleanup", ArgsSig: "()",
		Language: "plpgsql", Body: "begin null; end;",
		Grants: map[string][]string{"batch_role": {"execute"}},
	})
	p := Plan(emptyLive(), desired, false)
	if !findAlter(p, `grant execute on procedure "public"."cleanup"() to "batch_role";`) {
		t.Errorf("expected GRANT EXECUTE, got %v", p.Alters)
	}
}

func TestProcedureGrantSkippedIfLive(t *testing.T) {
	live := emptyLive()
	live.Procedures["public.cleanup()"] = true
	live.ProcedureDefs["public.cleanup()"] = &LiveProcedure{Body: "begin null; end;", Security: "invoker"}
	live.FunctionGrants["public.cleanup()"] = map[string]map[string]bool{"batch_role": {"execute": true}}
	desired := procDesired("public.cleanup", &schema.Procedure{
		Schema: "public", Name: "cleanup", ArgsSig: "()",
		Language: "plpgsql", Body: "begin null; end;",
		Grants: map[string][]string{"batch_role": {"execute"}},
	})
	p := Plan(live, desired, false)
	if findAlter(p, "grant execute on procedure") {
		t.Errorf("live grant should be skipped, got %v", p.Alters)
	}
}

// --- table partitioning ---

func TestPartitionedTableCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.measurement": {
			Name:        "measurement",
			Columns:     map[string]*schema.Column{"logdate": {Type: "date"}},
			PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"logdate"}},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `partition by range ("logdate")`) {
		t.Errorf("expected PARTITION BY clause, got %+v", p.Creates)
	}
}

func TestPartitionedTableSkippedIfExists(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.measurement": {
			Name:        "measurement",
			Columns:     map[string]*schema.Column{"logdate": {Type: "date"}},
			PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"logdate"}},
		},
	}}
	live := liveWithTable("public.measurement", map[string]*LiveColumn{"logdate": {Type: "date"}})
	p := Plan(live, desired, false)
	if findCreate(p, "create table") {
		t.Errorf("existing partitioned table should be skipped, got %+v", p.Creates)
	}
}

func TestPartitionRangeCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.measurement_y2024": {
			Name:        "measurement_y2024",
			Columns:     map[string]*schema.Column{},
			PartitionOf: "public.measurement",
			Partition:   &schema.PartitionSpec{From: []string{"2024-01-01"}, To: []string{"2025-01-01"}, Modulus: -1, Remainder: -1},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	want := `create table if not exists "public"."measurement_y2024" partition of "public"."measurement" for values from ('2024-01-01') to ('2025-01-01');`
	if !findCreate(p, want) {
		t.Errorf("want %q, got %+v", want, p.Creates)
	}
}

func TestPartitionSkippedIfExists(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.measurement_y2024": {
			Name:        "measurement_y2024",
			Columns:     map[string]*schema.Column{},
			PartitionOf: "public.measurement",
			Partition:   &schema.PartitionSpec{From: []string{"2024-01-01"}, To: []string{"2025-01-01"}, Modulus: -1, Remainder: -1},
		},
	}}
	live := liveWithTable("public.measurement_y2024", map[string]*LiveColumn{})
	p := Plan(live, desired, false)
	if findCreate(p, "partition of") {
		t.Errorf("existing partition should be skipped, got %+v", p.Creates)
	}
}

func TestPartitionListCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.events_eu": {
			Name:        "events_eu",
			Columns:     map[string]*schema.Column{},
			PartitionOf: "public.events",
			Partition:   &schema.PartitionSpec{In: []string{"de", "fr"}, Modulus: -1, Remainder: -1},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "for values in ('de', 'fr')") {
		t.Errorf("expected list bound, got %+v", p.Creates)
	}
}

func TestPartitionHashCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users_p0": {
			Name:        "users_p0",
			Columns:     map[string]*schema.Column{},
			PartitionOf: "public.users",
			Partition:   &schema.PartitionSpec{Modulus: 4, Remainder: 0},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "for values with (modulus 4, remainder 0)") {
		t.Errorf("expected hash bound, got %+v", p.Creates)
	}
}

func TestPartitionDefaultCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.measurement_default": {
			Name:        "measurement_default",
			Columns:     map[string]*schema.Column{},
			PartitionOf: "public.measurement",
			Partition:   &schema.PartitionSpec{Default: true, Modulus: -1, Remainder: -1},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `partition of "public"."measurement" default;`) {
		t.Errorf("expected DEFAULT partition, got %+v", p.Creates)
	}
}

func TestPartitionBoundLiterals(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t_low": {
			Name:        "t_low",
			Columns:     map[string]*schema.Column{},
			PartitionOf: "public.t",
			Partition:   &schema.PartitionSpec{From: []string{"minvalue"}, To: []string{"100"}, Modulus: -1, Remainder: -1},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "for values from (minvalue) to (100)") {
		t.Errorf("MINVALUE keyword and numbers must stay unquoted, got %+v", p.Creates)
	}
}

func TestPartitionCreatedAfterParent(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.measurement": {
			Name:        "measurement",
			Columns:     map[string]*schema.Column{"logdate": {Type: "date"}},
			PartitionBy: &schema.PartitionBy{Type: "range", Columns: []string{"logdate"}},
		},
		"public.measurement_y2024": {
			Name:        "measurement_y2024",
			Columns:     map[string]*schema.Column{},
			PartitionOf: "public.measurement",
			Partition:   &schema.PartitionSpec{From: []string{"2024-01-01"}, To: []string{"2025-01-01"}, Modulus: -1, Remainder: -1},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	parentIdx, childIdx := -1, -1
	for i, s := range p.Creates {
		if strings.Contains(s, "partition by range") {
			parentIdx = i
		}
		if strings.Contains(s, "partition of") {
			childIdx = i
		}
	}
	if parentIdx < 0 || childIdx < 0 {
		t.Fatalf("missing statements: %+v", p.Creates)
	}
	if parentIdx > childIdx {
		t.Errorf("parent create (%d) must precede partition create (%d): %+v", parentIdx, childIdx, p.Creates)
	}
}

func TestPartitionByExpressionKey(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.events": {
			Name:        "events",
			Columns:     map[string]*schema.Column{"created_at": {Type: "timestamptz"}},
			PartitionBy: &schema.PartitionBy{Type: "hash", Columns: []string{"date_trunc('day', created_at)"}},
		},
	}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, "partition by hash ((date_trunc('day', created_at)))") {
		t.Errorf("expression key should pass through parenthesized, got %+v", p.Creates)
	}
}

// --- column alterations ---

func TestAlterColumnTypeUnsafe(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"name": {Type: "character varying(100)"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"name": {Type: "varchar(255)"},
		}},
	}}
	p := Plan(live, desired, true)
	if !findAlter(p, `alter table "public"."t" alter column "name" type varchar(255);`) {
		t.Errorf("expected type alter, got %+v", p.Alters)
	}
}

func TestAlterColumnTypeSafeSkipped(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"name": {Type: "character varying(100)"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"name": {Type: "varchar(255)"},
		}},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "type varchar(255)") {
		t.Errorf("type alter must be gated behind unsafe, got %+v", p.Alters)
	}
}

func TestAlterColumnTypeUsing(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"amount": {Type: "text"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"amount": {Type: "numeric(10,2)", Using: "amount::numeric(10,2)"},
		}},
	}}
	p := Plan(live, desired, true)
	if !findAlter(p, `alter column "amount" type numeric(10,2) using amount::numeric(10,2);`) {
		t.Errorf("expected type alter with using clause, got %+v", p.Alters)
	}
}

func TestAlterColumnTypeEquivalentSkipped(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{
		"a": {Type: "character varying(255)"},
		"b": {Type: "integer"},
		"c": {Type: "timestamp with time zone"},
		"d": {Type: "numeric(10, 2)"},
		"e": {Type: "double precision"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"a": {Type: "varchar(255)"},
			"b": {Type: "int"},
			"c": {Type: "timestamptz"},
			"d": {Type: "numeric(10,2)"},
			"e": {Type: "float8"},
		}},
	}}
	p := Plan(live, desired, true)
	if len(p.Alters) != 0 {
		t.Errorf("equivalent types must not diff, got %+v", p.Alters)
	}
}

func TestAlterColumnSerialNotDiffedAgainstInteger(t *testing.T) {
	live := liveWithTablePK("public.t", map[string]*LiveColumn{
		"id": {Type: "integer", Default: "nextval('t_id_seq'::regclass)"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"id": {Type: "serial", PrimaryKey: true},
		}},
	}}
	p := Plan(live, desired, true)
	if len(p.Alters) != 0 {
		t.Errorf("serial column must not diff against its live integer+nextval form, got %+v", p.Alters)
	}
}

func TestAlterColumnSetDefault(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"status": {Type: "text"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"status": {Type: "text", Default: "'active'"},
		}},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, `alter table "public"."t" alter column "status" set default 'active';`) {
		t.Errorf("expected set default, got %+v", p.Alters)
	}
}

func TestAlterColumnSetDefaultChanged(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"status": {Type: "text", Default: "'old'::text"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"status": {Type: "text", Default: "'new'"},
		}},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, `set default 'new';`) {
		t.Errorf("expected set default for changed default, got %+v", p.Alters)
	}
}

func TestAlterColumnDefaultEquivalentSkipped(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{
		"status": {Type: "text", Default: "'active'::text"},
		"created": {Type: "timestamptz", Default: "NOW()"},
		"meta": {Type: "jsonb", Default: "'{}'::jsonb"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"status":  {Type: "text", Default: "'active'"},
			"created": {Type: "timestamptz", Default: "now()"},
			"meta":    {Type: "jsonb", Default: "'{}'"},
		}},
	}}
	p := Plan(live, desired, false)
	if len(p.Alters) != 0 {
		t.Errorf("equivalent defaults must not diff, got %+v", p.Alters)
	}
}

func TestAlterColumnDropDefault(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"status": {Type: "text", Default: "'active'::text"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"status": {Type: "text"},
		}},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, `alter table "public"."t" alter column "status" drop default;`) {
		t.Errorf("expected drop default, got %+v", p.Alters)
	}
}

func TestAlterColumnDropDefaultSkipsNextval(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "integer", Default: "nextval('t_id_seq'::regclass)"}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"id": {Type: "int"},
		}},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "drop default") {
		t.Errorf("nextval defaults (serial) must never be dropped, got %+v", p.Alters)
	}
}

func TestAlterColumnSetNotNull(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"email": {Type: "text", Nullable: true}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"email": {Type: "text", Nullable: false},
		}},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, `alter table "public"."t" alter column "email" set not null;`) {
		t.Errorf("expected set not null, got %+v", p.Alters)
	}
}

func TestAlterColumnDropNotNull(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"email": {Type: "text", Nullable: false}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"email": {Type: "text", Nullable: true},
		}},
	}}
	p := Plan(live, desired, false)
	if !findAlter(p, `alter table "public"."t" alter column "email" drop not null;`) {
		t.Errorf("expected drop not null, got %+v", p.Alters)
	}
}

func TestAlterColumnDropNotNullSkippedForPK(t *testing.T) {
	live := liveWithTablePK("public.t", map[string]*LiveColumn{"id": {Type: "bigint", Nullable: false}})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {
			Name:       "t",
			PrimaryKey: []string{"id"},
			Columns: map[string]*schema.Column{
				"id": {Type: "bigint", Nullable: true},
			},
		},
	}}
	p := Plan(live, desired, false)
	if findAlter(p, "drop not null") {
		t.Errorf("primary key columns must never drop not null, got %+v", p.Alters)
	}
}

func TestAlterColumnIdentityNeverAltered(t *testing.T) {
	live := liveWithTablePK("public.t", map[string]*LiveColumn{
		"id": {Type: "bigint", Nullable: false, Identity: "always"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"id": {Type: "bigint", PrimaryKey: true, Identity: "always"},
		}},
	}}
	p := Plan(live, desired, true)
	if len(p.Alters) != 0 {
		t.Errorf("identity columns are create-only, got %+v", p.Alters)
	}
}

func TestAlterColumnNoChangeNoSQL(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{
		"email": {Type: "text", Nullable: false, Default: "'x'::text"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{
			"email": {Type: "text", Nullable: false, Default: "'x'"},
		}},
	}}
	p := Plan(live, desired, true)
	if len(p.Alters) != 0 || len(p.Creates) != 0 || len(p.Drops) != 0 {
		t.Errorf("identical column must emit no SQL, got %+v %+v %+v", p.Creates, p.Alters, p.Drops)
	}
}

// --- plan determinism ---

func TestPlanDeterministic(t *testing.T) {
	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			"app.a": {Name: "a", Columns: map[string]*schema.Column{"id": {Type: "uuid"}}},
			"app.b": {Name: "b", Columns: map[string]*schema.Column{"id": {Type: "uuid"}},
				DependsOn: []string{"table app.a"},
				ForeignKeys: []*schema.ForeignKey{
					{Name: "b_a_fkey", Columns: []string{"id"}, RefTable: "app.a", RefColumns: []string{"id"}},
				},
				Triggers: []*schema.Trigger{
					{Name: "b_trg", Timing: "before", Events: []string{"update"}, Level: "row", Procedure: "public.fn()"},
				},
			},
			"app.c": {Name: "c", Columns: map[string]*schema.Column{"id": {Type: "uuid"}}},
		},
		Functions: map[string]*schema.Function{
			"public.fn": {Name: "fn", Returns: "trigger", Language: "plpgsql", Body: "BEGIN RETURN NEW; END;"},
			"public.g":  {Name: "g", Returns: "boolean", Language: "sql", Body: "SELECT true;", DependsOn: []string{"table app.c"}},
		},
	}
	first := Plan(emptyLive(), desired, false)
	for i := 0; i < 30; i++ {
		again := Plan(emptyLive(), desired, false)
		if strings.Join(again.Creates, "\n") != strings.Join(first.Creates, "\n") {
			t.Fatalf("run %d: Creates order differs:\n%v\nvs\n%v", i, again.Creates, first.Creates)
		}
		if strings.Join(again.Alters, "\n") != strings.Join(first.Alters, "\n") {
			t.Fatalf("run %d: Alters order differs", i)
		}
	}
}

// --- canonical type spellings in function args ---

func TestFunctionTimestamptzArgSkippedIfExists(t *testing.T) {
	l := emptyLive()
	l.Functions["public.f(ts timestamp with time zone[])"] = true
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.f": {
				Name: "f", Schema: "public", ArgsSig: "(ts timestamptz[])",
				Returns: "text", Language: "sql", Body: "select 'x'",
			},
		},
	}
	p := Plan(l, desired, false)
	if findCreate(p, "create function") {
		t.Errorf("timestamptz[] should match live timestamp with time zone[]; creates: %v", p.Creates)
	}
}

func TestFunctionUnnamedTimestamptzArgSkippedIfExists(t *testing.T) {
	l := emptyLive()
	l.Functions["public.f(timestamp with time zone[])"] = true
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.f": {
				Name: "f", Schema: "public", ArgsSig: "(timestamptz[])",
				Returns: "text", Language: "sql", Body: "select 'x'",
			},
		},
	}
	p := Plan(l, desired, false)
	if findCreate(p, "create function") {
		t.Errorf("unnamed timestamptz[] should match live timestamp with time zone[]; creates: %v", p.Creates)
	}
}

func TestFunctionTimetzAndDoublePrecisionArgsSkippedIfExists(t *testing.T) {
	l := emptyLive()
	l.Functions["public.f(t time with time zone, d double precision)"] = true
	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			"public.f": {
				Name: "f", Schema: "public", ArgsSig: "(t timetz, d float8)",
				Returns: "text", Language: "sql", Body: "select 'x'",
			},
		},
	}
	p := Plan(l, desired, false)
	if findCreate(p, "create function") {
		t.Errorf("alias arg types should match live canonical spellings; creates: %v", p.Creates)
	}
}

// --- single-buffer ordering: add column before dependent creates ---

func createIndexOf(p *PlanDiff, substr string) int {
	for i, s := range p.Creates {
		if strings.Contains(s, substr) {
			return i
		}
	}
	return -1
}

func TestAddColumnBeforeIndexCreate(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{
		"id": {Type: "int"},
	})
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.users": {
			Name: "users",
			Columns: map[string]*schema.Column{
				"id":    {Type: "int"},
				"email": {Type: "text"},
			},
			Indexes: []*schema.Index{{Name: "users_email_idx", Columns: []string{"email"}}},
		},
	}}
	p := Plan(live, desired, false)
	addIdx := createIndexOf(p, "add column")
	ixIdx := createIndexOf(p, "create index")
	if addIdx < 0 || ixIdx < 0 {
		t.Fatalf("expected add column and create index in creates: %v", p.Creates)
	}
	if addIdx > ixIdx {
		t.Errorf("add column must precede create index in one buffer; creates: %v", p.Creates)
	}
}

func TestAddColumnBeforeDependentFunctionCreate(t *testing.T) {
	live := liveWithTable("public.users", map[string]*LiveColumn{
		"id": {Type: "int"},
	})
	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			"public.users": {Name: "users", Columns: map[string]*schema.Column{
				"id":    {Type: "int"},
				"email": {Type: "text"},
			}},
		},
		Functions: map[string]*schema.Function{
			"public.emails": {
				Name: "emails", Schema: "public", ArgsSig: "()",
				Returns: "setof text", Language: "sql",
				Body:      "select email from public.users",
				DependsOn: []string{"public.users"},
			},
		},
	}
	p := Plan(live, desired, false)
	addIdx := createIndexOf(p, "add column")
	fnIdx := createIndexOf(p, "create function")
	if addIdx < 0 || fnIdx < 0 {
		t.Fatalf("expected add column and create function in creates: %v", p.Creates)
	}
	if addIdx > fnIdx {
		t.Errorf("add column must precede dependent function create; creates: %v", p.Creates)
	}
}
