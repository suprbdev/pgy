package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

// --- helpers ---

func emptyLive() *Live {
	return &Live{
		Schemas:    map[string]bool{},
		Tables:     map[string]*LiveTable{},
		Types:      map[string]bool{},
		Functions:  map[string]bool{},
		Extensions: map[string]bool{},
		Views:      map[string]bool{},
		MatViews:   map[string]bool{},
		TableGrants:        map[string]map[string]map[string]bool{},
		FunctionGrants:     map[string]map[string]map[string]bool{},
		SchemaGrants:       map[string]map[string]map[string]bool{},
		FunctionPublicExec: map[string]bool{},
	}
}

func liveWithTable(fq string, cols map[string]*LiveColumn) *Live {
	l := emptyLive()
	l.Tables[fq] = &LiveTable{
		Columns:     cols,
		Constraints: map[string]bool{},
		Indexes:     map[string]bool{},
		Triggers:    map[string]bool{},
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
	if len(p.Alters) != 1 {
		t.Fatalf("want 1 alter, got %d", len(p.Alters))
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
		RowLevelSecurity: true,
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

func TestPolicyCreate(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "member_select", For: "select", To: []string{"kickly_member"},
			Using: "member_id = current_setting('app.member_id')::bigint"},
	})}}
	p := Plan(emptyLive(), desired, false)
	want := `create policy "member_select" on "public"."t" for select to "kickly_member" using (member_id = current_setting('app.member_id')::bigint);`
	if !findCreate(p, want) {
		t.Errorf("expected %s; creates: %v", want, p.Creates)
	}
}

func TestPolicyWithCheck(t *testing.T) {
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "member_insert", For: "insert", WithCheck: "member_id = 1"},
	})}}
	p := Plan(emptyLive(), desired, false)
	if !findCreate(p, `create policy "member_insert" on "public"."t" for insert with check (member_id = 1);`) {
		t.Errorf("expected with check policy; creates: %v", p.Creates)
	}
}

func TestPolicySkippedIfExists(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["public.t"].RLSEnabled = true
	live.Tables["public.t"].Policies = map[string]bool{"member_select": true}
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "member_select", For: "select", Using: "true"},
	})}}
	p := Plan(live, desired, false)
	if findCreate(p, "create policy") {
		t.Errorf("policy already live, should not re-create; creates: %v", p.Creates)
	}
}

func TestPolicyDroppedOnRemoval(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["public.t"].RLSEnabled = true
	live.Tables["public.t"].Policies = map[string]bool{"old_policy": true}
	desired := &schema.Database{Tables: map[string]*schema.Table{"public.t": rlsTable([]*schema.Policy{
		{Name: "member_select", For: "select", Using: "true"},
	})}}
	p := Plan(live, desired, false)
	found := false
	for _, s := range p.Drops {
		if s == `drop policy "old_policy" on "public"."t";` { found = true }
	}
	if !found {
		t.Errorf("expected drop of removed policy; drops: %v", p.Drops)
	}
}

func TestPolicyNoDropWithoutPoliciesBlock(t *testing.T) {
	live := liveWithTable("public.t", map[string]*LiveColumn{"id": {Type: "bigint"}})
	live.Tables["public.t"].Policies = map[string]bool{"some_policy": true}
	desired := &schema.Database{Tables: map[string]*schema.Table{
		"public.t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "bigint"}}},
	}}
	p := Plan(live, desired, false)
	if len(p.Drops) != 0 {
		t.Errorf("no policies block, policies unmanaged, should not drop; drops: %v", p.Drops)
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
	if !findAlter(p, "add column") {
		t.Errorf("expected add column; alters: %v", p.Alters)
	}
	if !findAlter(p, "email") {
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
	live.Tables["public.t"].Triggers["trg_audit"] = true
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
