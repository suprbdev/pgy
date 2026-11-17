# pgy

[![CI](https://github.com/suprbdev/pgy/actions/workflows/ci.yaml/badge.svg)](https://github.com/suprbdev/pgy/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/suprbdev/pgy)](https://github.com/suprbdev/pgy/releases/latest)

pgy is a declarative, forward-only migration tool for PostgreSQL 15–18. You describe the database you want in YAML. `pgy diff` compares it with a live database and writes the SQL needed to get there to a buffer file for you to review. `pgy commit` turns the buffer into a numbered, checksummed migration, and `pgy migrate` applies pending migrations. There are no down migrations.

- **Broad PostgreSQL coverage:** tables with partitioning, inheritance, generated and identity columns, every kind of constraint, indexes (including concurrent builds), triggers, rules, row-level security, views and materialized views, enum/composite/range types, domains, sequences, functions, procedures, aggregates, collations, text search, foreign tables with their servers and wrappers, roles, grants, ownership, default privileges, publications, event triggers, and the rows of lookup tables. See the [feature matrix](docs/feature_support.md).
- **Safe by default:** changes that can lose data need `--unsafe`, and dropping objects the YAML no longer declares also needs `--prune`. Renames are declared with `renamedFrom`, so they never become drop-and-create. `pgy diff` warns about statements that would lock a busy table.
- **Adopt an existing database:** `pgy pull` writes YAML for a database. The pulled YAML diffs clean against that database and can rebuild it from scratch.
- **Editor and CI support:** a JSON Schema gives completion and validation in your editor. `pgy validate` checks schema files without a database, and `pgy diff` exits 2 when the database has drifted from the YAML.

## Install

### Prebuilt binaries

Download the archive for your platform from the [latest release](https://github.com/suprbdev/pgy/releases/latest) (`pgy_linux_amd64.tar.gz`, `pgy_linux_arm64.tar.gz`, `pgy_darwin_amd64.tar.gz`, `pgy_darwin_arm64.tar.gz` or `pgy_windows_amd64.zip`). Optionally verify it against `checksums.txt`, then put `pgy` on your `PATH`:

```sh
curl -LO https://github.com/suprbdev/pgy/releases/latest/download/pgy_linux_amd64.tar.gz
tar xzf pgy_linux_amd64.tar.gz
sudo install -m 0755 pgy /usr/local/bin/pgy
pgy version
```

### go install

```sh
go install github.com/suprbdev/pgy/cmd/pgy@latest
```

### Build from source

```sh
make build     # bin/pgy, with version information
make test      # unit tests (set PGY_TEST_DSN to run the integration tests too)
make install   # installs to $HOME/go/bin if present, else PREFIX/bin (/usr/local/bin)
```

## Quick start

**1. Describe the schema.** Create `schemas/app.yml`:

```yaml
# yaml-language-server: $schema=https://github.com/suprbdev/pgy/releases/latest/download/pgy.schema.json
schema app:
  table account:
    columns:
      id: {type: bigint, identity: always}
      email: {type: text}
      created_at: {type: timestamptz, default: now()}
    primaryKey: [id]
    indexes:
      account_email_key: {columns: [lower(email)], unique: true}
  table note:
    columns:
      id: {type: bigint, identity: always}
      account_id: {type: bigint}
      body: {type: text}
    primaryKey: [id]
    foreignKeys:
      note_account_fkey:
        columns: [account_id]
        references: {table: app.account, columns: [id]}
        onDelete: cascade
```

The first line turns on completion and validation in editors that use yaml-language-server (see [Editor support](#editor-support)). If you already have a database, generate the YAML instead with `pgy pull --dsn "$PG_DSN" --out schemas`.

**2. Check it.** `pgy validate` needs no database and exits 1 on errors:

```sh
pgy validate --schema-root schemas
```

**3. Generate the SQL.** pgy writes the statements to `.pgy.buffer.sql`. Read them before going further:

```sh
export PGY_DSN=postgres://localhost/app
pgy diff --schema-root schemas     # exit code 2: changes were written to the buffer
pgy buffer                         # or: pgy diff --schema-root schemas --buffer - | less
```

**4. Commit and apply.**

```sh
pgy commit create_app      # writes migrations/0001_create_app.sql
pgy migrate                # applies pending migrations, each in its own transaction
pgy status                 # current, last and pending migrations
```

From then on, edit the YAML and repeat steps 3 and 4. In CI, `pgy diff` against a database migrated to `main` exits 0 when the YAML and the migrations agree.

## Editor support

[`schemas/pgy.schema.json`](schemas/pgy.schema.json) is a JSON Schema for pgy's YAML. Editors that use [yaml-language-server](https://github.com/redhat-developer/yaml-language-server), such as VS Code with the Red Hat YAML extension or Neovim, use it to complete keys, validate values and show documentation on hover. Point the modeline at the schema attached to the latest release:

```yaml
# yaml-language-server: $schema=https://github.com/suprbdev/pgy/releases/latest/download/pgy.schema.json
```

To skip the modeline, map files to the schema in VS Code's settings instead. See [Editor Support](docs/schema.md#editor-support) for that and for what the schema checks.

## Configuration

Settings come from flags, then environment variables, then a config file, then defaults:

| Flag | Environment | Default | |
|------|-------------|---------|---|
| `--dsn` | `PGY_DSN` | | PostgreSQL connection string |
| `--schema-root` | `PGY_SCHEMA_ROOT` | `.` | directory with the YAML files |
| `--schemas` | `PGY_SCHEMAS` | every `.yml`/`.yaml` under the root, except dotfiles, pgy config files and files in dot-directories, `node_modules` and `vendor` | comma-separated files, relative to the root; a missing file is an error |
| `--migrations-dir` | `PGY_MIGRATIONS_DIR` | `./migrations` | |
| `--migrations-table` | `PGY_MIGRATIONS_TABLE` | `pgy.migrations` | `schema.table` recording applied migrations |
| `--buffer` | `PGY_BUFFER` | `./.pgy.buffer.sql` | `-` prints `pgy diff` output to stdout |
| `--config` | `PGY_CONFIG` | `pgy.yaml`, `pgy.yml`, `.pgy.yaml`, `.pgy.yml` | config file |
| `--quiet`, `--verbose`, `--json` | `PGY_QUIET=1`, `PGY_VERBOSE=1`, `PGY_JSON=1` | | |
| `--strict` | `PGY_STRICT=1` | off | for CI: fail on the findings in `ci.fail_on` (see [Strict mode](#strict-mode)) |

A config file uses the same names:

```yaml
dsn: postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
schema_root: ./schemas
schemas: ["base.yml", "ext.yml"]
migrations_dir: ./migrations
migrations_table: pgy.migrations
buffer: ./.pgy.buffer.sql
```

It also takes the `ci:` and `policy:` blocks described under [Strict mode](#strict-mode), and the `prune:` block described under [Unsafe changes and pruning](#unsafe-changes-and-pruning).

### Strict mode

`--strict` (or `PGY_STRICT=1`) is meant for CI. It makes `pgy diff`, `pgy validate` and `pgy pull` exit 1 when they report a finding of a kind listed in `ci.fail_on`, after writing their output as usual. It also makes `pgy diff` refuse `--unsafe` and `--prune` while `ci.forbid_unsafe` is true. Without `--strict` nothing changes: findings stay warnings.

```yaml
ci:
  fail_on: [lock, public_revoke, policy, validate]   # the default
  forbid_unsafe: true                                # the default
```

| Kind | Finding |
|------|---------|
| `lock` | `pgy diff`: a statement locks or rewrites an existing table (see [Lock warnings](#lock-warnings)) |
| `public_revoke` | `pgy diff`: a grants block revokes PUBLIC's privileges |
| `dependents` | `pgy diff`: a column type change is blocked by an object the YAML doesn't declare, or by a generated column that uses the column and can't be dropped and added again; a column is not dropped because objects the plan keeps still use it; an extension `--prune` would drop is kept because objects still use it; an extension update may be rejected because views, materialized views or SQL-standard routine bodies use the extension (see [Updating extensions](docs/schema.md#updating-extensions)) |
| `unsafe` | `pgy diff`: a statement left out because it needs `--unsafe` (see [Unsafe changes and pruning](#unsafe-changes-and-pruning)) |
| `policy` | `pgy diff` and `pgy validate`: a schema breaks a rule of the `policy:` block (below) |
| `validate` | `pgy validate` warnings and errors; in `pgy diff`, validation findings for the schema files |
| `unsupported` | `pgy pull`: objects it can't express |
| `all` | every warning |

Without a `ci:` block, `--strict` uses the defaults shown above; a key in `ci:` replaces just that default.

A `policy:` block sets rules for the schema itself. `pgy validate` and `pgy diff` report each object that breaks one as a warning, and `--strict` fails on them while `ci.fail_on` includes `policy`:

```yaml
policy:
  security_definer: hardened            # SECURITY DEFINER routines must set search_path and revoke PUBLIC's EXECUTE
  require_grants: [tables, views, functions]   # these must declare a grants block
  require_owners: true                  # every object must declare an owner (or a list of kinds)
  row_level_security: [app]             # tables in these schemas must enable RLS and declare a policy
  concurrent_indexes: true              # indexes added to existing tables must set concurrently: true
```

The kinds for `require_grants` and `require_owners` are `schemas`, `tables`, `views`, `sequences`, `functions`, `procedures`, `types`, `domains` and `servers`; partitions inherit their parent's rules and aren't checked. Neither rule checks a declaration of an object pgy never creates (a function or procedure without `body`, a view without `query`, a type without `type`), which is how [objects an extension owns](docs/schema.md#objects-an-extension-owns) are declared, and `require_owners` does not check the `sequence` block of a serial or identity column, which always has its table's owner. `pgy pull --owners` leaves out owners that are the connecting role; with `require_owners` set, it writes the owner of every object of the kinds the rule lists, the connecting role included, so that pulled YAML passes the rule. `concurrent_indexes` needs the database, so only `pgy diff` checks it.

Some more `policy:` settings change what pgy does rather than adding checks:

```yaml
policy:
  public_grants: authoritative   # or explicit; the default is by_kind
  lock_timeout: 5s               # pgy migrate's default --lock-timeout
  statement_timeout: 1min        # pgy migrate's default --statement-timeout
  strict_expressions: true       # pgy diff --strict-expressions on every diff
```

- **`public_grants`** sets which grants blocks cover PUBLIC. `by_kind`, the default, covers it where PostgreSQL gives PUBLIC nothing by default (see [grants](docs/schema.md#grants)). `authoritative` covers it in every grants block, so PostgreSQL's default `EXECUTE` on routines and `USAGE` on types, domains and the `public` schema are revoked unless the block lists `public`. `explicit` covers it only in blocks that list `public`.
- **`lock_timeout`** and **`statement_timeout`** set the timeouts `pgy migrate` uses unless its `--lock-timeout` and `--statement-timeout` flags are given.
- **`strict_expressions`** makes every `pgy diff` compare all declared expressions through the server, as `--strict-expressions` does (see [How expressions are compared](docs/schema.md#how-expressions-are-compared)). `--strict-expressions=false` turns it off for one run.

## Commands

| Command | What it does |
|---------|--------------|
| `pgy pull [--out DIR] [--schema S] [--exclude-schema S] [--database-settings] [--data T] [--owners] [--roles]` | Writes YAML for an existing database: one file per schema plus `database.yml`. `--schema` pulls only the schemas named and `--exclude-schema` leaves schemas out; `database.yml` then holds only what the pulled schemas need (see [Pulling some schemas](#pulling-some-schemas)). `--data schema.table` also pulls a table's rows. `--roles` writes the cluster's roles except the bootstrap superuser, and the connecting role with its memberships and settings only (see [Roles](docs/schema.md#roles)). Objects pgy cannot express are reported on stderr: functions written in C, operators, operator classes and families, conversions, base types, casts, text search parsers and templates, procedural languages and transforms that no extension owns, and column privileges on an extension's tables. Functions, aggregates, tables, sequences, views, types, schemas, languages and foreign-data wrappers of an extension are written when their privileges changed, and its text search dictionaries with their options (see [Objects an extension owns](docs/schema.md#objects-an-extension-owns)). |
| `pgy validate [--json]` | Checks schema files without a database: JSON Schema, unknown columns, unresolvable `dependsOn`, cycles, invalid combinations, and warnings such as policies on a table without row-level security. Exits 1 on errors. |
| `pgy diff [--unsafe] [--prune] [--split[=dir]] [--from-empty] [--strict-expressions]` | Writes the SQL that brings the database to the YAML. Exits 2 when there are changes. `--from-empty` plans against an empty database without connecting. `--strict-expressions` compares every declared expression through the server, not only those the text comparison can't settle, which takes longer (see [How expressions are compared](docs/schema.md#how-expressions-are-compared)). Warnings about locks and schema problems go to stderr (`--no-warnings` silences them). |
| `pgy buffer [--stat] [--clear]` | Prints, summarises or deletes the buffer. |
| `pgy commit NAME` | Turns the buffer into `migrations/NNNN_NAME.sql` with a checksum header. Some changes add files around it: `NNNN_NAME_extension_updates.sql` first for extension updates, `NNNN_NAME_enum_values.sql` before it for new enum labels, and `NNNN_NAME_concurrent.sql` after it for statements that must run outside a transaction. A split buffer writes several groups. Warns when a schema file changed after `pgy diff` wrote the buffer. |
| `pgy migrate [--dry-run] [--until N] [--limit N] [--lock-timeout D] [--statement-timeout D] [--retries N] [--retry-wait D]` | Applies pending migrations in order, each in its own transaction, after verifying checksums. `--retries` retries a migration that failed to get a lock. |
| `pgy status [--json]` | Shows the current, last and pending migrations, and history warnings. Exits 2 when some are pending. |
| `pgy init` | Creates the migrations table. `migrate` and `status` also create it when it is missing. Only what is missing is created: a role without `CREATE` on the database can use a migrations table in a schema it owns (`--migrations-table app.pgy_migrations`), and one that already exists needs no `CREATE` privilege at all. |
| `pgy mark-applied --force NAME` | Records migrations up to NAME as applied without running them, and removes later records. |

### Pulling some schemas

`pgy pull --schema app` (or `--exclude-schema platform` for everything else) writes the objects of the pulled schemas and only the database-wide objects they need, so that the YAML rebuilds a database without the other schemas:

- **Extensions** installed in a pulled schema or used by an object in one: a column or domain of the extension's type, an index with its operator class, a default, constraint, trigger, view or SQL-standard routine body that calls its functions (calls in bodies written as text are not recorded by PostgreSQL and do not count). The extensions these require, and those that provide the foreign-data wrapper of a pulled server or own a pulled language entry, are written too.
- **Publications** that publish tables or schemas of the pulled schemas and nothing else. One that also publishes tables of other schemas is left out with a warning, since writing only part of it would take the other tables out of it. `FOR ALL TABLES` publications are left out.
- **Event triggers** whose function is in a pulled schema.
- **Database settings** (the `database:` block) are left out, since they belong to the database as a whole and may hold secrets such as a hosting platform's `app.settings.jwt_secret`. `--database-settings` writes them anyway.

Servers, foreign-data wrappers, languages, default privileges for all schemas and, with `--roles`, roles are written as without these flags. `--unsafe --prune` treats `publications:` and `eventTriggers:` as the complete list, so when the pulled YAML has such a block and the pull left other publications or event triggers out, `pgy pull` warns that a pruning diff with this YAML would drop them.

## Safe changes

### Unsafe changes and pruning

Without flags, `pgy diff` only adds and adjusts. It creates missing objects, adds columns, constraints and indexes, and changes defaults, comments, grants and similar settings. Changes that can fail on existing data or lose it need `--unsafe`. That covers column type changes, dropped columns, and redefined constraints and indexes, which are dropped and recreated. A column that a view, materialized view, trigger, rule, policy or generated column still uses once the plan has run is not dropped: PostgreSQL would reject the drop, so `pgy diff` warns (kind `dependents`), naming what uses it, and says whether `--prune` drops those first.

Without `--unsafe`, `pgy diff` still plans those changes to report them: each statement it left out gets a `needs --unsafe, so it was left out` warning (the first 20 are printed; `--json` lists them all with kind `unsafe`), so a YAML change never goes unnoticed. `ci.fail_on: [unsafe]` makes `--strict` fail on them.

`--unsafe --prune` also drops what the YAML no longer declares, in every schema the YAML declares at least one object in:

- tables, views, materialized views, functions, procedures, aggregates, sequences, domains, enum/composite/range types, collations, text search configurations and dictionaries
- triggers, foreign keys, check/unique/exclude constraints and indexes on declared tables, and indexes on declared materialized views
- event triggers and publications, when the YAML has an `eventTriggers:` / `publications:` block
- foreign servers and foreign-data wrappers, when the YAML has a `servers:` / `foreignDataWrappers:` block; a server that foreign tables still use and a wrapper that servers still use are kept
- extensions installed in those schemas, when the YAML has an `extensions:` block that does not list them and nothing that stays uses them (see [Removing extensions](docs/schema.md#removing-extensions))
- role memberships that a role's `inRoles` no longer lists, for roles that declare `inRoles`
- rows of tables with a `data:` block that the block no longer lists

Some things are never dropped: objects in other schemas, extension members, partitions (dropping the parent drops them), undeclared inheritance children of a table the plan keeps (their rows are part of the parent's, as with TimescaleDB chunks; `pgy diff` names them in a warning), primary keys, indexes that back a constraint, objects a partition inherits from its parent, sequences owned by a column, window functions, functions and procedures written in C or `internal` (YAML cannot declare them), functions PostgreSQL created for another object (such as range type constructors), roles, schemas, `plpgsql`, extensions that an extension staying installed requires, and the migrations table.

A `prune:` block in the config file names extensions that `--prune` must leave alone, for example ones the hosting platform installed:

```yaml
prune:
  keep_extensions: [pg_stat_statements, postgis]
```

An undeclared extension that is still in use is not dropped either: `pgy diff` warns and names what uses it, instead of writing a `DROP EXTENSION` that PostgreSQL would reject.

> **Warning:** run `--prune` with **all** of your schema files. With an explicit `--schemas` subset, it drops every object the subset doesn't mention in the schemas it touches, including whole tables and their data. Always read the buffer before committing it.

### Renames

A renamed object looks like a new object plus an old one to drop. Declare the old name with `renamedFrom: <old name>` and pgy emits `ALTER ... RENAME` instead. Once the rename has run, the key does nothing, so it can stay until every environment has migrated. See [Renames](docs/schema.md#renames).

### Lock warnings

`pgy diff` reports statements that would block traffic on a table that already exists. Warnings go to stderr, and into the `warnings` field with `--json`, which gives each warning a `kind`. They never change the plan, and they only change the exit code under [`--strict`](#strict-mode).

| Statement | Why | Instead |
|-----------|-----|---------|
| `CREATE INDEX` | blocks writes while the index builds | `concurrently: true` |
| `ADD PRIMARY KEY` / `UNIQUE` / `EXCLUDE` | builds an index under `ACCESS EXCLUSIVE` | schedule a maintenance window |
| `SET NOT NULL` | scans the table under `ACCESS EXCLUSIVE`; skipped if a validated `CHECK (col IS NOT NULL)` exists | add that check first. On PostgreSQL 18 pgy does this itself: it adds the constraint `NOT VALID` and validates it in the follow-up no-transaction migration, which doesn't block writes |
| `ALTER COLUMN ... TYPE` | rewrites the table, except when widening `varchar`/`numeric` or switching `varchar` ↔ `text` | a new column plus a backfill |
| `SET LOGGED` / `SET UNLOGGED` | rewrites the table | — |
| `SET ACCESS METHOD` | rewrites the table or materialized view and its indexes under `ACCESS EXCLUSIVE` (needs `--unsafe`) | — |
| `ADD COLUMN` with a volatile default, a stored generated expression, identity or serial | writes every row | add the column bare, then backfill |

A default is volatile when it calls a volatile function. pgy takes a function's volatility from the YAML that declares it, or else from the database's catalog, which covers the functions of installed extensions. Of the built-in functions it knows `random()`, `gen_random_uuid()`, `clock_timestamp()`, `timeofday()`, `nextval()`, `uuidv4()` and `uuidv7()`.

Foreign keys and checks added to an existing table don't need a warning: pgy adds them `NOT VALID` in the main migration, which enforces them for new rows straight away, and validates the existing rows in the no-transaction migration after it. `VALIDATE CONSTRAINT` doesn't block reads or writes. If existing rows violate the constraint, that migration fails and the constraint stays `NOT VALID`; fix the rows and migrate again (`pgy diff` emits the pending `VALIDATE` meanwhile). Set `notValid: true` to leave a constraint unvalidated, or `validate: immediate` (on the constraint or the table) to add it validated in one step for tables that refuse `VALIDATE CONSTRAINT`, at the price of the lock warning above. Before PostgreSQL 18 a partitioned table's foreign key cannot be `NOT VALID`, so it is still added validated, with a warning.

Even a quick `ALTER TABLE` waits for its lock behind long-running queries, and every query that arrives meanwhile waits behind it. `pgy migrate --lock-timeout 2s --retries 5` (or `policy.lock_timeout` in the config) gives up on a lock after two seconds instead, rolls the migration back and tries again, waiting `--retry-wait` (2s by default) before the first retry and twice as long before each later one, up to 30s. Deadlocks are retried the same way. Migrations without a transaction are never retried, because a failure can leave part of them applied.

### Migrations without a transaction

Each migration normally runs in its own transaction. A migration whose leading comment lines include `-- pgy:no-transaction` runs statement by statement instead, as `CREATE INDEX CONCURRENTLY` and some other statements require. It is recorded only after every statement succeeds, but a failure leaves the earlier statements applied, so keep such files idempotent (`IF NOT EXISTS`). `pgy diff` puts indexes declared with `concurrently: true` into such a migration itself. On a partitioned table it builds the index on each partition concurrently and attaches the partition indexes.

A plan that adds enum labels likewise puts the `ALTER TYPE ... ADD VALUE` statements in a migration of their own, numbered before the rest. PostgreSQL rejects a new label until the transaction that added it commits, and this lets the main migration use it in defaults, checks, indexes, views, partitions and `data:` rows.

A migration whose leading comment lines include `-- pgy:new-session` runs each statement outside a transaction as the first statement of a connection opened for it, with `--lock-timeout` and `--statement-timeout` passed as connection parameters, and is recorded once every statement succeeded. `pgy diff` puts `ALTER EXTENSION ... UPDATE` into such a migration, numbered before the rest, because some extensions (TimescaleDB) accept an update only as the first statement of a session (see [Updating extensions](docs/schema.md#updating-extensions)). Like no-transaction migrations, these are not retried by `--retries`.

## Grouped migrations

When prototyping you often wipe `migrations/` and regenerate everything, which normally gives one huge initial migration. `pgy diff --split` writes the buffer as a series of groups instead, one per schema file (`--split=file`, the default) or per directory (`--split=dir`). `pgy commit` then turns each group into its own migration. Group names come from the file paths relative to their common directory, so `schemas/modules/core/user.yml` becomes `0001_modules_core_user.sql`, or `0001_init_modules_core_user.sql` with `pgy commit init`.

```sh
pgy diff --from-empty --split      # one group per YAML file
pgy buffer --stat                  # per-group statement counts
pgy commit init                    # 0001_init_core_user.sql, 0002_init_core_org.sql, ...
```

- Each group is diffed against the state the previous groups leave behind. A file that only adds columns to another file's table, like the link files in `examples/links/`, becomes an `alter table ... add column` with its foreign keys and indexes, in its own migration.
- Groups keep the order of `--schemas`, or the discovery order, unless a dependency forces otherwise. A group always comes after the groups it depends on, and groups whose dependencies form a cycle are merged into one migration named `a+b`. Dependencies come from:
  - `dependsOn`, `partitionOf`, `inherits` and sequence `ownedBy`
  - types used by columns, attributes, domains and function arguments or results
  - functions used by aggregates and range types
  - objects named in view queries, SQL function bodies and domain checks
  - the file that owns a table another file extends
- Foreign keys, triggers, policies, indexes, constraints and defaults that reference something a later group creates are moved into that later group, so circular references between files still work.
- Extensions and roles always go into the first migration. Drops (`--unsafe`, `--prune`) only go into the last one.
- Against an existing database, only groups with changes produce migrations. Each change lands in the group of the file that declares it, once: a view, routine or rule with `replace: true` is re-emitted there and not again in the groups after it.

References inside SQL text are found by name. A name that only looks like a reference can, at worst, reorder or merge groups. Use `dependsOn` for any cross-file dependency the scan cannot see.

## Documentation

- [Schema reference](docs/schema.md): every YAML key, with examples and diff behaviour.
- [Feature matrix](docs/feature_support.md): what pgy supports, and how it is tested.
- [`examples/`](examples): composable starter modules (users, orgs, e-commerce, billing, CRM, messaging and more) and link files that wire them together.

## Notes

- Forward-only: there are no down migrations. To undo a change, write the reverse change as a new migration.
- `pgy init` and `pgy migrate` take an advisory lock, so concurrent runs wait for each other.
- Committed migrations carry a checksum, which `pgy migrate` verifies before applying them. It treats CR LF line endings as LF, so a checkout that converts line endings doesn't invalidate it. `pgy migrate` and `pgy status` also warn when an applied migration's file has changed or disappeared since it ran, and when a pending migration is numbered below the latest applied one, which usually means a merge from another branch. Such a migration still runs, just out of order.
- SQL is split into statements with single and double quotes, `E'...'` strings, dollar quotes and comments respected.
