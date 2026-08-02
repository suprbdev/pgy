package schema

import (
	"os"
	"path/filepath"
	"strings"
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
	if tbl.RowLevelSecurity == nil || !*tbl.RowLevelSecurity {
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

// rowLevelSecurity is tri-state: absent (nil/unmanaged), true, false.
func TestRLSTriStateParse(t *testing.T) {
	yaml := `
tables:
  public.a:
    columns:
      id:
        type: bigint
    rowLevelSecurity: false
  public.b:
    columns:
      id:
        type: bigint
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if rls := db.Tables["public.a"].RowLevelSecurity; rls == nil || *rls {
		t.Errorf("want RowLevelSecurity false, got %v", rls)
	}
	if rls := db.Tables["public.b"].RowLevelSecurity; rls != nil {
		t.Errorf("want RowLevelSecurity nil (unmanaged), got %v", *rls)
	}
}

func TestRLSFalseParsesInSchemaBlockAndListFormats(t *testing.T) {
	block := `
schema app:
  table orders:
    columns:
      id: bigint
    rowLevelSecurity: false
`
	db, err := parseFlexibleDatabase([]byte(block))
	if err != nil {
		t.Fatal(err)
	}
	if rls := db.Tables["app.orders"].RowLevelSecurity; rls == nil || *rls {
		t.Errorf("schema-block format: want false, got %v", rls)
	}

	list := `
tables:
  - name: orders
    columns:
      id:
        type: bigint
    rowLevelSecurity: false
`
	db, err = parseFlexibleDatabase([]byte(list))
	if err != nil {
		t.Fatal(err)
	}
	if rls := db.Tables["public.orders"].RowLevelSecurity; rls == nil || *rls {
		t.Errorf("list format: want false, got %v", rls)
	}
}

// A later file setting rowLevelSecurity: false must win over an earlier true,
// and a later file omitting the key must not clear an earlier setting.
func TestRLSMergeAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	on := write("a.yml", `
tables:
  public.t:
    columns:
      id:
        type: bigint
    rowLevelSecurity: true
`)
	off := write("b.yml", `
tables:
  public.t:
    rowLevelSecurity: false
`)
	silent := write("c.yml", `
tables:
  public.t:
    columns:
      name:
        type: text
`)

	db, err := LoadAndMerge([]string{on, off})
	if err != nil {
		t.Fatal(err)
	}
	if rls := db.Tables["public.t"].RowLevelSecurity; rls == nil || *rls {
		t.Errorf("later false must win, got %v", rls)
	}

	db, err = LoadAndMerge([]string{on, silent})
	if err != nil {
		t.Fatal(err)
	}
	if rls := db.Tables["public.t"].RowLevelSecurity; rls == nil || !*rls {
		t.Errorf("file omitting the key must not clear earlier true, got %v", rls)
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

func TestIndexUsingAndWhere(t *testing.T) {
	yaml := `
tables:
  public.places:
    columns:
      geom:
        type: geometry(Point, 4326)
    indexes:
      idx_geom:
        columns: [geom]
        using: gist
        where: geom is not null
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	ixs := db.Tables["public.places"].Indexes
	if len(ixs) != 1 {
		t.Fatalf("want 1 index, got %d", len(ixs))
	}
	if ixs[0].Using != "gist" {
		t.Errorf("using: %q", ixs[0].Using)
	}
	if ixs[0].Where != "geom is not null" {
		t.Errorf("where: %q", ixs[0].Where)
	}
}

func TestIndexOpclassParse(t *testing.T) {
	yaml := `
tables:
  public.users:
    columns:
      name:
        type: text
      tags:
        type: jsonb
    indexes:
      idx_name_trgm:
        columns: [name]
        using: gin
        opclass: gin_trgm_ops
      idx_mixed:
        columns: ["lower(name)", tags]
        using: gin
        opclasses:
          lower(name): gin_trgm_ops
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	ixs := db.Tables["public.users"].Indexes
	if len(ixs) != 2 {
		t.Fatalf("want 2 indexes, got %d", len(ixs))
	}
	// sorted by name: idx_mixed, idx_name_trgm
	if ixs[0].Name != "idx_mixed" || ixs[1].Name != "idx_name_trgm" {
		t.Fatalf("unexpected order: %s, %s", ixs[0].Name, ixs[1].Name)
	}
	if ixs[1].Opclass != "gin_trgm_ops" {
		t.Errorf("opclass: %q", ixs[1].Opclass)
	}
	if ixs[0].Columns[0] != "lower(name)" {
		t.Errorf("expression column: %q", ixs[0].Columns[0])
	}
	if ixs[0].Opclasses["lower(name)"] != "gin_trgm_ops" {
		t.Errorf("per-column opclass: %v", ixs[0].Opclasses)
	}
}

// --- column grants ---

func TestColumnGrantsParse(t *testing.T) {
	yaml := `
tables:
  public.users:
    columns:
      email:
        type: text
        grants:
          reporting: [select, UPDATE]
      id:
        type: int
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	cols := db.Tables["public.users"].Columns
	g := cols["email"].Grants
	if g == nil {
		t.Fatal("expected grants on email column")
	}
	privs := g["reporting"]
	if len(privs) != 2 || privs[0] != "select" || privs[1] != "update" {
		t.Errorf("privs: %v (want lowercased [select update])", privs)
	}
	if cols["id"].Grants != nil {
		t.Error("id column has no grants block, Grants must stay nil (unmanaged)")
	}
}

// An empty privilege list must survive parsing as an empty (non-nil) entry:
// `role: []` means "revoke everything for this role", which is distinct from
// omitting the grants block entirely (unmanaged). Dropping it made removing a
// grant from the YAML a silent no-op.
func TestGrantsEmptyListPreserved(t *testing.T) {
	yaml := `
tables:
  public.person:
    columns:
      id:
        type: bigint
        grants:
          anon: []
      email:
        type: text
    grants:
      anon: []
      other: [select]
  public.unmanaged:
    columns:
      id:
        type: bigint
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	tbl := db.Tables["public.person"]
	if tbl.Grants == nil {
		t.Fatal("present grants block must not parse to nil (unmanaged)")
	}
	privs, ok := tbl.Grants["anon"]
	if !ok {
		t.Fatal("role with an empty list must be kept, not dropped")
	}
	if len(privs) != 0 {
		t.Errorf("want empty privilege list, got %v", privs)
	}
	if got := tbl.Grants["other"]; len(got) != 1 || got[0] != "select" {
		t.Errorf("sibling role wrong: %v", got)
	}
	if cg := tbl.Columns["id"].Grants; cg == nil {
		t.Error("column grants block with an empty list must not be nil")
	} else if p, ok := cg["anon"]; !ok || len(p) != 0 {
		t.Errorf("column role with empty list must be kept empty, got %v ok=%v", p, ok)
	}
	if db.Tables["public.unmanaged"].Grants != nil {
		t.Error("absent grants block must stay nil (unmanaged)")
	}
}

// --- trigger when guard ---

func TestTriggerWhenParse(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      id:
        type: int
    triggers:
      trg_supersede:
        timing: after
        events: [insert]
        level: row
        procedure: public.supersede()
        when: old.created_at <= new.created_at
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	trs := db.Tables["public.t"].Triggers
	if len(trs) != 1 {
		t.Fatalf("want 1 trigger, got %d", len(trs))
	}
	if trs[0].When != "old.created_at <= new.created_at" {
		t.Errorf("when: %q", trs[0].When)
	}
}

// --- topological sort cycle detection ---

func TestTopologicalSortCycleError(t *testing.T) {
	db := &Database{
		Tables: map[string]*Table{
			"public.a": {Name: "a", DependsOn: []string{"table public.b"}},
			"public.b": {Name: "b", DependsOn: []string{"table public.a"}},
		},
	}
	sorted, err := TopologicalSort(db)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "public.a") || !strings.Contains(err.Error(), "public.b") {
		t.Errorf("cycle error should name members: %v", err)
	}
	if len(sorted) != 2 {
		t.Errorf("cycle members must still be returned; got %d entities", len(sorted))
	}
}

func TestTopologicalSortFunctionAfterTable(t *testing.T) {
	db := &Database{
		Tables: map[string]*Table{
			"public.t": {Name: "t"},
		},
		Functions: map[string]*Function{
			"public.fn": {Name: "fn", Schema: "public", DependsOn: []string{"table public.t"}},
		},
	}
	sorted, err := TopologicalSort(db)
	if err != nil {
		t.Fatal(err)
	}
	ti, fi := -1, -1
	for i, e := range sorted {
		if e.Key == "public.t" { ti = i }
		if e.Key == "public.fn" { fi = i }
	}
	if ti < 0 || fi < 0 || fi < ti {
		t.Errorf("function dependsOn table must sort after it; order: %v", sorted)
	}
}

func TestTopologicalSortDeterministic(t *testing.T) {
	db := &Database{
		Tables: map[string]*Table{
			"app.a": {Name: "a"}, "app.b": {Name: "b"}, "app.c": {Name: "c"},
			"app.d": {Name: "d", DependsOn: []string{"table app.a"}},
		},
		Functions: map[string]*Function{
			"public.f1": {Name: "f1"}, "public.f2": {Name: "f2"},
			"public.f3": {Name: "f3", DependsOn: []string{"table app.b"}},
		},
		Types: map[string]*TypeDef{
			"public.t1": {Name: "t1", Kind: "enum"}, "public.t2": {Name: "t2", Kind: "enum"},
		},
	}
	first, err := TopologicalSort(db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		again, err := TopologicalSort(db)
		if err != nil {
			t.Fatal(err)
		}
		for j := range first {
			if again[j].Key != first[j].Key {
				t.Fatalf("run %d: order differs at %d: %s vs %s", i, j, again[j].Key, first[j].Key)
			}
		}
	}
}

func TestTopologicalSortIgnoresTriggerFunctionEdge(t *testing.T) {
	// table -> its trigger function -> the table it reads would be a cycle,
	// but edges to trigger-returning functions are dropped (triggers are
	// emitted after every create, so nothing references them at CREATE time)
	db := &Database{
		Tables: map[string]*Table{
			"app.entry": {Name: "entry", DependsOn: []string{"function app.check_entry"}},
		},
		Functions: map[string]*Function{
			"app.check_entry": {Name: "check_entry", Returns: "trigger", DependsOn: []string{"table app.entry"}},
		},
	}
	sorted, err := TopologicalSort(db)
	if err != nil {
		t.Fatalf("trigger-function edge must not create a cycle: %v", err)
	}
	ti, fi := -1, -1
	for i, e := range sorted {
		if e.Key == "app.entry" { ti = i }
		if e.Key == "app.check_entry" { fi = i }
	}
	if ti < 0 || fi < 0 || fi < ti {
		t.Errorf("trigger function's own dependsOn on the table must still hold; order: %v", sorted)
	}
}

func TestTopologicalSortCycleErrorShowsPath(t *testing.T) {
	db := &Database{
		Tables: map[string]*Table{
			"public.a": {Name: "a", DependsOn: []string{"table public.b"}},
			"public.b": {Name: "b", DependsOn: []string{"table public.a"}},
		},
	}
	_, err := TopologicalSort(db)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "public.a -> public.b -> public.a") {
		t.Errorf("cycle error should show the concrete path: %v", err)
	}
}

func TestTableObjectsSortedByName(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      a:
        type: uuid
      b:
        type: uuid
    foreignKeys:
      z_fkey:
        columns: [b]
        references:
          table: public.o
          columns: [id]
      a_fkey:
        columns: [a]
        references:
          table: public.o
          columns: [id]
    indexes:
      z_idx:
        columns: [b]
      a_idx:
        columns: [a]
    triggers:
      z_trg:
        timing: before
        events: [update]
        level: row
        procedure: public.fn()
      a_trg:
        timing: before
        events: [insert]
        level: row
        procedure: public.fn()
    constraints:
      z_chk:
        type: check
        expression: "a is not null"
      a_chk:
        type: check
        expression: "b is not null"
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	tb := db.Tables["public.t"]
	if tb.ForeignKeys[0].Name != "a_fkey" || tb.ForeignKeys[1].Name != "z_fkey" {
		t.Errorf("foreign keys not sorted by name: %s, %s", tb.ForeignKeys[0].Name, tb.ForeignKeys[1].Name)
	}
	if tb.Indexes[0].Name != "a_idx" || tb.Indexes[1].Name != "z_idx" {
		t.Errorf("indexes not sorted by name: %s, %s", tb.Indexes[0].Name, tb.Indexes[1].Name)
	}
	if tb.Triggers[0].Name != "a_trg" || tb.Triggers[1].Name != "z_trg" {
		t.Errorf("triggers not sorted by name: %s, %s", tb.Triggers[0].Name, tb.Triggers[1].Name)
	}
	if tb.Constraints[0].Name != "a_chk" || tb.Constraints[1].Name != "z_chk" {
		t.Errorf("constraints not sorted by name: %s, %s", tb.Constraints[0].Name, tb.Constraints[1].Name)
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

func TestExtensionsStringShorthand(t *testing.T) {
	yaml := `
extensions: [pg_trgm, pgcrypto]
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Extensions) != 2 {
		t.Fatalf("want 2 extensions, got %d", len(db.Extensions))
	}
	for i, want := range []string{"pg_trgm", "pgcrypto"} {
		if db.Extensions[i].Name != want {
			t.Errorf("extension %d: %q, want %q", i, db.Extensions[i].Name, want)
		}
		if !db.Extensions[i].IfNotExists {
			t.Errorf("shorthand %s should imply ifNotExists", want)
		}
	}
}

func TestExtensionsMixedShorthandAndMap(t *testing.T) {
	yaml := `
extensions:
  - pg_trgm
  - name: postgis
    ifNotExists: false
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Extensions) != 2 {
		t.Fatalf("want 2 extensions, got %d", len(db.Extensions))
	}
	if db.Extensions[0].Name != "pg_trgm" || !db.Extensions[0].IfNotExists {
		t.Errorf("shorthand entry: %+v", db.Extensions[0])
	}
	if db.Extensions[1].Name != "postgis" || db.Extensions[1].IfNotExists {
		t.Errorf("map entry: %+v", db.Extensions[1])
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

func TestFunctionQualifiedArgTypeParse(t *testing.T) {
	yaml := `
schema public:
  function fn(t public.my_type):
    returns: text
    language: sql
    body: select 'ok'
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
	if fn.Name != "fn" {
		t.Errorf("name: %s", fn.Name)
	}
	if fn.ArgsSig != "(t public.my_type)" {
		t.Errorf("argssig: %s", fn.ArgsSig)
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

func TestFunctionLeakproof(t *testing.T) {
	yaml := `
schema public:
  function add(a int, b int):
    returns: int
    language: sql
    leakproof: true
    body: "select a + b"
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range db.Functions {
		if !f.Leakproof {
			t.Error("expected Leakproof=true")
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

func TestTopologicalSortFunctionDepWithParens(t *testing.T) {
	// The table name sorts before the function name alphabetically, so
	// without the resolved dependency the table would be emitted first.
	yaml := `
schema public:
  function set_updated_at():
    returns: trigger
    language: plpgsql
    body: |
      BEGIN RETURN NEW; END;
  table setting:
    dependsOn: ["function public.set_updated_at()"]
    columns:
      key:
        type: text
        primaryKey: true
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := TopologicalSort(db)
	if err != nil {
		t.Fatal(err)
	}
	fnIdx, tableIdx := -1, -1
	for i, e := range sorted {
		if e.Kind == "function" && e.Key == "public.set_updated_at" {
			fnIdx = i
		}
		if e.Kind == "table" && e.Key == "public.setting" {
			tableIdx = i
		}
	}
	if fnIdx == -1 || tableIdx == -1 {
		t.Fatalf("missing entities: %v", sorted)
	}
	if fnIdx >= tableIdx {
		t.Errorf("function (idx %d) must precede table (idx %d)", fnIdx, tableIdx)
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

func TestLoadAndMergeLinkFileAddsForeignKeys(t *testing.T) {
	dir := t.TempDir()

	f1 := filepath.Join(dir, "post.yaml")
	f2 := filepath.Join(dir, "post_user.yaml")
	os.WriteFile(f1, []byte(`
schema public:
  table post:
    columns:
      id:
        type: uuid
        primaryKey: true
      title:
        type: text
    indexes:
      post_title_idx:
        columns: [title]
    dependsOn:
      - extension pgcrypto
`), 0o644)
	os.WriteFile(f2, []byte(`
schema public:
  table post:
    columns:
      author_id:
        type: uuid
    foreignKeys:
      post_author_fkey:
        columns: [author_id]
        references:
          table: public.user
          columns: [id]
        onDelete: cascade
    indexes:
      post_author_idx:
        columns: [author_id]
    dependsOn:
      - table public.user
`), 0o644)

	db, err := LoadAndMerge([]string{f1, f2})
	if err != nil {
		t.Fatal(err)
	}
	post := db.Tables["public.post"]
	if post == nil {
		t.Fatal("expected public.post")
	}
	if len(post.Columns) != 3 {
		t.Errorf("expected 3 merged cols, got %d", len(post.Columns))
	}
	if len(post.ForeignKeys) != 1 || post.ForeignKeys[0].Name != "post_author_fkey" {
		t.Fatalf("expected merged FK post_author_fkey, got %+v", post.ForeignKeys)
	}
	if post.ForeignKeys[0].RefTable != "public.user" {
		t.Errorf("FK refTable = %q", post.ForeignKeys[0].RefTable)
	}
	if len(post.Indexes) != 2 {
		t.Errorf("expected 2 merged indexes, got %d", len(post.Indexes))
	}
	want := []string{"extension pgcrypto", "table public.user"}
	if len(post.DependsOn) != 2 || post.DependsOn[0] != want[0] || post.DependsOn[1] != want[1] {
		t.Errorf("dependsOn = %v, want %v", post.DependsOn, want)
	}
}

func TestLoadAndMergeNamedEntryReplacedByLaterFile(t *testing.T) {
	dir := t.TempDir()

	f1 := filepath.Join(dir, "a.yaml")
	f2 := filepath.Join(dir, "b.yaml")
	os.WriteFile(f1, []byte(`
schema public:
  table item:
    columns:
      id:
        type: uuid
        primaryKey: true
      qty:
        type: int
    constraints:
      item_qty_check:
        type: check
        expression: "qty > 0"
`), 0o644)
	os.WriteFile(f2, []byte(`
schema public:
  table item:
    constraints:
      item_qty_check:
        type: check
        expression: "qty >= 0"
`), 0o644)

	db, err := LoadAndMerge([]string{f1, f2})
	if err != nil {
		t.Fatal(err)
	}
	item := db.Tables["public.item"]
	if item == nil {
		t.Fatal("expected public.item")
	}
	if len(item.Constraints) != 1 {
		t.Fatalf("expected 1 constraint after same-name merge, got %d", len(item.Constraints))
	}
	if item.Constraints[0].Expression != "qty >= 0" {
		t.Errorf("expected later file to win, got %q", item.Constraints[0].Expression)
	}
}

func TestLoadAndMergeExtensionDedupe(t *testing.T) {
	dir := t.TempDir()

	f1 := filepath.Join(dir, "a.yaml")
	f2 := filepath.Join(dir, "b.yaml")
	os.WriteFile(f1, []byte(`
extensions:
  - name: citext
    ifNotExists: true
`), 0o644)
	os.WriteFile(f2, []byte(`
extensions:
  - name: citext
    ifNotExists: true
  - name: pgcrypto
    ifNotExists: true
`), 0o644)

	db, err := LoadAndMerge([]string{f1, f2})
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Extensions) != 2 {
		t.Fatalf("expected 2 deduped extensions, got %d", len(db.Extensions))
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

func TestParseViewGrants(t *testing.T) {
	yml := `
schema public:
  view active_users:
    query: "select id from users where active"
    grants:
      reporting: [select]
  materialized view user_stats:
    query: "select count(*) from users"
    grants:
      reporting: [SELECT]
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	g := db.Views["public.active_users"].Grants
	if g == nil || len(g["reporting"]) != 1 || g["reporting"][0] != "select" {
		t.Errorf("view grants: %v", g)
	}
	mg := db.Views["public.user_stats"].Grants
	if mg == nil || len(mg["reporting"]) != 1 || mg["reporting"][0] != "select" {
		t.Errorf("matview grants (lowercased): %v", mg)
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

func TestParseProcedure(t *testing.T) {
	yml := `
schema public:
  procedure archive_user(user_id bigint):
    language: plpgsql
    security: definer
    set:
      search_path: public
    body: |
      begin
        update public.users set archived = true where id = user_id;
      end;
    grants:
      batch_role: [execute]
    revokePublic: true
    comment: "archives a user"
    dependsOn:
      - table public.users
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	pr, ok := db.Procedures["public.archive_user"]
	if !ok {
		t.Fatal("expected public.archive_user procedure")
	}
	if pr.ArgsSig != "(user_id bigint)" {
		t.Errorf("argsSig wrong: %q", pr.ArgsSig)
	}
	if pr.Language != "plpgsql" {
		t.Errorf("language wrong: %q", pr.Language)
	}
	if pr.Security != "definer" {
		t.Errorf("security wrong: %q", pr.Security)
	}
	if pr.Set["search_path"] != "public" {
		t.Errorf("set wrong: %v", pr.Set)
	}
	if !strings.Contains(pr.Body, "update public.users") {
		t.Errorf("body wrong: %q", pr.Body)
	}
	if len(pr.Grants["batch_role"]) != 1 || pr.Grants["batch_role"][0] != "execute" {
		t.Errorf("grants wrong: %v", pr.Grants)
	}
	if !pr.RevokePublic {
		t.Error("revokePublic: want true")
	}
	if pr.Comment != "archives a user" {
		t.Errorf("comment wrong: %q", pr.Comment)
	}
	if len(pr.DependsOn) != 1 || pr.DependsOn[0] != "table public.users" {
		t.Errorf("dependsOn wrong: %v", pr.DependsOn)
	}
}

func TestParseProcedureNoArgs(t *testing.T) {
	yml := `
schema public:
  procedure cleanup:
    language: sql
    body: "delete from public.audit;"
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	pr, ok := db.Procedures["public.cleanup"]
	if !ok {
		t.Fatal("expected public.cleanup procedure")
	}
	if pr.ArgsSig != "()" {
		t.Errorf("argsSig: want (), got %q", pr.ArgsSig)
	}
	if pr.Language != "sql" {
		t.Errorf("language wrong: %q", pr.Language)
	}
}

func TestProcedureMerge(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.yml")
	f2 := filepath.Join(dir, "b.yml")
	if err := os.WriteFile(f1, []byte("schema public:\n  procedure p1():\n    language: sql\n    body: select 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("schema public:\n  procedure p2():\n    language: sql\n    body: select 2;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := LoadAndMerge([]string{f1, f2})
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Procedures) != 2 {
		t.Fatalf("want 2 merged procedures, got %d", len(db.Procedures))
	}
	if p := db.Procedures["public.p1"]; p == nil || p.Language != "sql" {
		t.Errorf("p1 lost in merge: %+v", p)
	}
}

func TestPartitionByParse(t *testing.T) {
	yml := `
schema public:
  table measurement:
    columns:
      logdate:
        type: date
    partitionBy:
      type: range
      columns: [logdate]
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	tbl := db.Tables["public.measurement"]
	if tbl == nil || tbl.PartitionBy == nil {
		t.Fatalf("partitionBy not parsed: %+v", tbl)
	}
	if tbl.PartitionBy.Type != "range" {
		t.Errorf("want range, got %q", tbl.PartitionBy.Type)
	}
	if len(tbl.PartitionBy.Columns) != 1 || tbl.PartitionBy.Columns[0] != "logdate" {
		t.Errorf("columns wrong: %+v", tbl.PartitionBy.Columns)
	}
}

func TestPartitionByShorthandParse(t *testing.T) {
	yml := `
schema public:
  table events:
    columns:
      tenant:
        type: text
    partitionBy:
      list: [tenant]
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	pb := db.Tables["public.events"].PartitionBy
	if pb == nil || pb.Type != "list" || len(pb.Columns) != 1 || pb.Columns[0] != "tenant" {
		t.Errorf("shorthand partitionBy wrong: %+v", pb)
	}
}

func TestPartitionOfRangeParse(t *testing.T) {
	yml := `
schema public:
  table measurement_y2024:
    partitionOf: measurement
    forValues:
      from: [2024-01-01]
      to: ["2025-01-01"]
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	tbl := db.Tables["public.measurement_y2024"]
	if tbl == nil || tbl.PartitionOf != "public.measurement" {
		t.Fatalf("partitionOf wrong: %+v", tbl)
	}
	ps := tbl.Partition
	if ps == nil {
		t.Fatal("partition spec missing")
	}
	if len(ps.From) != 1 || ps.From[0] != "2024-01-01" {
		t.Errorf("from wrong (bare YAML date should coerce to string): %+v", ps.From)
	}
	if len(ps.To) != 1 || ps.To[0] != "2025-01-01" {
		t.Errorf("to wrong: %+v", ps.To)
	}
}

func TestPartitionOfListParse(t *testing.T) {
	yml := `
schema public:
  table events_eu:
    partitionOf: events
    forValues:
      in: [de, fr]
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	ps := db.Tables["public.events_eu"].Partition
	if ps == nil || len(ps.In) != 2 || ps.In[0] != "de" || ps.In[1] != "fr" {
		t.Errorf("list bound wrong: %+v", ps)
	}
}

func TestPartitionOfHashParse(t *testing.T) {
	yml := `
schema public:
  table users_p0:
    partitionOf: users
    forValues:
      modulus: 4
      remainder: 0
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	ps := db.Tables["public.users_p0"].Partition
	if ps == nil || ps.Modulus != 4 || ps.Remainder != 0 {
		t.Errorf("hash bound wrong: %+v", ps)
	}
}

func TestPartitionOfDefaultParse(t *testing.T) {
	yml := `
schema public:
  table measurement_default:
    partitionOf: measurement
    default: true
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	tbl := db.Tables["public.measurement_default"]
	if tbl.PartitionOf != "public.measurement" {
		t.Errorf("partitionOf wrong: %q", tbl.PartitionOf)
	}
	if tbl.Partition == nil || !tbl.Partition.Default {
		t.Errorf("default partition not parsed: %+v", tbl.Partition)
	}
}

func TestPartitionOfQualifiedParent(t *testing.T) {
	yml := `
schema app:
  table logs_2024:
    partitionOf: metrics.logs
    forValues:
      from: [1]
      to: [100]
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Tables["app.logs_2024"].PartitionOf; got != "metrics.logs" {
		t.Errorf("qualified parent should not be re-qualified, got %q", got)
	}
}

func TestPartitionTopologicalOrder(t *testing.T) {
	yml := `
schema public:
  table measurement_y2024:
    partitionOf: measurement
    forValues:
      from: ["2024-01-01"]
      to: ["2025-01-01"]
  table measurement:
    columns:
      logdate:
        type: date
    partitionBy:
      range: [logdate]
`
	db, err := parseFlexibleDatabase([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := TopologicalSort(db)
	if err != nil {
		t.Fatal(err)
	}
	parentIdx, childIdx := -1, -1
	for i, e := range sorted {
		switch e.Key {
		case "public.measurement":
			parentIdx = i
		case "public.measurement_y2024":
			childIdx = i
		}
	}
	if parentIdx < 0 || childIdx < 0 {
		t.Fatalf("entities missing from sort: parent=%d child=%d", parentIdx, childIdx)
	}
	if parentIdx > childIdx {
		t.Errorf("parent (%d) must sort before partition child (%d)", parentIdx, childIdx)
	}
}

func TestColumnUsingParse(t *testing.T) {
	yaml := `
tables:
  public.t:
    columns:
      amount:
        type: numeric(10,2)
        using: amount::numeric(10,2)
      plain:
        type: text
`
	db, err := parseFlexibleDatabase([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	cols := db.Tables["public.t"].Columns
	if cols["amount"].Using != "amount::numeric(10,2)" {
		t.Errorf("want using expression, got %q", cols["amount"].Using)
	}
	if cols["plain"].Using != "" {
		t.Errorf("plain column should have no using, got %q", cols["plain"].Using)
	}
}
