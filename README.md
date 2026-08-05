# pgy

[![CI](https://github.com/suprbdev/pgy/actions/workflows/ci.yaml/badge.svg)](https://github.com/suprbdev/pgy/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/suprbdev/pgy)](https://github.com/suprbdev/pgy/releases/latest)

Forward-only PostgreSQL migration tool. It reads YAML schema files, diffs against a live DB, writes SQL to a buffer, and commits/applies migrations. No rollbacks.

## Install

### Prebuilt binaries

Download the archive for your platform from the [latest release](https://github.com/suprbdev/pgy/releases/latest) (Linux/macOS amd64+arm64, Windows amd64), verify against `checksums.txt` if desired, and put `pgy` on your `PATH`:

```sh
# example: v0.1.0 on Linux amd64
curl -LO https://github.com/suprbdev/pgy/releases/download/v0.1.0/pgy_0.1.0_linux_amd64.tar.gz
tar xzf pgy_0.1.0_linux_amd64.tar.gz
sudo install -m 0755 pgy /usr/local/bin/pgy
pgy version
```

### go install

```sh
go install github.com/suprbdev/pgy/cmd/pgy@latest
```

(Note: `go install` builds report version `0.1.0` without the release stamping.)

### Build from source

```
make build     # builds bin/pgy with version ldflags
make test      # runs unit tests
make clean     # cleans bin/ and buffer file
make install   # installs to $HOME/go/bin if present, else PREFIX/bin (/usr/local/bin)
```

The binary is placed at `bin/pgy`.

## Configuration

Configuration precedence: flags > env > config file > defaults.

- Common flags and mirrored env vars:
  - `--config` / `PGY_CONFIG` (config file path; default lookup: `pgy.yaml`, `pgy.yml`, then legacy `.pgy.yaml`, `.pgy.yml`)
  - `--dsn` / `PGY_DSN` (PostgreSQL DSN)
  - `--schema-root` / `PGY_SCHEMA_ROOT` (root for YAML files)
  - `--schemas` / `PGY_SCHEMAS` (comma-separated YAML files, relative to schema-root)
  - `--migrations-dir` / `PGY_MIGRATIONS_DIR` (default: `./migrations`)
  - `--buffer` / `PGY_BUFFER` (default: `./.pgy.buffer.sql`)
  - `--quiet` / `PGY_QUIET=1`
  - `--verbose` / `PGY_VERBOSE=1`
  - `--json` / `PGY_JSON=1`

- Optional `pgy.yaml` (in project root; `pgy.yml` and legacy `.pgy.yaml`/`.pgy.yml` also work):

```yaml
dsn: postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
schema_root: ./schemas
schemas: ["base.yml", "ext.yml"]
migrations_dir: ./migrations
buffer: ./.pgy.buffer.sql
quiet: false
verbose: false
json: false
```

If `--schemas`/`PGY_SCHEMAS`/config are not set, pgy auto-discovers all `.yml`/`.yaml` files under `schema-root`.

## Commands (basics)

- Init migrations table (idempotent):

```
pgy init --dsn "$PG_DSN"            # defaults: schema pgy, table migrations
```

- Generate diff from YAML to DB (writes SQL to buffer):

```
pgy diff --dsn "$PG_DSN" --schema-root ./schemas --schemas "base.yml,ext.yml"
# exit code 2 when changes are detected
```

- Inspect or clear buffer:

```
pgy buffer           # prints buffer SQL
pgy buffer --stat    # size + statement count
pgy buffer --clear   # delete buffer file
```

- Commit buffer to numbered migration with checksum header:

```
pgy commit users_and_auth   # creates ./migrations/0001_users_and_auth.sql
```

- Apply pending migrations (each in its own transaction):

```
pgy migrate --dsn "$PG_DSN"                  # apply all pending (exit 0 on success)
pgy migrate --dsn "$PG_DSN" --dry-run        # show what would run (exit 2 if pending)
pgy migrate --dsn "$PG_DSN" --until 0003     # apply up to migration 0003*
pgy migrate --dsn "$PG_DSN" --limit 1        # apply only one
pgy migrate --dsn "$PG_DSN" --lock-timeout 5s --statement-timeout 30s
```

- Manually mark applied (requires confirmation):

```
pgy mark-applied --dsn "$PG_DSN" --force 0003_products
# accepts bare name or full path; inserts missing up to target and removes later ones
```

- Status summary:

```
pgy status --dsn "$PG_DSN"   # shows current, last, pending (exit 2 if pending)
```

## YAML schema (minimal model)

The minimal YAML supported by the initial diff engine:

```yaml
tables:
  public.users:           # or just "users" (defaults to public)
    columns:
      id:
        type: int
        nullable: false
      email:
        type: text
        nullable: false
    constraints:
      email_unique:
        type: unique
        columns: [email]
      age_check:
        type: check
        expression: "id > 0"  # Example check constraint
```

*Note: In addition to `primaryKey`, `foreignKeys`, and `indexes`, table `constraints` (such as `check`, `unique`, and `exclude`) are fully supported and will be emitted when creating new tables.*

See `docs/schema.md` for the full YAML reference, and `examples/` for a
library of composable starter modules (users, orgs, e-commerce, billing, CRM,
messaging, and more) plus link files that wire them together.

Diff behavior highlights:
- Creates missing schemas, extensions, tables, types, functions, views, triggers, and more; skips objects already live.
- Adds missing columns to existing tables; alters changed columns and constraints (type/default/nullability changes gated behind `--unsafe`).
- Drops columns only with `--unsafe`.

See `docs/feature_support.md` for the full capability matrix.

## Notes
- Forward-only: no down/rollback.
- Advisory locks during init/migrate.
- Checksums added to committed files; verified before applying.
- SQL splitting respects single/double quotes, dollar quotes, and line/block comments. `E'...'` backslash-escape strings are not handled.
