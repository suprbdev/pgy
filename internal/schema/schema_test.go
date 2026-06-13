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
