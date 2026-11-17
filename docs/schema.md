# Schema YAML Format Documentation

This document describes the structure and format of the YAML schema files used by `pgy`.

## Contents

- [Overview](#overview)
- [Editor Support](#editor-support)
- [YAML Formats](#yaml-formats)
  - [Format 1: `schema <name>:` blocks (recommended)](#format-1-schema-name-blocks-recommended)
  - [Format 2: `tables:` map](#format-2-tables-map)
  - [Format 3: `tables:` list](#format-3-tables-list)
  - [Names](#names)
  - [Name resolution](#name-resolution)
  - [How expressions are compared](#how-expressions-are-compared)
  - [How types are compared](#how-types-are-compared)
- [Merging across files](#merging-across-files)
- [Extensions](#extensions)
  - [Updating extensions](#updating-extensions)
  - [Objects an extension owns](#objects-an-extension-owns)
- [Database Settings](#database-settings)
  - [Parameters an extension defines](#parameters-an-extension-defines)
- [Tables](#tables)
  - [Columns](#columns)
  - [Primary Key](#primary-key)
  - [Indexes](#indexes)
  - [Foreign Keys](#foreign-keys)
  - [Triggers](#triggers)
  - [Rules](#rules)
  - [Constraints](#constraints)
  - [Partitioning](#partitioning)
  - [Inheritance](#inheritance)
  - [Typed tables](#typed-tables)
- [Foreign Tables](#foreign-tables)
- [Foreign-Data Wrappers](#foreign-data-wrappers)
- [Views](#views)
  - [Materialized Views](#materialized-views)
- [Types](#types)
  - [Enum](#enum)
  - [Composite](#composite)
  - [Range](#range)
- [Domains](#domains)
- [Sequences](#sequences)
- [Functions](#functions)
  - [Transforms](#transforms)
- [Aggregates](#aggregates)
- [Procedures](#procedures)
- [Languages](#languages)
- [Collations](#collations)
- [Text Search](#text-search)
- [Row Level Security](#row-level-security)
- [Roles](#roles)
- [Grants](#grants)
  - [Ownership](#ownership)
  - [Default Privileges](#default-privileges)
  - [Column-Level Grants](#column-level-grants)
- [Comments](#comments)
- [Publications](#publications)
- [Event Triggers](#event-triggers)
- [Reference Data](#reference-data)
- [Renames](#renames)
- [Dependencies](#dependencies)
- [Example Schema](#example-schema)

## Overview

Schema files declare the database you want: schemas, tables (with columns, keys, indexes, constraints, triggers, rules, policies, partitions and inheritance), views and materialized views, types, domains, sequences, functions, procedures and aggregates, collations and text search configurations, foreign-data wrappers, servers and foreign tables, roles, grants (including those on procedural languages), ownership and default privileges, publications, event triggers, and the rows of reference tables. `pgy diff` compares them with a live PostgreSQL database (15–18) and writes the SQL that brings the database in line. Anything that could lose data needs `--unsafe`, and dropping whole objects also needs `--prune`.

## Editor Support

A JSON Schema for schema files ships at [`schemas/pgy.schema.json`](../schemas/pgy.schema.json). Editors running [yaml-language-server](https://github.com/redhat-developer/yaml-language-server) (VS Code via the Red Hat YAML extension, Neovim, etc.) use it for validation, completion, and hover documentation.

Enable it per file with a modeline at the top of the YAML file:

```yaml
# yaml-language-server: $schema=https://github.com/suprbdev/pgy/releases/latest/download/pgy.schema.json
```

That URL always serves the latest release's schema. To match an older pgy you run, pin it to that release instead: `https://github.com/suprbdev/pgy/releases/download/vX.Y.Z/pgy.schema.json` (releases from v0.34.3 attach it). A relative path such as `$schema=./schemas/pgy.schema.json` also works. Or map the schema by filename in VS Code settings:

```json
{
  "yaml.schemas": {
    "https://github.com/suprbdev/pgy/releases/latest/download/pgy.schema.json": ["pgy.yaml", "*.pgy.yml", "schema*.yml"]
  }
}
```

The schema validates property names and value types (catching typos like `funtion get():` or `uniqe: true`), enforces enums (trigger timing/events, volatility, partition strategies), and documents every property on hover. One limitation: the editor cannot autocomplete the dynamic header keys themselves (`table <name>:`, `function <name>(args):`) since those are pattern-matched, but everything inside their bodies completes normally.

The schema is kept in sync with the parser by `internal/schema/jsonschema_test.go`, which validates `examples/schema.yml` and format fixtures against it as part of `make test`.

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
      email:
        type: citext
        notNull: true
      org_id:
        type: uuid
      active:
        type: boolean
        default: "true"
      updated_at:
        type: timestamptz
        default: now()
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

### Names

Names are written unquoted and pgy quotes them in SQL, so `Order Items`, `user` or `a.b` need no SQL quotes in YAML.

- **A key is always a name.** YAML reads an unquoted `2024`, `1.5`, `true`, `null` or `~` as a number, a boolean or null. pgy takes such a key as the text you wrote: `2024: {type: numeric}` declares the column `2024`, and `null: "NULL"` under a foreign table's `options` is file_fdw's `null` option. Quoting them (`"2024":`) is equivalent, and is what `pgy pull` writes so that other YAML tools read the file the same way.
- **In a list of names**, such as `primaryKey: [id, 2024]` or an enum's `labels: [1, 2]`, such an entry is taken as a name too. YAML has already normalised a number by then (`1.0` becomes `1`, `0x10` becomes `16`), and the JSON Schema asks for strings there, so quote these: `primaryKey: [id, "2024"]`.
- **Column, attribute, constraint, index, trigger, rule, policy and statistics names may contain dots**: `a.b: {type: integer}` is the column `"a.b"`. A sequence's `ownedBy` is read as `schema.table.column` (or `table.column`), with everything after the table being the column.
- **In table, view, type, function and other schema-level names, a dot separates the schema from the name**: `table x.y:` inside `schema app:` declares table `y` in schema `x`. To declare a name that contains a dot, write it in double quotes as in SQL, with `""` for a double quote in it: `table "x.y":` inside `schema app:` declares the table `app."x.y"`, and `function "f.n"(a int):` the function. Wherever such an object is referred to (`inherits`, `partitionOf`, `of`, a foreign key's `references.table`, `dependsOn`, `ownedBy`, publication `tables`, `renamedFrom`, `collate`, text search `mappings`, an event trigger's `function`), quote the name the same way: `inherits: [app."x.y"]`, `dependsOn: [view app."v.w"]`, `ownedBy: app."x.y".id`, and `renamedFrom: '"x.y"'` for a name in the same schema (YAML itself removes the quotes of a value that starts with one, so put the whole value in single quotes). That includes collations PostgreSQL ships whose name contains a dot: `collate: '"en_US.utf8"'`. Names inside SQL (a column's `type`, a view's `query`, a trigger's `procedure`, a default) are quoted as in SQL anyway: `type: app."en.um"`. `pgy pull` writes all of these names quoted. A name without a dot may be quoted too: `table "orders":` is `table orders:`.
- **Schema names cannot contain a dot.** pgy reads the first dot of every reference as the end of the schema name, so `pgy validate` rejects a `schema <name>:` block or `schemas:` entry whose name has one. `pgy pull` leaves such a schema out with a warning, and `--unsafe --prune` leaves its objects alone, along with any object whose dotted name starts with the name of such a schema.

### Name resolution

The objects a schema file declares are always created in the schema it puts them in. The names it only *uses* (a column's type, a function in a default, an operator in a check, an operator class, a table in a view's query) are passed to PostgreSQL as written:

- **An unqualified name resolves as in SQL, when the migration runs**, on the `search_path` of the session that runs it. `type: citext` works when the extension's schema is on that `search_path`, and `type: extensions.citext` works everywhere. Unqualified names for objects in `public` and `pg_catalog` are the usual case, since both are on the default `search_path`.
- **Comparing with the database does not depend on the session.** pgy reads the database with `search_path` set to `public`, whatever the role, the database or the DSN sets, so the same YAML gives the same diff through every connection. When it compares a declared definition with the live one, the schema of anything in `pg_catalog`, `public`, or a schema on the `search_path` of the session pgy connected with, is optional: `pg_catalog.int4` and `integer` are the same type wherever a type is compared (columns, routine arguments, transforms), and with `extensions` on the `search_path`, `citext` and `extensions.citext`, `uuid_generate_v4()` and `extensions.uuid_generate_v4()`, and `email <> ''` and `email OPERATOR(extensions.<>) ''` are the same. A name in a schema that is not on the `search_path` has to be qualified.
- **`pgy pull` qualifies everything outside `public`** (and `pg_catalog`), also when the database's `search_path` lists other schemas. The YAML is the same whichever connection it is pulled through, and rebuilds the database through any session that has `public` on its `search_path`.
- **`database: {set: {search_path: ...}}` also applies to the migration that sets it.** `ALTER DATABASE ... SET` only reaches sessions that start later, so pgy follows it with `SET search_path` for the session: the rest of that migration, and the migrations `pgy migrate` applies after it in the same run, resolve unqualified names on the new `search_path`. In a split diff the database settings go into the first group for the same reason.

Two spellings of an operator are compared with more care, because PostgreSQL prints them only when they matter. `OPERATOR(pg_catalog.op)` or `OPERATOR(public.op)` in the YAML matches a live definition that prints the bare operator. The other way round they differ: PostgreSQL prints `OPERATOR(pg_catalog.&&)` only where the bare `&&` would now resolve to another operator, as it does for `integer[]` once `intarray` is installed, so a definition created before the extension keeps the built-in operator while the YAML's bare `&&` asks for the extension's. Write `OPERATOR(pg_catalog.&&)` in the YAML to keep the built-in one.

Names are compared as text, so pgy cannot tell a function in `public` from one of the same name in `pg_catalog` (`public.gen_random_uuid()` from an old `pgcrypto` and the built-in `gen_random_uuid()`), or two objects of the same name in two schemas that are both on the `search_path`.

A function body is resolved when the function runs, and PostgreSQL 17 and later run `CREATE INDEX`, `REINDEX`, `REFRESH MATERIALIZED VIEW` and the other maintenance commands with `search_path` set to `pg_catalog, pg_temp`, whatever the session has. A function those commands call then finds nothing its body leaves unqualified, objects in `public` included: the usual immutable SQL wrapper around an extension function, used in an index, fails with `function unaccent(text) does not exist` as soon as the index is built over existing rows. Qualify what the body calls, including an object it names in a string, or give the function a path of its own with `set: {search_path: ...}`:

```yaml
extensions:
  - {name: unaccent, schema: public}
schema app:
  function f_unaccent(text):
    returns: text
    language: sql
    immutable: true
    body: select public.unaccent('public.unaccent', $1)
  table city:
    dependsOn: [function app.f_unaccent(text)]
    columns:
      name: {type: text}
    indexes:
      city_name_unaccent: {columns: [app.f_unaccent(name)]}
```

### How expressions are compared

`pgy diff` compares a declared expression with the one in the database in two steps.

1. **As text.** Both sides are normalised first: casts, parentheses, whitespace, identifier quoting and optional schema qualifiers are ignored, and the rewrites PostgreSQL applies when it stores an expression (`in` lists, `between`, `like`) are understood. [Constraint alterations](#constraint-alterations) lists them. YAML that `pgy pull` wrote matches here, and so does most hand-written YAML. The casts and schema qualifiers the normalisation removes can change what an expression means, though, so it keeps track of them:
   - **A cast of the same expression to another type or modifier on each side** is a change: `(emb::halfvec(3))` against `(emb::halfvec(4))`, `name::varchar(10)` against `name::varchar(20)`.
   - **A cast that only one side has** is left to the second step: an index on `(email::text)` against one on the `citext` column `email`, or a check `email::text = lower(email::text)` against `email = lower(email)`, which compare letter case differently. A cast of a constant doesn't count, since PostgreSQL prints the type it gave a literal (`'active'::text`, `(0)::numeric`), which YAML leaves out.
   - **A schema qualifier that only one side has, or another schema on each side,** is left to the second step when `pg_catalog` has a function, operator or text search configuration of the name too: pgcrypto's `public.gen_random_uuid()` against core `gen_random_uuid()`, or `operator(public.=)` against `OPERATOR(pg_catalog.=)`. One query asks the server which names those are, and only when the YAML has such a qualifier. `public.my_function()` against `my_function()` is not left to it when `pg_catalog` has no `my_function`.
2. **Through the server,** for what still differs, and for what the first step left to it. PostgreSQL stores some things in a form only it knows. A constant is rewritten by its type's input and output functions: `'1 hour'::interval` is stored as `'01:00:00'::interval`, `'a=>1'::hstore` as `'"a"=>"1"'::hstore`, `'(0,0,0)'::cube` as `'(0, 0, 0)'::cube`, `'2020-1-1'::date` as `'2020-01-01'::date` and `1e3` as `'1000'::numeric`. A function-style cast or a `type 'literal'` constant becomes a cast: `issn('1436-4522')` is stored as `'1436-4522'::issn`. So pgy has PostgreSQL rewrite the declared definition as well. In one transaction, which it always rolls back, it copies each table concerned into a temporary table (`CREATE TEMPORARY TABLE ... (LIKE ...)`, which copies the columns and takes an `ACCESS SHARE` lock), attaches the declared definitions to the copy and reads them back the way it reads the live ones. A definition that comes back exactly as the live one is unchanged. One that comes back different has changed and is planned as usual, also when the text comparison took the two for the same.

The second step covers column defaults, generated column expressions, check and exclusion constraints, index expressions and predicates, policies (`using` and `withCheck`), triggers (`when` and the arguments), view column defaults, domain defaults and checks, and the argument defaults of functions and procedures. In all of them, any spelling PostgreSQL accepts gives a clean diff, and a cast or schema that changes what a definition means is a change.

It runs only when the first step leaves something to it. A schema that is in sync costs nothing extra when it is spelled as `pgy pull` writes it, with every cast and qualifier PostgreSQL prints, and very little when it is written by hand. When it runs, it is one round trip to the server and about a millisecond per table.

It needs the `TEMPORARY` privilege on the database (`PUBLIC` has it by default) and a writable session. Without them, on a read-only standby for instance, `pgy diff` prints one warning and reports what the text comparison finds: a definition PostgreSQL stores in another spelling shows up as changed on every diff, and a cast or schema qualifier that only one side has goes unnoticed. Write definitions the way PostgreSQL prints them to get an exact diff there: `pgy pull`, or `\d` in psql, shows the stored form.

The copy also needs what `CREATE TABLE ... (LIKE ...)` needs: USAGE on the table's schema and SELECT on the table. A role that lacks them for some tables, such as the tables of a hosted platform's own schema, has the definitions on those tables compared as text, and the others through the server as usual. `pgy diff` prints one warning that names them, together with the views and types it couldn't compare for a missing privilege (see [Views](#views)). A missing privilege never makes `pgy diff` plan a statement: what is planned is what the text comparison finds.

Some definitions are compared as text only:

- **A definition that uses something the same plan creates:** a column it adds, a function or a table it creates, an extension's objects in the schema it moves the extension to. PostgreSQL can't attach it to the copy yet, so that diff reports it as changed, and the next one compares it through the server. Tables, columns, constraints, indexes, domains and functions the plan renames with [`renamedFrom`](#renames) are no problem.
- **A policy that reads its own table through a name without a schema** (`exists (select 1 from docs d where ...)` in a policy on `docs`), because the copy would stand in for it. Qualify the table (`from public.docs d`). Qualifying a column with the table's name (`docs.team_id`) is fine.
- **A definition on a table that has the same name as a type it names without a schema.** Qualify the type.
- **Triggers on views, rule conditions (`where`), publication row filters, extended statistics expressions and partition bounds.**
- **Differences the normalisation hides without removing a cast or a schema qualifier:** parentheses that regroup an expression (`(a + b) * c` against `a + b * c`) and the letter case of a quoted name. Also a trigger function written as `public.f()` against a live `f()`, which is how `pgy pull` writes it, although `pg_catalog` might have an `f` too. The next paragraph covers them.

**Comparing every expression.** `pgy diff --strict-expressions`, or `strict_expressions: true` in the `policy:` block of the [config file](../README.md#strict-mode), sends every declared definition of the kinds above that exists in the database to the server, also the ones that match as text, so nothing the normalisation hides goes unnoticed. That costs a temporary copy of every table with such a definition: with 1000 tables and about 16,000 definitions, `pgy diff` takes about 2 seconds instead of half a second. It is meant for CI and for an occasional audit. `--strict-expressions=false` turns the config setting off for one run. Its failures and fallbacks are those of the second step.

### How types are compared

The type of a column (arrays included), of a composite type's attribute and a domain's base type is compared with the live one as text first, ignoring case, quoting, an optional schema and the aliases PostgreSQL knows (`varchar` and `character varying`, `int` and `integer`, `timestamptz` and `timestamp with time zone`). A type modifier is something only the type knows how to read, though: `numeric(10)` is stored as `numeric(10,0)`, PostGIS's `geography(Point)` as `geography(Point,4326)` and `geometry(Point, 0)` as `geometry(Point)`, and spaces inside the parentheses (`varchar( 5 )`, pgvector's `vector( 3 )`) are not part of it. So a declared type that still differs from the live one is resolved by PostgreSQL, in the round trip of the [expression comparison](#how-expressions-are-compared): on the session's `search_path`, as the migration will resolve it, with `to_regtype` and `to_regtypemod` on PostgreSQL 17 and later and as a column of a temporary table before, and printed with `format_type` the way the live type is read. One that comes back exactly as the live type is unchanged; one that comes back different, or that PostgreSQL can't resolve (a type the same plan creates), is a change, as before.

Types `pgy pull` wrote match as text and cost nothing. Without the `TEMPORARY` privilege or in a read-only session, types are compared as text only, with the warning the expression comparison prints: write them the way PostgreSQL prints them there (`pgy pull`, or `\d` in psql).

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
- **Extensions** are deduplicated by name, and the first declaration wins.
  `pgy validate` warns when two declarations of one extension differ.
- **Types, functions, procedures, views, sequences, and domains** are keyed by
  name; a later declaration replaces an earlier one.

This enables composable schema files: one file can declare a base table and a
supplementary "link" file can add a foreign-key column pointing at another
module's table (see `examples/modules/` and `examples/links/`).

`post.yml`, a standalone module:

```yaml
schema public:
  table post:
    columns:
      id: { type: uuid, primaryKey: true, default: gen_random_uuid() }
      title: { type: text, notNull: true }
```

`post_author.yml`, a link file to load together with `post.yml` and `user.yml`:

```yaml
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

A plain string entry is shorthand for `{name: <string>, ifNotExists: true}`, so the common case is just:

```yaml
extensions: [pg_trgm, pgcrypto]
```

| Property | Type | Description |
|----------|------|-------------|
| `name` | string | Extension name |
| `ifNotExists` | boolean | Adds `IF NOT EXISTS` to the SQL statement |
| `schema` | string | `WITH SCHEMA`: where the extension's objects are installed. The schema is created if missing (system schemas such as `pg_catalog` never are). A live, relocatable extension in another schema is moved with `ALTER EXTENSION ... SET SCHEMA`; non-relocatable extensions (e.g. PostGIS) are left where they are, and `pgy diff` warns (see [moving an extension](#extensions)). Omitted = unmanaged: PostgreSQL then installs the extension into the schema its control file names, or else into the first schema of the connection's `search_path`, so give a `schema` to be sure where it lands |
| `version` | string | `VERSION`. A live extension on a different version is updated with `ALTER EXTENSION ... UPDATE TO` (see [Updating extensions](#updating-extensions)). Quote it (`"1.10"`), since an unquoted YAML number loses trailing zeros. Omitted = unmanaged, and `pgy pull` writes it only when the installed version is not the server's default |
| `cascade` | boolean | `CASCADE`: also install the extensions this one requires |
| `comment` | string or `null` | `COMMENT ON EXTENSION`, replacing the comment from the extension's control file. Diffed against live; omitted (or `""`) = unmanaged. `comment: null` means no comment: `COMMENT ON EXTENSION ... IS NULL` after `CREATE EXTENSION`, and whenever one is set again. `pgy pull` writes the comment when it differs from the control file's and `null` when there is none, so a rebuild matches; a comment equal to the control file's is left out and stays unmanaged, as the `public` schema's default comment does. `null` is only for extensions, the one kind of object that gets a comment it wasn't declared with |
| `dependsOn` | list | `extension <name>` entries name extensions this one has to be created after. See [Dependencies](#dependencies) |

```yaml
extensions:
  - name: pg_trgm
    ifNotExists: true
    schema: extensions
    version: "1.6"
```

Extension statements come right after `CREATE SCHEMA`, before every object that could use the extension, and in requirement order: an extension is created after the declared extensions it requires. With a connection pgy reads the requirements from the server (`pg_available_extension_versions`); `diff --from-empty` knows those between PostgreSQL's own extensions (`earthdistance` needs `cube`, each transform such as `hstore_plperl` needs its language and its type) and otherwise follows `dependsOn: [extension <name>]`, which `pgy pull` writes for every extension that requires another.

With a connection, `pgy diff` also warns about an extension the server does not offer, a `version` it cannot install, a `schema` other than the one the control file of an extension it creates fixes (`postgis_topology` has to go into `topology`, `plpgsql` into `pg_catalog`), which `CREATE EXTENSION` rejects, and a `version` the installed one has no update path to. The last case is left out of the plan, and the warning says why when it can tell: the declared version is older than the installed one (PostgreSQL cannot downgrade an extension), or the server does not have it.

Give every extension a `schema:`. Without one, an extension whose control file names no schema goes into the first schema of the `search_path`, and PostgreSQL's default `search_path` is `"$user", public`. That matters most for a role named like the schema of pgy's migrations table: `pgy init` creates the schema `pgy`, so for a role called `pgy` it comes first, `extensions: [citext]` installs the extension into `pgy`, and the objects a hand-written migration creates without a schema land there too. Connect as a role with another name, or keep the migrations table in another schema with `--migrations-table` (`migrations_table` in `.pgy.yml`).

Moving an extension (`schema:` changed on a relocatable one) takes one migration: change `schema:` and the references to its types, functions, operators and operator classes (`ext.citext`, `ext.gin_trgm_ops`, `operator(ext.<>)`) together, also in the keys of an index's `opclasses`, `collations` and `statisticsTargets`, which match its `columns` entries. PostgreSQL carries columns, defaults, indexes, constraints, triggers, views and the argument and return types of routines along with the extension, so pgy emits only `ALTER EXTENSION ... SET SCHEMA` for them. Function bodies written as text are not carried along: update them in the same change, and the routine, matched by its signature as it is after the move, is replaced with `CREATE OR REPLACE` after the extension has moved.

An extension that is not relocatable (`postgis`, `xml2`, `plpgsql`) cannot be moved. When its `schema:` differs from the schema it is installed in, `pgy diff` warns and plans no `ALTER EXTENSION`; with a connection, references to its objects qualified with the declared schema (`gis.geometry`) are planned as references to the schema it stays in, so nothing that only works after a move is planned. Declare the schema it is in, or drop the extension and create it in the other schema by hand.

A name left unqualified only resolves while the extension's schema is on the `search_path` (see [Name resolution](#name-resolution)). When an extension moves to a schema that is neither on the `search_path` of the session pgy connected with nor in a declared `database.set.search_path`, `pgy diff` warns and names the declarations that still use its types or functions without a schema: column, domain and composite attribute types, routine argument and result types, casts, and calls in column defaults, generated columns, check constraints, domains and trigger functions. A call is not counted when `pg_catalog` has a function of the same name (pgcrypto's `gen_random_uuid()`), since it may mean the built-in one; bodies and view queries are not looked at.

### Updating extensions

Raising `version:` emits `ALTER EXTENSION ... UPDATE TO`. Many update scripts drop one of the extension's objects and create it again: every `pg_stat_statements` update recreates the `pg_stat_statements` view, and `postgres_fdw` 1.2 replaces `postgres_fdw_get_connections()`. PostgreSQL rejects that while something outside the extension uses the object (`cannot drop view pg_stat_statements because other objects depend on it`), and the migration fails. The update scripts live on the server, so pgy cannot know which objects an update replaces. With a connection, it looks up in `pg_depend` the views, materialized views and SQL-standard routine bodies that use an object of each extension it is about to update, directly or through other views, and treats all of them as possibly in the way:

- **Declared views.** With `--unsafe`, they are dropped right before the update, dependents first, and recreated from the YAML after it, in the same migration. They get back what they had, including what the YAML leaves unmanaged, as around a column type change (see [Recreated views](#recreated-views)). Without `--unsafe` the plan is the update alone, and `pgy diff` warns (kind `dependents`), naming the views. The statements `--unsafe` would add are reported as left out, like every other unsafe change.
- **Materialized views** are never dropped for an update, because recreating one rebuilds its data and its indexes. `pgy diff` warns (kind `dependents`), naming the materialized view and the extension. If the update replaces an object it uses, the migration fails until the materialized view is dropped in a hand-written migration. The next `pgy diff` creates a declared one again.
- **Functions and procedures with a SQL-standard body** (`BEGIN ATOMIC`, `RETURN`) are reported the same way and never dropped. PostgreSQL does not record what a body written as text uses, so such a routine neither blocks the update nor is reported.
- **Views the YAML doesn't declare** can't be recreated, so they stay and are reported too. With `--unsafe --prune`, a view or materialized view that pruning drops anyway is dropped before the update instead of after it.
- **Views that something of the above uses** stay as well, since dropping them would fail: a view a materialized view selects from, or one that a policy, a column of its row type or a SQL-standard body refers to. The warning names what holds them back.

Some extensions accept `ALTER EXTENSION ... UPDATE` only as the first statement of a session: TimescaleDB loads its library on the first statement a session runs and then refuses to update (`cannot be updated after the old version has already been loaded`). So an update that needs nothing else in the plan to run first goes in a buffer section of its own, `extension_updates`, which starts with `-- pgy:new-session`. `pgy commit` writes it to a `NNNN_<name>_extension_updates.sql` migration numbered before the rest, and `pgy migrate` runs each of its statements on a new connection, as that connection's first statement, outside a transaction (`--lock-timeout` and `--statement-timeout` are passed as connection parameters). It records the migration once every statement succeeded. Rerunning an update that already took effect only prints a notice, so a migration that failed half way can be run again. Afterwards `pgy migrate` replaces its other connections, since TimescaleDB ends the sessions that loaded the old version. An update stays in the main migration, after what it needs, when the new version requires an extension the same migration creates, or when the plan drops views that use the extension (see above): the drops, the update and the views' recreation must share a transaction. An extension that wants the update first in a session rejects it there, so run that update without `--unsafe` first, which keeps the views and gives the update a migration of its own.

An extension that none of these use is updated with the single statement. Other users of an extension's objects, such as a column default that calls `uuid_generate_v4()`, an index with `gin_trgm_ops` or a trigger on an extension's function, are not looked at: update scripts rarely replace what those use, and if one does, PostgreSQL's error names them.

With `--split`, extensions are updated in the first migration, where only the views of the first group count as declared.

### Removing extensions

Without `--prune`, pgy never drops an extension. With `pgy diff --unsafe --prune`, a present `extensions:` block is authoritative, like `servers:`, `eventTriggers:` and `publications:`: an installed extension it does not list is dropped with `DROP EXTENSION` (never `CASCADE`), after every other drop in the plan. `extensions: []` declares that no extension is wanted. Without the block, extensions are unmanaged and none is dropped.

Several rules keep this from removing something the database still needs:

- **Scope.** Only extensions installed in a schema the YAML declares objects in (tables, views, functions, types and so on) are considered, which is the scope `--prune` uses for those objects. An extension in any other schema is left alone. That covers a dedicated `extensions` schema and the schemas a hosting platform installs its own extensions into. A `schema:` on a declared extension does not put that schema in scope.
- **Ignore list.** Extensions named in `prune.keep_extensions` of the [config file](../README.md#unsafe-changes-and-pruning) are never dropped. Use it for extensions the application does not own, such as a `pg_stat_statements` installed in `public`.
- **`plpgsql`** is never dropped.
- **Declared objects.** An extension is kept while the YAML declares one of its own objects (see [Objects an extension owns](#objects-an-extension-owns)). `pgy diff` warns (kind `dependents`) and names the declaration.
- **Requirements.** An extension that one staying installed requires is kept: declaring `earthdistance` without listing `cube` never drops `cube`.
- **Dependents.** PostgreSQL refuses to drop an extension while objects outside it use its types, functions, operators or operator classes: a column of type `citext`, an index with `gin_trgm_ops`, a view calling `akeys()`. With a connection, pgy looks them up in `pg_depend` for each extension it is about to drop. When the same plan drops all of them (the table is pruned, the column or the index is dropped), `DROP EXTENSION` follows those drops. Otherwise the statement is left out and `pgy diff` warns (kind `dependents`), naming the extension and what still uses it, so the migration always applies. Once nothing uses the extension, the next `pgy diff --unsafe --prune` drops it. That takes two migrations when a dependent is changed instead of dropped: retyping a column from `citext` to `text` and removing `citext` from the block changes the type in the first and drops the extension in the second.

PostgreSQL does not record what a function body written as text (`plpgsql`, or `language sql` with a quoted body) calls, so such a function does not hold an extension back, and fails the next time it runs. SQL-standard bodies (`BEGIN ATOMIC`, `RETURN`) are tracked.

`pgy pull` lists every installed extension, so pulled YAML never plans a drop against the database it came from. A pull limited with `--schema` or `--exclude-schema` lists those installed in the pulled schemas, which keeps that true within the scope above, and those the pulled schemas' objects use.

### Objects an extension owns

The functions, aggregates, tables, sequences, views, types, text search dictionaries, schemas, languages and foreign-data wrappers that `CREATE EXTENSION` installs come and go with their extension: pgy never creates, replaces or drops them on their own. Their privileges and a few attributes can be declared, though. Leave out the part that defines the object, and the declaration manages one that already exists:

| Declaration | Left out | Managed |
|-------------|----------|---------|
| `function <name>(<args>):` | `body`, `returns`, `language` | `security`, `stable`/`volatile`/`immutable`, `strict`, `leakproof`, `parallel`, `cost`, `rows`, `set`, `grants`, `revokePublic`, `owner`, `comment` |
| `procedure <name>(<args>):` | `body`, `language` | `security`, `set`, `grants`, `revokePublic`, `owner`, `comment` |
| `aggregate <name>(<args>):` | `sfunc`, `stype` and the other options | `grants`, `revokePublic`, `owner`, `comment` |
| `table <name>:`, `foreign table <name>:` | `columns` (and `of`, `partitionOf`, `inherits`, `server`) | `grants`, `owner`, `comment`, `rowLevelSecurity`, `forceRowLevelSecurity`, `policies`, `data` (with `key`) |
| `sequence <name>:` | every option | `grants`, `owner`, `comment` |
| `text search dictionary <name>:` | `template` | `options`, `owner`, `comment`. See [Text Search](#text-search) |
| `schema <name>:` | | `grants`, `owner`, `comment` of a schema the extension's script creates, such as `pg_search`'s `pdb` |
| `view <name>:`, `materialized view <name>:` | `query` | `grants`, `owner`, `comment`, `columnComments` |
| `type <name>:` | `type` | `grants`, `revokePublic`, `owner`, `comment`; base types such as `hstore` and `citext` included |
| `languages:` entry | | `grants`. See [Languages](#languages) |
| `foreignDataWrappers:` entry | `handler`, `validator` | `options`, `owner`, `grants`, `comment`. See [Foreign-Data Wrappers](#foreign-data-wrappers) |

```yaml
extensions:
  - {name: pgcrypto, schema: public}
  - {name: hstore, schema: public}
  - {name: pg_stat_statements, schema: public}
  - {name: pg_buffercache, schema: public}
roles:
  app: {}
  monitoring: {}
schema public:
  function gen_random_bytes(integer):
    revokePublic: true
    grants: {app: [execute]}
  function each(hstore):
    rows: 50
  function pg_buffercache_pages():
    grants: {monitoring: [execute]}
  view pg_stat_statements:
    grants: {monitoring: [select], public: []}
  type hstore:
    revokePublic: true
    grants: {app: [usage]}
```

PostGIS's configuration table and aggregates:

```yaml
extensions:
  - {name: postgis, schema: public}
roles:
  app: {}
schema public:
  table spatial_ref_sys:
    grants: {app: [select, insert]}
  aggregate st_union(geometry):
    grants: {app: [execute]}
```

- **Matching.** A function or procedure is found by its argument types, as PostgreSQL identifies it: `function each(hstore):` is the extension's `each(hs hstore, OUT key text, OUT value text)`. Argument names and `OUT` arguments may be written or left out.
- **Privileges** are compared with the ones the extension's script set (`pg_init_privs`), which is also what `pg_dump` keeps of an extension's objects. What the extension granted is left alone unless the `grants:` block names the grantee: `pg_buffercache_pages()` is granted to `pg_monitor` by its extension, and the block above adds `monitoring` without revoking that. A named grantee gets exactly the privileges listed, so `pg_monitor: []` revokes. Everything else follows the usual [grants](#grants) rules: privileges that were granted later to roles the block doesn't list are revoked. The same holds for a `grants:` block on any other declared object that an extension owns.
- **In the migration that creates the extension**, the statements for its objects run after `CREATE EXTENSION`: every declared attribute is set, the listed privileges are granted, and a grantee listed with `[]` is revoked. One migration is enough unless the extension grants a listed role more than the block lists, which the next diff revokes. The same applies when the plan updates an extension (`ALTER EXTENSION ... UPDATE`).
- **An object that doesn't exist**, in a plan that creates or updates no extension, gets no statements, and `pgy diff` warns with the name of the declaration.
- **Tables.** A table declared without `columns`, `of`, `partitionOf`, `inherits` and the keys that need columns (`primaryKey`, `indexes`, `constraints`, `triggers`, `rules`, `statistics`, storage and partitioning) is never created, altered or dropped, and prune leaves its columns, keys, indexes, triggers and rules alone. An empty `columns: {}` still declares a table without columns that pgy creates. A table that an extension owns, `CREATE EXTENSION` or `ALTER EXTENSION ... ADD` alike, is treated this way even when the YAML declares its columns, with a warning: its definition is the extension's. Column privileges cannot be declared on it. Its rows can, for a configuration table: see [Reference Data](#reference-data).
- **Rules and triggers** of a table or view that an extension owns, or of one declared without columns or `query`, are never pruned. PostGIS's `geometry_columns` view keeps its rules when the YAML declares its grants.
- **Sequences.** A sequence declared without options is created with PostgreSQL's defaults when it doesn't exist, as before. Sequences are created after the extensions, so once an extension has created one, the declaration only manages its privileges, owner and comment.
- **Aggregates** are granted with `GRANT ... ON FUNCTION`; an ordered-set aggregate is declared as `aggregate percentile(float8 order by anyelement):` and granted on `(float8, anyelement)`. `grants` and `revokePublic` work on every aggregate, not only on one an extension owns.
- **Order.** `ALTER FUNCTION` and `ALTER PROCEDURE` for changed attributes run before the tables, indexes and views of the same migration, so `function unaccent(text): {immutable: true}` can be declared together with an index on `unaccent(title)`.
- **Schemas.** Such a declaration does not make pgy create its schema. Some extensions create their own schema in their script (`pg_cron` creates `cron`, `postgis_topology` creates `topology`) and fail when it already exists, so `schema cron:` holding only declarations like these leaves the schema to the extension. A schema that also holds an object pgy creates is created as usual. The grants of a schema an extension owns (`pg_search`'s `pdb`) are compared with `pg_init_privs`, like those of its other objects. Schemas are never dropped.
- **`pgy pull`** writes such a declaration for each function, procedure, view, type, language and foreign-data wrapper of an extension whose privileges are no longer the ones the extension set, with the grantees that changed: `revokePublic: true` for a revoked default (`public: []` on a language), `role: []` for a grantee that lost its privileges. The owner's own privileges are never compared, those of the role that owned the object when the extension was created included, so a changed owner alone is not a changed privilege. Tables, sequences, aggregates and schemas are written the same way, a table also with its row level security and policies, which `pg_dump` keeps too; a schema an extension created is never written as the application's, with its owner and comment. PostgreSQL keeps no record of the options an extension's script gave its text search dictionaries, so pull writes the current options of each one that has any (`unaccent`'s `rules: unaccent` included), which a rebuild sets after `CREATE EXTENSION`; `pg_dump` loses them. Changed attributes of an extension's functions are not pulled, since PostgreSQL does not record what they were, and neither are column privileges on an extension's table, which pull reports.

## Database Settings

A top-level `database:` block manages settings of the database pgy is connected to, for all roles (`ALTER DATABASE ... SET`):

```yaml
database:
  set:
    timezone: UTC
    search_path: "app, public"
    statement_timeout: 30s
  roles:                        # ALTER ROLE ... IN DATABASE ... SET
    app_user: {statement_timeout: 5s}
```

`roles` holds per-role settings that apply only in this database, and is authoritative for each role it lists. Settings for every database belong on the role itself (see [Roles](#roles)). When present, `set` is authoritative: changed settings are set, and settings it doesn't list are reset. Without it, database settings are unmanaged. The statements name the database with `current_database()`, so a migration works whatever the database is called in each environment. Values are written as in `postgresql.conf` and quoted when needed, as for [role settings](#roles).

A changed `search_path` under `set` is also set for the session that runs the migration, so the statements after it resolve unqualified names the way later sessions will (see [Name resolution](#name-resolution)). Other settings, and the per-role ones under `roles`, take effect in sessions that start afterwards.

Database settings are applied after the extensions are created, so a setting an extension defines (`pg_trgm.similarity_threshold`) can be declared together with the extension. A role that is not a superuser may need some help to set it; see the next section.

### Parameters an extension defines

A parameter that an extension's library defines (`pg_trgm.similarity_threshold`, pgvector's `hnsw.ef_search` and `ivfflat.probes`) can appear wherever settings do: in the `set:` of a [function](#functions), a [procedure](#procedures) or a [role](#roles), and under `database:` in `set:` and `roles:`. All of them are applied after the extensions are created, so such a setting can be declared together with its extension.

PostgreSQL knows such a parameter only in a session that has loaded the extension's library. Until then it lets only a superuser set it: `CREATE FUNCTION ... SET`, `ALTER FUNCTION`, `ALTER PROCEDURE`, `ALTER ROLE` and `ALTER DATABASE ... SET` run by another role fail with `permission denied to set parameter "hnsw.ef_search"`. The migration that creates the extension has loaded its library, but a later migration that adds or changes the setting has not. The prefix of a parameter is not always the extension's name (pgvector's are `hnsw` and `ivfflat`), so pgy cannot tell which library to load, and `LOAD` is itself reserved to superusers. Two ways out, on PostgreSQL 15 and later alike:

- **Grant the parameter once.** A superuser runs `GRANT SET ON PARAMETER hnsw.ef_search TO deployer;` for the role that runs migrations. PostgreSQL accepts the grant before the library is loaded, and the setting can then be applied in any session.
- **Load the library first.** Add a statement that uses the extension at the top of the migration, by hand: a call to one of its functions (`select show_limit();` for `pg_trgm`) or a cast to one of its types (`select '[1]'::vector;` for pgvector). Edit the buffer before `pgy commit`, since a committed migration's checksum covers its text.

## Tables

A table declared without `columns`, `of`, `partitionOf` and `inherits` (nor the keys that only a table pgy creates can have) is never created: it manages the `grants`, `owner`, `comment`, `rowLevelSecurity`, `forceRowLevelSecurity`, `policies` and `data` of one that already exists, such as an extension's configuration table. See [Objects an extension owns](#objects-an-extension-owns). `columns: {}` declares a table without columns.

### Columns

| Property | Type | Description |
|----------|------|-------------|
| `type` | string | PostgreSQL data type (e.g. `text`, `uuid`, `int`, `jsonb`) |
| `nullable` | boolean | `true` allows NULL. Default: `false` (NOT NULL) |
| `notNull` | boolean | `true` means NOT NULL. Inverse alias for `nullable` |
| `default` | string, boolean, or number | SQL default expression (e.g. `NOW()`, `uuid_generate_v4()`). Bare YAML scalars like `false` or `0` are coerced to their SQL literal form |
| `unique` | boolean | Adds a UNIQUE constraint on this column |
| `primaryKey` | boolean | Marks column as part of the primary key |
| `identity` | string, boolean or map | `always` or `byDefault` emits `GENERATED ALWAYS AS IDENTITY` / `GENERATED BY DEFAULT AS IDENTITY`. Bare `true` means `always`. A map adds sequence options: `{generated: always, start: 1000, increment: 1, minValue, maxValue, cache, cycle, sequenceName}` (`generated` defaults to `always`). `sequenceName` names the identity's sequence (unqualified, it's in the table's schema), and a changed one renames the live sequence. Grants and a comment on the sequence go in a `sequence` block of its name (see [Sequences](#sequences)). See [Column alterations](#column-alterations) for changes. When set, `default` is ignored (PostgreSQL forbids both) |
| `generated` | string | Generated column expression, emitted as `GENERATED ALWAYS AS (<expr>) STORED`. When set, `default` and `identity` are ignored. See [Column alterations](#column-alterations) for changes |
| `collate` | string | `COLLATE` for the column (e.g. `C`, `und-x-icu`). Changing it on a live column emits `ALTER COLUMN ... TYPE ... COLLATE` and requires `--unsafe`. Omitted = type default |
| `compression` | string | `pglz`, `lz4` or `default`: TOAST compression method. Diffed against live → `ALTER COLUMN ... SET COMPRESSION` (affects newly written values only). Omitted = unmanaged |
| `storage` | string | `plain`, `external`, `extended` or `main` → `ALTER COLUMN ... SET STORAGE`, diffed against live. Omitted = unmanaged |
| `statisticsTarget` | integer | Per-column planner statistics target → `ALTER COLUMN ... SET STATISTICS <n>` (0 disables collection, -1 restores the default), diffed against live. Omitted = unmanaged |
| `nDistinct` | number | Planner override for the column's number of distinct values → `ALTER COLUMN ... SET (n_distinct = <n>)`: a positive count, or a negative fraction of the row count (`-1` = every value distinct). `0` removes the override. Diffed against live; omitted = unmanaged. Takes effect at the next `ANALYZE` |
| `nDistinctInherited` | number | Like `nDistinct`, for statistics across inheritance children and partitions (`n_distinct_inherited`) |
| `virtual` | boolean | With `generated`: emit `VIRTUAL` (computed when read, not stored) instead of `STORED`. PostgreSQL 18+. A column cannot be switched between stored and virtual in place; drop and re-add it |
| `notValid` | boolean | Keep a NOT NULL column's not-null constraint `NOT VALID` (PostgreSQL 18+): existing rows aren't checked, and `pgy diff` adds it with `NOT VALID` and never validates it. Without it, pgy validates a `NOT VALID` not-null constraint it finds |
| `validate` | string | `immediate` or `later`: how making an existing column NOT NULL is validated on PostgreSQL 18+. `later` (the default) adds the constraint `NOT VALID` and validates it in the no-transaction migration; `immediate` uses `SET NOT NULL`, which scans the table under `ACCESS EXCLUSIVE`. See [Validating constraints in one step](#validating-constraints-in-one-step). Omitted = the table's `validate` |
| `notNullName` | string | Name of the column's NOT NULL constraint (PostgreSQL 18+, which records one per NOT NULL column). Used when the table or the column is created and when an existing column becomes NOT NULL; a live constraint with another name is renamed in place with `ALTER TABLE ... RENAME CONSTRAINT`. Omitted = unmanaged: a new constraint gets PostgreSQL's `<table>_<column>_not_null`, and `pgy pull` writes the key only for a name that differs from that. Before PostgreSQL 18 the key is ignored and `pgy diff` warns. It applies to columns a table or foreign table declares with a type; a partition or inheritance child takes the name from its parent |
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
      ticket_no:
        type: bigint
        identity: {generated: always, start: 1000, increment: 10}
      total:
        type: numeric
        nullable: true
        generated: price * quantity   # GENERATED ALWAYS AS (price * quantity) STORED
```

#### Table storage

```yaml
schema public:
  table events_log:
    unlogged: true              # UNLOGGED: no WAL, emptied after a crash
    tablespace: fast_ssd        # TABLESPACE (created outside pgy)
    accessMethod: heap          # USING: table access method
    with:                       # storage parameters
      fillfactor: 80
      autovacuum_vacuum_scale_factor: 0.05
    columns:
      payload:
        type: text
        compression: lz4
        storage: external
      code:
        type: text
        collate: C
```

`unlogged` and `with` are diffed against the live table. Switching `unlogged` on a live table emits `ALTER TABLE ... SET LOGGED / SET UNLOGGED`, which rewrites the table, so it requires `--unsafe`. A present `with` block is authoritative: changed parameters emit `ALTER TABLE ... SET (...)`, and live parameters it no longer lists emit `RESET (...)`. Omitting either key leaves the live setting alone. A value that is not a number or a lower-case word is passed as a string literal, so its case and punctuation reach PostgreSQL as written (`tokenizer: TokenBigram` becomes `tokenizer = 'TokenBigram'`), and so is a word PostgreSQL would read as a type name or reject, such as `time`, `int` or `values`; a value already in single quotes is passed as it is. This applies to every `with` (tables, materialized views, indexes and key constraints), and so does the order: the parameters are emitted in the order a `schema <name>:` block lists them (other YAML layouts sort them by name), and `pgy pull` writes them in the order PostgreSQL stores them, so a rebuilt database lists them as the original does. The comparison with the database ignores the order. `toast.*` parameters apply to the table's TOAST table, which PostgreSQL only creates when a column can be toasted (such as `text` or `jsonb`). Without one it discards them, so pgy skips them until the table has one.

A parameter with a namespace other than `toast` (`tsdb.hypertable`, `timescaledb.continuous`) is create-time only. PostgreSQL never keeps such a parameter, so when `CREATE TABLE` or `CREATE MATERIALIZED VIEW` accepts one, an extension took it while the relation was created: TimescaleDB turns `with: {tsdb.hypertable: true, tsdb.partition_column: time, tsdb.chunk_interval: 1 day}` into a hypertable. pgy passes it to `CREATE` and afterwards compares it only while the live relation's `reloptions` show it, so it is not emitted again. Changing it, or adding it to a table that already exists, does nothing; use the extension's own functions in a hand-written migration for that. Values are quoted like any other parameter's (`1 day` becomes `'1 day'`). A materialized view declared with such a parameter that the database has as a plain view, because the extension made it one (a TimescaleDB continuous aggregate), counts as created: its query and storage are left alone, and its `comment`, `columnComments`, `grants` and `owner` are managed on the view.

#### Tablespaces

`tablespace:` puts a table, partition, materialized view, index, or the index of a primary key, unique or exclusion constraint in a tablespace that already exists; pgy doesn't create tablespaces. New objects get `TABLESPACE` (or `USING INDEX TABLESPACE`). An existing one in another tablespace is moved with `ALTER ... SET TABLESPACE`, which rewrites it under an `ACCESS EXCLUSIVE` lock, so `pgy diff` gives it a lock warning (and `--strict` fails on it by default). An index with `concurrently: true` moves with `REINDEX (TABLESPACE ...) INDEX CONCURRENTLY` in the no-transaction migration instead, without blocking writes. On a partitioned table or index, `SET TABLESPACE` only sets the default for new partitions, so it has no warning. `tablespace: pg_default` moves an object back; a relation in the database's default tablespace matches its name. Without the key, the tablespace is unmanaged. `pgy pull` writes it for every relation stored outside the database default.

#### Access methods

`accessMethod:` names the table access method of a table, partition, partitioned table or materialized view, such as `columnar` from Citus. It is compared with the live `pg_class.relam`. New relations get `USING`, and an existing table or materialized view with another access method is changed with `ALTER ... SET ACCESS METHOD`, which rewrites it and its indexes under an `ACCESS EXCLUSIVE` lock. So that needs `--unsafe` and gets a lock warning (and `--strict` fails on it by default). On a partitioned table it only sets the access method that partitions created later get, which needs neither. PostgreSQL keeps an access method for a partitioned table from version 17 on; before that the key is ignored there with a warning, so declare it on the partitions. Foreign tables and plain views have no storage, so `pgy validate` rejects the key on them. Without the key, the access method is unmanaged. `pgy pull` writes it for every relation whose access method is not the session's `default_table_access_method`.

#### Replica identity and extended statistics

```yaml
schema app:
  table events:
    replicaIdentity: full       # default | full | nothing | index <unique index name>
    statistics:
      events_tenant_kind:
        kinds: [ndistinct, dependencies]   # ndistinct | dependencies | mcv; omit for all
        columns: [tenant_id, kind]         # columns and/or expressions such as lower(name)
        statisticsTarget: 500              # ALTER STATISTICS ... SET STATISTICS; -1 = default
        comment: correlated columns
```

#### Clustering

```yaml
schema app:
  table events:
    clusterOn: events_created_idx   # or none to remove the mark
```

`clusterOn` marks the index a bare `CLUSTER` uses (`ALTER TABLE ... CLUSTER ON`, or `SET WITHOUT CLUSTER` for `none`), diffed against live. pgy only sets the mark; run `CLUSTER` yourself, since it rewrites the table under an exclusive lock. Partitioned tables can't be marked (mark each partition), and `pgy validate` rejects partial indexes.

`replicaIdentity` controls what logical replication sends for updated and deleted rows. Tables without a primary key need it, if they're published, to replicate UPDATE/DELETE. It is diffed against live and emits `ALTER TABLE ... REPLICA IDENTITY`. `statistics` declares `CREATE STATISTICS` objects in the table's schema. They are compared as sets of kinds and columns, since PostgreSQL stores columns in table order, and a changed one is dropped and recreated (metadata only). `statisticsTarget` is diffed against live and changed in place with `ALTER STATISTICS ... SET STATISTICS` (`-1` restores the default); left out, it is unmanaged. Comments are diffed, and `--prune` drops undeclared statistics on declared tables.

#### Column alterations

For columns that already exist in the live database, `pgy diff` reconciles their attributes:

- **Default**: a differing `default` emits `SET DEFAULT`; removing `default` from YAML emits `DROP DEFAULT`. Comparison strips the casts PostgreSQL adds to live defaults, so `'active'` matches the live `'active'::text`, and a default PostgreSQL stores in another spelling (`'1 hour'` on an `interval` column, stored as `'01:00:00'::interval`) is [compared through the server](#how-expressions-are-compared). `nextval(...)` defaults (serial columns) are never dropped.
- **Nullability**: emits `SET NOT NULL` / `DROP NOT NULL` to match `nullable`/`notNull`. Primary-key and identity columns never emit `DROP NOT NULL`. On PostgreSQL 18, making an existing column NOT NULL instead adds `CONSTRAINT <table>_<column>_not_null NOT NULL <column> NOT VALID`, which enforces it for new rows without scanning, and validates it in the no-transaction migration `pgy commit` writes after the main one: `VALIDATE CONSTRAINT` scans under a lock that doesn't block reads or writes. If existing rows hold NULLs, that migration fails; fix the rows and migrate again, or rerun `pgy diff`, which emits the pending `VALIDATE`. Primary key, identity and generated columns, partitions, foreign tables and columns with a validated `CHECK (col IS NOT NULL)` keep `SET NOT NULL`. With `notNullName` the constraint takes that name instead (`ADD CONSTRAINT <name> NOT NULL <column>` where `SET NOT NULL` would be used), and a NOT NULL column whose constraint has another name gets `RENAME CONSTRAINT`.
- **Serial**: changing an existing column's type to `serial`, `bigserial` or `smallserial` creates its `<table>_<column>_seq` sequence owned by the column, starts it after the column's largest value, and makes `nextval()` its default.
- **Generated**: an existing column's expression changes with `ALTER COLUMN ... SET EXPRESSION` (PostgreSQL 17+, under `--unsafe`), and dropping `generated` emits `DROP EXPRESSION`. PostgreSQL cannot make a plain column generated, so declaring `generated` on one only warns.
- **Type** (requires `--unsafe`): a differing type emits `ALTER COLUMN ... TYPE`, with `using` appended as the cast expression when set. Types are compared normalized — `varchar(255)` matches the live `character varying(255)`, `int`/`integer`/`int4` are equivalent, and `serial` matches its live `integer` + `nextval` form — and, where that is not enough, [through the server](#how-types-are-compared): `numeric(10)` matches the live `numeric(10,0)`, `varchar( 5 )` the live `character varying(5)`, PostGIS's `geography(Point)` the live `geography(Point,4326)`. Type changes can rewrite the table or fail on incompatible casts, hence the `--unsafe` gate. PostgreSQL won't change the type of a column that a view, materialized view, trigger, policy or rule uses, so pgy drops those first, along with the views built on them (also those that use one without naming its columns, such as `select count(*) from v`) and the triggers, policies and rules that use those views, and recreates them from the YAML after the change. A table, column or view renamed with `renamedFrom` in the same diff is followed under its new name. A recreated view gets back what it had, including what its YAML leaves unmanaged (see [Recreated views](#recreated-views)), and a materialized view is repopulated. A dependent the YAML doesn't declare can't be recreated: `pgy diff` warns about it and leaves the column's dependents alone, so the change still fails until you declare it. The statements around the change are ordered so that nothing written for the old type is in its way:
  - The column's default is dropped before the change and the declared `default` set again after it. PostgreSQL has to cast a default left in place to the new type and refuses when there is no implicit cast (`default for column ... cannot be cast automatically`), whatever `using` says. `nextval(...)` defaults stay.
  - Indexes and check, unique and exclusion constraints on the column that the same diff redefines (or drops, with `--prune`) are dropped before the change and created after it. Left in place, PostgreSQL rebuilds them from their old definition, and an operator class or expression written for the old type may not accept the new one. Indexes and constraints the YAML leaves as they are stay for PostgreSQL to carry over.
  - A foreign key whose columns change type on both sides is dropped before either change and added again after both, since PostgreSQL checks it after each one, when the two types may not be comparable. Like any foreign key added to an existing table it comes back `NOT VALID` and is validated in the no-transaction migration.
  - PostgreSQL refuses the change while a generated column uses the column. A stored generated column that the YAML drops or now declares as a plain column gets `DROP EXPRESSION` first. One that stays generated is dropped before the change and added again after it from the YAML (see [Generated columns added again](#generated-columns-added-again)).
- **Identity**: a changed kind emits `SET GENERATED ALWAYS | BY DEFAULT`, and changed sequence options emit `SET START WITH / SET INCREMENT BY / ...` (options left out of the YAML are unmanaged; `start` only affects a later `RESTART`). Under `--unsafe`, an identity declared on an existing column is added with `ADD GENERATED ... AS IDENTITY` (dropping its default first), and one the YAML no longer declares is removed with `DROP IDENTITY`. The new sequence starts at `start`, not after existing values, so set `start` past them, for example when converting a `serial` column.
- **Generated** (requires `--unsafe`): a changed `generated` expression emits `ALTER COLUMN ... SET EXPRESSION AS (...)` on PostgreSQL 17+. PostgreSQL 15 and 16 have no `SET EXPRESSION`, so there the column is dropped and added again with the new expression (see [Generated columns added again](#generated-columns-added-again)); without `--unsafe`, `pgy diff` warns. Removing `generated` from a live generated column emits `DROP EXPRESSION`, turning it into a plain column that keeps its current values. Expressions are compared normalized, so `price * qty` matches the live `(price * (qty)::numeric)`, and [through the server](#how-expressions-are-compared) when that is not enough (`ean13(isbn)`, stored as `(isbn)::ean13`). A plain column cannot be turned into a generated one in place. Generated columns never get `SET DEFAULT`/`DROP DEFAULT`.
- **Removed** (requires `--unsafe`): a column the YAML no longer declares is dropped with `DROP COLUMN`, after the generated columns, policies and publications the same plan drops. PostgreSQL rejects the drop while a view, materialized view, trigger, rule, policy or generated column still uses the column, so when one of them stays (it isn't declared, and `--prune` is off or doesn't drop it), `pgy diff` keeps the column and warns (kind `dependents`), naming them. `--prune` drops undeclared views, materialized views, triggers and rules first; a policy goes when the table's `policies` block leaves it out.

```yaml
schema public:
  table orders:
    columns:
      amount:
        type: numeric(10,2)          # was: text
        using: amount::numeric(10,2) # cast for ALTER ... TYPE (with --unsafe)
```

#### Generated columns added again

PostgreSQL can't change a generated column in place in two cases: the type of a column it uses changes, and, before PostgreSQL 17, its expression changes. With `--unsafe`, pgy drops the generated column and adds it again from the YAML:

```sql
drop view "app"."lc";                                   -- what uses the column, as around a type change
alter table "app"."users" drop column "name_lc";
alter table "app"."users" alter column "name" type citext;
alter table "app"."users" add column "name_lc" text generated always as (lower(name)) stored;
create index if not exists "users_name_lc" on "app"."users"("name_lc");
create or replace view "app"."lc" as select id, name_lc from app.users;
```

- The indexes, constraints and extended statistics on the column go with it and are created again from the YAML, like new ones: a check or foreign key `NOT VALID` and validated in the no-transaction migration, an index with `concurrently: true` there too. The views, materialized views, triggers, policies and rules that use the column are dropped first and recreated after, like around a type change (see [Recreated views](#recreated-views)).
- What the YAML leaves unmanaged on the column comes back: its comment, grants, storage, compression, statistics target and `n_distinct` settings.
- The column moves to the end of the table, and a stored column is computed again for every row, which rewrites the table under an `ACCESS EXCLUSIVE` lock (a lock warning says so). `pgy diff` prints a warning for each column it adds again.
- pgy only does this when everything that goes with the column can be created again. It keeps the column, and warns with the reason, when the table is partitioned, has inheritance children, or is a foreign or typed table; when the column is part of the primary key, has a collation the YAML doesn't declare, or a foreign key references it or a publication lists it; when an index, constraint or statistics object on it isn't declared and `--prune` is off; or when an object that uses it isn't declared. The migration then has to be written by hand.

### Primary Key

Can be declared at the column level (`primaryKey: true`) or as a table-level block:

```yaml
primaryKey:
  <constraint_name>:
    columns: [col1, col2]
    include: [col3]           # optional: INCLUDE (non-key) columns of the key's index
    with: {fillfactor: 80}    # optional: storage parameters of the key's index
    tablespace: fast_ssd      # optional: USING INDEX TABLESPACE for the key's index
    comment: "natural key"    # optional: COMMENT ON CONSTRAINT
```

The block form names the constraint: it is created as `CONSTRAINT <constraint_name> PRIMARY KEY`, and a live primary key with another name is renamed (`ALTER TABLE ... RENAME CONSTRAINT`, which renames its index too). With the list form or `primaryKey: true` columns, PostgreSQL's default `<table>_pkey` is used and the live name is left alone. `comment` is diffed against the live key's comment, which a rename keeps. `pgy pull` writes the block form for primary keys whose name isn't the default or that have a comment.

PostgreSQL 18 temporal keys are written inline in the column list: `primaryKey: [id, valid without overlaps]` (the last column must be a range type; scalar columns need the `btree_gist` extension). The same `without overlaps` suffix works in `unique` constraint columns, and foreign keys reference them with a `period` prefix: `columns: [room_id, period valid]`, `references: {table: rooms, columns: [id, period valid]}`.

### Indexes

```yaml
indexes:
  <index_name>:
    columns: [col1, col2]
    unique: true            # optional, default false
    using: gist             # optional index method: btree (default) | gist | gin | brin | hash | spgist, or one an extension adds (bloom)
    where: deleted_at is null   # optional partial index predicate
    opclass: gin_trgm_ops   # optional operator class applied to every column
    opclasses:              # optional per-column operator classes (override opclass)
      col1: gist_trgm_ops(siglen=32)   # operator class parameters go in parentheses
    include: [col3]         # optional INCLUDE (non-key) columns for index-only scans
    nullsNotDistinct: true  # optional, unique indexes: NULLs compare equal (PostgreSQL 15+)
    concurrently: true      # optional: on an existing table, build without blocking writes
    with:                   # optional storage parameters
      fillfactor: 70
    tablespace: archive     # optional TABLESPACE
    comment: "lookup by col1"   # optional COMMENT ON INDEX (diffed against live)
```

If `name` is omitted, an auto-name `<schema>_<table>_<cols>_idx` is derived from the table and column names. Names longer than PostgreSQL's 63-byte identifier limit are truncated the way the server would store them; for auto-names the table/column part is shortened so the `_idx` (or `_key`, for column-level `unique`) suffix survives and the two never collide.

`using: btree` is the PostgreSQL default and is omitted from the generated SQL. The `where` expression is emitted verbatim (wrapped in parentheses), so any valid predicate works.

A `columns` entry containing any character outside `[A-Za-z0-9_]` (e.g. `lower(name)`) is treated as an expression: it is emitted verbatim wrapped in parentheses instead of being quoted as an identifier. Use the full expression string as the key in `opclasses` to attach an operator class to it.

Operator class parameters are written after the name, as in `CREATE INDEX`: `opclass: gist_trgm_ops(siglen=32)`, or per column `opclasses: {path: gist_ltree_ops(siglen=100), tags: gist__int_ops(numranges=100)}`. They are compared with the live index column by name and value, so spacing, quotes and the order of several parameters don't matter, and `pgy pull` writes them, including those of a default operator class (`tsvector_ops(siglen=100)`). A changed parameter is a changed index. An entry without parameters means the operator class defaults, so an index that has them in the database and not in the YAML is reported as changed.

`using` takes any index access method the database has. The built-in ones need nothing else; one that an extension adds, such as `bloom`, needs the extension under `extensions:`. Its options go in `with` (`using: bloom`, `with: {length: 80, col1: 2}`).

`collations` gives index columns a `COLLATE`, keyed like `opclasses`: `collations: {name: C}` emits `("name" collate "C")`, the usual way to let `LIKE 'abc%'` use an index on a column whose collation isn't `C`. It is compared with the live index, so a changed collation rebuilds the index under `--unsafe`. A column or expression without an entry is expected to keep its own collation: the column's, or for an expression the one PostgreSQL derives from it (`lower(name)` has the collation of `name`), whatever type the access method stores (a `hash` index stores integers). An entry that names that same collation matches too, and `pgy pull` writes only collations that differ from it.

`statisticsTargets` sets how much `ANALYZE` samples for an expression column, also keyed by the columns entry: `statisticsTargets: {lower(email): 500}` emits `ALTER INDEX ... ALTER COLUMN 1 SET STATISTICS 500` (the column's position in `columns`). `-1` restores the default. It is diffed against live and changed in place, without a rebuild. PostgreSQL only allows it on expressions, so `pgy validate` rejects a plain column; give that column a `statisticsTarget` instead. On a partitioned table's index, PostgreSQL applies it to the partitions' indexes too.

Existing indexes are matched by name and their definitions compared against the live catalog: uniqueness, method, key columns or expressions and their sort order, `include`, `where`, `nullsNotDistinct`, `with`, operator classes with their parameters, and collations. Key expressions and `where` are [compared as expressions](#how-expressions-are-compared), so `where: "starts_on > '2020-1-1'"` matches the stored `starts_on > '2020-01-01'::date`. A changed index is dropped and recreated in the same migration; since rebuilding can be slow and briefly leaves queries without the index, this requires `--unsafe`. Without `--unsafe`, a changed index emits no SQL.

`concurrently: true` makes an index added to (or, under `--unsafe`, rebuilt on) an existing table use `CREATE INDEX CONCURRENTLY` / `DROP INDEX CONCURRENTLY`, which do not block writes but cannot run inside a transaction. `pgy diff` puts these statements in a separate `concurrent` buffer section that starts with `-- pgy:no-transaction`, and `pgy commit` writes it as its own migration after the main one. Indexes on tables created by the same plan are built normally. PostgreSQL cannot build an index concurrently on a partitioned table, so for an existing one pgy creates the index `ON ONLY` the parent, builds it `CONCURRENTLY` on each partition (recursing into sub-partitioned ones), and attaches the partition indexes, which makes the parent index valid. A partition's index is named `<partition>_<index>`, unless the partition declares an index with the same definition, whose name it takes (see [Partitioning](#partitioning)). Partitions created by the same plan are included, and a partition whose index is attached already is left as it is. All of that goes in the no-transaction section. Every statement can be rerun. If a build is interrupted and leaves an index invalid, the next `pgy diff` finishes the partitioned sequence, or drops and rebuilds a plain concurrent index.

A `columns` entry may end in a sort order — `asc`, `desc`, `nulls first`, `nulls last`, or a combination such as `created_at desc nulls last`. The suffix is emitted after any operator class, and an `opclasses` key may name the column with or without it.

Example — a GIN trigram index (requires the `pg_trgm` extension), an expression index, a PostGIS spatial index, and a partial unique index:

```yaml
indexes:
  idx_users_name_trgm:
    columns: [name]
    using: gin
    opclass: gin_trgm_ops
  idx_users_lower_email:
    columns: ["lower(email)"]
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
    onDelete: cascade   # cascade | restrict | set null | set default | no action
    onUpdate: cascade   # optional, same actions as onDelete
    match: full         # optional: full | simple (default) | partial
    deferrable: true    # optional DEFERRABLE
    initiallyDeferred: true   # optional DEFERRABLE INITIALLY DEFERRED (implies deferrable)
    notValid: true      # optional: add NOT VALID, existing rows are not checked
    validate: immediate # optional: add it validated in one step (default later)
    enforced: false     # optional: NOT ENFORCED (PostgreSQL 18+)
    comment: "@fieldName owner"   # optional COMMENT ON CONSTRAINT (diffed against live)
```

`no action` and `match: simple` are PostgreSQL's defaults and are omitted from the generated SQL. A foreign key or check added to a table that already exists is added `NOT VALID` in the main migration and validated with `ALTER TABLE ... VALIDATE CONSTRAINT` in the no-transaction migration that `pgy commit` writes after it. Validation checks the existing rows without blocking reads or writes, while new rows are checked from the start. If existing rows violate the constraint, that migration fails, the constraint stays `NOT VALID` and `pgy diff` emits the pending `VALIDATE` until it succeeds. `notValid: true` leaves the constraint unvalidated instead; removing it later validates it the same way. Before PostgreSQL 18, a NOT VALID foreign key can't be added to a partitioned table: it is added validated, with a lock warning, and with `notValid: true` `pgy diff` emits nothing and prints a warning unless the table is new in the same migration. This applies to check constraints too, which have no such restriction. A constraint validated outside pgy while the YAML still says `notValid: true` is not treated as a change.

#### Validating constraints in one step

Some tables refuse `VALIDATE CONSTRAINT` while they accept an `ADD CONSTRAINT` that checks the rows itself: a TimescaleDB hypertable with columnstore enabled rejects it (`operation not supported on hypertables that have columnstore enabled`), so the no-transaction migration fails on every attempt. `validate: immediate` on a check constraint, a foreign key or a column (for its NOT NULL on PostgreSQL 18), or on the table for all of them, adds the constraint validated in the main migration instead: `ADD CONSTRAINT` without `NOT VALID`, or `SET NOT NULL`. That checks the existing rows under the stronger lock of those statements (`ACCESS EXCLUSIVE` for a check and `SET NOT NULL`; a foreign key blocks writes to both tables), so `pgy diff` gives it a lock warning (and `--strict` fails on it by default). A constraint that exists `NOT VALID` is dropped and added again validated in the same migration, and a `NOT VALID` NOT NULL is validated by `SET NOT NULL`. `validate: later`, the default, keeps the two steps; on a constraint or column it overrides the table's `immediate`. `notValid: true` still leaves a constraint unvalidated, and `pgy validate` rejects it together with `validate: immediate` on the same constraint. If a `VALIDATE` migration has already failed, it was never recorded: delete that file before you commit the new diff, or `pgy migrate` keeps running it first.

```yaml
schema app:
  table conditions:
    validate: immediate       # every check, foreign key and NOT NULL added to it from now on
    constraints:
      hum_check: {type: check, expression: humidity >= 0}
      temp_check: {type: check, expression: temperature > -100, validate: later}
```

### Triggers

```yaml
triggers:
  <trigger_name>:
    timing: before     # before | after
    events: [insert, update, delete]
    level: row         # row | statement (default statement, as in PostgreSQL)
    procedure: public.set_updated_at()
    when: old.updated_at is distinct from new.updated_at   # optional WHEN (condition) guard
    referencing:       # optional transition tables (AFTER triggers)
      old: old_rows
      new: new_rows
    enabled: true      # optional: true/origin | false/disabled | replica | always
```

`enabled` manages the trigger's firing state (`ALTER TABLE ... ENABLE / DISABLE [REPLICA | ALWAYS] TRIGGER`), diffed against live. `replica` fires only when `session_replication_role = replica` (logical replication apply), `always` fires in both modes. Omitted = unmanaged.

Events may name columns, `"update of status, total"` (quote it in a YAML flow list, since the comma would otherwise split it). `referencing` emits `REFERENCING OLD TABLE AS ... NEW TABLE AS ...`, which statement-level `AFTER` triggers use to see every affected row at once.

Triggers are compared against the live database by **definition**, not just name: if a live trigger's `pg_get_triggerdef()` no longer matches the desired trigger (timing, events, level, `when:` guard, procedure, deferrability), it is dropped and recreated in the same migration. The comparison normalizes cosmetic differences (case, quoting, parentheses, type casts, `public.` qualifiers, event order, `EXECUTE FUNCTION` vs `EXECUTE PROCEDURE`, a `pg_catalog.` qualifier on the function), so an unchanged trigger emits no SQL. A table trigger that still differs, because of a constant in its `when:` condition for instance, is [compared through the server](#how-expressions-are-compared) before it counts as changed.

`procedure` is the function call as `CREATE TRIGGER` takes it, arguments included. PostgreSQL stores every trigger argument as a string, so arguments are compared by value and any spelling it accepts matches: `moddatetime(updated_at)`, `moddatetime('updated_at')`, `f($$doc$$)` and `f(E'doc')` are the same call, a bare identifier is folded to lower case unless it is double-quoted, and `007` is the string `7`. A changed argument recreates the trigger; `'public.parent'` and `'parent'` are different arguments.

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
    language: sql          # body validated at CREATE unless the plan creates relations (see Functions)
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

### Rules

```yaml
rules:
  <rule_name>:
    event: delete          # select | insert | update | delete
    instead: true          # DO INSTEAD (default DO ALSO)
    where: old.id > 0      # optional condition
    do: update app.orders set deleted = true where id = old.id   # nothing, a statement, or (stmt; stmt)
    replace: false         # optional: re-emit CREATE OR REPLACE RULE on every diff
    comment: soft delete
```

Rules (on tables and plain views) are emitted as `CREATE OR REPLACE RULE` after every table and function. PostgreSQL rewrites a rule's action when it stores it (adding column lists, qualifying names), so only `event`, `instead` and `where` are compared against live. Set `replace: true` while changing an action. With `--unsafe --prune`, undeclared rules on declared tables and views are dropped (views' internal `_RETURN` rule is never touched).


### Constraints

```yaml
constraints:
  <constraint_name>:
    type: check        # check | unique | exclude
    expression: "col > 0"       # for check or exclude
    def: "col > 0"              # alias for expression
    columns: [col1, col2]       # for unique
    nullsNotDistinct: true      # unique: NULLS NOT DISTINCT (PostgreSQL 15+)
    include: [col3]             # unique: INCLUDE (non-key) columns of its index
    with: {fillfactor: 70}      # unique/exclude: storage parameters of its index
    tablespace: fast_ssd        # unique/exclude: USING INDEX TABLESPACE
    deferrable: true            # unique/exclude: DEFERRABLE
    initiallyDeferred: true     # unique/exclude: DEFERRABLE INITIALLY DEFERRED
    noInherit: true             # check: NO INHERIT
    notValid: true              # check: add NOT VALID, existing rows are not checked
    validate: immediate         # check: add it validated in one step (default later)
    enforced: false             # check: NOT ENFORCED (PostgreSQL 18+)
```

#### Constraint alterations

Constraints (including foreign keys) are matched against the live database by name, and their definitions are compared via `pg_get_constraintdef()`. A constraint whose definition changed is dropped and re-added — since the re-add revalidates existing rows (and can fail on them), this requires `--unsafe`. Without `--unsafe`, a redefined constraint emits no SQL.

Definition comparison is normalized: casts (`''::text`), parentheses, whitespace, identifier quoting, and the qualifiers of `public` and of the schemas on the connection's `search_path` (see [Name resolution](#name-resolution)) are ignored, so a live `CHECK ((email <> ''::text))` matches a YAML `expression: "email <> ''"` without churn. The rewrites PostgreSQL applies when storing an expression are also understood: `x in ('a', 'b')` matches `x = ANY (ARRAY['a', 'b'])`, `between` matches its `>=`/`<=` form, `like`/`ilike` match `~~`/`~~*`, `!=` matches `<>`, and `-1` matches `'-1'::integer`. `cast(x as type)` matches `x::type`, `between` may have negative bounds, and a `pg_catalog.` qualifier is ignored. A string literal is compared as written (`'public.x'` and `'x'` are different values), except where it names an object: `'public.seq'::regclass`, `nextval('public.seq')` and `to_tsvector('public.cfg', body)` match the unqualified names PostgreSQL prints for the objects of `public`, and so do the names of objects in a schema on the connection's `search_path`. A trailing `NOT VALID` is ignored on both sides. The casts and qualifiers the comparison ignores still count when they change what the constraint means: a cast to another type on each side is a change, and one on one side only, or a schema `pg_catalog` shares, is settled by the server (see [How expressions are compared](#how-expressions-are-compared)). The `with` of a unique constraint or primary key is compared with its index's storage parameters and changed in place (`ALTER INDEX ... SET / RESET`), and is unmanaged when left out. Primary keys are otherwise matched by existence only and never dropped, so a primary key's `include` applies when it is created.

An exclusion constraint's `expression` is everything after `EXCLUDE`: `using gist (room_id with =, during with &&) where (active)`. It is compared with the live constraint element by element, so spellings PostgreSQL does not print back make no difference: leaving out `using` (btree), naming the operator class that is the default for the column (`room_id gist_int4_ops with =`), and `with operator(pg_catalog.=)` or `with operator(public.=)` for an operator PostgreSQL finds without the qualifier. Its index's storage parameters come from a `WITH (...)` clause in the expression (which is where `pgy pull` writes them), from the `with` key, or both, the key winning; they are applied when the constraint is created, and afterwards compared and changed in place like a unique constraint's. With neither, they are unmanaged.

A check or exclusion constraint that still differs after all this is [compared through the server](#how-expressions-are-compared) before it counts as changed. A constant may therefore be written in any form its type reads: `'24 hours'` for an interval PostgreSQL prints as `'1 day'`, `'a=>1'` for the `hstore` it prints as `'"a"=>"1"'`, `cube '(0,0,0)'` for `'(0, 0, 0)'::cube`, or a function-style cast such as `isbn('...')`, which it stores as `'...'::isbn`. Where that comparison can't run (no `TEMPORARY` privilege, a read-only session), such a constant has to be written the way PostgreSQL prints it; `pgy pull`, or `\d` in psql, shows the stored form.

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

- Both parents and partitions are created when missing. When an existing partition's bound or parent differs from the YAML, `--unsafe` detaches it and attaches it again with the declared bound (`DETACH PARTITION`, then `ATTACH PARTITION`, which locks the parent and scans the partition, so it gets a lock warning, and fails if rows fall outside the new bound); without `--unsafe` it is reported as left out. Bound values are compared as values, so `'2024-01-01'` matches PostgreSQL's `'2024-01-01 00:00:00+00'`. A declared partition that exists as an ordinary table (for example one that was detached) is attached with `ALTER TABLE ... ATTACH PARTITION`, which scans it to check its rows fit the bound. pgy never emits `DETACH PARTITION`.
- Indexes, foreign keys, constraints, and triggers declared on a partition child apply to that child as usual.
- PostgreSQL gives every partition its own copy of each index of the parent, attached to it, when the index is created on the parent or the partition is created or attached. The copies are named `<partition>_<columns>_idx` (an expression column is named after its function, or `expr`) and need not be declared. To give one a name of your own, declare an index with the parent index's definition under that name on the partition. pgy then creates it before the parent's index, which attaches it instead of creating another, or renames the copy PostgreSQL made with `ALTER INDEX ... RENAME TO`, also for a partition the same plan creates. When a new partition would get two copies named alike, so that pgy cannot tell which is which, `pgy diff` warns and leaves the rename to the next diff. `pgy pull` writes a partition's attached index only when its name is not the one PostgreSQL would give it, or when it has a comment. Rebuilding a parent's index under `--unsafe` drops the indexes attached to it too, so pgy creates the ones the partitions declare again before the parent's index (or, with `concurrently: true`, builds them under their names). An attached partition index whose declaration alone changed cannot be dropped on its own: `pgy diff` warns and leaves it until the parent's index changes the same way.
- A parent index that only some partitions are attached to (made with `CREATE INDEX ... ON ONLY` and `ALTER INDEX ... ATTACH PARTITION`, or left by an interrupted build) is invalid, which is a transitional state that pgy does not reproduce. `pgy pull` writes it as an ordinary index, with a warning, and the partition indexes attached to it as above, so a database rebuilt from the YAML has the index complete on every partition. `pgy diff` leaves such an index alone, unless it has `concurrently: true`, in which case the next diff finishes building it.
- A partition takes its columns from the parent, but can set its own options for them under `columns:`, without a type: `columns: {value: {default: 5, statisticsTarget: 200, comment: tuned for 2025}}`. The options are `default`, `nullable: false` (it can only add NOT NULL), `storage`, `compression`, `statisticsTarget`, `nDistinct`, `nDistinctInherited` and `comment`. Each one is set after the partition is created and diffed against live, and options left out keep what the partition copied from the parent (or nothing, for the statistics target, `n_distinct` and the comment, which PostgreSQL doesn't copy). `pgy pull` writes only the options that differ from the parent's.
- PostgreSQL requires a partitioned table's primary key to include the partition key columns.

### Inheritance

Classic table inheritance (`INHERITS`) is declared with `inherits:`, a list of parent tables. A child declares only its own columns. It gets the parents' columns from PostgreSQL.

```yaml
schema app:
  table event:
    columns:
      id: {type: bigint}
      at: {type: timestamptz}
  table login_event:
    inherits: [app.event]           # CREATE TABLE ... INHERITS (app.event)
    columns:
      ip: {type: inet}
```

- The child is created after its parents.
- On an existing table, a parent missing from the live table is added with `ALTER TABLE ... INHERIT`. With `--unsafe`, a live parent the list no longer names is removed with `NO INHERIT`, and its columns stay on the child. `inherits: []` removes every parent. Without the key, parents are unmanaged.
- Columns a table gets from a parent are never altered or dropped on the child. That includes columns the child also declares itself. Change them on the parent.
- An inherited column can be listed on the child without a type to set the child's own options for it, as for [partitions](#partitioning): `columns: {created_at: {default: "now()", comment: child timestamp}}`. `pgy validate` accepts a column without a type only on a partition, or on a child one of whose parents declares it.
- `pgy pull` emits `inherits` and leaves inherited columns out of the child, apart from the options the child sets for them. `pgy validate` checks a child's index and key columns against its parents' too. Use `partitionOf` for declarative partitioning. `inherits` cannot be combined with it.


### Typed tables

A typed table takes its columns from a composite type (`CREATE TABLE ... OF type`). Declare the type, and `of` on the table. List a column only to set its options, without a type, as for [partitions](#partitioning): `default`, `nullable: false`, `storage`, `compression`, `statisticsTarget`, `nDistinct` and `comment`.

```yaml
schema app:
  type addr:
    type: composite
    attributes: {street: {type: text}, city: {type: text}}
  table addresses:
    of: app.addr
    columns:
      city: {default: "'Paris'", nullable: false, comment: city name}
    primaryKey: [street]
```

- The table is created after its type with `CREATE TABLE ... OF`. An existing ordinary table declared with `of` is linked with `ALTER TABLE ... OF`, which PostgreSQL allows only when its columns match the type exactly. A live typed table whose YAML has no `of`, or a different one, is unlinked with `NOT OF` under `--unsafe` and reported otherwise.
- Its columns change with the type. Attributes added, changed or dropped on a type that a typed table uses are altered with `CASCADE`, and `--unsafe` never drops a typed table's columns.
- `pgy validate` rejects a typed table's column that has a type or isn't one of the type's attributes, and `of` together with `partitionOf` or `inherits`. `pgy pull` writes `of` and only the options a column sets.
## Foreign Tables

Foreign tables read through a foreign server, which a foreign-data wrapper such as `postgres_fdw` or `file_fdw` provides. Declare the wrapper's extension under `extensions:`, the servers under a top-level `servers:` block, and each table as a `foreign table <name>:` key in a `schema <name>:` block. The wrapper itself needs an entry only to manage its privileges, options, owner or comment, or when no extension provides it: see [Foreign-Data Wrappers](#foreign-data-wrappers).

```yaml
extensions: [postgres_fdw]

servers:
  warehouse:
    wrapper: postgres_fdw
    options: {host: wh.internal, port: "5432", dbname: analytics}
    comment: analytics warehouse

schema reporting:
  foreign table orders:
    server: warehouse
    options: {schema_name: public, table_name: orders}
    columns:
      id: {type: bigint}
      total: {type: "numeric(12,2)", nullable: true, options: {column_name: amount}}
    grants:
      reporting_role: [select]
    comment: orders in the warehouse
```

Servers are created after extensions and [foreign-data wrappers](#foreign-data-wrappers), and before everything else. A server's `grants` (`usage`, e.g. `grants: {app_user: [usage]}`) work like other grants blocks. Their `version`, `options`, `owner` and `comment` are diffed against the live server (`ALTER SERVER ... VERSION`, `OPTIONS (ADD / SET / DROP ...)`, `OWNER TO`). `version` and option values are passed on as written, so `version: 15.10` stays `15.10`. An omitted `version` is unmanaged, and the YAML has no way to say that a server has none: to remove a server's version, run `ALTER SERVER ... VERSION NULL` in a hand-written migration. `renamedFrom` renames a server with `ALTER SERVER ... RENAME TO`, and its foreign tables follow. PostgreSQL cannot change a server's wrapper or type in place, so `pgy diff` reports such a change and emits nothing else for that server until it has been recreated by hand. With `diff --split`, the group that declares a server comes before the groups with foreign tables on it.

A present `servers:` block is authoritative: `--unsafe --prune` drops servers it does not list, after their foreign tables and together with their user mappings (`DROP USER MAPPING`). A server that still has foreign tables the plan keeps (in a schema the YAML doesn't manage, or declared on another server name) is not dropped, and `pgy diff` says so. Without the block, servers are unmanaged.

User mappings are not managed, because their options hold passwords, which do not belong in schema files. Create them separately. For `postgres_fdw` servers, `pgy diff` warns while the connected role, or `PUBLIC`, has none. A materialized view that reads a foreign table is populated when it is created, which fails without a user mapping. `pgy diff` warns when a plan creates such a view while the server has none: set `withData: false` and refresh the view once the mapping exists, or create the server and its user mapping before running the migration.

Foreign table columns are diffed like table columns (added, dropped under `--unsafe`, retyped, `NOT NULL`, defaults, comments, grants), and their `options` are diffed with `ALTER FOREIGN TABLE ... ALTER COLUMN ... OPTIONS`. Table `options` are diffed the same way. Option blocks are authoritative when present: options they no longer list are dropped, and `options: {}` means none. Columns can be `generated` (stored, or `virtual` on PostgreSQL 18) and take a `storage` mode. PostgreSQL cannot move a foreign table to another server, so a changed `server` is only reported, also under `--unsafe`. Nor can it turn a table into a foreign table or back: a table declared as one kind that is the other in the database is reported and left alone. Check constraints, `triggers`, `owner`, `renamedFrom` and `dependsOn` work as on tables. `--unsafe --prune` drops undeclared foreign tables with `DROP FOREIGN TABLE`.

A foreign table can be a partition of a local partitioned table (`partitionOf` with `forValues` or `default`, as for [partitions](#partitioning)), which is how `postgres_fdw` shards are attached: pgy emits `CREATE FOREIGN TABLE ... PARTITION OF ... SERVER ...`. It can also be an inheritance child (`inherits`, as for [inheritance](#inheritance)): `CREATE FOREIGN TABLE ... INHERITS (...) SERVER ...`. In both cases the columns it gets from its parent are left to the parent. List one without a `type` to set what the foreign table keeps of its own for it, such as the wrapper's column `options`. Foreign partitions cannot be indexed, so `concurrently: true` indexes on their parent are built with a plain `CREATE INDEX`, which skips them, and `pgy diff` warns. Unique indexes and primary keys on such a parent are rejected by PostgreSQL. Foreign tables take no primary keys, indexes, foreign keys or table storage parameters, and cannot be partitioned themselves.

`pgy pull` writes servers to `database.yml` and foreign tables to their schema's file. It writes the `options` block of every server and foreign table, as `options: {}` when there are none, so the pulled YAML also notices options added to them later. A column gets an `options` block only when it has options: an option added later to a column without one goes unnoticed unless the YAML lists `options: {}` for that column.

Server properties:

| Property | Type | Description |
|----------|------|-------------|
| `wrapper` | string | Required. `FOREIGN DATA WRAPPER`, usually the name of the extension providing it, or a wrapper declared under [`foreignDataWrappers:`](#foreign-data-wrappers) |
| `type` | string | `TYPE`. Cannot be changed in place |
| `version` | string | `VERSION`, diffed against live |
| `options` | map | Wrapper options. Authoritative when present; omitted = unmanaged |
| `owner` | string | Owner role, diffed against live |
| `comment` | string | `COMMENT ON SERVER` |
| `renamedFrom` | string | Previous name: `ALTER SERVER ... RENAME TO` when only that name exists live |

Foreign table properties:

| Property | Type | Description |
|----------|------|-------------|
| `server` | string | Required. The server the table reads through |
| `options` | map | Wrapper table options. Authoritative when present; omitted = unmanaged |
| `partitionOf`, `forValues`, `default` | | Attach as a partition of a local partitioned table, as for [partitions](#partitioning) |
| `inherits` | list | Parent tables (`INHERITS`), as for [inheritance](#inheritance) |
| `columns` | map | Columns: `type`, `nullable`/`notNull`, `notNullName`, `default`, `generated`, `virtual`, `collate`, `storage`, `comment`, `grants`, `statisticsTarget`, `nDistinct`, `renamedFrom`, `using`, and `options` for the wrapper's column options. Columns of a partition or inheritance child take no `type` |
| `constraints` | map | Check constraints, as on [tables](#constraints) |
| `triggers` | map | Triggers, as on [tables](#triggers) |
| `grants`, `owner`, `comment`, `renamedFrom`, `dependsOn` | | As on tables |

## Foreign-Data Wrappers

A top-level `foreignDataWrappers:` block manages foreign-data wrappers. What an entry does depends on who owns the wrapper:

- **A wrapper an extension owns** (`postgres_fdw`, `file_fdw`, the `dblink_fdw` of `dblink`, `mysql_fdw` and so on) is never created or dropped by pgy. Its entry manages `grants`, `options`, `owner` and `comment`, whichever it lists. `handler` and `validator` belong to the extension, and `pgy validate` rejects them on such an entry.
- **Any other wrapper** is created by pgy when the database does not have it, and altered in place when it does. `handler` and `validator` name functions that exist already, usually those of an extension. Like every other key they are unmanaged when left out: a new wrapper is created without the clause, which gives it no handler or no validator, and an existing wrapper keeps the function it has. So `mywrapper: {grants: {app: [usage]}}` only touches the grants of a wrapper that was made by hand. The empty string says that the wrapper has none: `handler: ""` is `NO HANDLER`, `validator: ""` is `NO VALIDATOR`. A wrapper with neither is a named set of options, which is how `dblink` uses `dblink_fdw`.

```yaml
extensions: [postgres_fdw]

foreignDataWrappers:
  postgres_fdw:                 # owned by the extension: only attributes are managed
    grants: {app_owner: [usage]}
  pg2:                          # created by pgy
    handler: postgres_fdw_handler
    validator: postgres_fdw_validator
    owner: admin
    comment: second wrapper on the postgres_fdw handler
  legacy:                       # a catalogue of connection settings
    handler: ""                 # NO HANDLER, also when someone gave it one
    validator: ""
    options: {debug: "true"}
  inherited:                    # made by hand: its handler and validator stay as they are
    grants: {app_owner: [usage]}

servers:
  warehouse: {wrapper: postgres_fdw, options: {host: wh.internal, dbname: analytics}, owner: app_owner}
  mirror: {wrapper: pg2, options: {dbname: mirror}}
```

| Property | Type | Description |
|----------|------|-------------|
| `handler` | string | `HANDLER` function, optionally schema-qualified (`ext.postgres_fdw_handler`). `""` = `NO HANDLER`. Omitted = unmanaged: an existing wrapper keeps its handler, a new one is created without. Not allowed on a wrapper an extension owns |
| `validator` | string | `VALIDATOR` function, optionally schema-qualified. `""` = `NO VALIDATOR`. Omitted = unmanaged, like `handler`. Not allowed on a wrapper an extension owns |
| `options` | map | Wrapper options. Authoritative when present (`options: {}` means none); omitted = unmanaged. Values are passed on as written |
| `owner` | string | Owner role, diffed against live. PostgreSQL requires the owner of a wrapper to be a superuser |
| `grants` | map | Role → privileges (`usage`; `all` stands for it). See below |
| `comment` | string | `COMMENT ON FOREIGN DATA WRAPPER` |
| `renamedFrom` | string | Previous name: `ALTER FOREIGN DATA WRAPPER ... RENAME TO` when only that name exists live. Its servers follow |

- **Order.** Wrapper statements run after the extension statements and before servers: `CREATE FOREIGN DATA WRAPPER`, changes to the definition, `OWNER TO` and the grants. A server that the same migration creates, or hands to another role, then finds the wrapper and the privilege it needs. Handler and validator functions are C functions, which pgy cannot declare, so they have to exist already or come with a declared extension. With `diff --split`, wrappers go into the first group, together with the extensions.
- **Changes** are made in place: `ALTER FOREIGN DATA WRAPPER ... HANDLER` or `NO HANDLER`, `VALIDATOR` or `NO VALIDATOR`, `OPTIONS (ADD / SET / DROP ...)` and `OWNER TO`. Only the keys the entry has are compared. Foreign tables are read through the handler of their server's wrapper, so `pgy diff` warns, naming them, when a plan changes or removes the handler of a wrapper that has foreign tables. When a declared function is compared with the live one, its schema is optional for `pg_catalog`, `public` and the schemas on the connection's `search_path` (see [Name resolution](#name-resolution)).
- **Privileges.** `USAGE` on a wrapper lets a role that is not a superuser create servers on it, and PostgreSQL demands it of the new owner in `ALTER SERVER ... OWNER TO`. The block follows the [grants](#grants) rules for objects without a `PUBLIC` default: it is authoritative, `PUBLIC` included. On an extension's wrapper, privileges are compared with the ones the extension set (`pg_init_privs`), and what the extension granted is left alone unless the block names the grantee, as for the other [objects an extension owns](#objects-an-extension-owns).
- **In the migration that creates the extension**, the entry of its wrapper is applied after `CREATE EXTENSION`. Before the extension is installed, pgy takes a wrapper to be an extension's when it is named like an extension the YAML declares (`postgres_fdw`, `file_fdw`, `mysql_fdw`) or is one PostgreSQL ships (`dblink_fdw` comes with `dblink`). Afterwards the database says which wrappers belong to an extension. An entry for the wrapper of one of PostgreSQL's own extensions gets no statements, and a warning, while that extension is neither installed nor declared.
- **Pruning.** A present block is authoritative: `--unsafe --prune` drops the wrappers it does not list with `DROP FOREIGN DATA WRAPPER`, after the servers the same plan drops and before `DROP EXTENSION`. The wrappers of extensions are never dropped. A wrapper that a remaining server still uses is kept, and `pgy diff` says so. Without the block, wrappers are unmanaged. An extension is kept while the YAML has an entry for its wrapper, or while a wrapper that stays uses one of its functions (see [Removing extensions](#removing-extensions)).
- **`pgy pull`** writes wrappers to `database.yml`. One that no extension owns is written in full, with `handler: ""`, `validator: ""` and `options: {}` for what it does not have, so the pulled YAML also notices a handler, a validator or options given to it later. An extension's wrapper is written only when something about it is no longer as the extension left it: the grantees whose privileges changed, its options, its comment, and with `--owners` an owner other than the extension's.

## Views

Views are defined inside `schema <name>:` blocks using `view <name>:` keys. They are created with `CREATE OR REPLACE VIEW`.

For views and materialized views that already exist, `pgy diff` compares the declared `query` with the live definition. PostgreSQL rewrites a stored query (qualifying names, adding casts and aliases, expanding `*`), so pgy has PostgreSQL rewrite the declared one too: it creates it as a temporary view, inside a transaction it always rolls back, and compares the two `pg_get_viewdef` outputs.

- **Same definition:** the view is left alone.
- **Changed query, same columns** (or new columns added at the end): `CREATE OR REPLACE VIEW`.
- **A column dropped, renamed or retyped:** `CREATE OR REPLACE VIEW` can't do that, so with `--unsafe` the view is dropped and recreated together with the declared views that select from it, which get back what they had (see [Recreated views](#recreated-views)). Without `--unsafe`, `pgy diff` warns. A view that an undeclared view (or one declared without a `query`) depends on is left alone with a warning.
- **A materialized view whose query changed:** dropped and recreated with its indexes under `--unsafe`, with a lock warning, since it is refilled and unreadable meanwhile.
- **A declared query that can't be compared,** because it uses something the plan creates: a plain view is replaced, and a materialized view is left alone, each with a warning.
- **A declared query the connecting role may not parse,** because it lacks USAGE on a schema the query names (a hosted platform's own schema, such as RDS's `rdsadmin`): the query is compared with the live definition as text. One that matches, as `pgy pull` writes it, is the same; one that doesn't is left as it is, never replaced, and `pgy diff` names it in one warning together with the other definitions it couldn't compare for a missing privilege.

The comparison needs the TEMP privilege (PUBLIC has it by default) and a writable session. On a read-only standby, `pgy diff` warns and leaves existing views as they are. `replace: true` still forces `CREATE OR REPLACE VIEW` on every diff.

```yaml
schema public:
  view active_users:
    query: "select id, email from users where active = true"
    replace: true   # re-emit CREATE OR REPLACE on every diff
    dependsOn:
      - table public.users
    grants:
      reporting: [select]
    securityInvoker: true   # check table permissions as the querying user (PostgreSQL 15+)
    checkOption: local      # local | cascaded | none
```

`securityInvoker`, `securityBarrier` and `checkOption` are diffed against the live view's options: a difference emits `ALTER VIEW ... SET (...)` / `RESET (...)`, no `--unsafe` needed. Omitted keys are unmanaged. Since `CREATE OR REPLACE VIEW` replaces a view's options wholesale, the `replace: true` path carries live options the YAML does not manage over into its `WITH (...)` clause.

A view or materialized view declared without `query` is never created: the declaration manages the `grants`, `owner`, `comment` and `columnComments` of one that already exists, such as an extension's `pg_stat_statements`. See [Objects an extension owns](#objects-an-extension-owns).

### Recreated views

pgy drops a view and creates it again from the YAML when its columns change (above), when the type of a column it uses changes ([Column alterations](#column-alterations)) and around an extension update ([Updating extensions](#updating-extensions)). In all three cases the new view gets back what the old one had:

- what the YAML declares for it: `comment`, `grants`, `owner`, options, `columnComments`, `columnDefaults`, `triggers`, `rules` and, for a materialized view, `indexes`;
- what the YAML leaves unmanaged, restored from the database: the grants on the view and on its columns (column grants can't be declared, so they are always restored), the comment, column comments and column defaults, the owner, the options, and the comments of declared triggers and rules;
- triggers and rules the YAML doesn't list, created again from their definitions in the database, with their comments. With `--prune`, which drops them anyway, they are not.

A materialized view is refilled; indexes on it that the YAML doesn't declare are not created again.

### Materialized Views

Use `materialized view <name>:` keys. Created with `CREATE MATERIALIZED VIEW IF NOT EXISTS`; a changed query is handled as described under [Views](#views) (there is no `REFRESH` support). `withData: false` creates it `WITH NO DATA`. `with` sets storage parameters such as `fillfactor` or `autovacuum_enabled`, diffed like [table storage parameters](#tables) (`ALTER MATERIALIZED VIEW ... SET / RESET`). `indexes:` takes the same entries as table indexes and is diffed the same way. A unique index makes `REFRESH MATERIALIZED VIEW CONCURRENTLY` possible.

```yaml
schema public:
  materialized view user_stats:
    query: "select count(*) as total, status from users group by status"
    withData: false
    with: {autovacuum_enabled: false}
    tablespace: archive
    indexes:
      user_stats_status:
        columns: [status]
        unique: true
    dependsOn:
      - table public.users
```

| Property | Type | Description |
|----------|------|-------------|
| `query` | string | The `SELECT` statement defining the view |
| `replace` | boolean | Plain views only. Always emit `CREATE OR REPLACE VIEW`, even when the query matches the live one |
| `securityInvoker` | boolean | Plain views only. `security_invoker` option (PostgreSQL 15+). Omitted = unmanaged |
| `securityBarrier` | boolean | Plain views only. `security_barrier` option. Omitted = unmanaged |
| `checkOption` | string | Plain views only. `local`, `cascaded`, or `none` to remove it. Omitted = unmanaged |
| `withData` | boolean | Materialized views only. `false` creates `WITH NO DATA` |
| `with` | map | Materialized views only. Storage parameters, authoritative when present. Omitted = unmanaged |
| `tablespace` | string | Materialized views only. See [Tablespaces](#tablespaces). Omitted = unmanaged |
| `accessMethod` | string | Materialized views only. Table access method (`USING`); changing it rewrites the view and needs `--unsafe`. See [Access methods](#access-methods). Omitted = unmanaged |
| `indexes` | map | Materialized views only. Same format as [table indexes](#indexes) |
| `triggers` | map | Plain views only. Same format as [table triggers](#triggers), usually `timing: instead of` (always `FOR EACH ROW`) to make a view writable |
| `columnComments` | map | View column → `COMMENT ON COLUMN` text, diffed against live |
| `columnDefaults` | map | Views only. View column → `DEFAULT` expression, used by `INSERT`s through the view (`ALTER VIEW ... ALTER COLUMN ... SET DEFAULT`). Diffed against live, [as an expression](#how-expressions-are-compared); columns not listed are unmanaged |
| `dependsOn` | list | See [Dependencies](#dependencies) |
| `grants` | map | Role → privilege list. Views and materialized views share table ACL semantics (`GRANT ... ON TABLE`). See [Grants](#grants) |

## Types

Types are defined inside `schema <name>:` blocks. A `type <name>:` declaration without `type` creates nothing: it manages the `grants`, `revokePublic`, `owner` and `comment` of a type that already exists, whatever its kind, such as an extension's `hstore`. See [Objects an extension owns](#objects-an-extension-owns).

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

For an existing enum, new labels are added with `ALTER TYPE ... ADD VALUE`, positioned `BEFORE` the next label that already exists so the declared order is preserved. Removing or reordering existing labels is not supported (PostgreSQL cannot drop enum values) and is silently ignored. To rename a label, use [`renamedLabels`](#renames). PostgreSQL rejects a new label until the transaction that added it commits, so `pgy diff` puts the `ADD VALUE` statements in a buffer section of their own, and `pgy commit` writes them to a `NNNN_<name>_enum_values.sql` migration numbered before the rest. Defaults, checks, indexes, views, partitions and `data:` rows in the main migration can then use the new labels.

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
        comment: expiry, in seconds since the epoch
    dependsOn:
      - type public.auth_role
```

Attribute order in `CREATE TYPE ... AS (...)` follows YAML declaration order (relevant for positional `ROW(...)::type` casts). An attribute can also be written as just its type: `attributes: {role: public.auth_role, exp: bigint}`. `collate` gives an attribute a collation: `label: {type: text, collate: C}`, and `comment` sets `COMMENT ON COLUMN` for it (diffed against live, like column comments). Changing it on an existing type emits `ALTER ATTRIBUTE ... TYPE ... COLLATE`, under `--unsafe` like other attribute type changes.

For an existing composite type, attributes the live type lacks are added with `ALTER TYPE ... ADD ATTRIBUTE` (appended, since PostgreSQL cannot insert an attribute in the middle). Under `--unsafe`, a changed attribute type emits `ALTER ATTRIBUTE ... TYPE` and attributes removed from YAML emit `DROP ATTRIBUTE`. PostgreSQL rejects both while a table column uses the type. Attribute types are compared like column types, [through the server](#how-types-are-compared) where the text differs.

### Range

```yaml
schema public:
  type floatrange:
    type: range
    subtype: float8
    subtypeDiff: float8mi           # optional: subtypeOpclass, collation, canonical, multirangeTypeName
```

Emitted as `CREATE TYPE ... AS RANGE (subtype = ..., ...)`, together with the multirange type PostgreSQL derives from it. Range types are create-only.

## Domains

Domains are defined inside `schema <name>:` blocks using `domain <name>:` keys. A domain is a named base type with an optional default, `NOT NULL`, and `CHECK` constraints: one with `check`, or several named ones with `checks` (the value under test is referenced as `VALUE`). Created with `CREATE DOMAIN`. For an existing domain, `default` and `notNull` are diffed (`ALTER DOMAIN ... SET/DROP DEFAULT`, `SET/DROP NOT NULL`; removing `default` drops it). On PostgreSQL 17+, `notNullName` names the NOT NULL constraint (default `<domain>_not_null`), and a different live name is renamed. The `check` constraint is matched by `constraintName`, or by PostgreSQL's default `<domain>_check` name: a missing one is added, while a changed or removed one is dropped and re-added only under `--unsafe`, since adding a domain constraint validates every column that uses the domain. The default and the checks are [compared as expressions](#how-expressions-are-compared), through the server where the text differs. The base type and collation cannot be changed in place; `pgy diff` warns when the base type differs from the live one, which it compares like a column type, [through the server](#how-types-are-compared) where the text differs. Comments are diffed against live.

```yaml
schema public:
  domain email:
    type: text
    notNull: true
    check: "value ~ '^[^@]+@[^@]+$'"
    constraintName: email_format
    comment: "validated email address"

  domain score:
    type: int
    checks:                     # several named checks
      score_min: VALUE >= 0
      score_max: VALUE <= 100

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
| `checks` | map | Named `CHECK` constraints (`name: expression`, or `name: {check: expression, comment: text}` to also set `COMMENT ON CONSTRAINT ... ON DOMAIN`). When present, the declared set is authoritative: a missing check is added, and with `--unsafe` a changed one is replaced and an undeclared one dropped. Can be combined with `check` |
| `comment` | string | `COMMENT ON DOMAIN` (diffed against live) |
| `dependsOn` | list | See [Dependencies](#dependencies) |

Tables using a domain as a column type must declare `dependsOn: [domain <schema.name>]` so the domain is created first.

## Sequences

Sequences are defined inside `schema <name>:` blocks using `sequence <name>:` keys. Created with `CREATE SEQUENCE IF NOT EXISTS`. Only the options set in YAML are emitted, and PostgreSQL defaults apply to the rest. For an existing sequence, options set in YAML (`as`, `increment`, `minValue`, `maxValue`, `start`, `cache`, `cycle`) are compared against `pg_sequence` and changes emit one `ALTER SEQUENCE`. A changed `unlogged` emits `ALTER SEQUENCE ... SET UNLOGGED` or `SET LOGGED`. A changed `ownedBy` emits `ALTER SEQUENCE ... OWNED BY`, and `ownedBy: none` removes the owner. Options left out are unmanaged. The sequence's current value is never touched, so new `minValue`/`maxValue` bounds must still contain it (PostgreSQL rejects the change otherwise). A sequence declared without options can also be one an extension creates: sequences are created after the extensions, so the declaration then only manages its `grants`, `owner` and `comment`. See [Objects an extension owns](#objects-an-extension-owns).

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
| `unlogged` | boolean | `CREATE UNLOGGED SEQUENCE`: changes aren't WAL-logged, so the sequence resets after a crash and isn't replicated. Sequences of identity and serial columns follow their table's `unlogged` setting instead |
| `ownedBy` | string | `table.column` (or `schema.table.column`; the table must be in the sequence's schema) — sequence is dropped with the column. Emitted as `ALTER SEQUENCE ... OWNED BY` after all tables are created, so it works even when the owning table is created in the same plan |
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

A `serial`/`bigserial` column and an `identity` column create their own sequence: `<table>_<column>_seq` (PostgreSQL shortens the two names to fit 63 bytes), or the identity's `sequenceName`. To grant on it or comment it, declare a `sequence` block of that name holding `grants` and `comment`. pgy never creates such a sequence itself, whatever the order of the declarations: the column does, and the grants and comment are applied after it. An identity's sequence options are set under the column's `identity` map. Such a sequence always has the owner of its table, so `pgy validate` rejects an `owner` on it. `pgy pull` writes such blocks itself, with a `dependsOn` on the table that older versions of pgy need:

```yaml
schema app:
  table audit:
    columns:
      id: {type: bigserial, primaryKey: true}
    grants: {app_role: [insert]}
  table events:
    columns:
      id: {type: bigint, identity: always, primaryKey: true}
  sequence audit_id_seq:
    grants: {app_role: [usage]}     # nextval() needs USAGE on the sequence
  sequence events_id_seq:
    grants: {app_role: [usage, select]}
```

A sequence PostgreSQL had to number because its name was taken (`audit_id_seq1`) is not recognised as the column's, so give such an identity a `sequenceName`.

## Functions

Functions are defined inside `schema <name>:` blocks. The key format is `function <name>(<args>):`.

A language's validator checks a body when the routine is created: a `language: sql` body against the catalog, PL/pgSQL syntax and `%TYPE` or `%ROWTYPE` declarations, a PL/Java body against the classes of the installed jars. A plan that creates or replaces a single routine and otherwise changes only privileges, comments, owners or routine attributes, or drops objects, the usual edit of one function, keeps that check, so a mistake in the body fails `pgy migrate`. When the plan creates or replaces more than one routine, or changes anything else (tables, columns, views, types, domains, extensions, schemas, ...), pgy creates the routines with a string body between `SET check_function_bodies = off` and `RESET check_function_bodies`, whatever the language, as `pg_dump` does; routines created one after another share one pair. Their bodies may then use relations and routines created later in the same migration, including a table whose default calls the function, and are checked when first run. PostgreSQL records no dependency between a string body and what it uses, so pgy cannot order them, and this is what lets YAML written by `pgy pull` rebuild a database. SQL-standard bodies are parsed at `CREATE` whatever the setting (see below).

`language: sql` routines may also use a SQL-standard body (PostgreSQL 14+), written as the `body` itself: `body: begin atomic select x + 1; end` or `body: return x * 2`. pgy emits it after the attributes instead of as an `AS` string. PostgreSQL parses such a body when the routine is created and records what it references, so the tables and routines it uses must exist first: declare them in `dependsOn`, which `pgy pull` fills in from `pg_depend`. PostgreSQL stores the body in a rewritten form, so pgy cannot compare it, as with views. A changed SQL-standard body is applied only while `replace: true` is set. Switching between a string body and a SQL-standard one is always applied.

A string `body` is dollar-quoted in the SQL pgy emits. A body on one line is passed to PostgreSQL as written. A body of several lines goes on the lines between the dollar quotes: PostgreSQL stores a newline, the body, and a closing newline unless the body already ends with one, so a `|` block and a `|-` block create the same function. Whitespace around the body is ignored when it is compared with the live function, so these newlines never show up as a change. `pgy pull` writes every body so that the function is recreated with the same source byte for byte, which matters for whitespace-sensitive languages such as PL/Python: a body indented as a whole keeps the indentation of its first line (`body: |2`). A stored source the two rules above cannot produce, such as one of several lines that starts right after the opening quote, is pulled with three newlines appended; pgy takes a body ending in three or more newlines as written, without those three. `body: ""` declares a function with an empty body, which `sql`, PL/Perl and PL/Tcl accept; leaving `body` out is an attribute-only declaration (see the table below).

The same goes for procedures. A routine's `language` is compared with the live one, and a different language is applied with `CREATE OR REPLACE`.

A language's validator may store something other than the source. PL/Rust (`plrust`) compiles a function when it is created and keeps a JSON document in `prosrc`, holding the source, the toolchain version and the compiled library for the server's architecture. For `plrust` routines pgy compares and pulls the `src` member of that document, so the YAML holds the Rust source only.

Overloads — functions sharing a name but taking different arguments — are separate declarations: `function area(r float8):` and `function area(w float8, h float8):` are created, compared, granted and commented independently. The same applies to procedures.

Argument types may be schema-qualified, e.g. `function fn(t public.my_type):` or `function fn(t app.my_type):`. `fn(t my_type)` and `fn(t public.my_type)` refer to the same function, `fn(n integer)` and `fn(n pg_catalog.int4)` too, and so do `fn(t my_type)` and `fn(t app.my_type)` while `app` is on the connection's `search_path` (see [Name resolution](#name-resolution)).

Type modifiers on argument types (`varchar(10)`, `numeric(6,2)`, `timestamp(0)`, `interval day to second`, PostGIS `geometry(Point,4326)`) are accepted and kept in the `CREATE` statement, but PostgreSQL discards them, so `fn(x varchar(10))` is the live `fn(x character varying)` and pgy ignores them when it matches routines. This holds for the arguments of functions, procedures and aggregates, `OUT` and `INOUT` ones included; a function's return type keeps no modifier either. Two declarations that differ only in modifiers are the same routine.

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
| `transform` | list | Types of the `TRANSFORM FOR TYPE` clause, e.g. `[hstore, jsonb]`. See [Transforms](#transforms). Omitted = unmanaged; `[]` = none |
| `security` | string | `definer` or `invoker` |
| `stable` | boolean | Marks function `STABLE` |
| `volatile` | boolean | Marks function `VOLATILE` |
| `immutable` | boolean | Marks function `IMMUTABLE` |
| `strict` | boolean | `STRICT` vs `CALLED ON NULL INPUT`. Omitted = unmanaged |
| `leakproof` | boolean | `LEAKPROOF` vs `NOT LEAKPROOF` (requires superuser to apply). Omitted = unmanaged |
| `parallel` | string | `safe`, `restricted` or `unsafe` → `PARALLEL ...`. PostgreSQL's default is `unsafe`, which keeps any query calling the function off parallel plans. Omitted = unmanaged |
| `cost` | number | Planner cost estimate (`COST`, in `cpu_operator_cost` units; default 100). Omitted = unmanaged |
| `rows` | number | Estimated result rows for set-returning functions (`ROWS`; default 1000). `pgy validate` rejects it on functions that don't return `setof` or `table`. Omitted = unmanaged |
| `set` | map | `SET` configuration options (e.g. `search_path`). Values are written and quoted as for [role settings](#roles): strings, numbers or booleans, one value each unless the parameter is a list such as `search_path`. Diffed against live `proconfig`: changed or missing settings emit `ALTER FUNCTION ... SET`, live settings absent from the map emit `RESET`. Quotes, whitespace around list commas and parameter name case are ignored when comparing. Omitted = unmanaged; `set: {}` resets every setting. `pgy pull` writes `set: {}` for a `security: definer` function without settings, so that one added later, such as a `search_path`, is reset; for other functions without settings it leaves the key out. For a parameter an extension defines, see [Parameters an extension defines](#parameters-an-extension-defines) |
| `body` | string | Function body (dollar-quoted in output SQL) |
| `grants` | map | Role → privilege list. See [Grants](#grants) |
| *(replace-on-change)* | — | Existing functions are compared against the live definition. A body change (via `prosrc`), a language change or a changed `transform` list emits `CREATE OR REPLACE FUNCTION`. PostgreSQL resets every attribute that statement does not give, so pgy gives each attribute the YAML leaves out (security, volatility, strict, leakproof, parallel, cost, rows, set) its live value, and the declared ones their declared value. When only attributes differ (volatility, security, strict, leakproof, parallel, cost, rows, set), `ALTER FUNCTION` is emitted instead — no body replacement. A return type change cannot be made in place, so with `--unsafe` it emits `DROP FUNCTION` and `CREATE FUNCTION` and restores the comment, grants and owner (the drop fails while views, triggers or defaults use the function); without it, `pgy diff` warns. Changing the argument types declares a different function: the old one stays, or is dropped with `--unsafe --prune`. Renaming arguments or changing their modes while the types stay the same is still the same function to PostgreSQL, and `CREATE OR REPLACE` can't do it, so it is handled like a return type change: `DROP` and `CREATE` with `--unsafe`, a warning without. Argument defaults (`a int default 1` or `a int = 1`) are compared too, [as expressions](#how-expressions-are-compared): a changed or added default emits `CREATE OR REPLACE`, while a removed one needs `DROP` and `CREATE`, as above. Procedures work the same way. To rename a function and keep its arguments, use [`renamedFrom`](#renames) |
| *(attribute-only declarations)* | — | Omitting `body`, `returns` and `language` leaves the function itself unmanaged: pgy emits `ALTER FUNCTION` for managed attributes that differ from live and never `CREATE OR REPLACE`. Use this to manage attributes and privileges of extension-owned functions (e.g. marking PostGIS's `geometry_overlaps(geometry, geometry)` leakproof — a `LANGUAGE c` function whose body pgy cannot express). The function is found by its argument types. If it doesn't exist and the plan creates no extension, the declaration is skipped with a warning. See [Objects an extension owns](#objects-an-extension-owns) |
| `revokePublic` | boolean | Emit `REVOKE ALL ... FROM PUBLIC` (security-definer pattern) |
| `dependsOn` | list | See [Dependencies](#dependencies) |

### Transforms

A transform converts a type between its SQL form and a procedural language's native one: with `hstore_plpython3u` a PL/Python function receives an `hstore` argument as a dict instead of a string. PostgreSQL ships transforms as extensions (`hstore_plperl`, `hstore_plpython3u`, `jsonb_plperl`, `jsonb_plpython3u`, `ltree_plpython3u`, `bool_plperl` and the `plperlu` variants), and a function or procedure opts in per type with `TRANSFORM FOR TYPE`, which is the `transform` list:

```yaml
extensions:
  - {name: hstore_plpython3u, cascade: true}   # installs hstore and plpython3u too
  - {name: jsonb_plpython3u, cascade: true}

schema app:
  function merge(doc jsonb, attrs hstore):
    returns: jsonb
    language: plpython3u
    transform: [jsonb, hstore]     # TRANSFORM FOR TYPE jsonb, FOR TYPE hstore
    body: |
      doc.update(attrs)
      return doc
```

The list is compared with the routine's live transforms (`pg_proc.protrftypes`) in any order, and a difference emits `CREATE OR REPLACE`. Without `transform` the routine's transforms are unmanaged: pgy does not compare them, and a `CREATE OR REPLACE` it emits for another reason keeps the ones the routine has. `transform: []` declares a routine without transforms. `pgy pull` writes the list, and writes `transform: []` for the other routines of a language that has transforms, so that one added later is noticed. A clause written into `language` (`language: plpython3u transform for type hstore`) is read the same way. Transforms themselves (`CREATE TRANSFORM`) come from their extensions; `pgy pull` reports one that no extension owns.


## Aggregates

Aggregates are defined inside `schema <name>:` blocks with `aggregate <name>(<args>):` keys (`(*)` for `count(*)`-style aggregates) and emitted as `CREATE OR REPLACE AGGREGATE` after the functions they use. A definition that differs from `pg_aggregate` is replaced in place.

```yaml
schema app:
  function add2(a int, b int):
    returns: int
    language: sql
    immutable: true
    body: select a + b
  aggregate total(int):
    sfunc: app.add2          # state transition function (required)
    stype: int               # state type (required)
    initcond: "0"            # optional initial state, as a string
    combinefunc: app.add2    # optional: enables parallel aggregation
    parallel: safe           # safe | restricted | unsafe
    comment: sum of ints
```

`initcond: ""` is an initial state of its own, the empty string (an empty `text` accumulator, the empty `hstore`), and is emitted, compared and pulled as such. Leave `initcond` out for an aggregate that starts from NULL. `minitcond` works the same way.

The other `CREATE AGGREGATE` options use the same names, in camelCase where PostgreSQL's have an underscore:

| Property | Description |
|----------|-------------|
| `sspace` | Average state size in bytes (planner hint) |
| `finalfuncExtra` | `true` passes the aggregate's arguments to `finalfunc` as extra null arguments |
| `finalfuncModify` | `read_only` (default), `shareable` or `read_write`: whether `finalfunc` modifies the state |
| `serialfunc`, `deserialfunc` | Serialize an `internal` state, so it can be combined in parallel |
| `msfunc`, `minvfunc`, `mstype` | Moving-aggregate mode for window frames: forward and inverse transition functions and their state type |
| `msspace`, `mfinalfunc`, `mfinalfuncExtra`, `mfinalfuncModify`, `minitcond` | The moving-aggregate versions of `sspace`, `finalfunc`, `finalfuncExtra`, `finalfuncModify` and `initcond` |
| `sortop` | Sort operator for `min`/`max`-like aggregates, which can then use an index (`">"`, or `myschema.>`) |
| `hypothetical` | `true` for a hypothetical-set aggregate (ordered-set aggregates only) |

These are compared with `pg_aggregate` only when set; options left out are unmanaged. A replacement redefines the whole aggregate, though, so an option missing from the YAML is lost when another one changes. Ordered-set aggregates put `order by` between their direct and aggregated arguments, as in `aggregate my_pct(float8 order by float8):` or `aggregate my_mode(order by anyelement):`. `hypothetical: true` makes one a hypothetical-set aggregate, like `rank`. PostgreSQL can't change `hypothetical` with `CREATE OR REPLACE`, so `pgy diff` drops and re-creates the aggregate under `--unsafe`, and warns without it.

```yaml
schema app:
  aggregate my_rank(variadic "any" order by variadic "any"):
    sfunc: ordered_set_transition_multi
    stype: internal
    finalfunc: rank_final
    finalfuncExtra: true
    hypothetical: true
```

`grants` (role -> `[execute]`) and `revokePublic` work as on [functions](#functions); the statements are `GRANT ... ON FUNCTION`, with the direct and aggregated arguments of an ordered-set aggregate in one list. An aggregate declared without `sfunc`, `stype` and the other options creates nothing: it manages the `grants`, `revokePublic`, `owner` and `comment` of one that already exists, such as PostGIS's `st_union(geometry)`. See [Objects an extension owns](#objects-an-extension-owns).

## Procedures

Procedures (PostgreSQL 11+, invoked with `CALL`) are defined inside `schema <name>:` blocks using `procedure <name>(<args>):` keys. Argument types may be schema-qualified; unqualified types are treated as `public` (same rules as functions). Unlike functions they have no return type, volatility, or strictness. Existing procedures are compared against the live definition (body via `prosrc`, language, `transform`, security, `set` via `proconfig`); on change, `CREATE OR REPLACE PROCEDURE` is emitted, with the live `security` and `set` when the YAML leaves them out. Bodies are stored and pulled as described under [Functions](#functions), and `body: ""` is an empty body.

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
| `transform` | list | Types of the `TRANSFORM FOR TYPE` clause. See [Transforms](#transforms). Omitted = unmanaged; `[]` = none |
| `security` | string | `definer` or `invoker` |
| `set` | map | `SET` configuration options (e.g. `search_path`). Compared and pulled like function `set`; omitted = unmanaged. See [Parameters an extension defines](#parameters-an-extension-defines) |
| `body` | string | Procedure body (dollar-quoted in output SQL) |
| `grants` | map | Role → privilege list. See [Grants](#grants) |
| `revokePublic` | boolean | Emit `REVOKE ALL ... FROM PUBLIC` (security-definer pattern) |
| `comment` | string | `COMMENT ON PROCEDURE` (diffed against live) |
| `dependsOn` | list | See [Dependencies](#dependencies) |

Signature changes are not supported. Create a new procedure instead. To rename a procedure and keep its arguments, use [`renamedFrom`](#renames).

A procedure declared without `body` and `language` is never created: the declaration manages the `security`, `set`, `grants`, `revokePublic`, `owner` and `comment` of one that already exists, with `ALTER PROCEDURE` for the first two. See [Objects an extension owns](#objects-an-extension-owns).

## Languages

A top-level `languages:` block manages the privileges of procedural languages: those every database has (`sql`, `plpgsql`) and those extensions create (`plperl`, `pltcl` and so on). `USAGE` on a language is what lets a role create functions in it, and `PUBLIC` has it by default.

```yaml
extensions: [plperl]

languages:
  plpgsql:
    grants: {public: [], app_dev: [usage]}   # only app_dev may create PL/pgSQL routines
  plperl:
    grants: {app_dev: [usage]}
```

| Property | Type | Description |
|----------|------|-------------|
| `grants` | map | Required. Role → privileges (`usage`, or `usage with grant option`; `all` stands for `usage`) |

- **Privileges only.** pgy never creates or drops a language: `CREATE LANGUAGE` needs handler functions written in C, so languages come from extensions. In the migration that creates a language's extension, the grants run after `CREATE EXTENSION`. pgy takes the extension to be the one named like the language (`plperl` for the language `plperl`), which is how the extensions of procedural languages are called. Any other language that does not exist gets no statements, and `pgy diff` warns with the name of the entry.
- **`PUBLIC`** follows the [grants](#grants) rule for objects with a default privilege, like functions and types: it is left alone unless the block lists it. `public: []` revokes the default `USAGE`, and `public: [usage]` keeps or restores it. The other roles are exact: `USAGE` held by a role the block does not list is revoked, except for what an extension itself granted on its language (see [Objects an extension owns](#objects-an-extension-owns)).
- **Trusted languages only.** PostgreSQL rejects `GRANT` and `REVOKE` on untrusted languages (`c`, `internal`, `plpython3u`, `plperlu`, `pltclu`), which only superusers can use. `pgy validate` rejects entries for those, and `pgy diff` reports any other untrusted language and plans nothing for it.
- **Order.** The statements run right after the extension statements and before any function or procedure, so a migration that grants its own role `USAGE` can create routines in that language further down. With `diff --split`, they go into the first group, together with the extensions.
- **`pgy pull`** writes an entry for each trusted language whose privileges are not its initial ones, with the grantees that changed. The initial privileges are those `pg_init_privs` records for a language an extension owns, which `plpgsql` is, and `PUBLIC`'s `USAGE` for any other. That includes `sql`, whose privileges `pg_dump` leaves out.

## Collations

Collations are defined inside `schema <name>:` blocks using `collation <name>:` keys and created with `CREATE COLLATION IF NOT EXISTS` before types, domains and tables. PostgreSQL cannot change a collation's definition in place, so existing collations are left as they are; only comments are diffed.

```yaml
schema app:
  collation case_insensitive:
    provider: icu              # icu | libc | builtin (PostgreSQL 17+)
    locale: und-u-ks-level2
    deterministic: false       # equality ignores case
    comment: case-insensitive text
  collation c_copy:
    from: pg_catalog.C         # copy an existing collation instead
  table users:
    columns:
      email:
        type: text
        collate: app.case_insensitive
```

| Property | Type | Description |
|----------|------|-------------|
| `provider` | string | `icu`, `libc` or `builtin` |
| `locale` | string | Locale for the provider |
| `lcCollate` / `lcCtype` | string | libc `LC_COLLATE` / `LC_CTYPE` |
| `deterministic` | boolean | `false` makes equal-under-collation strings compare equal (ICU only) |
| `rules` | string | ICU tailoring rules (PostgreSQL 16+) |
| `from` | string | Copy an existing collation (`CREATE COLLATION ... FROM`); other options are ignored |
| `dependsOn` | list | See [Dependencies](#dependencies) |
| `comment` | string | `COMMENT ON COLLATION`, diffed against live |

Column and domain `collate` values may name these collations schema-qualified; quotes around names are optional.

## Text Search

Text search configurations and dictionaries are declared inside `schema <name>:` blocks with `text search configuration <name>:` and `text search dictionary <name>:` keys. Both are created before types and tables, so generated `tsvector` columns and indexes can use them.

```yaml
extensions: [unaccent]
schema app:
  text search dictionary english_nostop:
    template: snowball
    options: {language: english}
  text search configuration english_unaccent:
    copy: pg_catalog.english          # or parser: default
    mappings:
      asciiword: [unaccent, app.english_nostop]
      word: [unaccent, app.english_nostop]
      email: []                       # remove the mapping
    comment: accent-insensitive english
  table docs:
    columns:
      body: {type: text}
      tsv:
        type: tsvector
        nullable: true
        generated: "to_tsvector('app.english_unaccent'::regconfig, body)"
```

`mappings` lists only the token types the YAML manages: for an existing configuration they are compared against `pg_ts_config_map` and emit `ADD`, `ALTER` or `DROP MAPPING`. Other token types keep whatever they have. A dictionary's `options` are compared with live and changed with `ALTER TEXT SEARCH DICTIONARY`, which also removes options the YAML doesn't list (omit `options` to leave them unmanaged; `options: {}` means none, which is what `pgy pull` writes for a dictionary without options); its template can't be changed in place, so a changed one is reported. A template an extension provides can be named with or without its schema (`template: unaccent`, `template: public.unaccent`). `owner` is diffed with `ALTER ... OWNER TO`. Comments are diffed, and `--prune` drops undeclared configurations and dictionaries (extension-owned ones are kept).

A dictionary declared without `template` is never created: it manages the `options`, `owner` and `comment` of one that already exists. That is how to configure the dictionaries that `dict_int` (`intdict`), `dict_xsyn` (`xsyn`) and `unaccent` (`unaccent`) create, as their documentation does with `ALTER TEXT SEARCH DICTIONARY`. In the migration that creates the extension, the options are set right after it; options the extension's script set and the YAML doesn't list are removed by the next diff. See [Objects an extension owns](#objects-an-extension-owns).

```yaml
extensions:
  - {name: dict_int, schema: public}
  - {name: unaccent, schema: public}
schema public:
  text search dictionary intdict:
    options: {maxlen: 4, rejectlong: true}
  text search dictionary unaccent:
    options: {rules: my_rules}
```

## Row Level Security

Tables accept `rowLevelSecurity: true` / `false` and a `policies:` map of policy
name → spec.

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
      not_archived:
        as: restrictive
        using: "archived_at is null"
```

| Property | Type | Description |
|----------|------|-------------|
| `rowLevelSecurity` | boolean | `true` emits `ALTER TABLE ... ENABLE ROW LEVEL SECURITY` when not already enabled. `false` emits `... DISABLE ROW LEVEL SECURITY` when currently enabled, and only under `--unsafe`. Omit the key to leave the live setting alone |
| `forceRowLevelSecurity` | boolean | `true` emits `ALTER TABLE ... FORCE ROW LEVEL SECURITY`, so policies apply to the table owner too. `false` emits `NO FORCE ROW LEVEL SECURITY`, only under `--unsafe` since it widens access. Omitted = unmanaged |
| `policies.<name>.as` | string | `permissive` (default) or `restrictive`. Permissive policies are OR-ed together; every restrictive policy must also pass |
| `policies.<name>.for` | string | `select` \| `insert` \| `update` \| `delete` \| `all`. Omit for `ALL` |
| `policies.<name>.to` | string or list | Role(s). Omit for all roles |
| `policies.<name>.using` | string | `USING` expression |
| `policies.<name>.withCheck` | string | `WITH CHECK` expression |

Behavior:

- Policies are matched by name, and their definitions are diffed. A live policy whose `as`, `for`, `to`, `using` or `withCheck` no longer matches is dropped and recreated in the same migration transaction. This is not gated behind `--unsafe`: `DROP POLICY` is metadata-only and has no dependents.
- Expression comparison is normalized, so the casts, parentheses and quoting PostgreSQL adds when echoing an expression back (`member_id = (current_setting('app.member_id'::text))::bigint`) do not count as a change. A `using` or `withCheck` expression that still differs is [compared through the server](#how-expressions-are-compared), so a constant may be written in any form its type reads. Role lists are compared order- and case-insensitively.
- A present `policies:` block is authoritative: live policies not listed are dropped. Tables without a `policies:` block are unmanaged.
- `ENABLE ROW LEVEL SECURITY` and `CREATE POLICY` are emitted after all object creates/alters, so policy expressions may freely reference functions created in the same plan.
- `rowLevelSecurity` is tri-state. Omitting the key leaves the table's live RLS setting untouched, so adopting `pgy` for an existing database never silently changes it.
- `rowLevelSecurity: false` is **gated behind `--unsafe`**. Disabling RLS stops every policy on the table from filtering rows, which widens access for all non-owner roles — it is a security-relevant change, so it never happens on a default `diff`. Note that disabling RLS does not drop the policies; they remain defined and take effect again if RLS is re-enabled.

## Roles

Roles are cluster-level objects declared in a top-level `roles:` map (not inside a `schema <name>:` block). Each entry is created with `CREATE ROLE` if missing from the live database and skipped otherwise. The statement is wrapped in a `DO $$ ... $$` block guarded by a `pg_roles` lookup, so it is a no-op if the role already exists in the cluster (roles survive database re-creation, so `diff --from-empty` cannot assume they are absent). For an existing role, the attributes the YAML sets explicitly (`login`, `superuser`, `createdb`, `createrole`, `replication`, `bypassRLS`, `inherit`, `connectionLimit`) are compared against `pg_roles`, and differences emit `ALTER ROLE ... WITH ...`. Attributes left out are unmanaged, so declaring a role with no attributes never changes it. Roles are never dropped, and removed memberships are never revoked.

```yaml
roles:
  readonly: {}
  app_user:
    login: true
    inherit: false
    connectionLimit: 10
    validUntil: "2030-01-01T00:00:00Z"
    inRoles: [readonly, {role: migrator, admin: true, inherit: false}]
    set: {search_path: "app, public", statement_timeout: 30s}
    comment: "application login role"
  migrator:
    login: true
    createdb: true
    createrole: true
```

Behavior:

- Role creates are emitted **first** in the plan, before schemas and all other objects, so grants, policies (`to:`), and memberships can reference them.
- `inRoles` memberships are added when missing from live (`pg_auth_members`): `GRANT <parent> TO <role>`. With `--unsafe --prune`, a role that declares `inRoles` (even `inRoles: []`) also loses the memberships it doesn't list: `REVOKE <parent> FROM <role>`, once per grantor (`GRANTED BY`) on PostgreSQL 16+, which records a grant per grantor. A role without `inRoles` keeps its memberships. On PostgreSQL 16+, `CREATE ROLE` run by a role that is not a superuser makes that role a member of the new role `WITH ADMIN OPTION` (granted by the bootstrap superuser, without `INHERIT` and `SET`), which is how it manages the roles it created; `--prune` never revokes such a membership of the role pgy connects as. An entry can be a map that also sets the membership's options: `admin` (`WITH ADMIN OPTION`), and on PostgreSQL 16+ `inherit` and `set`. Options it sets are diffed against live (`REVOKE ADMIN OPTION FOR`, or `GRANT ... WITH INHERIT/SET`), and options left out are unmanaged. On PostgreSQL 15, `inherit` and `set` are skipped with a warning.
- `validUntil` is compared with `rolvaliduntil`. Give it a zone (`2030-01-01T00:00:00Z`), since a value without one is read in the server's time zone when it's set but compared as UTC. `infinity` removes the expiry.
- Passwords are intentionally unsupported — secrets do not belong in schema YAML. Set passwords out of band (`ALTER ROLE ... PASSWORD`).
- `pgy pull --roles` writes every role except the `pg_` roles and the bootstrap superuser that `initdb` created (whatever its name; a role called `postgres` that is not that superuser, as on Supabase, is written). The role pgy connects as is written with its memberships, settings and comment only, leaving its attributes unmanaged, since it exists wherever pgy can connect but its memberships need not; the memberships `CREATE ROLE` gave it (see `inRoles` above) are left out, because they come back when it creates those roles again.
- `comment` is diffed against `pg_shdescription` like other object comments.
- `set` manages the role's settings for all databases (`ALTER ROLE ... SET`, stored in `pg_db_role_setting`). When present it is authoritative: changed settings are set, and settings it doesn't list are reset. Values are written as in `postgresql.conf`. Anything that isn't a number or a plain word, such as `30s`, `64MB` or `America/New_York`, is quoted for you, and so are words SQL reserves, such as `all` (the same applies to function and procedure `set:`, and to `database:`). A parameter an extension defines may need a grant first; see [Parameters an extension defines](#parameters-an-extension-defines).
  - A value is one value, commas included: `application_name: "a,b"` sets `a,b`. Only PostgreSQL's quoted-list parameters are split on commas into elements: `search_path`, `temp_tablespaces`, `session_preload_libraries`, `shared_preload_libraries`, `local_preload_libraries` and `unix_socket_directories`.
  - An empty string sets an empty value (`search_path: ""` is `SET search_path = ''`).
  - YAML numbers and booleans are fine (`statement_timeout: 0`, `jit: false`).
  - A value you quote yourself as one SQL string is used as it is: `datestyle: "'ISO, MDY'"`. `pgy pull` writes a value that contains quotes this way.

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
| `validUntil` | string | `VALID UNTIL`: when the role's password stops being accepted, or `infinity` |
| `inRoles` | list | Parent roles; each emits `GRANT <parent> TO <role>` when missing. An entry `{role, admin, inherit, set}` also sets the membership's options |
| `set` | map | Settings for the role in all databases (`ALTER ROLE ... SET` / `RESET`); omit to leave them unmanaged |
| `comment` | string | `COMMENT ON ROLE` |

## Grants

Tables, views, materialized views, sequences (`usage`, `select`, `update`), types and domains (`usage`), functions, procedures, schemas, foreign servers, [foreign-data wrappers](#foreign-data-wrappers) and [languages](#languages) (`usage`), and individual columns accept a `grants:` map of role name → privilege list. Privileges are lowercased in output SQL; roles can be declared in a top-level [`roles:`](#roles) block or must already exist. View grants are emitted as `GRANT ... ON TABLE` (views are relations; this matches pg_dump). A privilege written as `insert with grant option` is granted `WITH GRANT OPTION`, which lets the role grant it on. Removing the suffix later emits `REVOKE GRANT OPTION FOR insert ...` and keeps the privilege itself.

`all` (or `all privileges`, optionally `with grant option`) stands for every privilege of the object's kind, which is how PostgreSQL stores `GRANT ALL`. `pgy diff` expands it before comparing, using the server's version, and writes the expanded list in the migration:

| Kind | `all` stands for |
|------|------------------|
| Tables, views, materialized views, foreign tables | `select`, `insert`, `update`, `delete`, `truncate`, `references`, `trigger`, and `maintain` on PostgreSQL 17 and later |
| Columns | `select`, `insert`, `update`, `references` |
| Sequences | `usage`, `select`, `update` |
| Functions, procedures | `execute` |
| Schemas | `usage`, `create` |
| Types, domains, foreign servers, foreign-data wrappers, languages | `usage` |

The same holds for the kinds of [default privileges](#default-privileges). So `all` on a table grants `MAINTAIN` on PostgreSQL 17 and 18, and a database upgraded to 17 gets it with the next migration. Without a server, as in `diff --from-empty`, `all` stays in the migration as `GRANT ALL`. `pgy pull` writes the expanded list, as PostgreSQL reports it.

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

- A present `grants:` block is **authoritative** for that object: missing privileges are granted; live privileges for roles not listed (or privileges removed from a role's list) are revoked. Object owners are never touched: an owner holds every privilege implicitly, so grants to it in the YAML are ignored (`pgy validate` warns about them). The owner is the declared `owner`, else the live owner, else the role running the migration. Roles can also be `current_user`, `current_role` or `session_user`, and are compared as the connected role.
- Objects **without** a `grants:` block are unmanaged — no grants or revokes are emitted.
- An **empty privilege list** (`some_role: []`) is not the same as omitting the block. It means "this role holds nothing on this object" and revokes whatever the live database still grants. Use it to take a privilege away while keeping the object managed.
- How a `grants:` block treats `PUBLIC` depends on whether PostgreSQL gives `PUBLIC` anything by default:
  - **Tables, views, materialized views, sequences, columns, foreign servers, foreign-data wrappers and schemas other than `public`** get nothing by default, so a `PUBLIC` grant on them was made on purpose. A grants block covers `PUBLIC` like any other role: `PUBLIC` privileges it doesn't list are revoked, and `pgy diff` warns about each such revoke. List `public` to keep them: `grants: {public: [select]}`.
  - **Functions, procedures, types, domains, languages and the `public` schema** come with `PUBLIC` privileges (`EXECUTE`, `USAGE`). Their grants block leaves `PUBLIC` alone unless it lists `public`. `public: []` revokes the default, and `public: [execute]` keeps it. On routines, types and domains, `revokePublic: true` does the same as `public: []` (recommended for `security: definer` functions); it is emitted for new objects and whenever the live database still grants the default.
  - Before v0.43.0 a grants block never revoked `PUBLIC` privileges, so an upgrade can revoke `PUBLIC` grants that were made by hand; the warnings name each one.
  - `policy.public_grants` in the config changes this: `authoritative` makes every grants block cover `PUBLIC`, routines, types, domains and the `public` schema included, and `explicit` makes only blocks that list `public` cover it. See [Strict mode](../README.md#strict-mode).
- On **objects an extension owns**, the privileges the extension's own script granted are left alone unless the block names their grantee. See [Objects an extension owns](#objects-an-extension-owns).
- Grant/revoke statements are emitted after all object creation and FK alters. Those on languages and foreign-data wrappers are the exception: they run right after the extension statements, ahead of the routines and servers that need them.
- **All `REVOKE`s are emitted before all `GRANT`s.** PostgreSQL's `REVOKE ... ON TABLE` also strips that role's column-level privileges, so narrowing a table-wide grant to per-column grants in a single migration only works if the revoke runs first. Ordering this the other way silently leaves the role with no privileges at all.

### Ownership

Schemas (in the `schema <name>:` block or `schemas:` map entry), tables, views, materialized views, sequences, functions, procedures, types and domains accept `owner: <role>`. A differing live owner emits `ALTER ... OWNER TO`, before any grants. Omitted = unmanaged. The sequence of a serial or identity column always has its table's owner and takes no `owner`. `pgy pull --owners` writes the owners that are not the connecting role, and every owner of the kinds a `policy.require_owners` rule lists (see the [README](../README.md#strict-mode)).

```yaml
schema app:
  owner: app_owner
  table orders:
    owner: app_owner
    columns:
      id: {type: bigint}
```

### Default Privileges

A top-level `defaultPrivileges:` list manages `ALTER DEFAULT PRIVILEGES`, the privileges objects receive automatically when they are created later.

```yaml
defaultPrivileges:
  - for: app_owner            # role creating the objects (default: the connecting role)
    in: [app]                 # schemas (omit for all schemas)
    tables: {app_read: [select], app_rw: [select, insert, update, delete]}
    sequences: {app_rw: [usage]}
  - for: app_owner
    functions: {public: []}   # stop granting EXECUTE to PUBLIC by default
```

Kinds are `tables`, `sequences`, `functions`, `types` and `schemas` (the last only without `in`). Like grants, the list for each grantee is authoritative: missing privileges are granted, extra ones revoked, and an empty list revokes everything. Grantees not listed are unmanaged. `select with grant option` sets up the privilege `WITH GRANT OPTION`, as in grants blocks. The implicit `EXECUTE` on functions and `USAGE` on types that PostgreSQL gives `PUBLIC` count as live privileges. Default privileges are emitted right after schemas, before any object is created, so objects created by the same migration already receive them. An object created by the plan that declares its own `grants` is compared against what those defaults give it, so grants it doesn't list are revoked in the same migration. `grants: {}` therefore keeps a new table free of them. When a database has default privileges that some older objects lack, `pgy pull` writes `grants: {}` on those objects, so a rebuild doesn't hand them grants the original never had.

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
- Column grants cover `PUBLIC` like tables do: a `PUBLIC` column privilege the column's block doesn't list is revoked, with a warning.
- To narrow a table-wide grant to specific columns, empty the table-level list and add per-column grants in the same change: `grants: {role: []}` on the table plus `grants: {role: [select]}` on each column. The table revoke is ordered ahead of the column grants, so the result is select on exactly those columns.
- Column grants are independent of table-level grants: a table-level `select` already covers every column, so column grants are typically used *instead of* a table-level privilege to restrict access to a subset of columns.

| Property | Type | Description |
|----------|------|-------------|
| `grants` | map | Role name → list of privileges (e.g. `select`, `insert`, `execute`, `usage`, `create`; on columns: `select`, `insert`, `update`, `references`) |
| `revokePublic` | boolean | Functions, procedures, types and domains. Revoke `PUBLIC`'s default `EXECUTE` or `USAGE` |

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
      user_id:
        type: bigint
    indexes:
      orders_user_idx:
        columns: [user_id]
        comment: "lookup by user"
    foreignKeys:
      fk_user:
        columns: [user_id]
        references: { table: app.users, columns: [id] }
        comment: "@fieldName owner"
  function fn():
    returns: int
    language: sql
    comment: "computes things"
    body: select 1
```

Supported on: schemas, tables, columns, primary keys, indexes (also on materialized views), foreign keys, check/unique/exclude constraints, triggers, policies, types, domains, collations, functions, procedures, views, materialized views, sequences, roles, publications and event triggers.

Index comments are emitted as `COMMENT ON INDEX <schema>.<index>` (auto-named indexes use the derived name); constraint comments (foreign keys included) as `COMMENT ON CONSTRAINT <name> ON <schema>.<table>`; trigger and policy comments as `COMMENT ON TRIGGER|POLICY <name> ON <schema>.<table>`. When a constraint, trigger or policy is dropped and recreated because its definition changed, its comment is re-applied in the same migration.

Behavior:

- Emitted when the desired comment differs from the live comment; skipped when identical (idempotent).
- An absent or empty `comment:` is unmanaged — existing comments are never cleared. To clear a comment, run `COMMENT ON ... IS NULL` manually. The exception is an extension, which `CREATE EXTENSION` gives its control file's comment: there `comment: null` removes it (see [Extensions](#extensions)).
- Single quotes are escaped automatically.

## Publications

Logical replication publications are declared in a top-level `publications:` map.

```yaml
publications:
  app_cdc:
    tables:
      - public.users
      - table: public.orders
        columns: [id, status, total]      # column list
        where: "status <> 'draft'"        # row filter
      - {table: public.events, only: true} # TABLE ONLY: without its inheritance children
    publish: [insert, update, delete]     # default: all actions
    viaPartitionRoot: true
    owner: replication_admin   # optional: ALTER PUBLICATION ... OWNER TO
    generatedColumns: stored              # PostgreSQL 18+: also replicate stored generated columns
    comment: change data capture
  billing_cdc:
    schemas: [billing]                    # FOR TABLES IN SCHEMA (not combinable with column lists)
  everything:
    allTables: true
```

Publications are created after every table, so their tables and columns exist. As in PostgreSQL, a table is published with its inheritance children unless it sets `only: true`; list just the parent, not the children too. For an existing publication, a changed membership emits `ALTER PUBLICATION ... SET TABLE ..., TABLES IN SCHEMA ...`, or `DROP TABLE`/`DROP TABLES IN SCHEMA` when the YAML leaves it empty. Changed `publish`, `viaPartitionRoot` and `generatedColumns` emit `ALTER PUBLICATION ... SET (...)`, and comments and `owner` are diffed. `generatedColumns` (`none` or `stored`) is PostgreSQL 18's `publish_generated_columns`; on earlier servers `stored` is reported as a warning and left unset. Switching a live publication to or from `allTables` needs `DROP` + `CREATE`, which briefly unpublishes everything, so it requires `--unsafe`. Without a `publications:` block publications are unmanaged. With one, `--unsafe --prune` drops live publications it doesn't list. PostgreSQL does not allow column lists in a publication that also publishes whole schemas.

## Event Triggers

Database-wide event triggers are declared in a top-level `eventTriggers:` map (they are not schema-scoped). Creating them requires superuser.

```yaml
eventTriggers:
  log_ddl:
    event: ddl_command_end          # ddl_command_start | ddl_command_end | sql_drop | table_rewrite | login (PostgreSQL 17+)
    tags: [CREATE TABLE, ALTER TABLE]   # optional WHEN TAG IN filter
    function: audit.log_ddl()       # a function returning event_trigger
    enabled: true                   # true/origin | false/disabled | replica | always
    comment: DDL audit
```

Event triggers are created after every function. A changed `event`, `tags` or `function` drops and recreates the trigger in the same migration (no `--unsafe` needed). `enabled` and `comment` are reconciled with `ALTER EVENT TRIGGER` / `COMMENT ON`. Without an `eventTriggers:` block, event triggers are unmanaged. With one, `--unsafe --prune` drops live event triggers it doesn't list, except those owned by an extension.

## Reference Data

Lookup tables (plans, statuses, countries) can declare their rows with `data:`. pgy keeps the live table in line with them:

```yaml
schema app:
  table plan:
    columns:
      code: {type: text}
      name: {type: text}
      seats: {type: int}
      price: {type: "numeric(6,2)", nullable: true}
    primaryKey: [code]
    data:
      key: [code]          # optional: defaults to the primary key
      rows:
        - {code: free, name: Free, seats: 1, price: 0}
        - {code: pro, name: Pro, seats: 10, price: "19.99"}
```

- A row missing from the table is inserted. A row whose listed columns differ is updated, and only those columns are written. Columns a row doesn't list are left alone. With `--unsafe --prune`, rows the YAML no longer declares are deleted.
- PostgreSQL does the comparison: each declared row is converted to the table's column types first. So `10` matches a `numeric(6,2)` value of `10.00`, and enum labels, arrays (`[a, b]`), maps (for `json`/`jsonb` and composite-type columns) and dates compare by value. Every value of a `json` or `jsonb` column is a JSON document (`[]` is an empty JSON array and a string is a JSON string) and is written as a `jsonb` literal. A map for a composite-type column is written through `jsonb_to_record`, the same conversion the comparison uses. A list for a column declared as an array (`text[]`, `box[]`, `geometry(Point,4326)[]`) is written as `array['a', 'b']::text[]`, its elements read by the element type's input function as in an array literal; this works for element types whose arrays use another delimiter than a comma (`box` and PostGIS `geometry` use `;`), and the column's type modifiers are checked when the array is assigned to it. A list for a column of any other type (a domain over an array type, or a type whose input reads the list's JSON text, such as pgvector's `vector` reading `[1, 2, 3]` or a range reading `[1, 3]`) is written through `jsonb_to_record` too, so it is written the way it was compared. Two things don't survive YAML: the original spacing of `json` values, and the difference between a JSON `null` and SQL `NULL` in `json`/`jsonb` columns, which both become YAML `null` and are written as SQL `NULL`. YAML numbers go through floating point, so quote decimals that need exact digits (`"19.99"`).
- A type YAML has no notation for takes its value as text, the way PostgreSQL reads and prints it: an `hstore` is `attrs: '"seats"=>"1"'` (or `seats=>1`), an `ltree` is `path: top.science`, a range is `during: "[1,5)"`, and a composite value may also be written as `'(1,x)'`. This covers extension types and domains over them. A map given to a column that takes none (an `hstore` column, say) is an error in `pgy diff`, which names the row and the column, when the table or the column's type already exists in the database; otherwise PostgreSQL rejects it when the migration runs. `pgy pull --data` writes types that PostgreSQL would turn into JSON objects through a cast (`hstore`) in their text form, so its rows load back.
- Rows are matched on the key with the key columns' own equality, so a `citext` key matches in any case. A key that matches but is written differently is updated to the spelling in the YAML. Two rows whose keys are equal that way (`free` and `FREE` in a `citext` column) are an error in `pgy diff` when the key's types exist in the database; `pgy validate`, which has no database, only catches keys written identically.
- That equality is the one PostgreSQL itself uses for the type in unique indexes and `ORDER BY` (its default btree operator class), whatever the `search_path` is. When the type's `=` operator lives in a schema that is neither `public` nor on the `search_path` of the session pgy connected with (a `citext` installed in `ext`), the `UPDATE` and `DELETE` statements name it: `where "code" operator("ext".=) 'FREE'`. A bare `=` would compare such a key as `text` there, or find no operator at all.
- A key column whose type has a modifier changes some values when it stores them: `numeric(6,2)` stores `1.005` as `1.01`, `timestamp(0)` rounds the seconds, and PostGIS gives a `geometry(Point,4326)` key written without an SRID the SRID 4326. The comparison converts declared keys to the column's type, modifier included, and the `UPDATE` and `DELETE` statements cast them to it too, so that they find the stored row: `where "amount" = '1.005'::numeric(6,2)`.
- A key column needs a type with a default btree operator class, as a primary key or unique constraint does. `json`, `xml` and the geometric types (`point`, `box`, ...) have none, so rows cannot be matched or ordered by them. `pgy validate` rejects those built-in types as key columns, and `pgy diff` rejects any type without such a class when the table or the type exists in the database. Both name the table and the column.
- A column whose type the same plan changes (`varchar(3)` to `varchar(5)`, with `--unsafe`) is compared in its declared type, without its modifier, so rows that only fit the new type are updated after the change. When a key column's type changes, or the new type does not exist in the database yet, the rows are written with `INSERT ... ON CONFLICT (key) DO UPDATE` instead. Without `--unsafe` the type change is left out, so rows are compared in the live type, and a row that only fits the new type is an error in `pgy diff`.
- Data statements run after the table changes and before new foreign keys are added. A table's rows come after the rows of the data tables its foreign keys reference, and deletes run in the reverse order. Explicit values for `identity: always` columns are inserted with `OVERRIDING SYSTEM VALUE`.
- `pgy pull --data schema.table` (repeatable) adds the current rows of those tables as a `data:` block.
- **An extension's configuration tables** (the ones its script marks with `pg_extension_config_dump`, such as PostGIS's `spatial_ref_sys` or `topology.topology`) hold rows of the extension and rows an application added, which `pg_dump` tells apart by the table's condition (`pg_extension.extcondition`). Declare such a table without columns and with `data.key`. Its rows are inserted and updated as declared, after the extension is created, but `--unsafe --prune` only deletes rows the condition selects, so the extension's own rows stay. `pgy pull --data` writes only those rows too, with the key and a `dependsOn` on the configuration tables with rows that its foreign keys reference, whose rows are then inserted first. It warns instead of writing the rows of an extension's table that is not a configuration table. See [Objects an extension owns](#objects-an-extension-owns).

```yaml
extensions:
  - {name: postgis, schema: public}
schema public:
  table spatial_ref_sys:
    data:
      key: [srid]
      rows:
        - {srid: 990001, auth_name: local, auth_srid: 990001, srtext: 'LOCAL_CS["local",UNIT["metre",1]]', proj4text: +proj=longlat +datum=WGS84 +no_defs}
```
- `pgy validate` checks that every row has a key and no two rows share one, and that rows only set declared columns and no generated columns. A table without a primary key needs `key`. A `key` that isn't the primary key or a unique constraint gets a warning.

## Renames

Without a hint, a renamed object looks like a new object plus an undeclared old one: pgy creates the new one, and `--unsafe --prune` would drop the old one along with its data. Declare the previous name with `renamedFrom` and pgy emits `ALTER ... RENAME` instead:

```yaml
schema app:
  table products:
    renamedFrom: items             # ALTER TABLE app.items RENAME TO products
    columns:
      quantity: {type: int, renamedFrom: qty}
    indexes:
      products_quantity_idx: {columns: [quantity], renamedFrom: items_qty_idx}
    constraints:
      products_quantity_check: {type: check, expression: quantity >= 0, renamedFrom: items_qty_check}
  table archive:
    renamedFrom: legacy.archive    # a qualified name moves it: SET SCHEMA, then RENAME
    columns:
      id: {type: int}
  type feeling:
    renamedFrom: mood
    type: enum
    labels: [ok, so_so]
    renamedLabels: {so_so: meh}    # ALTER TYPE ... RENAME VALUE 'meh' TO 'so_so'
  function double_it(x int):
    renamedFrom: twice             # same arguments, new name
    returns: int
    language: sql
    body: select x * 2
```

`renamedFrom` is accepted on tables, columns, views, materialized views, sequences, types, domains, indexes, constraints, foreign keys, functions and procedures, and on the entries of `servers:` and `foreignDataWrappers:`. A bare name is looked up in the object's own schema; a qualified one may name another schema. Enum labels are renamed with `renamedLabels` (new label → old label).

- A rename happens only when the old name exists in the database and the new name does not. Once it has been applied the key is a no-op, so it can stay in the YAML until every environment has migrated.
- Renames are emitted right after schema creation, before anything else in the migration. The rest of the diff, including `--prune`, then treats the object as already existing under its new name. References PostgreSQL updates on its own (foreign keys, triggers, indexes and constraints naming a renamed column, columns of a renamed type) don't cause any churn.
- Anything else about the object is diffed as usual. Change a column's type and its name in the same edit, and the plan renames the column, then alters its type.
- A domain's or table's existing constraints keep their names when the object is renamed. Rename them too, with `renamedFrom` on the constraint, if you want them to follow.
- `pgy validate` rejects a `renamedFrom` naming an object the YAML still declares, two objects renamed from the same name, and `renamedLabels` that don't match the enum's labels.

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
| `collation <schema.collation>` | `collation app.case_insensitive` |
| `schema <name>` | `schema private` (informational only, not resolved) |

The sort is **deterministic**: entities with no ordering constraint between them are emitted by kind (types → domains → sequences → functions → procedures → tables → views), then by name, so identical YAML always produces an identical buffer.

pgy also infers the dependencies it can read from the YAML, so these need no `dependsOn`:

- partitions, `inherits` children and typed tables on their parent or type
- anything naming a declared type, domain, table or view as a type: column types, composite attributes, range subtypes, domain base types, routine arguments and return types (`returns setof app.items`)
- declared functions called in domain defaults and checks, column defaults and generated expressions, check constraints, index expressions and argument defaults
- in view queries and SQL-standard routine bodies, relations after `FROM` or `JOIN` and any schema-qualified relation or function call

Unqualified names only match objects in `public`. Other references, such as an unqualified table in a comma join or a non-public table named only in a string-literal function body, still need `dependsOn`.

Functions, views and types that an extension owns need no `dependsOn` either: extensions are created before everything else. `pgy pull` leaves them out of the `dependsOn` lists it writes.

Two kinds of `dependsOn` are never needed (and can only manufacture cycles):

- **On trigger functions** (`returns: trigger`). Triggers are emitted after every other create, and no `CREATE` statement can call a trigger function, so edges pointing at trigger-returning functions are silently ignored. Declare `dependsOn` the other way if the trigger function's body needs a table (required for `language: sql`; `plpgsql` bodies are not validated at `CREATE`).
- **On functions referenced only by row-level-security policies or grants.** `CREATE POLICY`, `GRANT`, and `COMMENT` statements are all emitted after every create, so a table never needs `dependsOn` for functions its policies call.

A `function`/`procedure` dependency without an argument list (`function public.area`) depends on every overload of that name; with an argument list (`function public.area(w float8, h float8)`) it depends on the matching overload only, falling back to all overloads if none matches. An unqualified name resolves to `public` first, then to that name in any schema.

A genuine dependency cycle is a hard error; the message shows one concrete cycle path (`a -> b -> a`) to remove.

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
