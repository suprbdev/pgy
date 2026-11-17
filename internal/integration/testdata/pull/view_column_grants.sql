do $$ begin
  if not exists (select from pg_roles where rolname = 'pgy_fx_reader') then create role pgy_fx_reader; end if;
end $$;
create schema vcg;
create table vcg.t (id int primary key, a text, b int);
create view vcg.v as select id, a, b from vcg.t;
grant select (id, a) on vcg.v to pgy_fx_reader;
grant update (b) on vcg.v to pgy_fx_reader with grant option;
create materialized view vcg.mv as select id, b from vcg.t;
grant select (b) on vcg.mv to public;
