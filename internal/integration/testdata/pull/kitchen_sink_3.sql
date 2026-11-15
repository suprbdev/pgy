create schema s3;
create type s3.tone as enum ('it''s fine', 'two words', 'UPPER');
create domain s3.percent as numeric(5,2) default 0 constraint percent_min check (value >= 0) constraint percent_max check (value <= 100);
comment on constraint percent_min on domain s3.percent is 'not negative';

create table s3.accounts (
  id int generated always as identity (start with 10 increment by 5 maxvalue 1000 cycle) primary key,
  name text not null,
  share s3.percent,
  tone s3.tone,
  code text not null
);
create unique index accounts_code on s3.accounts (code);
alter table s3.accounts replica identity using index accounts_code;
alter table s3.accounts enable row level security;
alter table s3.accounts force row level security;
create policy accounts_read on s3.accounts for select using (true);
create statistics s3.accounts_expr (ndistinct) on (lower(name)), code from s3.accounts;

create table s3.measures (id int not null, at date not null, account_id int, value numeric) partition by range (at);
alter table s3.measures add constraint measures_pk primary key (id, at);
alter table s3.measures add constraint measures_value_check check (value is null or value >= 0);
alter table s3.measures add constraint measures_account_fk foreign key (account_id) references s3.accounts (id);
create table s3.measures_2025 partition of s3.measures for values from ('2025-01-01') to ('2026-01-01');
create table s3.measures_2026 partition of s3.measures for values from ('2026-01-01') to ('2027-01-01');
create index measures_account on s3.measures (account_id);
create function s3.touch() returns trigger language plpgsql as $$ begin return new; end $$;
create trigger measures_touch before insert on s3.measures for each row execute function s3.touch();

create table s3.notes (id int primary key, measure_id int, measure_at date, body text,
  constraint notes_measure_fk foreign key (measure_id, measure_at) references s3.measures (id, at) on delete cascade);

create view s3.named as select id, name from s3.accounts where tone is not null;
create view s3.named_upper as select id, upper(name) as name from s3.named;
create view s3.local_check as select id, name, code from s3.accounts where share > 10 with local check option;
create materialized view s3.named_count as select count(*) as n from s3.named;
comment on column s3.named_upper.name is 'shouted';

create function s3.split_name(in full_name text, out first text, out last text) language sql immutable as $$ select split_part(full_name, ' ', 1), split_part(full_name, ' ', 2) $$;
create procedure s3.record(a int, b text default 'x') language plpgsql as $$ begin perform a; end $$;
create function s3.on_drop() returns event_trigger language plpgsql as $$ begin null; end $$;
create event trigger s3_drops on sql_drop execute function s3.on_drop();
