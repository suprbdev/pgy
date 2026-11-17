create extension if not exists file_fdw;
create server files foreign data wrapper file_fdw;
create schema k;
create foreign table k.csv1 (a text, b text options (force_null 'true'), "null" text options (force_not_null 'true'))
  server files options (filename '/tmp/a.csv', format 'csv', null 'NULL');
create table k.pivot (
  id int primary key,
  "2024" numeric,
  "2025" numeric default 0,
  "true" boolean,
  "null" text,
  "1.5" text,
  "~" text,
  "0x1F" int,
  "1e3" int,
  "2024-01-01" date,
  "y" int,
  "no" int,
  "<<" int
);
create index pivot_years on k.pivot ("2024", "true") include ("2025");
alter table k.pivot add constraint pivot_u unique ("2025", "null");
create table k.child (id int primary key, "2025" numeric, "null" text,
  foreign key ("2025", "null") references k.pivot ("2025", "null"));
create type k.comp as ("1" int, "2" text, "null" text);
comment on column k.comp."1" is 'one';
create type k.e as enum ('1', '2', 'true', 'null', 'a');
grant select ("2024") on k.pivot to public;
comment on column k.pivot."2024" is 'year';
create statistics k.pivot_st on "2024", "2025" from k.pivot;
create table k.parts ("2024" int not null, v text) partition by list ("2024");
create view k.v as select id, "2024" from k.pivot;
comment on column k.v."2024" is 'view year';
