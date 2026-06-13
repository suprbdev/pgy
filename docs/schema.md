# Schema YAML Format Documentation

This document describes the structure and format of the YAML schema files used by `pgy`.

## Overview

Schema files define database objects including tables, columns, types, functions, extensions, views, and materialized views. `pgy` diffs them against a live PostgreSQL database and generates the SQL to bring the database in sync.

## YAML Formats

Three formats are supported and can be mixed across files merged via `pgy diff`.

### Format 1: `schema <name>:` blocks (recommended)

The most expressive format. Supports tables, functions, types, views, and materialized views. Column order in `CREATE TABLE` is preserved.

```yaml
schema public:
  table users:
    columns:
      id:
        type: uuid
        primaryKey: true
      email:
        type: citext
        notNull: true
    primaryKey:
      users_pkey:
        columns: [id]
    indexes:
      users_email_idx:
        columns: [email]
        unique: true
    foreignKeys:
      users_org_fkey:
        columns: [org_id]
        references:
          table: public.orgs
          columns: [id]
        onDelete: cascade
    constraints:
      users_email_check:
        type: check
        expression: "length(email) > 0"
    triggers:
      set_updated_at:
        timing: before
        events: [update]
        level: row
        procedure: public.set_updated_at()
    dependsOn:
      - table public.orgs

  function set_updated_at():
    returns: trigger
    language: plpgsql
    body: |
      BEGIN
        NEW.updated_at = NOW();
        RETURN NEW;
      END;

  type status:
    type: enum
    labels:
      - active
      - inactive

  view active_users:
    query: "select id, email from users where active = true"
    dependsOn:
      - table public.users

  materialized view user_stats:
    query: "select count(*) as total from users"
    dependsOn:
      - table public.users
```

### Format 2: `tables:` map

Supports tables only. Column order in `CREATE TABLE` is **not** preserved (sorted alphabetically).

```yaml
tables:
  public.users:
    columns:
      id:
        type: uuid
        primaryKey: true
      email:
        type: text
```

### Format 3: `tables:` list

Supports tables only. Useful when table names need an explicit `schema:` field.

```yaml
tables:
  - name: users
    schema: public
    columns:
      - name: id
        type: uuid
      - name: email
        type: text
        nullable: true
```

---

## Extensions

Defined at the top level of any file:

```yaml
extensions:
  - name: pgcrypto
    ifNotExists: true
  - name: citext
    ifNotExists: true
    dependsOn:
      - schema public
```

| Property | Type | Description |
|----------|------|-------------|
| `name` | string | Extension name |
| `ifNotExists` | boolean | Adds `IF NOT EXISTS` to the SQL statement |
| `dependsOn` | list | See [Dependencies](#dependencies) |

---

## Tables

### Columns

| Property | Type | Description |
|----------|------|-------------|
| `type` | string | PostgreSQL data type (e.g. `text`, `uuid`, `int`, `jsonb`) |
| `nullable` | boolean | `true` allows NULL. Default: `false` (NOT NULL) |
| `notNull` | boolean | `true` means NOT NULL. Inverse alias for `nullable` |
| `default` | string | SQL default expression (e.g. `NOW()`, `uuid_generate_v4()`) |
| `unique` | boolean | Adds a UNIQUE constraint on this column |
| `primaryKey` | boolean | Marks column as part of the primary key |

### Primary Key

Can be declared at the column level (`primaryKey: true`) or as a table-level block:

```yaml
primaryKey:
  <constraint_name>:
    columns: [col1, col2]
```

### Indexes

```yaml
indexes:
  <index_name>:
    columns: [col1, col2]
    unique: true   # optional, default false
```

If `name` is omitted, an auto-name is derived from the table and column names.

### Foreign Keys

```yaml
foreignKeys:
  <constraint_name>:
    columns: [col1]
    references:
      table: <schema.table>
      columns: [col1]
    onDelete: cascade   # cascade | restrict | set null | set default
```

### Triggers

```yaml
triggers:
  <trigger_name>:
    timing: before     # before | after
    events: [insert, update, delete]
    level: row         # row | statement
    procedure: public.set_updated_at()
```

### Constraints

```yaml
constraints:
  <constraint_name>:
    type: check        # check | unique | exclude
    expression: "col > 0"       # for check or exclude
    def: "col > 0"              # alias for expression
    columns: [col1, col2]       # for unique
```

---

## Type Definitions

Types are defined inside `schema <name>:` blocks.

### Enum

```yaml
schema public:
  type status:
    type: enum
    labels:
      - active
      - inactive
      - deleted
    dependsOn:
      - <dependency>
```

### Composite

```yaml
schema public:
  type jwt:
    type: composite
    attributes:
      role:
        type: public.auth_role
      exp:
        type: bigint
    dependsOn:
      - type public.auth_role
```

---

## Function Definitions

Functions are defined inside `schema <name>:` blocks. The key format is `function <name>(<args>):`.

```yaml
schema public:
  function set_updated_at():
    returns: trigger
    language: plpgsql
    security: definer    # definer | invoker
    stable: true         # stable: true  OR  volatile: true
    strict: true
    set:
      search_path: 'private, public'
    body: |
      BEGIN
        NEW.updated_at = NOW();
        RETURN NEW;
      END;
    dependsOn:
      - <dependency>
```

| Property | Type | Description |
|----------|------|-------------|
| `returns` | string | Return type |
| `language` | string | e.g. `plpgsql`, `sql` |
| `security` | string | `definer` or `invoker` |
| `stable` | boolean | Marks function `STABLE` |
| `volatile` | boolean | Marks function `VOLATILE` |
| `strict` | boolean | Adds `STRICT` attribute |
| `set` | map | `SET` configuration options (e.g. `search_path`) |
| `body` | string | Function body (dollar-quoted in output SQL) |
| `dependsOn` | list | See [Dependencies](#dependencies) |

> **Note:** `immutable` volatility is not yet supported.

---

## View Definitions

Views are defined inside `schema <name>:` blocks using `view <name>:` keys. They are created with `CREATE OR REPLACE VIEW` and skipped if already present in the live database.

```yaml
schema public:
  view active_users:
    query: "select id, email from users where active = true"
    dependsOn:
      - table public.users
```

### Materialized Views

Use `materialized view <name>:` keys. Created with `CREATE MATERIALIZED VIEW IF NOT EXISTS`. Skipped on subsequent runs (no `REFRESH` support).

```yaml
schema public:
  materialized view user_stats:
    query: "select count(*) as total, status from users group by status"
    dependsOn:
      - table public.users
```

| Property | Type | Description |
|----------|------|-------------|
| `query` | string | The `SELECT` statement defining the view |
| `dependsOn` | list | See [Dependencies](#dependencies) |

---

## Dependencies

All object types support `dependsOn` to control creation order. The topological sort resolves these before generating SQL.

| Prefix | Example |
|--------|---------|
| `table <schema.table>` | `table public.users` |
| `extension <name>` | `extension citext` |
| `type <schema.type>` | `type public.auth_role` |
| `function <schema.fn>(args)` | `function public.set_updated_at()` |
| `view <schema.view>` | `view public.active_users` |
| `materialized view <schema.view>` | `materialized view public.user_stats` |
| `schema <name>` | `schema private` (informational only, not resolved) |

---

## Example Schema

```yaml
extensions:
  - name: pgcrypto
    ifNotExists: true
  - name: uuid-ossp
    ifNotExists: true
  - name: citext
    ifNotExists: true

schema public:
  type auth_role:
    type: enum
    labels:
      - anonymous
      - member
      - admin

  type jwt:
    type: composite
    attributes:
      role:
        type: public.auth_role
      exp:
        type: bigint
      person_id:
        type: uuid
    dependsOn:
      - type public.auth_role

  function set_updated_at():
    returns: trigger
    language: plpgsql
    security: definer
    volatile: true
    body: |
      BEGIN
        NEW.updated_at = NOW();
        RETURN NEW;
      END;

  view person_summary:
    query: "select id, display_name, created_at from app.person"
    dependsOn:
      - table app.person

schema app:
  table person:
    columns:
      id:
        type: uuid
        primaryKey: true
        default: uuid_generate_v4()
      display_name:
        type: text
        notNull: true
      created_at:
        type: timestamptz
        default: NOW()
        notNull: true
      updated_at:
        type: timestamptz
        default: NOW()
        notNull: true
    primaryKey:
      person_pkey:
        columns: [id]
    indexes:
      person_display_name_idx:
        columns: [display_name]
    triggers:
      set_updated_at:
        timing: before
        events: [update]
        level: row
        procedure: public.set_updated_at()
    dependsOn:
      - function public.set_updated_at

schema private:
  table account:
    columns:
      person_id:
        type: uuid
        primaryKey: true
      username:
        type: citext
        unique: true
      password_hash:
        type: text
        notNull: true
    foreignKeys:
      account_person_id_fkey:
        columns: [person_id]
        references:
          table: app.person
          columns: [id]
        onDelete: cascade
    dependsOn:
      - extension citext
      - table app.person
```
