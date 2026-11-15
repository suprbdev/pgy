create extension if not exists pg_trgm;
create extension if not exists btree_gist;
create schema s2;
comment on schema s2 is 'second sink';
create collation s2.german (provider = libc, locale = 'C');
create type s2.status as enum ('draft', 'open', 'closed', 'archived');
comment on type s2.status is 'lifecycle';
create type s2.labelled as (label text collate "C", n int);
create type s2.textrange as range (subtype = text, collation = "C", multirange_type_name = s2.textmultirange);
create domain s2.code as text collate "C" not null check (value ~ '^[A-Z]{3}$');
comment on domain s2.code is 'three letters';
create sequence s2.small_seq as smallint minvalue 1 maxvalue 1000 no cycle;
comment on sequence s2.small_seq is 'small';

create table s2.items (
  id int primary key,
  code s2.code,
  name text not null,
  tags text[],
  price numeric(10,2) check (price >= 0) no inherit,
  status s2.status not null default 'draft',
  valid tstzrange,
  owner_id int,
  constraint items_name_key unique nulls not distinct (name, owner_id),
  constraint items_code_key unique (code) deferrable initially immediate,
  constraint items_no_overlap exclude using gist (owner_id with =, valid with &&) where (status <> 'archived')
);
comment on constraint items_name_key on s2.items is 'unique names';
create index items_name_trgm on s2.items using gin (name gin_trgm_ops);
create index items_name_c on s2.items (name collate "C" desc nulls last);
create index items_price_brin on s2.items using brin (price) with (pages_per_range = 32);
create index items_code_hash on s2.items using hash (code);
create index items_lower_name on s2.items ((lower(name))) include (price) where status = 'open';
comment on index s2.items_name_trgm is 'fuzzy search';
alter table s2.items replica identity full;
alter table s2.items cluster on items_name_c;

create table s2.audit (id bigint generated always as identity, item_id int, op text, at timestamptz default now());
create function s2.log_items() returns trigger language plpgsql as $$
begin insert into s2.audit (item_id, op) select id, tg_op from new_rows; return null; end $$;
create trigger items_audit after insert on s2.items referencing new table as new_rows for each statement execute function s2.log_items();
create function s2.noop() returns trigger language plpgsql as $$ begin return null; end $$;
create trigger items_truncate after truncate on s2.items for each statement execute function s2.noop();
create trigger items_price before update of price on s2.items for each row execute function s2.noop();
comment on trigger items_price on s2.items is 'price changes';

create table s2.h (id int, v text) partition by hash (id);
create table s2.h0 partition of s2.h for values with (modulus 2, remainder 0);
create table s2.h1 partition of s2.h for values with (modulus 2, remainder 1);
create table s2.r (id int, at date not null, region text not null) partition by range (at);
create table s2.r2025 partition of s2.r for values from ('2025-01-01') to ('2026-01-01') partition by list (region);
create table s2.r2025_eu partition of s2.r2025 for values in ('eu');
create table s2.r2025_rest partition of s2.r2025 default;
create table s2.p1 (a int);
create table s2.p2 (b int);
create table s2.multi (c int) inherits (s2.p1, s2.p2);

alter table s2.items enable row level security;
create policy items_all on s2.items for all to public using (status <> 'archived') with check (status <> 'archived');
comment on policy items_all on s2.items is 'hide archived';

create view s2.open_items with (check_option = cascaded, security_invoker = true) as select id, name, status from s2.items where status = 'open';
comment on view s2.open_items is 'open ones';
comment on column s2.open_items.name is 'item name';
create view s2.counts as with recursive n(i) as (select 1 union all select i + 1 from n where i < 3) select i from n;
create function s2.insert_open() returns trigger language plpgsql as $$ begin insert into s2.items (id, name, status) values (new.id, new.name, 'open'); return new; end $$;
create trigger open_items_ins instead of insert on s2.open_items for each row execute function s2.insert_open();
create materialized view s2.status_counts as select status, count(*) n from s2.items group by status;
create rule audit_no_update as on update to s2.audit do instead nothing;
comment on rule audit_no_update on s2.audit is 'append only';

create function s2.items_by(st s2.status) returns table (id int, name text) language sql stable as $$ select id, name from s2.items where status = st $$;
create function s2.concat_all(variadic parts text[]) returns text language sql immutable as $$ select array_to_string(parts, ',') $$;
create function s2.pad(s text, n int default 5, c text default ' ') returns text language sql immutable strict as $$ select lpad(s, n, c) $$;
create function s2.first_of(anyarray) returns anyelement language sql immutable as $$ select $1[1] $$;
create function s2.secret() returns text language sql stable security definer leakproof set search_path = s2, pg_catalog cost 5 as $$ select 'x' $$;
comment on function s2.secret() is 'definer';
create procedure s2.bump(inout n int) language plpgsql as $$ begin n := n + 1; end $$;
create function s2.acc(state int[], v int) returns int[] language sql immutable as $$ select state || v $$;
create function s2.acc_final(state int[]) returns int language sql immutable as $$ select coalesce(array_length(state, 1), 0) $$;
create function s2.acc_combine(a int[], b int[]) returns int[] language sql immutable as $$ select a || b $$;
create aggregate s2.cnt(int) (sfunc = s2.acc, stype = int[], finalfunc = s2.acc_final, combinefunc = s2.acc_combine, initcond = '{}', parallel = safe);
comment on aggregate s2.cnt(int) is 'counts';

create text search dictionary s2.snow (template = snowball, language = english, stopwords = english);
create text search configuration s2.en (parser = default);
alter text search configuration s2.en add mapping for asciiword, word with s2.snow;

create publication s2_pub for tables in schema s2 with (publish_via_partition_root = true);
create function s2.on_ddl() returns event_trigger language plpgsql as $$ begin null; end $$;
create event trigger s2_ddl on ddl_command_end when tag in ('CREATE TABLE') execute function s2.on_ddl();
alter event trigger s2_ddl disable;
comment on event trigger s2_ddl is 'ddl watcher';
