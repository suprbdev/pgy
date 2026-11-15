# pgy

[![CI](https://github.com/suprbdev/pgy/actions/workflows/ci.yaml/badge.svg)](https://github.com/suprbdev/pgy/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/suprbdev/pgy)](https://github.com/suprbdev/pgy/releases/latest)

pgy is a declarative, forward-only migration tool for PostgreSQL 15–18. You describe the database you want in YAML. `pgy diff` compares it with a live database and writes the SQL needed to get there to a buffer file for you to review. `pgy commit` turns the buffer into a numbered, checksummed migration, and `pgy migrate` applies pending migrations. There are no down migrations.

- **Broad PostgreSQL coverage:** tables with partitioning, inheritance, generated and identity columns, every kind of constraint, indexes (including concurrent builds), triggers, rules, row-level security, views and materialized views, enum/composite/range types, domains, sequences, functions, procedures, aggregates, collations, text search, roles, grants, ownership, default privileges, publications, event triggers, and the rows of lookup tables. See the [feature matrix](docs/feature_support.md).
- **Safe by default:** changes that can lose data need `--unsafe`, and dropping objects the YAML no longer declares also needs `--prune`. Renames are declared with `renamedFrom`, so they never become drop-and-create. `pgy diff` warns about statements that would lock a busy table.
- **Adopt an existing database:** `pgy pull` writes YAML for a database. The pulled YAML diffs clean against that database and can rebuild it from scratch.
- **Editor and CI support:** a JSON Schema gives completion and validation in your editor. `pgy validate` checks schema files without a database, and `pgy diff` exits 2 when the database has drifted from the YAML.

## Install

### Prebuilt binaries

Download the archive for your platform from the [latest release](https://github.com/suprbdev/pgy/releases/latest) (Linux and macOS on amd64 and arm64, Windows on amd64). Optionally verify it against `checksums.txt`, then put `pgy` on your `PATH`:

```sh
VERSION=0.19.8   # see the latest release
curl -LO https://github.com/suprbdev/pgy/releases/download/v${VERSION}/pgy_${VERSION}_linux_amd64.tar.gz
tar xzf pgy_${VERSION}_linux_amd64.tar.gz
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
# yaml-language-server: $schema=https://raw.githubusercontent.com/suprbdev/pgy/v0.19.8/schemas/pgy.schema.json
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

[`schemas/pgy.schema.json`](schemas/pgy.schema.json) is a JSON Schema for pgy's YAML. Editors that use [yaml-language-server](https://github.com/redhat-developer/yaml-language-server), such as VS Code with the Red Hat YAML extension or Neovim, use it to complete keys, validate values and show documentation on hover. Point the modeline at the schema of the pgy release you use, so the editor accepts exactly the keys that release understands:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/suprbdev/pgy/v0.19.8/schemas/pgy.schema.json
```

To skip the modeline, map files to the schema in VS Code's settings instead. See [Editor Support](docs/schema.md#editor-support) for that and for what the schema checks.

## Configuration

Settings come from flags, then environment variables, then a config file, then defaults:

| Flag | Environment | Default | |
|------|-------------|---------|---|
| `--dsn` | `PGY_DSN` | | PostgreSQL connection string |
| `--schema-root` | `PGY_SCHEMA_ROOT` | `.` | directory with the YAML files |
| `--schemas` | `PGY_SCHEMAS` | every `.yml`/`.yaml` under the root | comma-separated files, relative to the root; a missing file is an error |
| `--migrations-dir` | `PGY_MIGRATIONS_DIR` | `./migrations` | |
| `--migrations-table` | `PGY_MIGRATIONS_TABLE` | `pgy.migrations` | `schema.table` recording applied migrations |
| `--buffer` | `PGY_BUFFER` | `./.pgy.buffer.sql` | `-` prints `pgy diff` output to stdout |
| `--config` | `PGY_CONFIG` | `pgy.yaml`, `pgy.yml`, `.pgy.yaml`, `.pgy.yml` | config file |
| `--quiet`, `--verbose`, `--json` | `PGY_QUIET=1`, `PGY_VERBOSE=1`, `PGY_JSON=1` | | |

A config file uses the same names:

```yaml
dsn: postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
schema_root: ./schemas
schemas: ["base.yml", "ext.yml"]
migrations_dir: ./migrations
migrations_table: pgy.migrations
buffer: ./.pgy.buffer.sql
```

## Commands

| Command | What it does |
|---------|--------------|
| `pgy pull [--out DIR] [--schema S] [--data T] [--owners] [--roles]` | Writes YAML for an existing database: one file per schema plus `database.yml`. `--data schema.table` also pulls a table's rows. Objects pgy cannot express, such as functions written in C, are reported on stderr. |
| `pgy validate [--json]` | Checks schema files without a database: JSON Schema, unknown columns, unresolvable `dependsOn`, cycles, invalid combinations. Exits 1 on errors. |
| `pgy diff [--unsafe] [--prune] [--split[=dir]] [--from-empty]` | Writes the SQL that brings the database to the YAML. Exits 2 when there are changes. `--from-empty` plans against an empty database without connecting. Warnings about locks and schema problems go to stderr (`--no-warnings` silences them). |
| `pgy buffer [--stat] [--clear]` | Prints, summarises or deletes the buffer. |
| `pgy commit NAME` | Turns the buffer into `migrations/NNNN_NAME.sql` with a checksum header (several files for a split buffer). |
| `pgy migrate [--dry-run] [--until N] [--limit N] [--lock-timeout D] [--statement-timeout D]` | Applies pending migrations in order, each in its own transaction, after verifying checksums. |
| `pgy status` | Shows the current, last and pending migrations. Exits 2 when some are pending. |
| `pgy init` | Creates the migrations table. `migrate` and `status` also create it when it is missing. |
| `pgy mark-applied --force NAME` | Records migrations up to NAME as applied without running them, and removes later records. |

## Safe changes

### Unsafe changes and pruning

Without flags, `pgy diff` only adds and adjusts. It creates missing objects, adds columns, constraints and indexes, and changes defaults, comments, grants and similar settings. Changes that can fail on existing data or lose it need `--unsafe`. That covers column type changes, dropped columns, and redefined constraints and indexes, which are dropped and recreated.

`--unsafe --prune` also drops what the YAML no longer declares, in every schema the YAML declares at least one object in:

- tables, views, materialized views, functions, procedures, aggregates, sequences, domains, enum/composite/range types, collations, text search configurations and dictionaries
- triggers, foreign keys, check/unique/exclude constraints and indexes on declared tables, and indexes on declared materialized views
- event triggers and publications, when the YAML has an `eventTriggers:` / `publications:` block
- rows of tables with a `data:` block that the block no longer lists

Some things are never dropped: objects in other schemas, extension members, partitions (dropping the parent drops them), primary keys, indexes that back a constraint, objects a partition inherits from its parent, sequences owned by a column, window functions, functions PostgreSQL created for another object (such as range type constructors), roles, schemas, extensions, and the migrations table.

> **Warning:** run `--prune` with **all** of your schema files. With an explicit `--schemas` subset, it drops every object the subset doesn't mention in the schemas it touches, including whole tables and their data. Always read the buffer before committing it.

### Renames

A renamed object looks like a new object plus an old one to drop. Declare the old name with `renamedFrom: <old name>` and pgy emits `ALTER ... RENAME` instead. Once the rename has run, the key does nothing, so it can stay until every environment has migrated. See [Renames](docs/schema.md#renames).

### Lock warnings

`pgy diff` reports statements that would block traffic on a table that already exists. Warnings go to stderr, and into the `warnings` field with `--json`. They never change the plan or the exit code.

| Statement | Why | Instead |
|-----------|-----|---------|
| `CREATE INDEX` | blocks writes while the index builds | `concurrently: true` |
| `ADD FOREIGN KEY` | checks every row while blocking writes on both tables | `notValid: true`, then remove it in a later migration, so pgy emits `VALIDATE CONSTRAINT`, which doesn't block writes |
| `ADD CHECK` | scans the table under `ACCESS EXCLUSIVE` | the same two steps |
| `ADD PRIMARY KEY` / `UNIQUE` / `EXCLUDE` | builds an index under `ACCESS EXCLUSIVE` | schedule a maintenance window |
| `SET NOT NULL` | scans the table under `ACCESS EXCLUSIVE`; skipped if a validated `CHECK (col IS NOT NULL)` exists | add that check first |
| `ALTER COLUMN ... TYPE` | rewrites the table, except when widening `varchar`/`numeric` or switching `varchar` ↔ `text` | a new column plus a backfill |
| `SET LOGGED` / `SET UNLOGGED` | rewrites the table | — |
| `ADD COLUMN` with a volatile default, a stored generated expression, identity or serial | writes every row | add the column bare, then backfill |

### Migrations without a transaction

Each migration normally runs in its own transaction. A migration whose leading comment lines include `-- pgy:no-transaction` runs statement by statement instead, as `CREATE INDEX CONCURRENTLY` and some other statements require. It is recorded only after every statement succeeds, but a failure leaves the earlier statements applied, so keep such files idempotent (`IF NOT EXISTS`). `pgy diff` puts indexes declared with `concurrently: true` into such a migration itself. On a partitioned table it builds the index on each partition concurrently and attaches the partition indexes.

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
- Against an existing database, only groups with changes produce migrations.

References inside SQL text are found by name. A name that only looks like a reference can, at worst, reorder or merge groups. Use `dependsOn` for any cross-file dependency the scan cannot see.

## Documentation

- [Schema reference](docs/schema.md): every YAML key, with examples and diff behaviour.
- [Feature matrix](docs/feature_support.md): what pgy supports, and how it is tested.
- [`examples/`](examples): composable starter modules (users, orgs, e-commerce, billing, CRM, messaging and more) and link files that wire them together.

## Notes

- Forward-only: there are no down migrations. To undo a change, write the reverse change as a new migration.
- `pgy init` and `pgy migrate` take an advisory lock, so concurrent runs wait for each other.
- Committed migrations carry a checksum, which `pgy migrate` verifies before applying them.
- SQL is split into statements with single and double quotes, `E'...'` strings, dollar quotes and comments respected.
