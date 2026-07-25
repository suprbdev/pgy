# Schema YAML Format Documentation

This document describes the structure and format of the YAML schema files used by `pgy`.

## Overview

Schema files define database objects including tables, columns, types, functions, extensions, views, materialized views, and sequences. `pgy` diffs them against a live PostgreSQL database and generates the SQL to bring the database in sync.

## YAML Formats

Three formats are supported and can be mixed across files merged via `pgy diff`.

### Format 1: `schema <name>:` blocks (recommended)

The most expressive format. Supports tables, functions, types, views, materialized views, and sequences. Column order in `CREATE TABLE` is preserved.

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

## Merging across files

`pgy diff` accepts multiple schema files and merges them before diffing. When
the same table is declared in more than one file:

- **Columns** are combined; a column re-declared in a later file replaces the
  earlier declaration.
- **Indexes, foreign keys, constraints, and triggers** are combined by name:
  entries with new names are appended, and a same-named entry from a later
  file replaces the earlier one.
- **`dependsOn`** entries are appended (duplicates ignored).
- **`grants`, `policies`, `comment`**, and partition settings from a later
  file replace the earlier values when present.
- **Extensions** are deduplicated by name.
- **Types, functions, procedures, views, sequences, and domains** are keyed by
  name; a later declaration replaces an earlier one.

This enables composable schema files: one file can declare a base table and a
supplementary "link" file can add a foreign-key column pointing at another
module's table (see `examples/modules/` and `examples/links/`).

```yaml
# post.yml — standalone module
schema public:
  table post:
    columns:
      id: { type: uuid, primaryKey: true, default: gen_random_uuid() }
      title: { type: text, notNull: true }

# post_author.yml — link file; include together with post.yml and user.yml
schema public:
  table post:
    columns:
      author_id: { type: uuid, nullable: true }
    foreignKeys:
      post_author_id_fkey:
        columns: [author_id]
        references:
          table: public.user
          columns: [id]
        onDelete: set null
    dependsOn:
      - table public.user
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
| `default` | string, boolean, or number | SQL default expression (e.g. `NOW()`, `uuid_generate_v4()`). Bare YAML scalars like `false` or `0` are coerced to their SQL literal form |
| `unique` | boolean | Adds a UNIQUE constraint on this column |
| `primaryKey` | boolean | Marks column as part of the primary key |
| `identity` | string or boolean | `always` or `byDefault` emits `GENERATED ALWAYS AS IDENTITY` / `GENERATED BY DEFAULT AS IDENTITY`. Bare `true` means `always`. Applied only when the column is created (`CREATE TABLE` or `ADD COLUMN`) — existing columns are never altered. When set, `default` is ignored (PostgreSQL forbids both) |
| `using` | string | Cast expression for type changes on existing columns, emitted as `ALTER COLUMN ... TYPE ... USING <expr>`. Only meaningful together with `--unsafe` (see below); ignored at CREATE time |
| `grants` | map | Role → column privilege list (`select`, `insert`, `update`, `references`). See [Column-Level Grants](#column-level-grants) |

```yaml
schema public:
  table orders:
    columns:
      id:
        type: bigint
        identity: always
        primaryKey: true
      legacy_no:
        type: int
        identity: byDefault   # allows explicit inserts to override
```

#### Column alterations

For columns that already exist in the live database, `pgy diff` reconciles their attributes:

- **Default**: a differing `default` emits `SET DEFAULT`; removing `default` from YAML emits `DROP DEFAULT`. Comparison strips the casts PostgreSQL adds to live defaults, so `'active'` matches the live `'active'::text`. `nextval(...)` defaults (serial columns) are never dropped.
- **Nullability**: emits `SET NOT NULL` / `DROP NOT NULL` to match `nullable`/`notNull`. Primary-key and identity columns never emit `DROP NOT NULL`.
- **Type** (requires `--unsafe`): a differing type emits `ALTER COLUMN ... TYPE`, with `using` appended as the cast expression when set. Types are compared normalized — `varchar(255)` matches the live `character varying(255)`, `int`/`integer`/`int4` are equivalent, and `serial` matches its live `integer` + `nextval` form. Type changes can rewrite the table or fail on incompatible casts, hence the `--unsafe` gate.
- **Identity** is create-only: identity columns are never altered.

```yaml
schema public:
  table orders:
    columns:
      amount:
        type: numeric(10,2)          # was: text
        using: amount::numeric(10,2) # cast for ALTER ... TYPE (with --unsafe)
```

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
    unique: true            # optional, default false
    using: gist             # optional index method: btree (default) | gist | gin | brin | hash | spgist
    where: deleted_at is null   # optional partial index predicate
```

If `name` is omitted, an auto-name is derived from the table and column names.

`using: btree` is the PostgreSQL default and is omitted from the generated SQL. The `where` expression is emitted verbatim (wrapped in parentheses), so any valid predicate works.

Example — a PostGIS spatial index and a partial unique index:

```yaml
indexes:
  idx_places_geom:
    columns: [geom]
    using: gist
  uq_active_email:
    columns: [email]
    unique: true
    where: deleted_at is null
```

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
    when: old.updated_at is distinct from new.updated_at   # optional WHEN (condition) guard
```

Triggers are compared against the live database by **definition**, not just name: if a live trigger's `pg_get_triggerdef()` no longer matches the desired trigger (timing, events, level, `when:` guard, procedure, deferrability), it is dropped and recreated in the same migration. The comparison normalizes cosmetic differences (case, quoting, parentheses, type casts, `public.` qualifiers, event order, `EXECUTE FUNCTION` vs `EXECUTE PROCEDURE`), so an unchanged trigger emits no SQL.

The optional `when:` expression is emitted verbatim as `WHEN (<expression>)` between `FOR EACH ROW` and `EXECUTE`. Use it to guard row triggers, e.g. a supersede trigger that must not fire for rows older than the current one (`when: old.created_at <= new.created_at`) — with `AFTER ... FOR EACH ROW` triggers on multi-row inserts, every row's trigger sees the fully-inserted statement result, so unguarded triggers can outdate each other.

#### Trigger creation order

`CREATE TRIGGER` statements are always emitted **after** all table and function creates. A trigger function whose body references the trigger's own table (required for `language: sql` functions, whose bodies are validated at `CREATE` time) can therefore declare `dependsOn: [table <its_table>]` safely — the table does **not** need a reciprocal `dependsOn` for the function. Any `dependsOn` edge pointing at a `returns: trigger` function is ignored during ordering (nothing can reference one at `CREATE` time), so such declarations are harmless but redundant. Genuine dependency cycles are a hard error in `pgy diff`, reported with the concrete cycle path.

```yaml
schema public:
  table events:
    columns:
      id: { type: bigint, primaryKey: true }
    triggers:
      trg_supersede:
        timing: after
        events: [insert]
        level: row
        procedure: public.supersede()
  function supersede():
    returns: trigger
    language: sql          # body validated at CREATE — table must exist first
    dependsOn:
      - table public.events
    body: ...
```

#### Constraint Triggers

Set `constraint: true` for `CREATE CONSTRAINT TRIGGER`. Constraint triggers are always emitted as `AFTER ... FOR EACH ROW` (`timing`/`level` are ignored). `initiallyDeferred: true` implies `DEFERRABLE` — use it to defer enforcement to commit time, e.g. inserting a row and its required junction rows in one transaction:

```yaml
triggers:
  trg_check_requirements:
    constraint: true
    initiallyDeferred: true    # DEFERRABLE INITIALLY DEFERRED
    events: [insert, update]
    procedure: app.check_entry_requirements()
```

| Property | Type | Description |
|----------|------|-------------|
| `when` | string | `WHEN (<expression>)` row guard, emitted verbatim. Works on plain and constraint triggers |
| `constraint` | boolean | Emit `CREATE CONSTRAINT TRIGGER` |
| `deferrable` | boolean | `DEFERRABLE` (initially immediate) |
| `initiallyDeferred` | boolean | `DEFERRABLE INITIALLY DEFERRED`; check runs at commit |

### Constraints

```yaml
constraints:
  <constraint_name>:
    type: check        # check | unique | exclude
    expression: "col > 0"       # for check or exclude
    def: "col > 0"              # alias for expression
    columns: [col1, col2]       # for unique
```

#### Constraint alterations

Constraints (including foreign keys) are matched against the live database by name, and their definitions are compared via `pg_get_constraintdef()`. A constraint whose definition changed is dropped and re-added — since the re-add revalidates existing rows (and can fail on them), this requires `--unsafe`. Without `--unsafe`, a redefined constraint emits no SQL.

Definition comparison is normalized: casts (`''::text`), parentheses, whitespace, identifier quoting, and `public.` qualifiers are ignored, so a live `CHECK ((email <> ''::text))` matches a YAML `expression: "email <> ''"` without churn. FK clauses not modeled in YAML (`ON UPDATE`, `MATCH`, `DEFERRABLE`) make a live constraint compare unequal, and the re-added constraint will not include them. Primary keys are matched by existence only and never dropped.

### Partitioning

Declarative partitioning: a parent table declares `partitionBy`, and each partition is its own table with `partitionOf` plus a bound (`forValues`) or `default: true`.

Parent table:

```yaml
schema public:
  table measurement:
    columns:
      logdate: { type: date }
      value:   { type: numeric }
    partitionBy:
      type: range          # range | list | hash
      columns: [logdate]
    # shorthand: partitionBy: { range: [logdate] }
```

Partition children (columns are inherited from the parent — do not redeclare them):

```yaml
schema public:
  table measurement_y2024:
    partitionOf: measurement
    forValues:
      from: ["2024-01-01"]
      to: ["2025-01-01"]

  table events_eu:
    partitionOf: events
    forValues:
      in: [de, fr]

  table users_p0:
    partitionOf: users
    forValues:
      modulus: 4
      remainder: 0

  table measurement_default:
    partitionOf: measurement
    default: true          # DEFAULT partition, no forValues
```

| Property | Type | Description |
|----------|------|-------------|
| `partitionBy.type` | string | `range`, `list`, or `hash`. Shorthand: use the strategy as the key (`range: [col]`) |
| `partitionBy.columns` | list | Partition key columns. Values containing spaces, parens, or operators are treated as expressions and emitted as-is |
| `partitionOf` | string | Parent table name. Unqualified names take the child's schema; the child is automatically ordered after its parent |
| `forValues.from` / `forValues.to` | list | Range bounds. `minvalue` / `maxvalue` are emitted as keywords; numbers, booleans, and `null` stay bare; everything else is quoted as a string literal |
| `forValues.in` | list | List bound values (same literal rules) |
| `forValues.modulus` / `forValues.remainder` | int | Hash bound |
| `default` | boolean | Emit a `DEFAULT` partition instead of `FOR VALUES` |

Notes:

- Both parents and partitions are create-only and skipped when already live; partition bounds of existing partitions are never altered, and `pgy` never emits `ATTACH`/`DETACH PARTITION`.
- Indexes, foreign keys, constraints, and triggers declared on a partition child apply to that child as usual.
- PostgreSQL requires a partitioned table's primary key to include the partition key columns.

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

For an existing enum, new labels are added with `ALTER TYPE ... ADD VALUE`, positioned `BEFORE` the next label that already exists so the declared order is preserved. Removing or reordering existing labels is not supported (postgres cannot drop enum values) and is silently ignored. Note: an added enum value cannot be used in the same transaction that adds it.

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

Attribute order in `CREATE TYPE ... AS (...)` follows YAML declaration order (relevant for positional `ROW(...)::type` casts).

---

## Function Definitions

Functions are defined inside `schema <name>:` blocks. The key format is `function <name>(<args>):`.

Argument types may be schema-qualified, e.g. `function fn(t public.my_type):` or `function fn(t app.my_type):`. An unqualified type is treated as `public` — `fn(t my_type)` and `fn(t public.my_type)` refer to the same function. Note: if the connection's `search_path` includes a non-`public` schema, introspection may report that schema's types unqualified, which will not match a qualified declaration; keep non-`public` types qualified in YAML and off the `search_path`.

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
| `immutable` | boolean | Marks function `IMMUTABLE` |
| `strict` | boolean | Adds `STRICT` attribute |
| `set` | map | `SET` configuration options (e.g. `search_path`) |
| `body` | string | Function body (dollar-quoted in output SQL) |
| `grants` | map | Role → privilege list. See [Grants](#grants) |
| *(replace-on-change)* | — | Existing functions are compared against the live definition (body via `prosrc`, volatility, security, strict). On change, `CREATE OR REPLACE FUNCTION` is emitted. Signature or return type changes are not supported — rename the function instead |
| `revokePublic` | boolean | Emit `REVOKE ALL ... FROM PUBLIC` (security-definer pattern) |
| `dependsOn` | list | See [Dependencies](#dependencies) |


---

## Procedure Definitions

Procedures (PostgreSQL 11+, invoked with `CALL`) are defined inside `schema <name>:` blocks using `procedure <name>(<args>):` keys. Argument types may be schema-qualified; unqualified types are treated as `public` (same rules as functions). Unlike functions they have no return type, volatility, or strictness. Existing procedures are compared against the live definition (body via `prosrc`, security); on change, `CREATE OR REPLACE PROCEDURE` is emitted.

```yaml
schema public:
  procedure archive_user(user_id bigint):
    language: plpgsql
    security: definer    # definer | invoker
    set:
      search_path: public
    body: |
      BEGIN
        UPDATE public.users SET archived = true WHERE id = user_id;
      END;
    grants:
      batch_role: [execute]
    revokePublic: true
    comment: "archives a user"
    dependsOn:
      - table public.users
```

| Property | Type | Description |
|----------|------|-------------|
| `language` | string | e.g. `plpgsql`, `sql` |
| `security` | string | `definer` or `invoker` |
| `set` | map | `SET` configuration options (e.g. `search_path`) |
| `body` | string | Procedure body (dollar-quoted in output SQL) |
| `grants` | map | Role → privilege list. See [Grants](#grants) |
| `revokePublic` | boolean | Emit `REVOKE ALL ... FROM PUBLIC` (security-definer pattern) |
| `comment` | string | `COMMENT ON PROCEDURE` (diffed against live) |
| `dependsOn` | list | See [Dependencies](#dependencies) |

Signature changes are not supported — rename the procedure instead.

---

## Comments

All object types accept a `comment:` key emitted as `COMMENT ON`. Comment text is preserved exactly (multi-line included), so PostGraphile smart tags (`@behavior`, `@name`, ...) work as-is.

```yaml
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
    comment: "computes things"
    body: select 1
```

Supported on: schemas, tables, columns, types, functions, views, materialized views, sequences.

Behavior:

- Emitted when the desired comment differs from the live comment; skipped when identical (idempotent).
- An absent or empty `comment:` is unmanaged — existing comments are never cleared. To clear a comment, run `COMMENT ON ... IS NULL` manually.
- Single quotes are escaped automatically.

## Row Level Security

Tables accept `rowLevelSecurity: true` and a `policies:` map of policy name → spec.

```yaml
schema app:
  table orders:
    columns:
      id:
        type: bigint
      member_id:
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
```

| Property | Type | Description |
|----------|------|-------------|
| `rowLevelSecurity` | boolean | Emits `ALTER TABLE ... ENABLE ROW LEVEL SECURITY` when not already enabled. Never disables (forward-only) |
| `policies.<name>.for` | string | `select` \| `insert` \| `update` \| `delete` \| `all`. Omit for `ALL` |
| `policies.<name>.to` | string or list | Role(s). Omit for all roles |
| `policies.<name>.using` | string | `USING` expression |
| `policies.<name>.withCheck` | string | `WITH CHECK` expression |

Behavior:

- Policies are matched **by name only** — changes to an existing policy's expressions are not detected. Rename the policy to replace it.
- A present `policies:` block is authoritative: live policies not listed are dropped. Tables without a `policies:` block are unmanaged.
- `ENABLE ROW LEVEL SECURITY` and `CREATE POLICY` are emitted after all object creates/alters, so policy expressions may freely reference functions created in the same plan.

## Roles

Roles are cluster-level objects declared in a top-level `roles:` map (not inside a `schema <name>:` block). Each entry is created with `CREATE ROLE` if missing from the live database and skipped otherwise. The statement is wrapped in a `DO $$ ... $$` block guarded by a `pg_roles` lookup, so it is a no-op if the role already exists in the cluster (roles survive database re-creation, so `diff --from-empty` cannot assume they are absent). Forward-only: existing roles are never altered or dropped, and removed memberships are never revoked.

```yaml
roles:
  readonly: {}
  app_user:
    login: true
    inherit: false
    connectionLimit: 10
    inRoles: [readonly]
    comment: "application login role"
  migrator:
    login: true
    createdb: true
    createrole: true
```

Behavior:

- Role creates are emitted **first** in the plan, before schemas and all other objects, so grants, policies (`to:`), and memberships can reference them.
- `inRoles` memberships are reconciled additively: `GRANT <parent> TO <role>` is emitted for each membership missing from live (`pg_auth_members`).
- Passwords are intentionally unsupported — secrets do not belong in schema YAML. Set passwords out of band (`ALTER ROLE ... PASSWORD`).
- `comment` is diffed against `pg_shdescription` like other object comments.

| Property | Type | Description |
|----------|------|-------------|
| `login` | boolean | `LOGIN` (default `false` = nologin) |
| `superuser` | boolean | `SUPERUSER` |
| `createdb` | boolean | `CREATEDB` |
| `createrole` | boolean | `CREATEROLE` |
| `replication` | boolean | `REPLICATION` |
| `bypassRLS` | boolean | `BYPASSRLS` |
| `inherit` | boolean | Set `false` to emit `NOINHERIT` (default inherits) |
| `connectionLimit` | integer | `CONNECTION LIMIT n`; omit for unlimited |
| `inRoles` | list | Parent roles; each emits `GRANT <parent> TO <role>` when missing |
| `comment` | string | `COMMENT ON ROLE` |

---

## Grants

Tables, views, materialized views, functions, schemas, and individual columns accept a `grants:` map of role name → privilege list. Privileges are lowercased in output SQL; roles can be declared in a top-level [`roles:`](#roles) block or must already exist. View grants are emitted as `GRANT ... ON TABLE` (views are relations; this matches pg_dump).

```yaml
schema app:
  grants:
    kickly_member: [usage]
  table orders:
    columns:
      id:
        type: bigint
    grants:
      kickly_member: [select, insert]
      kickly_admin: [select, insert, update, delete]
  function secret_fn():
    returns: int
    language: sql
    security: definer
    revokePublic: true
    grants:
      kickly_member: [execute]
    body: select 1
```

Behavior:

- A present `grants:` block is **authoritative** for that object: missing privileges are granted; live privileges for roles not listed (or privileges removed from a role's list) are revoked. Object owners are never touched.
- Objects **without** a `grants:` block are unmanaged — no grants or revokes are emitted.
- `PUBLIC` is never auto-revoked by a `grants:` block. For functions, set `revokePublic: true` to revoke the default `PUBLIC` `EXECUTE` (recommended for `security: definer` functions). It is emitted for new functions and whenever the live database still shows `PUBLIC` execute.
- Grant/revoke statements are emitted after all object creation and FK alters.

### Column-Level Grants

A column can carry its own `grants:` block. It emits PostgreSQL column-privilege syntax and is reconciled against the live column ACLs (`pg_attribute.attacl`) with the same authoritative semantics as object-level grants:

```yaml
table users:
  columns:
    id:
      type: bigint
    email:
      type: text
      grants:
        reporting: [select]
        support: [select, update]
```

Generates:

```sql
grant select ("email") on table "public"."users" to "reporting";
grant select ("email"), update ("email") on table "public"."users" to "support";
```

- Valid column privileges are `select`, `insert`, `update`, and `references` (PostgreSQL restriction; pgy passes privileges through verbatim).
- A column **with** a `grants:` block is authoritative for that column: missing privileges are granted, extra live column privileges are revoked. Columns without a block are unmanaged.
- `PUBLIC` column grants are never auto-revoked.
- Column grants are independent of table-level grants: a table-level `select` already covers every column, so column grants are typically used *instead of* a table-level privilege to restrict access to a subset of columns.

| Property | Type | Description |
|----------|------|-------------|
| `grants` | map | Role name → list of privileges (e.g. `select`, `insert`, `execute`, `usage`, `create`; on columns: `select`, `insert`, `update`, `references`) |
| `revokePublic` | boolean | Functions only. Revoke default `PUBLIC` execute |

---

## View Definitions

Views are defined inside `schema <name>:` blocks using `view <name>:` keys. They are created with `CREATE OR REPLACE VIEW` and skipped if already present in the live database. Set `replace: true` to always emit `CREATE OR REPLACE VIEW` (live view definitions cannot be reliably text-compared because postgres rewrites the stored query, so replacement is opt-in).

```yaml
schema public:
  view active_users:
    query: "select id, email from users where active = true"
    replace: true   # re-emit CREATE OR REPLACE on every diff
    dependsOn:
      - table public.users
    grants:
      reporting: [select]
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
| `replace` | boolean | Plain views only. Always emit `CREATE OR REPLACE VIEW` |
| `dependsOn` | list | See [Dependencies](#dependencies) |
| `grants` | map | Role → privilege list. Views and materialized views share table ACL semantics (`GRANT ... ON TABLE`). See [Grants](#grants) |

---

## Sequence Definitions

Sequences are defined inside `schema <name>:` blocks using `sequence <name>:` keys. Created with `CREATE SEQUENCE IF NOT EXISTS`; existing sequences are never altered or dropped (forward-only). Only the options set in YAML are emitted — PostgreSQL defaults apply to the rest.

```yaml
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
    dependsOn:
      - table public.orders
```

| Property | Type | Description |
|----------|------|-------------|
| `as` | string | Data type: `smallint`, `integer`, or `bigint` (default `bigint`) |
| `increment` | integer | `INCREMENT BY` step (may be negative for descending sequences) |
| `minValue` | integer | `MINVALUE` |
| `maxValue` | integer | `MAXVALUE` |
| `start` | integer | `START WITH` value |
| `cache` | integer | Number of values preallocated per session |
| `cycle` | boolean | Wrap around when the limit is reached |
| `ownedBy` | string | `table.column` (or `schema.table.column`) — sequence is dropped with the column. Requires the table to exist; add a matching `dependsOn` |
| `comment` | string | `COMMENT ON SEQUENCE` (diffed against live) |
| `dependsOn` | list | See [Dependencies](#dependencies) |

To use a sequence as a column default, reference it with `nextval` and declare the ordering:

```yaml
schema public:
  sequence order_number_seq: {}

  table orders:
    dependsOn:
      - sequence public.order_number_seq
    columns:
      order_number:
        type: bigint
        default: nextval('public.order_number_seq')
```

---

## Domain Definitions

Domains are defined inside `schema <name>:` blocks using `domain <name>:` keys. A domain is a named base type with optional default, `NOT NULL`, and a `CHECK` constraint (the value under test is referenced as `VALUE`). Created with `CREATE DOMAIN`; existing domains are never altered or dropped (forward-only). Comments are diffed against live.

```yaml
schema public:
  domain email:
    type: text
    notNull: true
    check: "value ~ '^[^@]+@[^@]+$'"
    constraintName: email_format
    comment: "validated email address"

  table users:
    dependsOn:
      - domain public.email
    columns:
      email:
        type: public.email
```

| Property | Type | Description |
|----------|------|-------------|
| `type` | string | Underlying data type (required; `as` is accepted as an alias) |
| `collate` | string | `COLLATE` clause (collation identifier) |
| `default` | string | `DEFAULT` expression |
| `notNull` | boolean | `NOT NULL` constraint (`nullable: false` also accepted) |
| `check` | string | `CHECK` expression; use `value` to reference the value being tested |
| `constraintName` | string | Optional name for the `CHECK` constraint |
| `comment` | string | `COMMENT ON DOMAIN` (diffed against live) |
| `dependsOn` | list | See [Dependencies](#dependencies) |

Tables using a domain as a column type must declare `dependsOn: [domain <schema.name>]` so the domain is created first.

---

## Dependencies

All object types support `dependsOn` to control creation order. The topological sort resolves these before generating SQL.

| Prefix | Example |
|--------|---------|
| `table <schema.table>` | `table public.users` |
| `extension <name>` | `extension citext` |
| `type <schema.type>` | `type public.auth_role` |
| `function <schema.fn>(args)` | `function public.set_updated_at()` |
| `procedure <schema.proc>(args)` | `procedure public.archive_user` |
| `view <schema.view>` | `view public.active_users` |
| `materialized view <schema.view>` | `materialized view public.user_stats` |
| `sequence <schema.seq>` | `sequence public.order_number_seq` |
| `domain <schema.domain>` | `domain public.email` |
| `schema <name>` | `schema private` (informational only, not resolved) |

The sort is **deterministic**: entities with no ordering constraint between them are emitted by kind (types → domains → sequences → functions → procedures → tables → views), then by name, so identical YAML always produces an identical buffer.

Two kinds of `dependsOn` are never needed (and can only manufacture cycles):

- **On trigger functions** (`returns: trigger`). Triggers are emitted after every other create, and no `CREATE` statement can call a trigger function, so edges pointing at trigger-returning functions are silently ignored. Declare `dependsOn` the other way if the trigger function's body needs a table (required for `language: sql`; `plpgsql` bodies are not validated at `CREATE`).
- **On functions referenced only by row-level-security policies or grants.** `CREATE POLICY`, `GRANT`, and `COMMENT` statements are all emitted after every create, so a table never needs `dependsOn` for functions its policies call.

A genuine dependency cycle is a hard error; the message shows one concrete cycle path (`a -> b -> a`) to remove.

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
