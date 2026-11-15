create extension if not exists citext;
create extension if not exists btree_gist;
do $$ begin if not exists (select 1 from pg_roles where rolname = 'saas_app') then create role saas_app nologin; end if; end $$;
create schema saas;
create type saas.plan as enum ('free', 'pro', 'enterprise');
create table saas.tenant (id uuid primary key default gen_random_uuid(), slug citext not null unique, plan saas.plan not null default 'free', settings jsonb not null default '{}'::jsonb, created_at timestamptz not null default now(), constraint tenant_slug_format check (slug ~ '^[a-z0-9-]{3,40}$'));
create table saas.member (tenant_id uuid not null references saas.tenant(id) on delete cascade, id uuid not null default gen_random_uuid(), email citext not null, roles text[] not null default '{member}', deleted_at timestamptz, primary key (tenant_id, id));
create unique index member_email_live on saas.member (tenant_id, email) where deleted_at is null;
create table saas.booking (tenant_id uuid not null, id bigint generated always as identity, member_id uuid not null, room text not null, during tstzrange not null, meta jsonb, nights int generated always as (greatest(0, (extract(epoch from (upper(during) - lower(during))) / 86400)::int)) stored,
  primary key (tenant_id, id),
  constraint booking_member_fkey foreign key (tenant_id, member_id) references saas.member (tenant_id, id) deferrable initially deferred,
  constraint booking_no_overlap exclude using gist (tenant_id with =, room with =, during with &&));
create index booking_meta_gin on saas.booking using gin (meta jsonb_path_ops);
alter table saas.booking enable row level security;
alter table saas.member enable row level security;
create policy tenant_isolation on saas.booking using (tenant_id = current_setting('app.tenant_id')::uuid) with check (tenant_id = current_setting('app.tenant_id')::uuid);
create policy tenant_isolation on saas.member to saas_app using (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
create function saas.current_tenant() returns uuid language sql stable security definer set search_path = pg_catalog, saas as $$ select nullif(current_setting('app.tenant_id', true), '')::uuid $$;
revoke all on function saas.current_tenant() from public;
grant execute on function saas.current_tenant() to saas_app;
create table saas.audit (id bigserial primary key, tbl text not null, n int not null, at timestamptz not null default now());
create function saas.audit_rows() returns trigger language plpgsql as $$ begin insert into saas.audit (tbl, n) select tg_table_name, count(*) from new_rows; return null; end $$;
create trigger booking_audit after insert on saas.booking referencing new table as new_rows for each statement execute function saas.audit_rows();
create view saas.active_members with (security_invoker = true) as select tenant_id, id, email from saas.member where deleted_at is null;
grant usage on schema saas to saas_app;
grant select, insert, update on saas.booking, saas.member to saas_app;
grant select on saas.active_members to saas_app;
grant usage on sequence saas.audit_id_seq to saas_app;
alter default privileges in schema saas grant select on tables to saas_app;
create publication saas_pub for table saas.tenant, saas.booking (tenant_id, id, room, during) where (room <> 'secret');
comment on table saas.booking is 'room bookings; no overlaps per tenant and room';
comment on column saas.tenant.settings is 'free-form JSON settings';
