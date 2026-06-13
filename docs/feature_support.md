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

## Level 2: Advanced Types & Logic
Features that encapsulate business logic, complex data structures, or procedural code.

- [x] **ENUM Types**
- [x] **Composite Types**
- [x] **Extensions** (`CREATE EXTENSION`)
- [x] **Functions** (PL/pgSQL, etc.)
- [x] **Triggers**
- [ ] **Views** (Standard `CREATE VIEW`)
- [ ] **Materialized Views**
- [ ] **Domains** (Types with optional constraints)
- [ ] **Procedures** (`CREATE PROCEDURE`, distinct from functions)

## Level 3: Architecture & Security
Features necessary for scaling, multi-tenancy, and advanced security models.

- [ ] **Table Partitioning** (Declarative partitioning bounds/lists)
- [ ] **Row Level Security (RLS)** (Enable/disable and `CREATE POLICY`)
- [ ] **Grants & Privileges** (`GRANT` / `REVOKE` on tables, functions, etc.)
- [ ] **Roles & Users** (`CREATE ROLE`)
- [ ] **Sequences** (Explicit `CREATE SEQUENCE` declarations)
- [ ] **Identity Columns** (e.g., `GENERATED ALWAYS AS IDENTITY`)

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
| `internal/schema` | Map/list/schema-block YAML formats; column attributes (nullable, notNull, default, unique, primaryKey); primary keys; foreign keys; indexes; check/unique/exclude constraints; triggers; extensions; enum types; composite types; functions (security, volatility, strict); `dependsOn`; topological sort; `LoadAndMerge` including missing-file tolerance; `qualify` helper |
| `internal/diff` | CREATE TABLE SQL; column order preservation; primary key (table-level and column-level); foreign keys with ON DELETE; unique/non-unique indexes; auto-named indexes; check/unique/exclude constraints; triggers; extension create/skip-if-exists; enum/composite type create/skip-if-exists; function create/skip-if-exists with security+volatility; custom schema creation; public schema not created; add column; drop column (safe vs unsafe); `Render`; `pqIdent`; `normalizeFunctionSignature`; `PlanDiff.Summary` |
| `internal/cli` | `slugify`; `nextMigrationNumber`; checksum parse and body |

*Note: When building out unsupported features, ensure both the YAML model in `schema.go` and the introspection/diffing logic in `diff.go` are updated, and add corresponding tests.*
