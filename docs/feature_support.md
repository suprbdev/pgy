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
- [x] **Indexes** (Including unique indexes)
- [x] **UNIQUE Constraints** (Table-level and column-level)
- [x] **CHECK Constraints**
- [x] **NOT NULL Constraints** (Via column `nullable` property)
- [x] **EXCLUDE Constraints**
- [x] **Column Alterations** (`ALTER COLUMN ... TYPE / SET DEFAULT / DROP DEFAULT / SET NOT NULL / DROP NOT NULL` for existing columns. Types compared normalized — `varchar(255)` matches live `character varying(255)`, `serial` matches its live `integer` + `nextval` form; defaults compared with live casts stripped (`'x'::text` matches `'x'`). Type changes are gated behind `--unsafe` and support a column-level `using:` expression. Guard rails: identity columns are never altered, `nextval` defaults are never dropped, primary-key columns never emit `DROP NOT NULL`)
- [ ] **Constraint Alterations** (Detect changed definitions of existing FK/check/unique/exclude constraints. Currently matched by name existence only, so a redefined constraint emits no SQL. Implementation notes: requires introspecting constraint definitions — e.g. `pg_get_constraintdef()` — instead of the current name-only `Constraints map[string]bool`; a change is a `DROP CONSTRAINT` + `ADD CONSTRAINT`, which is destructive-adjacent and should likely be gated behind `--unsafe`)
- [x] **Comments** (`COMMENT ON` schemas, tables, columns, types, functions, views; diffed against live, PostGraphile smart tags safe)

## Level 2: Advanced Types & Logic
Features that encapsulate business logic, complex data structures, or procedural code.

- [x] **ENUM Types** (Including `ALTER TYPE ... ADD VALUE` for new labels, order-preserving)
- [x] **Composite Types** (Attribute order preserved from YAML declaration)
- [x] **Extensions** (`CREATE EXTENSION`)
- [x] **Functions** (PL/pgSQL, etc.; replace-on-change via body/attribute diff → `CREATE OR REPLACE`)
- [x] **Triggers** (Including `CREATE CONSTRAINT TRIGGER` with `deferrable`/`initiallyDeferred`)
- [x] **Views** (Standard `CREATE VIEW`; opt-in `replace: true` for `CREATE OR REPLACE` on every diff)
- [x] **Materialized Views**
- [x] **Domains** (`domain <name>:` blocks → `CREATE DOMAIN` with type/collate/default/notNull/check/constraintName; create-only, comments diffed)
- [x] **Procedures** (`procedure <name>(args):` blocks → `CREATE PROCEDURE`; replace-on-change via body/security diff → `CREATE OR REPLACE`; grants, `revokePublic`, comments)

## Level 3: Architecture & Security
Features necessary for scaling, multi-tenancy, and advanced security models.

- [x] **Table Partitioning** (Parent `partitionBy: {type: range|list|hash, columns: [...]}`; children as separate tables with `partitionOf` + `forValues` (from/to, in, modulus/remainder) or `default: true` → `CREATE TABLE ... PARTITION OF`; children auto-ordered after parent; create-only, no ATTACH/DETACH or bound alteration)
- [x] **Row Level Security (RLS)** (`rowLevelSecurity: true` + named `policies:` with for/to/using/withCheck; enable-only, policies matched by name)
- [x] **Grants & Privileges** (`GRANT` / `REVOKE` on tables, functions, schemas; `revokePublic` for functions)
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
| `internal/schema` | Map/list/schema-block YAML formats; column attributes (nullable, notNull, default, unique, primaryKey, identity, using); primary keys; foreign keys; indexes; check/unique/exclude constraints; triggers; extensions; enum types; composite types; functions (security, volatility, strict); procedures (args, security, set, body, grants, `revokePublic`, `dependsOn`, multi-file merge); views; materialized views; sequences (options, defaults, `dependsOn`, multi-file merge); domains (options, defaults, `dependsOn`, multi-file merge); grants (table/function/schema, `revokePublic`); roles (options, `inRoles`, defaults, multi-file merge); RLS + policies; comments (all object types); partitioning (partitionBy explicit + shorthand, partitionOf range/list/hash/default bounds, qualified parents, parent-before-child topological order); `dependsOn`; topological sort; `LoadAndMerge` including missing-file tolerance; `qualify` helper |
| `internal/diff` | CREATE TABLE SQL; column order preservation; primary key (table-level and column-level); foreign keys with ON DELETE; unique/non-unique indexes; auto-named indexes; check/unique/exclude constraints; trigger create/skip-if-exists incl. deferred constraint triggers; extension create/skip-if-exists; enum/composite type create/skip-if-exists; function create/skip-if-exists/replace-on-change (body, volatility) with security+volatility; enum add-value (positioned + appended, skip-if-same); procedure create/skip-if-exists/replace-on-change (body, security) with args/security-definer/set-clause/schema-creation/comment/revoke-public/grants; view create/skip-if-exists/replace-flag; materialized view create/skip-if-exists; sequence create/skip-if-exists/options/owned-by/schema-creation/comment/ordering-before-dependent-table; domain create/skip-if-exists/options/named-check/collate/schema-creation/comment/no-type-skip/ordering-before-dependent-table; grants (grant missing, revoke removed, skip live, `revokePublic`); role create/skip-if-exists/options/ordering-before-schemas/membership grant+skip/no-drop/comments; RLS enable/skip; policy create/skip/drop-on-removal; comments (emit/skip-if-same/update-if-changed, quote escaping); custom schema creation; public schema not created; add column; drop column (safe vs unsafe); column alterations (type change unsafe-gated/skipped-when-safe/`using` clause, equivalent-type and equivalent-default normalization, serial-vs-integer skip, set/drop default, nextval drop guard, set/drop not null, PK drop-not-null guard, identity never altered, no-change no-SQL); identity columns (always/by-default in CREATE TABLE and ADD COLUMN, skip-if-exists, default suppressed); partitioned table create/skip-if-exists incl. expression keys; partition child create/skip-if-exists (range/list/hash/default bounds, minvalue/numeric literals, parent-first ordering); `Render`; `pqIdent`; `normalizeFunctionSignature`; `PlanDiff.Summary` |
| `internal/cli` | `slugify`; `nextMigrationNumber`; checksum parse and body |

*Note: When building out unsupported features, ensure both the YAML model in `schema.go` and the introspection/diffing logic in `diff.go` are updated, and add corresponding tests.*
