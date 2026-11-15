do $$ begin
  if not exists (select from pg_roles where rolname = 'pgy_fx_reader') then create role pgy_fx_reader; end if;
end $$;
create extension if not exists postgres_fdw;
create schema s15;
grant usage on schema s15 to pgy_fx_reader with grant option;
create server s15_srv foreign data wrapper postgres_fdw options (host 'localhost');
grant usage on foreign server s15_srv to pgy_fx_reader;
create domain s15.pos as int check (value > 0);
grant usage on domain s15.pos to pgy_fx_reader;
create type s15.e as enum ('a');
revoke usage on type s15.e from public;
create function s15.f() returns int language sql as 'select 1';
grant execute on function s15.f() to pgy_fx_reader with grant option;
create sequence s15.q;
grant update on sequence s15.q to pgy_fx_reader;
alter default privileges in schema s15 grant usage on types to pgy_fx_reader;
alter default privileges in schema s15 grant select on sequences to pgy_fx_reader with grant option;
alter default privileges grant usage on schemas to pgy_fx_reader;
