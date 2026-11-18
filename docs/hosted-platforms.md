# Hosted platforms

Hosted PostgreSQL platforms (Supabase, Amazon RDS and Aurora, Neon, Google Cloud SQL, Azure Database for PostgreSQL) put schemas, roles, extensions, event triggers, publications, default privileges and settings into every database. The application doesn't own them: the platform creates them, changes them in its own upgrades, and its services stop working when they are dropped or reverted. This page shows how to adopt such a database with pgy and keep pgy away from what the platform owns.

## Contents

- [The `external:` block](#the-external-block)
  - [Patterns](#patterns)
  - [What pgy does with external objects](#what-pgy-does-with-external-objects)
  - [Declaring objects on platform tables](#declaring-objects-on-platform-tables)
  - [`prune.keep_extensions`](#prunekeep_extensions)
- [Adopting a database](#adopting-a-database)
- [Lists per platform](#lists-per-platform)
  - [Supabase](#supabase)
  - [Amazon RDS and Aurora](#amazon-rds-and-aurora)
  - [Neon](#neon)
  - [Google Cloud SQL](#google-cloud-sql)
  - [Azure Database for PostgreSQL](#azure-database-for-postgresql)
- [Running as a role that is not a superuser](#running-as-a-role-that-is-not-a-superuser)
- [Extensions created by a platform role](#extensions-created-by-a-platform-role)
- [Secrets in database settings](#secrets-in-database-settings)
- [A role named pgy](#a-role-named-pgy)
- [Extensions that create objects at run time](#extensions-that-create-objects-at-run-time)

## The `external:` block

The `external:` block of the [config file](../README.md#configuration) lists, as glob patterns, what the database has that the application doesn't own:

```yaml
external:
  schemas: [auth, storage, realtime, extensions]
  roles: [supabase_admin, anon, authenticated, service_role, "supabase_*"]
  settings: ["pgrst.*", "app.settings.*"]
  relations: ["_timescaledb_internal.*", "*._hyper_*_chunk"]
```

Without the block, `pgy pull`, `pgy diff` and `pgy validate` behave as they always have. pgy ships no built-in lists: platforms change what they install, and only you know which of it your application uses. The [lists below](#lists-per-platform) are a starting point to copy.

### Patterns

A pattern matches a whole name. `*` matches any run of characters, `?` any one character, and every other character itself. Schema, role and relation names are matched case-sensitively, as PostgreSQL stores them, and setting names in any case, as PostgreSQL compares them. A relation pattern is `schema.relation`, split at its first dot, each part matched on its own: `*._hyper_*_chunk` matches a TimescaleDB chunk in every schema. Quote a pattern that starts with `*` in YAML.

### What pgy does with external objects

| | `pgy pull` | `pgy diff` | `--unsafe --prune` | `pgy validate` |
|---|---|---|---|---|
| **`schemas`** | leaves them out, with every object in them, the extensions installed in them, the event triggers whose function is in them and the publications that publish only their tables | never creates or alters the schema; a table in it is managed [only as far as it is declared](#declaring-objects-on-platform-tables) | never drops anything in them, and nothing in them counts towards the schemas `--prune` works in | doesn't report references into them: foreign keys, triggers and event triggers calling their functions, `inherits`, `partitionOf`, `dependsOn` |
| **`roles`** | leaves out the objects they own (tables, views, sequences, routines, types, schemas, extensions, servers, wrappers, event triggers, publications and objects pgy can't express), their `defaultPrivileges` entries (`for:` the role) and their entries under `roles:` | treats them as unmanaged grantees: a grants block that doesn't name one neither revokes nor requires its privileges, one that names it makes them exact; never alters an existing external role declared under `roles:` (its attributes, settings, memberships and comment), with a warning, and never revokes a membership in one | never drops the event triggers, extensions, publications, servers and foreign-data wrappers they own | |
| **`settings`** | leaves them out of `database:` and of every role's `set:` | never resets them; a setting the YAML declares is still set | | |
| **`relations`** | leaves out matching tables, views, materialized views and sequences (with the sequences of their columns), and warns about a view or materialized view it writes that reads one (outside the external schemas, and not an extension's), since a database rebuilt from the YAML lacks it and cannot create the view | | never drops them, nor reports them as kept inheritance children | doesn't report references to them |

`pgy pull` also leaves out a `publications:` block when every publication is external, so the YAML doesn't claim the publications as the application's.

### Declaring objects on platform tables

The application often puts its own objects on a platform's tables: a policy on `storage.objects`, a trigger on `auth.users`, rows in `storage.buckets`, a grant on `auth.users`. A table in an external schema can be declared without columns to manage exactly those:

```yaml
schema auth:
  table users:
    triggers:
      on_auth_user_created: {timing: after, events: [insert], level: row, procedure: public.handle_new_user()}
schema storage:
  table objects:
    policies:
      avatars_read: {for: select, using: "bucket_id = 'avatars'"}
  table buckets:
    data:
      key: [id]
      rows:
        - {id: avatars, name: avatars, public: true}
```

Such a declaration manages `policies`, `triggers`, `grants`, `rowLevelSecurity`, `forceRowLevelSecurity`, `comment`, `owner` and `data` (with `key`), like a [table an extension owns](schema.md#objects-an-extension-owns), and triggers on top. pgy never creates, alters or drops the table, and a declaration that also lists columns or keys is cut down to those parts, with a warning. What it manages is additive, since the platform has policies, triggers, grants and rows of its own on the same tables:

- **Listed** policies and triggers are created, and dropped and recreated when their definition changes; listed rows are inserted and updated; a listed grantee gets exactly the privileges listed.
- **Unlisted** policies, triggers, grantees and rows are left alone, also by `--unsafe --prune`. Removing one from the YAML therefore doesn't drop it: drop it in a migration of its own.

`pgy pull` leaves external schemas out entirely, the application's policies and triggers on their tables included, so declare these by hand after a pull. The trigger function, `public.handle_new_user()` above, is the application's and is pulled as usual.

### `prune.keep_extensions`

[`prune.keep_extensions`](../README.md#unsafe-changes-and-pruning) predates the `external:` block and still works. It names extensions that `--prune` never drops, whatever their schema and owner, while `pgy pull` still writes them and `pgy diff` still manages their declarations. The `external:` block covers extensions the platform installs in one of its schemas or creates as one of its roles: they are neither pulled nor pruned. Use `keep_extensions` for an extension in a schema the application owns that the application's role created, such as a `pg_stat_statements` installed in `public` by the role pgy connects as, which the YAML should not have to list to keep it.

## Adopting a database

1. Write `.pgy.yml` with the `external:` block of your platform, from the [lists below](#lists-per-platform) and what `\dn`, `\du`, `\dx` and `\dy` show on your database.
2. `pgy pull --out schema/` writes the application: one file per schema plus `database.yml`. Add `--schema public` (or `--exclude-schema`) to pull fewer schemas; see [Pulling some schemas](../README.md#pulling-some-schemas).
3. Read the warnings `pgy pull` prints: objects pgy can't express, and [configuration rows of extensions](#extensions-that-create-objects-at-run-time) the YAML doesn't carry. `--json` (with `--out`) prints them with the files written.
4. Declare the application's objects on platform tables by hand, as [above](#declaring-objects-on-platform-tables).
5. `pgy diff` must report no changes, and so must `pgy diff --unsafe --prune --buffer -`. If either plans something for an object the platform owns, add a pattern for it.
6. Run `pgy validate --strict` in CI: references into external schemas are not reported, so an in-sync application passes.

## Lists per platform

These lists cover what the platforms install by default, based on hand-written imitations of their layouts. Projects differ (a Supabase project with Realtime or Queues enabled, an RDS instance with `aws_s3`), so check your database and add what it shows. A role is listed when the platform owns it, not when the application uses it: grants to an external role on the application's tables are still pulled and, once declared, managed.

### Supabase

```yaml
external:
  schemas: [auth, storage, realtime, graphql, graphql_public, vault, net, pgbouncer, supabase_functions, extensions]
  roles: ["supabase_*", authenticator, anon, authenticated, service_role, dashboard_user, pgbouncer]
  settings: ["pgrst.*", "app.settings.*"]
```

- `postgres` is the role the application connects as on Supabase, and is not listed. Its default privileges in `public` (which grant `anon`, `authenticated` and `service_role` everything on new tables) are pulled as the application's.
- The extensions the platform creates (`pg_graphql`, `supabase_vault`, `pg_net`, `pgcrypto`, `uuid-ossp`, `pg_stat_statements`, ...) live in external schemas or belong to `supabase_admin`, and are left out. An extension the application installs in `extensions` is left out too; declare it by hand under `extensions:` (with `schema: extensions`) if a rebuild should create it.
- The event triggers (`pgrst_ddl_watch`, `issue_pg_net_access`, ...) call functions in `extensions` and are left out.
- `supabase_realtime` is owned by `postgres` and publishes the tables you enabled Realtime for, so `pgy pull` writes it as the application's. Keep it in `publications:`, or leave the block out to leave publications unmanaged.
- `app.settings.jwt_secret` is a secret; see [Secrets in database settings](#secrets-in-database-settings).

### Amazon RDS and Aurora

```yaml
external:
  roles: [rdsadmin, "rds_*", rdstopmgr]
```

- The master user is the application's role. RDS's own schemas live in the `rdsadmin` database, not in yours; the extensions RDS creates as `rdsadmin` (`rds_tools`, and `pg_stat_statements` where the platform installs it) are left out by owner.
- Extensions the master user creates (`aws_s3` with `aws_commons`, `pg_trgm`, `citext`) are the application's and are pulled. Use `prune.keep_extensions` for one you don't want the YAML to own.
- Server parameters (`rds.*`, `shared_preload_libraries`) are set in parameter groups, not as database settings, and pgy never sees them.

### Neon

```yaml
external:
  schemas: [neon]
  roles: [cloud_admin, neon_superuser]
```

- `neondb_owner` (or the role you created) is the application's role and a member of `neon_superuser`; that membership is pulled with `--roles` and never revoked.
- `cloud_admin`'s default privileges in `public` (grants to `neon_superuser`) are left out.

### Google Cloud SQL

```yaml
external:
  schemas: [google_ml]
  roles: ["cloudsql*"]
```

- `cloudsqladmin` and `cloudsqlsuperuser` belong to the platform; the `postgres` user and the users you create are the application's.
- `google_ml_integration` keeps its model registry in a configuration table; its rows are the platform's and the extension is left out by owner, so pull doesn't report them.

### Azure Database for PostgreSQL

```yaml
external:
  schemas: [azure]
  roles: [azuresu, "azure_*"]
```

- `azuresu` is the platform's superuser and owns `azure` and `pgaadauth`, which it creates in `public`: they are left out by owner. The administrator you created is the application's role.
- Extensions must be on the server's `azure.extensions` allow-list before anyone can create them; see the next section.

## Running as a role that is not a superuser

On a hosted platform pgy connects as a role that is not a superuser, which changes four things:

- **The migrations table.** `pgy init`, `migrate` and `status` create only what is missing. A role without `CREATE` on the database keeps the table in a schema it owns: `--migrations-table app.pgy_migrations` (`migrations_table` in `.pgy.yml`). An existing table needs no `CREATE` privilege at all.
- **Extensions.** A role that is not a superuser can create a trusted extension, and the extensions the platform allows through its own hook on `CREATE EXTENSION`: Supabase's allow-list, RDS's `rds.allowed_extensions` parameter, Azure's `azure.extensions` server parameter, the list Cloud SQL supports. pgy emits a plain `CREATE EXTENSION`, so an untrusted extension the platform doesn't allow fails in the migration with `permission denied to create extension`: enable it the platform's way first.
- **Objects the role doesn't own.** Only an object's owner (or a member of that role, or a superuser) may drop or alter it, so `--prune` leaves out what a platform role owns, and `pgy diff` leaves out the `ALTER`, `COMMENT ON`, `CREATE OR REPLACE` and other statements for the declared objects such a role owns, along with the rest of what the plan does for them, with a warning of kind `not_owner` (see [Objects of other owners](schema.md#objects-of-other-owners)). A `GRANT` the role holds the grant option for, and a trigger on a table it has the `TRIGGER` privilege on, still go through. Listing that role under `external.roles` keeps `pgy pull` from writing its objects and `--prune` from touching them, without a warning. A change the YAML asks for on such an object that the role can't make, such as a policy on a platform table it lacks the owner's privileges for, is still left out with a `not_owner` warning, which says that the owner is listed under `external.roles`.
- **Roles the role can't change.** On PostgreSQL 16 and later, a role with `CREATEROLE` may alter, comment on, grant or revoke only the roles it holds the `ADMIN` option on, which it has on the roles it created, and nobody but a superuser may change a superuser role. `pgy diff` leaves out the `ALTER ROLE`, `COMMENT ON ROLE`, `GRANT` and `REVOKE` statements on the platform's roles that PostgreSQL would reject, such as the grant of a platform role to an application role, with a `not_owner` warning per role (see [Roles the connecting role can't change](schema.md#roles-the-connecting-role-cant-change)). On PostgreSQL 16 and later, a membership the platform granted to an application role (a grant whose grantor is a platform role or the platform's superuser) keeps the options the platform gave it: pgy changes a membership's options in each grant, `GRANTED BY` its grantor, and leaves out the statements on the platform's grants with the same warning. Listing the platform's roles under `external.roles` keeps their own attributes, settings, memberships and comments out of the plan; a grant of one to a role the YAML declares is still checked, and the warning says that the role is external.

## Extensions created by a platform role

When a platform role creates an extension, its objects get that role's default privileges: Supabase's `supabase_admin` grants `anon`, `authenticated` and `service_role` everything on what it creates in `extensions`. PostgreSQL does not record those grants as the extension's initial privileges (`pg_init_privs`), so to pgy they look like privileges granted later. Without an `external:` block, `pgy pull` writes a grants declaration for every function of such an extension, and `pgy diff` manages them from then on. Listing the extension's schema or the role that created it under `external:` leaves the extension and its objects out.

## Secrets in database settings

Some platforms keep secrets in database settings: Supabase's `app.settings.jwt_secret`. A full `pgy pull` writes database settings into `database.yml`, which ends up in version control. List them under `external.settings`, so that pull leaves them out and `pgy diff` never resets them, or pull with `--schema`, which leaves the whole `database:` block out unless `--database-settings` asks for it. pgy has no syntax for passwords, so roles' passwords never reach the YAML.

## A role named pgy

pgy keeps its migrations table in the schema `pgy` by default. PostgreSQL's default `search_path` is `"$user", public`, so for a role named `pgy` that schema comes first: an extension declared without `schema:`, and whatever a hand-written migration creates without a schema, land in `pgy`. Connect as a role with another name, or move the migrations table with `--migrations-table`; see [Extensions](schema.md#extensions).

## Extensions that create objects at run time

Some extensions create tables while the application runs, outside the extension, so they are not its members and pgy treats them as the application's: `pgy pull` writes them and `--unsafe --prune` drops them. List them under `external.relations`:

| Extension | Objects | Patterns |
|-----------|---------|----------|
| TimescaleDB | chunks of hypertables and the internals of continuous aggregates, in `_timescaledb_internal` or the hypertable's schema, and the continuous aggregates themselves (see below) | `"_timescaledb_internal.*"`, `"*._hyper_*_chunk"`, and each aggregate's name, such as `"app.conditions_hourly"` |
| pg_partman | partitions of a partition set and template tables | `"partman.template_*"`, and a pattern for your partition names, such as `"public.events_p*"` |
| pgmq | one queue table and one archive table per queue | `"pgmq.q_*"`, `"pgmq.a_*"` |

- **TimescaleDB's continuous aggregates.** A continuous aggregate is a view, in the schema it was created in, that reads the aggregate's materialized hypertable in `_timescaledb_internal`, which the patterns above make external. `pgy pull` writes that view and warns that it reads an external relation: a database rebuilt from the YAML lacks the hypertable, so the view cannot be created there. Add the aggregate's name (or a pattern for your naming convention) to `external.relations`, which leaves it out of the YAML and of `--prune`, or replace the pulled view with the aggregate's own declaration, which diffs clean against the database, also with `--unsafe --prune`, and creates the aggregate where the hypertable exists:

  ```yaml
  schema app:
    materialized view conditions_hourly:
      with: {timescaledb.continuous: true}
      withData: false
      query: |
        select time_bucket('1 hour', time) as bucket, device_id, avg(temperature) as avg_temp
        from app.conditions group by 1, 2
  ```
- **TimescaleDB's default time index.** `create_hypertable` creates an index on the time column (`<table>_<column>_idx`) unless it is called with `create_default_indexes => false`. The index is not declared, so `--unsafe --prune` drops it. Declare it under the hypertable's `indexes:`, or create hypertables without default indexes.
- **Configuration rows.** Extensions keep their own state in configuration tables (`pg_extension.extconfig`): TimescaleDB its hypertables, jobs and continuous aggregates, pg_cron its jobs, pg_partman its partition sets, pgmq its queues. `pgy pull` reports, as kind `unsupported` (so `ci.fail_on: [unsupported]` covers it), each configuration table of an extension it writes that holds rows its condition selects, the rows `pg_dump` keeps, with their number: a database rebuilt from the YAML would lack them, unless the extension makes them again itself (pg_search refills its type modifier cache when the indexes are created). Rows that are settings, such as pg_cron's jobs or PostGIS's own spatial reference systems, are pulled with `--data` (a `data:` block on the table declared without columns, see [Reference Data](schema.md#reference-data)); the report names the configuration tables with rows that a table's foreign keys reference, which `--data` has to pull with it. Rows that describe objects the extension's functions create at run time (TimescaleDB's hypertables and chunks, pg_partman's partition sets, pgmq's queues) don't make those objects when copied: call the functions from a migration in a rebuilt database, and list the table under `external.relations` to silence the report for good, as for logs such as `cron.job_run_details`. The report says when `--data` cannot pull a table at all: it has no primary key (`pgmq.meta`), a column of an object identifier type such as `regclass`, whose rows name objects of the database, or a foreign key to such a table or to an external relation. `pgy pull --data` also warns when the rows it pulls reference rows of a configuration table it doesn't pull, which a rebuilt database's foreign key would reject. pgy cannot tell rows an extension's own script inserted from the application's when the condition doesn't, so those are reported too. Configuration sequences, whose position `pg_dump` keeps, are reported with the table whose column they number (the rows `--data` pulls advance them in a rebuilt database), and on their own when no pulled rows bring them to their position: advance those with `setval` in a migration, or list them under `external.relations`.
