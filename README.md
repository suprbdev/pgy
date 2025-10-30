# pgy

Forward-only PostgreSQL migration tool. It reads YAML schema files, diffs against a live DB, writes SQL to a buffer, and commits/applies migrations. No rollbacks.

## Install / Build

- Build with Go:

```
make build     # builds bin/pgy with version ldflags
make test      # runs unit tests
make clean     # cleans bin/ and buffer file
make install   # installs to $HOME/go/bin if present, else PREFIX/bin (/usr/local/bin)
```

The binary is placed at `bin/pgy`.

## Configuration

Configuration precedence: flags > env > .pgy.yml > defaults.

- Common flags and mirrored env vars:
  - `--dsn` / `PGY_DSN` (PostgreSQL DSN)
  - `--schema-root` / `PGY_SCHEMA_ROOT` (root for YAML files)
  - `--schemas` / `PGY_SCHEMAS` (comma-separated YAML files, relative to schema-root)
  - `--migrations-dir` / `PGY_MIGRATIONS_DIR` (default: `./migrations`)
  - `--buffer` / `PGY_BUFFER` (default: `./.pgy.buffer.sql`)
  - `--quiet` / `PGY_QUIET=1`
  - `--verbose` / `PGY_VERBOSE=1`
  - `--json` / `PGY_JSON=1`

- Optional `.pgy.yml` (in project root):

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
pgy migrate --dsn "$PG_DSN"                  # apply all pending
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
```

Diff behavior (current minimal version):
- Creates missing tables with columns.
- Adds missing columns to existing tables.
- Drops columns only with `--unsafe`.

## Notes
- Forward-only: no down/rollback.
- Advisory locks during init/migrate.
- Checksums added to committed files; verified before applying.
- SQL splitting is naive (semicolon-separated); avoid dollar-quoted function bodies for now.
