package schema

import (
	"os"
	"path/filepath"
	"testing"
)

// --- parseFlexibleDatabase ---

func TestParseMapFormat(t *testing.T) {
	yaml := `
tables:
  public.users:
    columns:
      id:
        type: int
        primaryKey: true
      email:
        type: text
        nullable: false
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	tbl, ok := db.Tables["public.users"]
	if !ok {
		t.Fatal("expected public.users")
	}
	if len(tbl.Columns) != 2 {
		t.Fatalf("want 2 cols, got %d", len(tbl.Columns))
	}
	if !tbl.Columns["id"].PrimaryKey {
		t.Error("id.PrimaryKey should be true")
	}
	if tbl.Columns["email"].Nullable {
		t.Error("email.Nullable should be false")
	}
}

func TestParseListFormat(t *testing.T) {
	yaml := `
tables:
  - name: orders
    schema: public
    columns:
      - name: id
        type: bigint
      - name: total
        type: numeric
        nullable: true
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	tbl, ok := db.Tables["public.orders"]
	if !ok {
		t.Fatal("expected public.orders")
	}
	if len(tbl.Columns) != 2 {
		t.Fatalf("want 2 cols, got %d", len(tbl.Columns))
	}
	if !tbl.Columns["total"].Nullable {
		t.Error("total.Nullable should be true")
	}
}

func TestParseSchemaBlock(t *testing.T) {
	yaml := `
schema public:
  table users:
    columns:
      id:
        type: int
      name:
        type: text
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	tbl, ok := db.Tables["public.users"]
	if !ok {
		t.Fatal("expected public.users")
	}
	if len(tbl.Columns) != 2 {
		t.Fatalf("want 2 cols, got %d", len(tbl.Columns))
	}
}

func TestColumnOrderPreserved(t *testing.T) {
	yaml := `
schema public:
  table items:
    columns:
      z_col:
        type: text
      a_col:
        type: int
      m_col:
        type: bool
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	tbl := db.Tables["public.items"]
	if tbl == nil {
		t.Fatal("expected public.items")
	}
	if len(tbl.ColumnOrder) != 3 {
		t.Fatalf("want 3 ordered cols, got %d", len(tbl.ColumnOrder))
	}
	if tbl.ColumnOrder[0] != "z_col" || tbl.ColumnOrder[1] != "a_col" || tbl.ColumnOrder[2] != "m_col" {
		t.Errorf("wrong order: %v", tbl.ColumnOrder)
	}
}

func TestParseSchemasBlock(t *testing.T) {
	// schemas: <name>: passes the schema body directly to mergeTablesInto,
	// so the value must be a tables map (name -> spec), not { tables: {...} }.
	yaml := `
schemas:
  myschema:
    accounts:
      columns:
        id:
          type: int
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := db.Tables["myschema.accounts"]; !ok {
		t.Fatal("expected myschema.accounts")
	}
}

// --- column attributes ---

func TestColumnNotNullAlias(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      col:
        type: text
        notNull: true
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	col := db.Tables["public.t"].Columns["col"]
	if col.Nullable {
		t.Error("notNull: true should set Nullable=false")
	}
}

func TestColumnDefault(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      created_at:
        type: timestamptz
        default: now()
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	col := db.Tables["public.t"].Columns["created_at"]
	if col.Default != "now()" {
		t.Errorf("want default now(), got %q", col.Default)
	}
}

func TestColumnDefaultNonString(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      active:
        type: boolean
        default: false
      count:
        type: int
        default: 0
      ratio:
        type: numeric
        default: 0.5
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	cols := db.Tables["public.t"].Columns
	if cols["active"].Default != "false" {
		t.Errorf("want default false, got %q", cols["active"].Default)
	}
	if cols["count"].Default != "0" {
		t.Errorf("want default 0, got %q", cols["count"].Default)
	}
	if cols["ratio"].Default != "0.5" {
		t.Errorf("want default 0.5, got %q", cols["ratio"].Default)
	}
}

func TestTableGrantsParse(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      id:
        type: int
    grants:
      kickly_member: [select, insert]
      kickly_admin: [select, insert, update, delete]
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	g := db.Tables["public.t"].Grants
	if g == nil {
		t.Fatal("expected grants parsed")
	}
	if len(g["kickly_member"]) != 2 || g["kickly_member"][0] != "insert" || g["kickly_member"][1] != "select" {
		t.Errorf("want [insert select], got %v", g["kickly_member"])
	}
	if len(g["kickly_admin"]) != 4 {
		t.Errorf("want 4 privs for kickly_admin, got %v", g["kickly_admin"])
	}
}

func TestFunctionGrantsParse(t *testing.T) {
	yaml := `
schema public:
  function secret_fn():
    returns: int
    language: sql
    security: definer
    revokePublic: true
    grants:
      kickly_member: [execute]
    body: select 1
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	fn := db.Functions["public.secret_fn"]
	if fn == nil {
		t.Fatal("expected function parsed")
	}
	if !fn.RevokePublic {
		t.Error("want RevokePublic true")
	}
	if len(fn.Grants["kickly_member"]) != 1 || fn.Grants["kickly_member"][0] != "execute" {
		t.Errorf("want [execute], got %v", fn.Grants["kickly_member"])
	}
}

func TestSchemaGrantsParseBlock(t *testing.T) {
	yaml := `
schema app:
  grants:
    kickly_member: [usage]
  table t:
    columns:
      id:
        type: int
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	g := db.SchemaGrants["app"]
	if g == nil || len(g["kickly_member"]) != 1 || g["kickly_member"][0] != "usage" {
		t.Errorf("want schema grants usage, got %v", g)
	}
	if _, ok := db.Tables["app.t"]; !ok {
		t.Error("table t should still parse alongside grants")
	}
}

func TestSchemaGrantsParseSchemasForm(t *testing.T) {
	yaml := `
schemas:
  app:
    grants:
      kickly_member: [usage, create]
    accounts:
      columns:
        id:
          type: int
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	g := db.SchemaGrants["app"]
	if g == nil || len(g["kickly_member"]) != 2 {
		t.Errorf("want schema grants [create usage], got %v", g)
	}
	if _, ok := db.Tables["app.grants"]; ok {
		t.Error("grants key must not become a table")
	}
	if _, ok := db.Tables["app.accounts"]; !ok {
		t.Error("expected app.accounts table")
	}
}

func TestRLSPoliciesParse(t *testing.T) {
	yaml := `
tables:
  public.orders:
    columns:
      id:
        type: bigint
    rowLevelSecurity: true
    policies:
      member_select:
        for: select
        to: [kickly_member]
        using: "member_id = current_setting('app.member_id')::bigint"
      member_insert:
        for: insert
        to: kickly_member
        withCheck: "member_id = current_setting('app.member_id')::bigint"
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	tbl := db.Tables["public.orders"]
	if !tbl.RowLevelSecurity {
		t.Error("want RowLevelSecurity true")
	}
	if len(tbl.Policies) != 2 {
		t.Fatalf("want 2 policies, got %d", len(tbl.Policies))
	}
	// sorted by name: member_insert, member_select
	ins, sel := tbl.Policies[0], tbl.Policies[1]
	if ins.Name != "member_insert" || sel.Name != "member_select" {
		t.Fatalf("want sorted [member_insert member_select], got [%s %s]", ins.Name, sel.Name)
	}
	if sel.For != "select" || len(sel.To) != 1 || sel.To[0] != "kickly_member" || sel.Using == "" {
		t.Errorf("select policy fields wrong: %+v", sel)
	}
	if ins.For != "insert" || len(ins.To) != 1 || ins.WithCheck == "" || ins.Using != "" {
		t.Errorf("insert policy fields wrong: %+v", ins)
	}
}

func TestCommentsParse(t *testing.T) {
	yaml := `
schema app:
  comment: "application schema"
  table orders:
    comment: |-
      @behavior +list
      Customer orders.
    columns:
      id:
        type: bigint
        comment: "@name orderId"
  function fn():
    returns: int
    language: sql
    comment: "computes"
    body: select 1
  type status:
    type: enum
    labels: [a, b]
    comment: "status enum"
  view v:
    query: select 1
    comment: "@name simpleView"
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if db.SchemaComments["app"] != "application schema" {
		t.Errorf("schema comment: %q", db.SchemaComments["app"])
	}
	tbl := db.Tables["app.orders"]
	if tbl.Comment != "@behavior +list\nCustomer orders." {
		t.Errorf("table comment: %q", tbl.Comment)
	}
	if tbl.Columns["id"].Comment != "@name orderId" {
		t.Errorf("column comment: %q", tbl.Columns["id"].Comment)
	}
	if db.Functions["app.fn"].Comment != "computes" {
		t.Errorf("function comment: %q", db.Functions["app.fn"].Comment)
	}
	if db.Types["app.status"].Comment != "status enum" {
		t.Errorf("type comment: %q", db.Types["app.status"].Comment)
	}
	if db.Views["app.v"].Comment != "@name simpleView" {
		t.Errorf("view comment: %q", db.Views["app.v"].Comment)
	}
}

func TestConstraintTriggerParse(t *testing.T) {
	yaml := `
tables:
  app.entry:
    columns:
      id:
        type: bigint
    triggers:
      trg_check_requirements:
        constraint: true
        deferrable: true
        initiallyDeferred: true
        events: [insert, update]
        procedure: app.check_entry_requirements()
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	trs := db.Tables["app.entry"].Triggers
	if len(trs) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(trs))
	}
	tr := trs[0]
	if !tr.Constraint || !tr.Deferrable || !tr.InitiallyDeferred {
		t.Errorf("want constraint+deferrable+initiallyDeferred, got %+v", tr)
	}
	if tr.Procedure != "app.check_entry_requirements()" {
		t.Errorf("procedure: %q", tr.Procedure)
	}
}

func TestCompositeAttributeOrderPreserved(t *testing.T) {
	yaml := `
schema public:
  type jwt:
    type: composite
    attributes:
      role:
        type: text
      person_id:
        type: uuid
      exp:
        type: bigint
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	td := db.Types["public.jwt"]
	if td == nil {
		t.Fatal("expected public.jwt type")
	}
	want := []string{"role", "person_id", "exp"}
	if len(td.AttributeOrder) != 3 {
		t.Fatalf("want AttributeOrder %v, got %v", want, td.AttributeOrder)
	}
	for i, n := range want {
		if td.AttributeOrder[i] != n {
			t.Fatalf("want AttributeOrder %v, got %v", want, td.AttributeOrder)
		}
	}
}

func TestColumnUnique(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      email:
        type: text
        unique: true
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if !db.Tables["public.t"].Columns["email"].Unique {
		t.Error("expected Unique=true")
	}
}

// --- primary key ---

func TestPrimaryKeyTableLevel(t *testing.T) {
	yaml := `
tables:
  public.t:
    primaryKey: [id]
    columns:
      id:
        type: int
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	pk := db.Tables["public.t"].PrimaryKey
	if len(pk) != 1 || pk[0] != "id" {
		t.Errorf("unexpected pk: %v", pk)
	}
}

// --- foreign keys ---

func TestForeignKeys(t *testing.T) {
	yaml := `
tables:
  public.orders:
    columns:
      user_id:
        type: int
    foreignKeys:
      fk_user:
        columns: [user_id]
        references:
          table: public.users
          columns: [id]
        onDelete: cascade
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	fks := db.Tables["public.orders"].ForeignKeys
	if len(fks) != 1 {
		t.Fatalf("want 1 fk, got %d", len(fks))
	}
	fk := fks[0]
	if fk.Name != "fk_user" {
		t.Errorf("fk name: %s", fk.Name)
	}
	if fk.RefTable != "public.users" {
		t.Errorf("ref table: %s", fk.RefTable)
	}
	if fk.OnDelete != "cascade" {
		t.Errorf("onDelete: %s", fk.OnDelete)
	}
}

// --- indexes ---

func TestIndexes(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      email:
        type: text
    indexes:
      idx_email:
        columns: [email]
        unique: true
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	ixs := db.Tables["public.t"].Indexes
	if len(ixs) != 1 {
		t.Fatalf("want 1 index, got %d", len(ixs))
	}
	if !ixs[0].Unique {
		t.Error("expected unique index")
	}
	if ixs[0].Name != "idx_email" {
		t.Errorf("index name: %s", ixs[0].Name)
	}
}

// --- constraints ---

func TestCheckConstraint(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      age:
        type: int
    constraints:
      chk_age:
        type: check
        expression: "age > 0"
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	cts := db.Tables["public.t"].Constraints
	if len(cts) != 1 {
		t.Fatalf("want 1 constraint, got %d", len(cts))
	}
	ct := cts[0]
	if ct.Type != "check" || ct.Expression != "age > 0" {
		t.Errorf("unexpected constraint: %+v", ct)
	}
}

func TestUniqueConstraint(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      a:
        type: text
      b:
        type: text
    constraints:
      uq_ab:
        type: unique
        columns: [a, b]
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	cts := db.Tables["public.t"].Constraints
	if len(cts) != 1 || cts[0].Type != "unique" {
		t.Fatalf("unexpected constraint: %+v", cts)
	}
	if len(cts[0].Columns) != 2 {
		t.Errorf("want 2 columns, got %v", cts[0].Columns)
	}
}

func TestExcludeConstraint(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      range:
        type: tstzrange
    constraints:
      excl_range:
        type: exclude
        def: "using gist (range with &&)"
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	cts := db.Tables["public.t"].Constraints
	if len(cts) != 1 || cts[0].Type != "exclude" {
		t.Fatalf("unexpected constraint: %+v", cts)
	}
	if cts[0].Expression != "using gist (range with &&)" {
		t.Errorf("expression: %q", cts[0].Expression)
	}
}

// --- triggers ---

func TestTriggers(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      id:
        type: int
    triggers:
      trg_audit:
        timing: after
        events: [insert, update]
        level: row
        procedure: audit_fn()
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	trgs := db.Tables["public.t"].Triggers
	if len(trgs) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(trgs))
	}
	tr := trgs[0]
	if tr.Name != "trg_audit" || tr.Timing != "after" || tr.Level != "row" {
		t.Errorf("unexpected trigger: %+v", tr)
	}
	if len(tr.Events) != 2 {
		t.Errorf("want 2 events, got %v", tr.Events)
	}
}

// --- extensions ---

func TestExtensions(t *testing.T) {
	yaml := `
extensions:
  - name: pgcrypto
    ifNotExists: true
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Extensions) != 1 {
		t.Fatalf("want 1 extension, got %d", len(db.Extensions))
	}
	ext := db.Extensions[0]
	if ext.Name != "pgcrypto" || !ext.IfNotExists {
		t.Errorf("unexpected extension: %+v", ext)
	}
}

// --- enum types ---

func TestEnumType(t *testing.T) {
	yaml := `
schema public:
  type status:
    type: enum
    labels: [pending, active, archived]
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	td, ok := db.Types["public.status"]
	if !ok {
		t.Fatal("expected public.status type")
	}
	if td.Kind != "enum" {
		t.Errorf("kind: %s", td.Kind)
	}
	if len(td.Labels) != 3 {
		t.Errorf("want 3 labels, got %v", td.Labels)
	}
}

// --- composite types ---

func TestCompositeType(t *testing.T) {
	yaml := `
schema public:
  type address:
    type: composite
    attributes:
      street:
        type: text
      city:
        type: text
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	td, ok := db.Types["public.address"]
	if !ok {
		t.Fatal("expected public.address type")
	}
	if td.Kind != "composite" {
		t.Errorf("kind: %s", td.Kind)
	}
	if td.Attributes["street"] != "text" {
		t.Errorf("attribute street: %s", td.Attributes["street"])
	}
}

// --- functions ---

func TestFunction(t *testing.T) {
	yaml := `
schema public:
  function get_user(id int):
    returns: text
    language: plpgsql
    security: definer
    stable: true
    body: |
      begin
        return 'ok';
      end;
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	var fn *Function
	for _, f := range db.Functions {
		fn = f
		break
	}
	if fn == nil {
		t.Fatal("no function parsed")
	}
	if fn.Name != "get_user" {
		t.Errorf("name: %s", fn.Name)
	}
	if fn.Returns != "text" {
		t.Errorf("returns: %s", fn.Returns)
	}
	if fn.Security != "definer" {
		t.Errorf("security: %s", fn.Security)
	}
	if fn.Volatility != "stable" {
		t.Errorf("volatility: %s", fn.Volatility)
	}
}

func TestFunctionStrict(t *testing.T) {
	yaml := `
schema public:
  function add(a int, b int):
    returns: int
    language: sql
    strict: true
    body: "select a + b"
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range db.Functions {
		if !f.Strict {
			t.Error("expected Strict=true")
		}
		return
	}
	t.Fatal("no function parsed")
}

func TestFunctionImmutable(t *testing.T) {
	yaml := `
schema public:
  function add(a int, b int):
    returns: int
    language: sql
    immutable: true
    body: "select a + b"
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range db.Functions {
		if f.Volatility != "immutable" {
			t.Errorf("volatility: got %q want \"immutable\"", f.Volatility)
		}
		return
	}
	t.Fatal("no function parsed")
}

// --- dependsOn ---

func TestDependsOnTable(t *testing.T) {
	yaml := `
tables:
  public.orders:
    dependsOn: ["table public.users"]
    columns:
      id:
        type: int
  public.users:
    columns:
      id:
        type: int
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	dep := db.Tables["public.orders"].DependsOn
	if len(dep) != 1 || dep[0] != "table public.users" {
		t.Errorf("unexpected dependsOn: %v", dep)
	}
}

// --- topological sort ---

func TestTopologicalSortBasic(t *testing.T) {
	yaml := `
tables:
  public.orders:
    dependsOn: ["table public.users"]
    columns:
      id:
        type: int
  public.users:
    columns:
      id:
        type: int
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := TopologicalSort(db)
	if err != nil {
		t.Fatal(err)
	}
	// users must come before orders
	userIdx, ordersIdx := -1, -1
	for i, e := range sorted {
		if e.Key == "public.users" {
			userIdx = i
		}
		if e.Key == "public.orders" {
			ordersIdx = i
		}
	}
	if userIdx == -1 || ordersIdx == -1 {
		t.Fatalf("missing entities: %v", sorted)
	}
	if userIdx >= ordersIdx {
		t.Errorf("users (idx %d) must precede orders (idx %d)", userIdx, ordersIdx)
	}
}

func TestTopologicalSortTypeBeforeTable(t *testing.T) {
	yaml := `
schema public:
  type status:
    type: enum
    labels: [active]
  table users:
    dependsOn: ["type public.status"]
    columns:
      id:
        type: int
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := TopologicalSort(db)
	if err != nil {
		t.Fatal(err)
	}
	typeIdx, tableIdx := -1, -1
	for i, e := range sorted {
		if e.Key == "public.status" {
			typeIdx = i
		}
		if e.Key == "public.users" {
			tableIdx = i
		}
	}
	if typeIdx == -1 || tableIdx == -1 {
		t.Fatalf("missing entities: %v", sorted)
	}
	if typeIdx >= tableIdx {
		t.Errorf("type (idx %d) must precede table (idx %d)", typeIdx, tableIdx)
	}
}

// --- LoadAndMerge ---

func TestLoadAndMerge(t *testing.T) {
	dir := t.TempDir()

	f1 := filepath.Join(dir, "a.yaml")
	f2 := filepath.Join(dir, "b.yaml")
	os.WriteFile(f1, []byte(`
tables:
  public.users:
    columns:
      id:
        type: int
`), 0o644)
	os.WriteFile(f2, []byte(`
tables:
  public.users:
    columns:
      email:
        type: text
  public.orders:
    columns:
      id:
        type: int
`), 0o644)

	db, err := LoadAndMerge([]string{f1, f2})
	if err != nil {
		t.Fatal(err)
	}
	users := db.Tables["public.users"]
	if users == nil {
		t.Fatal("expected public.users")
	}
	if len(users.Columns) != 2 {
		t.Errorf("expected 2 merged cols, got %d", len(users.Columns))
	}
	if _, ok := db.Tables["public.orders"]; !ok {
		t.Error("expected public.orders")
	}
}

func TestLoadAndMergeMissingFile(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "real.yaml")
	missing := filepath.Join(dir, "ghost.yaml")
	os.WriteFile(f1, []byte(`tables:
  public.t:
    columns:
      id:
        type: int
`), 0o644)

	db, err := LoadAndMerge([]string{f1, missing})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := db.Tables["public.t"]; !ok {
		t.Error("expected public.t despite missing file")
	}
}

// --- qualify ---

func TestQualify(t *testing.T) {
	cases := []struct{ schema, table, want string }{
		{"public", "users", "public.users"},
		{"", "users", "public.users"},
		{"myschema", "t", "myschema.t"},
		{"", "myschema.t", "myschema.t"}, // already qualified
	}
	for _, c := range cases {
		got := qualify(c.schema, c.table)
		if got != c.want {
			t.Errorf("qualify(%q,%q)=%q want %q", c.schema, c.table, got, c.want)
		}
	}
}

// --- views ---

func TestParseView(t *testing.T) {
	yml := `
schema public:
  view active_users:
    query: "select id, email from users where active = true"
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	vw, ok := db.Views["public.active_users"]
	if !ok {
		t.Fatal("expected public.active_users view")
	}
	if vw.Materialized {
		t.Error("should not be materialized")
	}
	if vw.Query == "" {
		t.Error("query should be set")
	}
}

func TestParseViewReplaceFlag(t *testing.T) {
	yml := `
schema public:
  view active_users:
    query: "select id from users"
    replace: true
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if !db.Views["public.active_users"].Replace {
		t.Error("want Replace true")
	}
}

func TestParseMaterializedView(t *testing.T) {
	yml := `
schema public:
  materialized view user_stats:
    query: "select count(*) as cnt from users"
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	vw, ok := db.Views["public.user_stats"]
	if !ok {
		t.Fatal("expected public.user_stats view")
	}
	if !vw.Materialized {
		t.Error("should be materialized")
	}
}

func TestParseViewDependsOn(t *testing.T) {
	yml := `
schema public:
  view summary:
    query: "select * from orders"
    dependsOn:
      - table public.orders
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	vw, ok := db.Views["public.summary"]
	if !ok {
		t.Fatal("expected public.summary view")
	}
	if len(vw.DependsOn) != 1 {
		t.Errorf("want 1 dependsOn, got %d", len(vw.DependsOn))
	}
}

func TestRoleParse(t *testing.T) {
	yaml := `
roles:
  app_user:
    login: true
    createdb: true
    inherit: false
    connectionLimit: 10
    inRoles: [readonly, reporting]
    comment: "application login role"
  readonly: {}
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Roles) != 2 {
		t.Fatalf("want 2 roles, got %d", len(db.Roles))
	}
	r := db.Roles["app_user"]
	if r == nil {
		t.Fatal("expected app_user role")
	}
	if !r.Login || !r.CreateDB || r.Superuser || r.CreateRole || r.Replication || r.BypassRLS {
		t.Errorf("role flags wrong: %+v", r)
	}
	if !r.NoInherit {
		t.Error("inherit: false must set NoInherit")
	}
	if r.ConnectionLimit != 10 {
		t.Errorf("want connectionLimit 10, got %d", r.ConnectionLimit)
	}
	if len(r.InRoles) != 2 || r.InRoles[0] != "readonly" || r.InRoles[1] != "reporting" {
		t.Errorf("want inRoles [readonly reporting], got %v", r.InRoles)
	}
	if r.Comment != "application login role" {
		t.Errorf("comment wrong: %q", r.Comment)
	}
	ro := db.Roles["readonly"]
	if ro == nil {
		t.Fatal("expected readonly role")
	}
	if ro.ConnectionLimit != -1 {
		t.Errorf("unset connectionLimit must be -1, got %d", ro.ConnectionLimit)
	}
	if ro.Login || ro.NoInherit {
		t.Errorf("defaults wrong: %+v", ro)
	}
}

func TestRoleMerge(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.yml")
	f2 := filepath.Join(dir, "b.yml")
	if err := os.WriteFile(f1, []byte("roles:\n  app_user:\n    login: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("roles:\n  readonly: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := LoadAndMerge([]string{f1, f2})
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Roles) != 2 {
		t.Fatalf("want 2 merged roles, got %d", len(db.Roles))
	}
	if r := db.Roles["app_user"]; r == nil || !r.Login {
		t.Errorf("app_user role lost in merge: %+v", r)
	}
}

// --- sequences ---

func TestParseSequence(t *testing.T) {
	yml := `
schema public:
  sequence order_number_seq:
    as: bigint
    increment: 5
    minValue: 10
    maxValue: 99999
    start: 100
    cache: 20
    cycle: true
    ownedBy: orders.order_number
    comment: "order number allocator"
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	sq, ok := db.Sequences["public.order_number_seq"]
	if !ok {
		t.Fatal("expected public.order_number_seq sequence")
	}
	if sq.As != "bigint" {
		t.Errorf("as: want bigint, got %q", sq.As)
	}
	if sq.Increment != "5" {
		t.Errorf("increment: want 5, got %q", sq.Increment)
	}
	if sq.MinValue != "10" {
		t.Errorf("minValue: want 10, got %q", sq.MinValue)
	}
	if sq.MaxValue != "99999" {
		t.Errorf("maxValue: want 99999, got %q", sq.MaxValue)
	}
	if sq.Start != "100" {
		t.Errorf("start: want 100, got %q", sq.Start)
	}
	if sq.Cache != "20" {
		t.Errorf("cache: want 20, got %q", sq.Cache)
	}
	if !sq.Cycle {
		t.Error("cycle: want true")
	}
	if sq.OwnedBy != "orders.order_number" {
		t.Errorf("ownedBy: want orders.order_number, got %q", sq.OwnedBy)
	}
	if sq.Comment != "order number allocator" {
		t.Errorf("comment wrong: %q", sq.Comment)
	}
}

func TestParseSequenceDefaults(t *testing.T) {
	yml := `
schema billing:
  sequence invoice_seq: {}
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	sq, ok := db.Sequences["billing.invoice_seq"]
	if !ok {
		t.Fatal("expected billing.invoice_seq sequence")
	}
	if sq.As != "" || sq.Increment != "" || sq.MinValue != "" || sq.MaxValue != "" || sq.Start != "" || sq.Cache != "" || sq.Cycle || sq.OwnedBy != "" {
		t.Errorf("all options should be unset: %+v", sq)
	}
}

func TestParseSequenceDependsOn(t *testing.T) {
	yml := `
schema public:
  sequence order_number_seq:
    dependsOn:
      - table public.orders
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	sq, ok := db.Sequences["public.order_number_seq"]
	if !ok {
		t.Fatal("expected public.order_number_seq sequence")
	}
	if len(sq.DependsOn) != 1 || sq.DependsOn[0] != "table public.orders" {
		t.Errorf("dependsOn wrong: %v", sq.DependsOn)
	}
}

func TestSequenceMerge(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.yml")
	f2 := filepath.Join(dir, "b.yml")
	if err := os.WriteFile(f1, []byte("schema public:\n  sequence s1:\n    increment: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("schema public:\n  sequence s2: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := LoadAndMerge([]string{f1, f2})
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Sequences) != 2 {
		t.Fatalf("want 2 merged sequences, got %d", len(db.Sequences))
	}
	if s := db.Sequences["public.s1"]; s == nil || s.Increment != "2" {
		t.Errorf("s1 lost in merge: %+v", s)
	}
}

func TestIdentityColumnParse(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      id:
        type: bigint
        identity: always
      seq_no:
        type: int
        identity: byDefault
      alt:
        type: int
        identity: by default
      flag:
        type: bigint
        identity: true
      plain:
        type: text
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	cols := db.Tables["public.t"].Columns
	if cols["id"].Identity != "always" {
		t.Errorf("want identity always, got %q", cols["id"].Identity)
	}
	if cols["seq_no"].Identity != "by default" {
		t.Errorf("want identity by default, got %q", cols["seq_no"].Identity)
	}
	if cols["alt"].Identity != "by default" {
		t.Errorf("want identity by default, got %q", cols["alt"].Identity)
	}
	if cols["flag"].Identity != "always" {
		t.Errorf("identity: true should mean always, got %q", cols["flag"].Identity)
	}
	if cols["plain"].Identity != "" {
		t.Errorf("plain column should have no identity, got %q", cols["plain"].Identity)
	}
}

func TestIdentityColumnParseUnknownValue(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      id:
        type: bigint
        identity: sometimes
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Tables["public.t"].Columns["id"].Identity; got != "" {
		t.Errorf("unknown identity value should be unset, got %q", got)
	}
}

func TestParseDomain(t *testing.T) {
	yml := `
schema public:
  domain email:
    type: text
    collate: en_US
    default: "'unknown@example.com'"
    notNull: true
    check: "value ~ '^[^@]+@[^@]+$'"
    constraintName: email_format
    comment: "validated email address"
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	dm, ok := db.Domains["public.email"]
	if !ok {
		t.Fatal("expected public.email domain")
	}
	if dm.Type != "text" {
		t.Errorf("type: want text, got %q", dm.Type)
	}
	if dm.Collate != "en_US" {
		t.Errorf("collate: want en_US, got %q", dm.Collate)
	}
	if dm.Default != "'unknown@example.com'" {
		t.Errorf("default wrong: %q", dm.Default)
	}
	if !dm.NotNull {
		t.Error("notNull: want true")
	}
	if dm.Check != "value ~ '^[^@]+@[^@]+$'" {
		t.Errorf("check wrong: %q", dm.Check)
	}
	if dm.ConstraintName != "email_format" {
		t.Errorf("constraintName wrong: %q", dm.ConstraintName)
	}
	if dm.Comment != "validated email address" {
		t.Errorf("comment wrong: %q", dm.Comment)
	}
}

func TestParseDomainDefaults(t *testing.T) {
	yml := `
schema billing:
  domain money_amount:
    type: numeric(12,2)
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	dm, ok := db.Domains["billing.money_amount"]
	if !ok {
		t.Fatal("expected billing.money_amount domain")
	}
	if dm.Type != "numeric(12,2)" {
		t.Errorf("type wrong: %q", dm.Type)
	}
	if dm.Collate != "" || dm.Default != "" || dm.NotNull || dm.Check != "" || dm.ConstraintName != "" || dm.Comment != "" {
		t.Errorf("all options should be unset: %+v", dm)
	}
}

func TestParseDomainDependsOn(t *testing.T) {
	yml := `
schema public:
  domain status_code:
    type: text
    dependsOn:
      - type public.status
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	dm, ok := db.Domains["public.status_code"]
	if !ok {
		t.Fatal("expected public.status_code domain")
	}
	if len(dm.DependsOn) != 1 || dm.DependsOn[0] != "type public.status" {
		t.Errorf("dependsOn wrong: %v", dm.DependsOn)
	}
}

func TestDomainMerge(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.yml")
	f2 := filepath.Join(dir, "b.yml")
	if err := os.WriteFile(f1, []byte("schema public:\n  domain d1:\n    type: text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("schema public:\n  domain d2:\n    type: int\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := LoadAndMerge([]string{f1, f2})
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Domains) != 2 {
		t.Fatalf("want 2 merged domains, got %d", len(db.Domains))
	}
	if d := db.Domains["public.d1"]; d == nil || d.Type != "text" {
		t.Errorf("d1 lost in merge: %+v", d)
	}
}
