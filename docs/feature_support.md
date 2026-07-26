# PostgreSQL Feature Support

This document tracks `pgy`'s support for various PostgreSQL features within its YAML schema definitions and diff engine. It serves both as a capability matrix and a roadmap for future development.

Features are grouped by relative complexity and impact on the diffing engine.

## Level 1: Basic Schema & Tables
These are the foundational elements required for almost any relational database application.

- [x] **Schemas** (Custom namespaces)
- [x] **Tables** (Base tables)
- [x] **Columns** (Data types, nullability, defaults)
- [x] **Primary Keys**
- [x] **Foreign Keys**
- [x] **Indexes** (Including unique indexes, non-btree methods via `using:` — gist/gin/brin/hash/spgist — and partial indexes via `where:`)
- [x] **UNIQUE Constraints** (Table-level and column-level)
- [x] **CHECK Constraints**
- [x] **NOT NULL Constraints** (Via column `nullable` property)
- [x] **EXCLUDE Constraints**
- [x] **Column Alterations** (`ALTER COLUMN ... TYPE / SET DEFAULT / DROP DEFAULT / SET NOT NULL / DROP NOT NULL` for existing columns. Types compared normalized — `varchar(255)` matches live `character varying(255)`, `serial` matches its live `integer` + `nextval` form; defaults compared with live casts stripped (`'x'::text` matches `'x'`). Type changes are gated behind `--unsafe` and support a column-level `using:` expression. Guard rails: identity columns are never altered, `nextval` defaults are never dropped, primary-key columns never emit `DROP NOT NULL`)
- [x] **Constraint Alterations** (Changed definitions of existing FK/check/unique/exclude constraints emit `DROP CONSTRAINT` + `ADD CONSTRAINT`, gated behind `--unsafe`. Live definitions introspected via `pg_get_constraintdef()`; comparison is normalized — casts, parentheses, whitespace, identifier quoting, and `public.` qualifiers are ignored, so `CHECK ((email <> ''::text))` matches `email <> ''`. Limitations: clauses not modeled in YAML (`ON UPDATE`, `MATCH`, `DEFERRABLE`) make a live FK compare unequal and re-add without them; primary keys are not compared)
- [x] **Comments** (`COMMENT ON` schemas, tables, columns, types, functions, views; diffed against live, PostGraphile smart tags safe)

## Level 2: Advanced Types & Logic
Features that encapsulate business logic, complex data structures, or procedural code.

- [x] **ENUM Types** (Including `ALTER TYPE ... ADD VALUE` for new labels, order-preserving)
- [x] **Composite Types** (Attribute order preserved from YAML declaration)
- [x] **Extensions** (`CREATE EXTENSION`)
- [x] **Functions** (PL/pgSQL, etc.; replace-on-change via body/attribute diff → `CREATE OR REPLACE`; arg types may be schema-qualified — unqualified and `public.`-qualified are equivalent)
- [x] **Triggers** (Including `WHEN (condition)` guards via `when:` and `CREATE CONSTRAINT TRIGGER` with `deferrable`/`initiallyDeferred`. Matched by definition via `pg_get_triggerdef()` — a changed trigger is dropped and recreated in the same migration; equivalent definitions (case, casts, parens, event order, `EXECUTE FUNCTION` vs `PROCEDURE`) emit no SQL. Trigger creates are emitted after all table and function creates, so trigger functions may `dependsOn` their own table — needed for SQL-language bodies, which validate at CREATE time. `dependsOn` cycles are a hard error in `pgy diff`)
- [x] **Views** (Standard `CREATE VIEW`; opt-in `replace: true` for `CREATE OR REPLACE` on every diff)
- [x] **Materialized Views**
- [x] **Domains** (`domain <name>:` blocks → `CREATE DOMAIN` with type/collate/default/notNull/check/constraintName; create-only, comments diffed)
- [x] **Procedures** (`procedure <name>(args):` blocks → `CREATE PROCEDURE`; replace-on-change via body/security diff → `CREATE OR REPLACE`; grants, `revokePublic`, comments)

## Level 3: Architecture & Security
Features necessary for scaling, multi-tenancy, and advanced security models.

- [x] **Table Partitioning** (Parent `partitionBy: {type: range|list|hash, columns: [...]}`; children as separate tables with `partitionOf` + `forValues` (from/to, in, modulus/remainder) or `default: true` → `CREATE TABLE ... PARTITION OF`; children auto-ordered after parent; create-only, no ATTACH/DETACH or bound alteration)
- [x] **Row Level Security (RLS)** (`rowLevelSecurity: true` / `false` + named `policies:` with for/to/using/withCheck; omitting the key leaves live RLS unmanaged, `false` emits `DISABLE ROW LEVEL SECURITY` gated behind `--unsafe`; policies matched by name with definitions diffed — a changed for/to/using/withCheck drops and recreates the policy)
- [x] **Grants & Privileges** (`GRANT` / `REVOKE` on tables, views, materialized views, functions, schemas, and columns — column-level via `grants:` under a column; `revokePublic` for functions)
- [x] **Roles & Users** (Top-level `roles:` block → `CREATE ROLE` with login/superuser/createdb/createrole/replication/bypassRLS/noinherit/connection limit; `inRoles` memberships via `GRANT role TO role`; create-only, no passwords)
- [x] **Sequences** (`sequence <name>:` blocks → `CREATE SEQUENCE IF NOT EXISTS` with as/increment/minValue/maxValue/start/cache/cycle/ownedBy; create-only, comments diffed)
- [x] **Identity Columns** (Column `identity: always | byDefault` → `GENERATED ... AS IDENTITY`; create-only, existing columns never altered; overrides `default`)

## Level 4: Specialized Configurations
Advanced PostgreSQL-specific capabilities for niche use cases.

- [ ] **Foreign Data Wrappers (FDW)** (Servers, user mappings, and foreign tables)
- [ ] **Full Text Search Configurations** (Custom dictionaries, parsers, templates)
- [ ] **Collations** (Custom string sorting rules)
- [ ] **Rules** (`CREATE RULE` query rewrites)
- [ ] **Event Triggers** (DDL triggers)
- [ ] **Logical Replication** (Publications and Subscriptions)
- [ ] **Tablespaces** (Physical storage mapping)

---

## Test Coverage

Unit tests live in `internal/schema/schema_test.go` and `internal/diff/diff_test.go`. Run with:

```sh
make test
# or targeted:
go test ./internal/schema/...
go test ./internal/diff/...
```

Every checked feature above has at least one unit test. Coverage areas:

| Package | What's tested |
|---------|---------------|
| `internal/schema` | Map/list/schema-block YAML formats; column attributes (nullable, notNull, default, unique, primaryKey, identity, using); primary keys; foreign keys; indexes (unique, `using` method, `where` predicate); check/unique/exclude constraints; triggers (incl. `when` guard); extensions; enum types; composite types; functions (security, volatility, strict); procedures (args, security, set, body, grants, `revokePublic`, `dependsOn`, multi-file merge); views; materialized views; sequences (options, defaults, `dependsOn`, multi-file merge); domains (options, defaults, `dependsOn`, multi-file merge); grants (table/view/matview/function/schema/column, `revokePublic`); roles (options, `inRoles`, defaults, multi-file merge); RLS (tri-state parse across map/list/schema-block formats, cross-file merge override) + policies; comments (all object types); partitioning (partitionBy explicit + shorthand, partitionOf range/list/hash/default bounds, qualified parents, parent-before-child topological order); `dependsOn`; topological sort (incl. cycle detection error, function-after-table ordering); `LoadAndMerge` including missing-file tolerance; `qualify` helper |
| `internal/diff` | CREATE TABLE SQL; column order preservation; primary key (table-level and column-level); foreign keys with ON DELETE; unique/non-unique indexes; auto-named indexes; index method (`using gist`, btree omitted) and partial (`where`) indexes incl. combined; check/unique/exclude constraints; trigger create/skip-if-exists/drop+recreate-on-changed-definition (equivalent-definition normalization incl. event order and `EXECUTE FUNCTION` vs `PROCEDURE`, name-only live fallback skip) incl. deferred constraint triggers, `when` guards, and emission after all table+function creates; extension create/skip-if-exists; enum/composite type create/skip-if-exists; function create/skip-if-exists/replace-on-change (body, volatility) with security+volatility; enum add-value (positioned + appended, skip-if-same); procedure create/skip-if-exists/replace-on-change (body, security) with args/security-definer/set-clause/schema-creation/comment/revoke-public/grants; view create/skip-if-exists/replace-flag; materialized view create/skip-if-exists; sequence create/skip-if-exists/options/owned-by/schema-creation/comment/ordering-before-dependent-table; domain create/skip-if-exists/options/named-check/collate/schema-creation/comment/no-type-skip/ordering-before-dependent-table; grants (grant missing, revoke removed, skip live, `revokePublic`); column grants (grant missing, skip live, revoke removed, unmanaged without block, PUBLIC not auto-revoked); view/matview grants (grant missing, skip live, revoke removed); role create/skip-if-exists/options/ordering-before-schemas/membership grant+skip/no-drop/comments; RLS enable/skip/disable-under-unsafe/disable-gated-without-unsafe/skip-if-already-disabled/unmanaged-when-key-absent; policy create/skip/drop-on-removal/replace-on-changed-using-withCheck-command-roles (drop-before-create ordering, equivalent-expression and role order/case normalization, omitted `for` matches live ALL); comments (emit/skip-if-same/update-if-changed, quote escaping); custom schema creation; public schema not created; add column; drop column (safe vs unsafe); column alterations (type change unsafe-gated/skipped-when-safe/`using` clause, equivalent-type and equivalent-default normalization, serial-vs-integer skip, set/drop default, nextval drop guard, set/drop not null, PK drop-not-null guard, identity never altered, no-change no-SQL); constraint alterations (check/unique/exclude/FK drop+re-add on changed definition, unsafe-gated, drop-before-add ordering, equivalent-definition normalization incl. casts/parens/quoting/`public.`, name-only live data no-op, `normalizeConstraintDef`); identity columns (always/by-default in CREATE TABLE and ADD COLUMN, skip-if-exists, default suppressed); partitioned table create/skip-if-exists incl. expression keys; partition child create/skip-if-exists (range/list/hash/default bounds, minvalue/numeric literals, parent-first ordering); `Render`; `pqIdent`; `normalizeFunctionSignature`; `PlanDiff.Summary` |
| `internal/cli` | `slugify`; `nextMigrationNumber`; checksum parse and body |

*Note: When building out unsupported features, ensure both the YAML model in `schema.go` and the introspection/diffing logic in `diff.go` are updated, and add corresponding tests.*
