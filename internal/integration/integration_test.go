// Integration tests against a real PostgreSQL instance.
// Run via: PGY_TEST_DSN=postgres://pgy:pgy@localhost:5433/pgytest go test ./internal/integration/...
// Or: make test-integration (starts/stops Docker compose automatically)
package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func dsn(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("PGY_TEST_DSN")
	if dsn == "" {
		t.Skip("PGY_TEST_DSN not set; skipping integration tests")
	}
	return dsn
}

func connect(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// freshSchema creates a unique schema for each test and drops it on cleanup.
func freshSchema(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	name := "pgytest_" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "_")
	// trim to 63 chars (pg limit)
	if len(name) > 63 {
		name = name[:63]
	}
	ctx := context.Background()
	if _, err := pool.Exec(ctx, fmt.Sprintf("drop schema if exists %q cascade", name)); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("create schema %q", name)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), fmt.Sprintf("drop schema if exists %q cascade", name)) //nolint
	})
	return name
}

// applyPlan executes all SQL statements from a PlanDiff against the pool.
func applyPlan(t *testing.T, pool *pgxpool.Pool, p *diff.PlanDiff) {
	t.Helper()
	ctx := context.Background()
	all := append(append(p.Creates, p.Alters...), p.Drops...)
	for _, stmt := range all {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("apply SQL %q: %v", stmt, err)
		}
	}
}

// --- Extensions ---

func TestIntegrationExtensionCreate(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Extensions: []*schema.Extension{
			{Name: "pgcrypto", IfNotExists: true},
		},
	}
	p := diff.Plan(live, desired, false)

	if live.Extensions["pgcrypto"] {
		// already installed, plan should be empty
		if len(p.Creates) != 0 {
			t.Errorf("pgcrypto already live, expected no creates; got %v", p.Creates)
		}
		return
	}

	found := false
	for _, s := range p.Creates {
		if strings.Contains(s, "pgcrypto") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pgcrypto in creates; got %v", p.Creates)
	}

	applyPlan(t, pool, p)

	// verify extension now exists
	live2, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !live2.Extensions["pgcrypto"] {
		t.Error("pgcrypto not found after install")
	}
}

// --- Tables ---

func TestIntegrationCreateTable(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".users": {
				Name: "users",
				Columns: map[string]*schema.Column{
					"id":    {Type: "bigint", Nullable: false},
					"email": {Type: "text", Nullable: false},
				},
				PrimaryKey: []string{"id"},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	// verify table exists
	var count int
	err = pool.QueryRow(ctx,
		"select count(*) from information_schema.tables where table_schema=$1 and table_name='users'",
		sch,
	).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected users table to exist, got count=%d", count)
	}
}

func TestIntegrationAddColumn(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	// create table first
	_, err := pool.Exec(ctx, fmt.Sprintf(`create table %q.users (id bigint not null)`, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".users": {
				Name: "users",
				Columns: map[string]*schema.Column{
					"id":    {Type: "bigint", Nullable: false},
					"email": {Type: "text", Nullable: true},
				},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)

	if len(p.Alters) == 0 {
		t.Fatal("expected alter to add email column")
	}

	applyPlan(t, pool, p)

	// verify email column exists
	var colCount int
	err = pool.QueryRow(ctx,
		"select count(*) from information_schema.columns where table_schema=$1 and table_name='users' and column_name='email'",
		sch,
	).Scan(&colCount)
	if err != nil {
		t.Fatal(err)
	}
	if colCount != 1 {
		t.Errorf("expected email column, got count=%d", colCount)
	}
}

// --- Indexes ---

func TestIntegrationUniqueIndex(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".users": {
				Name: "users",
				Columns: map[string]*schema.Column{
					"id":    {Type: "bigint"},
					"email": {Type: "text"},
				},
				Indexes: []*schema.Index{
					{Name: "idx_users_email", Columns: []string{"email"}, Unique: true},
				},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var idxCount int
	err = pool.QueryRow(ctx,
		"select count(*) from pg_indexes where schemaname=$1 and tablename='users' and indexname='idx_users_email'",
		sch,
	).Scan(&idxCount)
	if err != nil {
		t.Fatal(err)
	}
	if idxCount != 1 {
		t.Errorf("expected idx_users_email, got count=%d", idxCount)
	}
}

// --- Foreign Keys ---

func TestIntegrationForeignKey(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".users": {
				Name:       "users",
				Columns:    map[string]*schema.Column{"id": {Type: "bigint"}},
				PrimaryKey: []string{"id"},
			},
			sch + ".orders": {
				Name: "orders",
				Columns: map[string]*schema.Column{
					"id":      {Type: "bigint"},
					"user_id": {Type: "bigint"},
				},
				PrimaryKey: []string{"id"},
				ForeignKeys: []*schema.ForeignKey{
					{
						Name:       "fk_orders_user",
						Columns:    []string{"user_id"},
						RefTable:   sch + ".users",
						RefColumns: []string{"id"},
						OnDelete:   "cascade",
					},
				},
				DependsOn: []string{"table " + sch + ".users"},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var fkCount int
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.referential_constraints
		where constraint_schema=$1 and constraint_name='fk_orders_user'
	`, sch).Scan(&fkCount)
	if err != nil {
		t.Fatal(err)
	}
	if fkCount != 1 {
		t.Errorf("expected fk_orders_user constraint, got count=%d", fkCount)
	}
}

// --- Check Constraints ---

func TestIntegrationCheckConstraint(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".products": {
				Name: "products",
				Columns: map[string]*schema.Column{
					"id":    {Type: "bigint"},
					"price": {Type: "numeric"},
				},
				Constraints: []*schema.Constraint{
					{Name: "chk_price_positive", Type: "check", Expression: "price > 0"},
				},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var ctCount int
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.check_constraints
		where constraint_schema=$1 and constraint_name='chk_price_positive'
	`, sch).Scan(&ctCount)
	if err != nil {
		t.Fatal(err)
	}
	if ctCount != 1 {
		t.Errorf("expected chk_price_positive, got count=%d", ctCount)
	}
}

// --- Enum Types ---

func TestIntegrationEnumType(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	// Ensure schema exists in live before using custom schema
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	live.Schemas[sch] = true // freshSchema already created it

	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Types: map[string]*schema.TypeDef{
			sch + ".status": {Name: "status", Schema: sch, Kind: "enum", Labels: []string{"active", "inactive", "pending"}},
		},
	}

	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var typCount int
	err = pool.QueryRow(ctx, `
		select count(*) from pg_type t
		join pg_namespace n on n.oid = t.typnamespace
		where n.nspname=$1 and t.typname='status' and t.typtype='e'
	`, sch).Scan(&typCount)
	if err != nil {
		t.Fatal(err)
	}
	if typCount != 1 {
		t.Errorf("expected status enum type, got count=%d", typCount)
	}
}

// --- Views ---

func TestIntegrationView(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	// create base table first
	_, err := pool.Exec(ctx, fmt.Sprintf(`create table %q.items (id bigint, active boolean)`, sch))
	if err != nil {
		t.Fatal(err)
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	live.Schemas[sch] = true

	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Views: map[string]*schema.View{
			sch + ".active_items": {
				Schema:       sch,
				Name:         "active_items",
				Query:        fmt.Sprintf(`select id from %q.items where active = true`, sch),
				Materialized: false,
			},
		},
	}

	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var vCount int
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.views
		where table_schema=$1 and table_name='active_items'
	`, sch).Scan(&vCount)
	if err != nil {
		t.Fatal(err)
	}
	if vCount != 1 {
		t.Errorf("expected active_items view, got count=%d", vCount)
	}
}

func TestIntegrationMaterializedView(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`create table %q.sales (id bigint, amount numeric)`, sch))
	if err != nil {
		t.Fatal(err)
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	live.Schemas[sch] = true

	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Views: map[string]*schema.View{
			sch + ".sales_summary": {
				Schema:       sch,
				Name:         "sales_summary",
				Query:        fmt.Sprintf(`select count(*) as cnt from %q.sales`, sch),
				Materialized: true,
			},
		},
	}

	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var mvCount int
	err = pool.QueryRow(ctx, `
		select count(*) from pg_matviews where schemaname=$1 and matviewname='sales_summary'
	`, sch).Scan(&mvCount)
	if err != nil {
		t.Fatal(err)
	}
	if mvCount != 1 {
		t.Errorf("expected sales_summary matview, got count=%d", mvCount)
	}
}

// --- Functions ---

func TestIntegrationFunction(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	live.Schemas[sch] = true

	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			sch + ".add_nums": {
				Name:       "add_nums",
				Schema:     sch,
				ArgsSig:    "(a integer, b integer)",
				Returns:    "integer",
				Language:   "sql",
				Volatility: "immutable",
				Body:       "select a + b",
			},
		},
	}

	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var fnCount int
	err = pool.QueryRow(ctx, `
		select count(*) from pg_proc p
		join pg_namespace n on n.oid = p.pronamespace
		where n.nspname=$1 and p.proname='add_nums'
	`, sch).Scan(&fnCount)
	if err != nil {
		t.Fatal(err)
	}
	if fnCount != 1 {
		t.Errorf("expected add_nums function, got count=%d", fnCount)
	}
}

// --- Idempotency: running Plan twice produces no new SQL ---

func TestIntegrationIdempotent(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".users": {
				Name: "users",
				Columns: map[string]*schema.Column{
					"id":    {Type: "bigint", Nullable: false},
					"email": {Type: "text", Nullable: false},
				},
				PrimaryKey: []string{"id"},
				Indexes: []*schema.Index{
					{Name: "idx_idem_email", Columns: []string{"email"}, Unique: true},
				},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p1 := diff.Plan(live, desired, false)
	applyPlan(t, pool, p1)

	// second plan — everything should already exist
	live2, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p2 := diff.Plan(live2, desired, false)
	if len(p2.Creates) != 0 || len(p2.Alters) != 0 || len(p2.Drops) != 0 {
		t.Errorf("expected empty second plan; creates=%v alters=%v drops=%v",
			p2.Creates, p2.Alters, p2.Drops)
	}
}

// --- Drop column (unsafe) ---

func TestIntegrationDropColumnUnsafe(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`create table %q.t (id bigint, junk text)`, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".t": {
				Name:    "t",
				Columns: map[string]*schema.Column{"id": {Type: "bigint"}},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, true) // unsafe=true
	applyPlan(t, pool, p)

	var colCount int
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.columns
		where table_schema=$1 and table_name='t' and column_name='junk'
	`, sch).Scan(&colCount)
	if err != nil {
		t.Fatal(err)
	}
	if colCount != 0 {
		t.Errorf("expected junk column to be dropped, got count=%d", colCount)
	}
}

// --- Custom schema auto-creation ---

func TestIntegrationCustomSchemaCreated(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()

	// use a schema name unlikely to exist
	sch := "pgytest_newschema_" + strings.ReplaceAll(t.Name(), "/", "_")
	if len(sch) > 63 {
		sch = sch[:63]
	}

	// ensure clean state
	pool.Exec(ctx, fmt.Sprintf("drop schema if exists %q cascade", sch)) //nolint

	t.Cleanup(func() {
		pool.Exec(context.Background(), fmt.Sprintf("drop schema if exists %q cascade", sch)) //nolint
	})

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".accounts": {
				Name:    "accounts",
				Columns: map[string]*schema.Column{"id": {Type: "bigint"}},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var sCount int
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.schemata where schema_name=$1
	`, sch).Scan(&sCount)
	if err != nil {
		t.Fatal(err)
	}
	if sCount != 1 {
		t.Errorf("expected schema %s to be created, got count=%d", sch, sCount)
	}
}
