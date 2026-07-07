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
	"github.com/suprbdev/pgy/internal/db"
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
	sch := "pgytest_newschema_itest"

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

// --- Primary key on existing table ---

func TestIntegrationPKOnExistingTable(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`create table %q.t (id bigint not null)`, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".t": {
				Name:       "t",
				Columns:    map[string]*schema.Column{"id": {Type: "bigint", Nullable: false}},
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

	var pkCount int
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.table_constraints
		where table_schema=$1 and table_name='t' and constraint_type='PRIMARY KEY'
	`, sch).Scan(&pkCount)
	if err != nil {
		t.Fatal(err)
	}
	if pkCount != 1 {
		t.Errorf("expected PK constraint, got count=%d", pkCount)
	}
}

func TestIntegrationPKSkippedIfAlreadyLive(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`create table %q.t (id bigint primary key)`, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".t": {
				Name:       "t",
				Columns:    map[string]*schema.Column{"id": {Type: "bigint", Nullable: false}},
				PrimaryKey: []string{"id"},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	if len(p.Alters) != 0 {
		t.Errorf("PK already exists, expected no alters; got %v", p.Alters)
	}
}

// --- Foreign key on existing table ---

func TestIntegrationFKOnExistingTable(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`
		create table %q.users (id bigint primary key);
		create table %q.orders (id bigint primary key, user_id bigint);
	`, sch, sch))
	if err != nil {
		t.Fatal(err)
	}

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
					{Name: "fk_ord_user", Columns: []string{"user_id"}, RefTable: sch + ".users", RefColumns: []string{"id"}, OnDelete: "restrict"},
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

	var fkCount int
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.referential_constraints
		where constraint_schema=$1 and constraint_name='fk_ord_user'
	`, sch).Scan(&fkCount)
	if err != nil {
		t.Fatal(err)
	}
	if fkCount != 1 {
		t.Errorf("expected fk_ord_user, got count=%d", fkCount)
	}
}

func TestIntegrationFKSkippedIfAlreadyLive(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`
		create table %q.users (id bigint primary key);
		create table %q.orders (id bigint primary key, user_id bigint,
			constraint fk_ord_user foreign key (user_id) references %q.users(id));
	`, sch, sch, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".users": {
				Name:    "users",
				Columns: map[string]*schema.Column{"id": {Type: "bigint"}},
			},
			sch + ".orders": {
				Name: "orders",
				Columns: map[string]*schema.Column{
					"id":      {Type: "bigint"},
					"user_id": {Type: "bigint"},
				},
				ForeignKeys: []*schema.ForeignKey{
					{Name: "fk_ord_user", Columns: []string{"user_id"}, RefTable: sch + ".users", RefColumns: []string{"id"}},
				},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	for _, s := range p.Alters {
		if strings.Contains(s, "fk_ord_user") {
			t.Errorf("FK already live, should not re-add; alters: %v", p.Alters)
		}
	}
}

// --- Check constraint on existing table ---

func TestIntegrationCheckConstraintOnExistingTable(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`create table %q.products (id bigint, price numeric)`, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".products": {
				Name: "products",
				Columns: map[string]*schema.Column{
					"id":    {Type: "bigint"},
					"price": {Type: "numeric"},
				},
				Constraints: []*schema.Constraint{
					{Name: "chk_price_pos", Type: "check", Expression: "price > 0"},
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
		where constraint_schema=$1 and constraint_name='chk_price_pos'
	`, sch).Scan(&ctCount)
	if err != nil {
		t.Fatal(err)
	}
	if ctCount != 1 {
		t.Errorf("expected chk_price_pos, got count=%d", ctCount)
	}
}

func TestIntegrationCheckConstraintSkippedIfAlreadyLive(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`
		create table %q.products (id bigint, price numeric,
			constraint chk_price_pos check (price > 0))
	`, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".products": {
				Name: "products",
				Columns: map[string]*schema.Column{
					"id":    {Type: "bigint"},
					"price": {Type: "numeric"},
				},
				Constraints: []*schema.Constraint{
					{Name: "chk_price_pos", Type: "check", Expression: "price > 0"},
				},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	for _, s := range p.Alters {
		if strings.Contains(s, "chk_price_pos") {
			t.Errorf("constraint already live, should not re-add; alters: %v", p.Alters)
		}
	}
}

// --- Unique index on existing table ---

func TestIntegrationUniqueIndexOnExistingTable(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`create table %q.users (id bigint, email text)`, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".users": {
				Name: "users",
				Columns: map[string]*schema.Column{
					"id":    {Type: "bigint"},
					"email": {Type: "text"},
				},
				Indexes: []*schema.Index{
					{Name: "idx_users_email_uniq", Columns: []string{"email"}, Unique: true},
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
	err = pool.QueryRow(ctx, `
		select count(*) from pg_indexes
		where schemaname=$1 and tablename='users' and indexname='idx_users_email_uniq'
	`, sch).Scan(&idxCount)
	if err != nil {
		t.Fatal(err)
	}
	if idxCount != 1 {
		t.Errorf("expected idx_users_email_uniq, got count=%d", idxCount)
	}
}

func TestIntegrationIndexSkippedIfAlreadyLive(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`
		create table %q.users (id bigint, email text);
		create unique index idx_users_email_skip on %q.users(email);
	`, sch, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".users": {
				Name: "users",
				Columns: map[string]*schema.Column{
					"id":    {Type: "bigint"},
					"email": {Type: "text"},
				},
				Indexes: []*schema.Index{
					{Name: "idx_users_email_skip", Columns: []string{"email"}, Unique: true},
				},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	for _, s := range p.Creates {
		if strings.Contains(s, "idx_users_email_skip") {
			t.Errorf("index already live, should not re-create; creates: %v", p.Creates)
		}
	}
}

// --- Enum type skip-if-live ---

func TestIntegrationEnumTypeSkippedIfAlreadyLive(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`create type %q.status as enum ('active', 'inactive')`, sch))
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
		Types: map[string]*schema.TypeDef{
			sch + ".status": {Name: "status", Schema: sch, Kind: "enum", Labels: []string{"active", "inactive"}},
		},
	}

	p := diff.Plan(live, desired, false)
	for _, s := range p.Creates {
		if strings.Contains(s, "create type") {
			t.Errorf("enum type already live, should not create; creates: %v", p.Creates)
		}
	}
}

// --- Composite type create and skip ---

func TestIntegrationCompositeTypeCreate(t *testing.T) {
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
		Types: map[string]*schema.TypeDef{
			sch + ".address": {
				Name: "address", Schema: sch, Kind: "composite",
				Attributes: map[string]string{"street": "text", "city": "text"},
			},
		},
	}

	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var typCount int
	err = pool.QueryRow(ctx, `
		select count(*) from pg_type t
		join pg_namespace n on n.oid = t.typnamespace
		where n.nspname=$1 and t.typname='address' and t.typtype='c'
	`, sch).Scan(&typCount)
	if err != nil {
		t.Fatal(err)
	}
	if typCount != 1 {
		t.Errorf("expected address composite type, got count=%d", typCount)
	}
}

func TestIntegrationCompositeTypeSkippedIfAlreadyLive(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`create type %q.address as (street text, city text)`, sch))
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
		Types: map[string]*schema.TypeDef{
			sch + ".address": {
				Name: "address", Schema: sch, Kind: "composite",
				Attributes: map[string]string{"street": "text", "city": "text"},
			},
		},
	}

	p := diff.Plan(live, desired, false)
	for _, s := range p.Creates {
		if strings.Contains(s, "create type") {
			t.Errorf("composite type already live, should not create; creates: %v", p.Creates)
		}
	}
}

// --- Function skip-if-live ---

func TestIntegrationFunctionSkippedIfAlreadyLive(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`
		create function %q.add_nums(a integer, b integer) returns integer
		language sql immutable as $$ select a + b $$
	`, sch))
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
		Functions: map[string]*schema.Function{
			sch + ".add_nums": {
				Name: "add_nums", Schema: sch,
				ArgsSig: "(a integer, b integer)", Returns: "integer",
				Language: "sql", Volatility: "immutable",
				Body: "select a + b",
			},
		},
	}

	p := diff.Plan(live, desired, false)
	for _, s := range p.Creates {
		if strings.Contains(s, "create function") {
			t.Errorf("function already live, should not create; creates: %v", p.Creates)
		}
	}
}

// --- View skip-if-live ---

func TestIntegrationViewSkippedIfAlreadyLive(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`
		create table %q.items (id bigint, active boolean);
		create view %q.active_items as select id from %q.items where active = true;
	`, sch, sch, sch))
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
				Schema: sch, Name: "active_items",
				Query:        fmt.Sprintf(`select id from %q.items where active = true`, sch),
				Materialized: false,
			},
		},
	}

	p := diff.Plan(live, desired, false)
	for _, s := range p.Creates {
		if strings.Contains(s, "active_items") {
			t.Errorf("view already live, should not create; creates: %v", p.Creates)
		}
	}
}

// --- Materialized view skip-if-live ---

func TestIntegrationMatViewSkippedIfAlreadyLive(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`
		create table %q.sales (id bigint, amount numeric);
		create materialized view %q.sales_summary as select count(*) as cnt from %q.sales;
	`, sch, sch, sch))
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
				Schema: sch, Name: "sales_summary",
				Query:        fmt.Sprintf(`select count(*) as cnt from %q.sales`, sch),
				Materialized: true,
			},
		},
	}

	p := diff.Plan(live, desired, false)
	for _, s := range p.Creates {
		if strings.Contains(s, "sales_summary") {
			t.Errorf("matview already live, should not create; creates: %v", p.Creates)
		}
	}
}

// --- Drop column safe (no drop) ---

func TestIntegrationDropColumnSafe(t *testing.T) {
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
	p := diff.Plan(live, desired, false) // safe mode
	for _, s := range p.Drops {
		if strings.Contains(s, "junk") {
			t.Errorf("safe mode must not drop columns; drops: %v", p.Drops)
		}
	}

	// column must still be there
	var colCount int
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.columns
		where table_schema=$1 and table_name='t' and column_name='junk'
	`, sch).Scan(&colCount)
	if err != nil {
		t.Fatal(err)
	}
	if colCount != 1 {
		t.Errorf("junk column should still exist in safe mode, got count=%d", colCount)
	}
}

// --- Public schema not created ---

func TestIntegrationPublicSchemaNotCreated(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	for _, s := range p.Creates {
		if strings.Contains(s, "create schema") && strings.Contains(s, "public") {
			t.Errorf("must not create public schema; creates: %v", p.Creates)
		}
	}
}

// --- Column unique flag ---

func TestIntegrationColumnUniqueFlag(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".users": {
				Name: "users",
				Columns: map[string]*schema.Column{
					"id":    {Type: "bigint"},
					"email": {Type: "text", Unique: true},
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
		select count(*) from information_schema.table_constraints
		where table_schema=$1 and table_name='users' and constraint_type='UNIQUE'
	`, sch).Scan(&ctCount)
	if err != nil {
		t.Fatal(err)
	}
	if ctCount != 1 {
		t.Errorf("expected UNIQUE constraint from column.Unique, got count=%d", ctCount)
	}
}

// --- Triggers ---

func TestIntegrationTriggerOnExistingTable(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`
		create table %q.t (id bigint primary key);
		create function %q.audit_fn() returns trigger language plpgsql as 'begin return new; end';
	`, sch, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".t": {
				Name:    "t",
				Columns: map[string]*schema.Column{"id": {Type: "bigint"}},
				Triggers: []*schema.Trigger{
					{Name: "trg_audit", Timing: "after", Events: []string{"insert"}, Level: "row", Procedure: fmt.Sprintf("%q.audit_fn()", sch)},
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

	var trgCount int
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.triggers
		where trigger_schema=$1 and trigger_name='trg_audit'
	`, sch).Scan(&trgCount)
	if err != nil {
		t.Fatal(err)
	}
	if trgCount == 0 {
		t.Error("expected trg_audit to exist")
	}
}

func TestIntegrationTriggerSkippedIfAlreadyLive(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`
		create table %q.t (id bigint primary key);
		create function %q.audit_fn() returns trigger language plpgsql as 'begin return new; end';
		create trigger trg_audit after insert on %q.t for each row execute procedure %q.audit_fn();
	`, sch, sch, sch, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".t": {
				Name:    "t",
				Columns: map[string]*schema.Column{"id": {Type: "bigint"}},
				Triggers: []*schema.Trigger{
					{Name: "trg_audit", Timing: "after", Events: []string{"insert"}, Level: "row", Procedure: fmt.Sprintf("%q.audit_fn()", sch)},
				},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	for _, s := range p.Creates {
		if strings.Contains(s, "trg_audit") {
			t.Errorf("trigger already live, should not re-create; creates: %v", p.Creates)
		}
	}
}

// --- Circular FK pair (person <-> asset) ---

func TestIntegrationCircularFK(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".person": {
				Name: "person",
				Columns: map[string]*schema.Column{
					"id":       {Type: "bigint"},
					"asset_id": {Type: "bigint"},
				},
				PrimaryKey: []string{"id"},
				ForeignKeys: []*schema.ForeignKey{
					{Name: "fk_person_asset", Columns: []string{"asset_id"}, RefTable: sch + ".asset", RefColumns: []string{"id"}},
				},
			},
			sch + ".asset": {
				Name: "asset",
				Columns: map[string]*schema.Column{
					"id":       {Type: "bigint"},
					"owner_id": {Type: "bigint"},
				},
				PrimaryKey: []string{"id"},
				ForeignKeys: []*schema.ForeignKey{
					{Name: "fk_asset_owner", Columns: []string{"owner_id"}, RefTable: sch + ".person", RefColumns: []string{"id"}},
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

	var fkCount int
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.referential_constraints
		where constraint_schema=$1 and constraint_name in ('fk_person_asset','fk_asset_owner')
	`, sch).Scan(&fkCount)
	if err != nil {
		t.Fatal(err)
	}
	if fkCount != 2 {
		t.Errorf("expected both circular FKs, got count=%d", fkCount)
	}
}

// --- Grants ---

// freshRole creates a unique role for a test and drops it on cleanup.
func freshRole(t *testing.T, pool *pgxpool.Pool, suffix string) string {
	t.Helper()
	name := "pgyrole_" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "_") + "_" + suffix
	if len(name) > 63 {
		name = name[:63]
	}
	ctx := context.Background()
	if _, err := pool.Exec(ctx, fmt.Sprintf("drop role if exists %q", name)); err != nil {
		t.Fatalf("drop role: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf("create role %q", name)); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		pool.Exec(ctx, fmt.Sprintf("drop owned by %q", name)) //nolint
		pool.Exec(ctx, fmt.Sprintf("drop role if exists %q", name)) //nolint
	})
	return name
}

func TestIntegrationTableGrants(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	role := freshRole(t, pool, "m")
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".t": {
				Name:    "t",
				Columns: map[string]*schema.Column{"id": {Type: "bigint"}},
				Grants:  map[string][]string{role: {"select", "insert"}},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var privs int
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.role_table_grants
		where table_schema=$1 and table_name='t' and grantee=$2
		and privilege_type in ('SELECT','INSERT')
	`, sch, role).Scan(&privs)
	if err != nil {
		t.Fatal(err)
	}
	if privs != 2 {
		t.Errorf("expected 2 privileges granted, got %d", privs)
	}

	// second plan must be empty (idempotent)
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, desired, false)
	for _, s := range p.Alters {
		if strings.Contains(s, "grant") || strings.Contains(s, "revoke") {
			t.Errorf("expected no grant churn on second plan; alters: %v", p.Alters)
		}
	}

	// remove insert from desired -> revoke emitted and applied
	desired.Tables[sch+".t"].Grants = map[string][]string{role: {"select"}}
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, desired, false)
	applyPlan(t, pool, p)
	err = pool.QueryRow(ctx, `
		select count(*) from information_schema.role_table_grants
		where table_schema=$1 and table_name='t' and grantee=$2 and privilege_type='INSERT'
	`, sch, role).Scan(&privs)
	if err != nil {
		t.Fatal(err)
	}
	if privs != 0 {
		t.Error("expected INSERT revoked after removal from grants block")
	}
}

func TestIntegrationFunctionRevokePublic(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	role := freshRole(t, pool, "m")
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Functions: map[string]*schema.Function{
			sch + ".secret_fn": {
				Schema: sch, Name: "secret_fn", ArgsSig: "()",
				Returns: "int", Language: "sql", Security: "definer",
				Body:         "select 1",
				RevokePublic: true,
				Grants:       map[string][]string{role: {"execute"}},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var roleExec bool
	err = pool.QueryRow(ctx,
		`select has_function_privilege($1, ($2 || '.secret_fn()')::text, 'execute')`,
		role, fmt.Sprintf("%q", sch)).Scan(&roleExec)
	if err != nil {
		t.Fatal(err)
	}

	// PUBLIC must not have execute: no grantee=0 EXECUTE entry in the acl
	var publicHas bool
	err = pool.QueryRow(ctx,
		`select coalesce(bool_or(a.grantee = 0), false)
		 from pg_proc p
		 join pg_namespace n on n.oid = p.pronamespace
		 cross join lateral aclexplode(p.proacl) a
		 where n.nspname = $1 and p.proname = 'secret_fn' and a.privilege_type = 'EXECUTE'`,
		sch).Scan(&publicHas)
	if err != nil {
		t.Fatal(err)
	}
	if publicHas {
		t.Error("expected PUBLIC execute revoked")
	}
	if !roleExec {
		t.Error("expected role granted execute")
	}

	// second plan idempotent
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, desired, false)
	for _, s := range p.Alters {
		if strings.Contains(s, "grant") || strings.Contains(s, "revoke") {
			t.Errorf("expected no grant churn on second plan; alters: %v", p.Alters)
		}
	}
}

func TestIntegrationSchemaGrants(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	role := freshRole(t, pool, "m")
	ctx := context.Background()

	desired := &schema.Database{
		Tables:       map[string]*schema.Table{},
		SchemaGrants: map[string]map[string][]string{sch: {role: {"usage"}}},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	var hasUsage bool
	err = pool.QueryRow(ctx, `select has_schema_privilege($1, $2, 'usage')`, role, sch).Scan(&hasUsage)
	if err != nil {
		t.Fatal(err)
	}
	if !hasUsage {
		t.Error("expected usage granted on schema")
	}

	// second plan idempotent
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, desired, false)
	for _, s := range p.Alters {
		if strings.Contains(s, "grant") || strings.Contains(s, "revoke") {
			t.Errorf("expected no grant churn on second plan; alters: %v", p.Alters)
		}
	}
}

// --- Row level security ---

func TestIntegrationRLSAndPolicies(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	role := freshRole(t, pool, "m")
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".orders": {
				Name:             "orders",
				Columns:          map[string]*schema.Column{"id": {Type: "bigint"}, "member_id": {Type: "bigint"}},
				RowLevelSecurity: true,
				Policies: []*schema.Policy{
					{Name: "member_select", For: "select", To: []string{role},
						Using: "member_id = current_setting('app.member_id')::bigint"},
					{Name: "member_insert", For: "insert", To: []string{role},
						WithCheck: "member_id = current_setting('app.member_id')::bigint"},
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

	var rlsEnabled bool
	err = pool.QueryRow(ctx, `
		select c.relrowsecurity from pg_class c
		join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = $1 and c.relname = 'orders'
	`, sch).Scan(&rlsEnabled)
	if err != nil {
		t.Fatal(err)
	}
	if !rlsEnabled {
		t.Error("expected RLS enabled")
	}
	var polCount int
	err = pool.QueryRow(ctx, `
		select count(*) from pg_policy pol
		join pg_class c on c.oid = pol.polrelid
		join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = $1 and c.relname = 'orders'
	`, sch).Scan(&polCount)
	if err != nil {
		t.Fatal(err)
	}
	if polCount != 2 {
		t.Errorf("expected 2 policies, got %d", polCount)
	}

	// second plan idempotent
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, desired, false)
	if len(p.Creates)+len(p.Alters)+len(p.Drops) != 0 {
		t.Errorf("expected empty second plan; creates=%v alters=%v drops=%v", p.Creates, p.Alters, p.Drops)
	}

	// remove one policy -> dropped
	desired.Tables[sch+".orders"].Policies = desired.Tables[sch+".orders"].Policies[:1]
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, desired, false)
	applyPlan(t, pool, p)
	err = pool.QueryRow(ctx, `
		select count(*) from pg_policy pol
		join pg_class c on c.oid = pol.polrelid
		join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = $1 and c.relname = 'orders'
	`, sch).Scan(&polCount)
	if err != nil {
		t.Fatal(err)
	}
	if polCount != 1 {
		t.Errorf("expected 1 policy after removal, got %d", polCount)
	}
}

// --- Comments ---

func TestIntegrationComments(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".orders": {
				Name:    "orders",
				Comment: "@behavior +list\nCustomer orders.",
				Columns: map[string]*schema.Column{
					"id": {Type: "bigint", Comment: "@name orderId"},
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

	var tblComment, colComment string
	err = pool.QueryRow(ctx, `
		select coalesce(obj_description(c.oid, 'pg_class'), ''),
		       coalesce(col_description(c.oid, a.attnum), '')
		from pg_class c
		join pg_namespace n on n.oid = c.relnamespace
		join pg_attribute a on a.attrelid = c.oid and a.attname = 'id'
		where n.nspname = $1 and c.relname = 'orders'
	`, sch).Scan(&tblComment, &colComment)
	if err != nil {
		t.Fatal(err)
	}
	if tblComment != "@behavior +list\nCustomer orders." {
		t.Errorf("table comment: %q", tblComment)
	}
	if colComment != "@name orderId" {
		t.Errorf("column comment: %q", colComment)
	}

	// second plan idempotent
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, desired, false)
	if len(p.Creates)+len(p.Alters)+len(p.Drops) != 0 {
		t.Errorf("expected empty second plan; creates=%v alters=%v drops=%v", p.Creates, p.Alters, p.Drops)
	}

	// change comment -> re-emitted
	desired.Tables[sch+".orders"].Comment = "@behavior -list"
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, desired, false)
	found := false
	for _, s := range p.Alters {
		if strings.Contains(s, "@behavior -list") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected comment update; alters: %v", p.Alters)
	}
}

// --- Replace on change ---

func TestIntegrationFunctionReplaceOnBodyChange(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	mkDesired := func(body string) *schema.Database {
		return &schema.Database{
			Tables: map[string]*schema.Table{},
			Functions: map[string]*schema.Function{
				sch + ".login": {
					Schema: sch, Name: "login", ArgsSig: "(email text)",
					Returns: "int", Language: "sql", Body: body,
				},
			},
		}
	}

	// phase 1: create
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, mkDesired("select 1"), false)
	applyPlan(t, pool, p)

	// same body -> empty plan
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, mkDesired("select 1"), false)
	if len(p.Creates)+len(p.Alters)+len(p.Drops) != 0 {
		t.Errorf("unchanged body, expected empty plan; creates=%v", p.Creates)
	}

	// phase 2: iterate body -> replaced
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, mkDesired("select 2"), false)
	if len(p.Creates) != 1 || !strings.Contains(p.Creates[0], "create or replace function") {
		t.Fatalf("expected create or replace; creates: %v", p.Creates)
	}
	applyPlan(t, pool, p)

	var result int
	err = pool.QueryRow(ctx, fmt.Sprintf(`select %q.login('x')`, sch)).Scan(&result)
	if err != nil {
		t.Fatal(err)
	}
	if result != 2 {
		t.Errorf("expected new body active (2), got %d", result)
	}
}

func TestIntegrationEnumAddValue(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`create type %q.status as enum ('active', 'closed')`, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Types: map[string]*schema.TypeDef{
			sch + ".status": {Schema: sch, Name: "status", Kind: "enum",
				Labels: []string{"active", "pending", "closed", "archived"}},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	rows, err := pool.Query(ctx, `
		select e.enumlabel
		from pg_enum e
		join pg_type t on t.oid = e.enumtypid
		join pg_namespace n on n.oid = t.typnamespace
		where n.nspname = $1 and t.typname = 'status'
		order by e.enumsortorder
	`, sch)
	if err != nil {
		t.Fatal(err)
	}
	labels := []string{}
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatal(err)
		}
		labels = append(labels, l)
	}
	rows.Close()
	want := "active,pending,closed,archived"
	if strings.Join(labels, ",") != want {
		t.Errorf("want labels %s, got %s", want, strings.Join(labels, ","))
	}

	// second plan idempotent
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, desired, false)
	if len(p.Alters) != 0 {
		t.Errorf("labels in sync, expected no alters; got %v", p.Alters)
	}
}

// --- Constraint triggers ---

func TestIntegrationDeferredConstraintTrigger(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	// enforcement function: every entry must have at least one tag by commit time
	_, err := pool.Exec(ctx, fmt.Sprintf(`
		create table %q.entry (id bigint primary key);
		create table %q.entry_tag (entry_id bigint, tag text);
		create function %q.check_entry_requirements() returns trigger language plpgsql as $fn$
		begin
			if not exists (select 1 from %q.entry_tag where entry_id = new.id) then
				raise exception 'entry %% has no tags', new.id;
			end if;
			return new;
		end
		$fn$;
	`, sch, sch, sch, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".entry": {
				Name:    "entry",
				Columns: map[string]*schema.Column{"id": {Type: "bigint"}},
				Triggers: []*schema.Trigger{
					{Name: "trg_check_requirements", Constraint: true, InitiallyDeferred: true,
						Events: []string{"insert"}, Procedure: fmt.Sprintf("%q.check_entry_requirements()", sch)},
				},
			},
			sch + ".entry_tag": {
				Name: "entry_tag",
				Columns: map[string]*schema.Column{
					"entry_id": {Type: "bigint"},
					"tag":      {Type: "text"},
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

	// entry + junction insert in ONE transaction must commit (check deferred to commit)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`insert into %q.entry values (1)`, sch)); err != nil {
		tx.Rollback(ctx)
		t.Fatalf("entry insert should not fire check immediately: %v", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`insert into %q.entry_tag values (1, 'music')`, sch)); err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit should succeed with tag present: %v", err)
	}

	// entry without tag must fail at commit
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`insert into %q.entry values (2)`, sch)); err != nil {
		tx.Rollback(ctx)
		t.Fatalf("insert itself should succeed (deferred): %v", err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Error("commit without tag should fail via deferred constraint trigger")
	}

	// second plan idempotent (constraint trigger introspected, not re-created)
	live, err = diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p = diff.Plan(live, desired, false)
	if len(p.Creates)+len(p.Alters)+len(p.Drops) != 0 {
		t.Errorf("expected empty second plan; creates=%v alters=%v", p.Creates, p.Alters)
	}
}

// Regression: policy on already-live table referencing a function created in
// the same plan previously failed with SQLSTATE 42883.
func TestIntegrationPolicyReferencingNewFunction(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	_, err := pool.Exec(ctx, fmt.Sprintf(`create table %q.t (id uuid)`, sch))
	if err != nil {
		t.Fatal(err)
	}

	desired := &schema.Database{
		Tables: map[string]*schema.Table{
			sch + ".t": {
				Name:             "t",
				Columns:          map[string]*schema.Column{"id": {Type: "uuid"}},
				RowLevelSecurity: true,
				Policies: []*schema.Policy{
					{Name: "admin_all", Using: fmt.Sprintf("%q.is_organisation_admin(id)", sch)},
				},
			},
		},
		Functions: map[string]*schema.Function{
			sch + ".is_organisation_admin": {
				Schema: sch, Name: "is_organisation_admin", ArgsSig: "(org uuid)",
				Returns: "boolean", Language: "sql", Volatility: "stable", Body: "select true",
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p) // previously failed: function did not exist yet

	var polCount int
	err = pool.QueryRow(ctx, `
		select count(*) from pg_policy pol
		join pg_class c on c.oid = pol.polrelid
		join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = $1 and c.relname = 't' and pol.polname = 'admin_all'
	`, sch).Scan(&polCount)
	if err != nil {
		t.Fatal(err)
	}
	if polCount != 1 {
		t.Errorf("expected admin_all policy, got count=%d", polCount)
	}
}

// --- Statement splitter through the real migrate path ---

// Regression: comment text containing ';' was split mid-string ->
// "unterminated quoted string".
func TestIntegrationApplyInTxSemicolonInComment(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	sql := fmt.Sprintf(`create table %q.t (id bigint);
comment on table %q.t is '@behavior +list; @name entries';
comment on column %q.t.id is 'it''s the id; primary';
`, sch, sch, sch)

	if err := db.ApplyInTx(ctx, pool, sql); err != nil {
		t.Fatalf("apply with semicolons in comments: %v", err)
	}

	var tblComment string
	err := pool.QueryRow(ctx, `
		select coalesce(obj_description(c.oid, 'pg_class'), '')
		from pg_class c join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = $1 and c.relname = 't'
	`, sch).Scan(&tblComment)
	if err != nil {
		t.Fatal(err)
	}
	if tblComment != "@behavior +list; @name entries" {
		t.Errorf("comment mangled: %q", tblComment)
	}
}

// --- Composite type attribute order ---

func TestIntegrationCompositeAttributeOrder(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()

	desired := &schema.Database{
		Tables: map[string]*schema.Table{},
		Types: map[string]*schema.TypeDef{
			sch + ".jwt": {
				Name: "jwt", Schema: sch, Kind: "composite",
				Attributes:     map[string]string{"role": "text", "person_id": "uuid", "exp": "bigint"},
				AttributeOrder: []string{"role", "person_id", "exp"},
			},
		},
	}

	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	p := diff.Plan(live, desired, false)
	applyPlan(t, pool, p)

	rows, err := pool.Query(ctx, `
		select a.attname
		from pg_attribute a
		join pg_type t on t.typrelid = a.attrelid
		join pg_namespace n on n.oid = t.typnamespace
		where n.nspname = $1 and t.typname = 'jwt' and a.attnum > 0
		order by a.attnum
	`, sch)
	if err != nil {
		t.Fatal(err)
	}
	attrs := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		attrs = append(attrs, a)
	}
	rows.Close()
	if strings.Join(attrs, ",") != "role,person_id,exp" {
		t.Errorf("want role,person_id,exp got %s", strings.Join(attrs, ","))
	}

	// positional ROW cast must line up with declared order
	var role string
	err = pool.QueryRow(ctx, fmt.Sprintf(
		`select (ROW('admin', '00000000-0000-0000-0000-000000000000'::uuid, 123)::%q.jwt).role`, sch)).Scan(&role)
	if err != nil {
		t.Fatalf("positional ROW cast: %v", err)
	}
	if role != "admin" {
		t.Errorf("want admin, got %q", role)
	}
}
