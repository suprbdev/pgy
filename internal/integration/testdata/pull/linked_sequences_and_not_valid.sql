do $$ begin
  if not exists (select from pg_roles where rolname = 'pgy_fid_owner') then create role pgy_fid_owner nologin; end if;
  if not exists (select from pg_roles where rolname = 'pgy_fid_reader') then create role pgy_fid_reader nologin; end if;
end $$;
create schema fid;
create sequence fid.seq_ticket start 1000;
create table fid.ticket (id int primary key default nextval('fid.seq_ticket'), body text, n int);
alter sequence fid.seq_ticket owned by fid.ticket.id;
alter table fid.ticket owner to pgy_fid_owner;
create sequence fid.seq_free;
alter sequence fid.seq_free owner to pgy_fid_owner;
create domain fid.pct as numeric(5,2) check (value >= 0);
alter domain fid.pct add constraint pct_not_99 check (value <> 99) not valid;
create domain fid.positive as int;
alter domain fid.positive add constraint positive_check check (value > 0) not valid;
comment on constraint positive_check on domain fid.positive is 'checked for new values only';
create materialized view fid.mv as select id, body, n from fid.ticket;
alter materialized view fid.mv alter column body set compression lz4;
alter materialized view fid.mv alter column body set storage main;
alter materialized view fid.mv alter column n set statistics 500;
alter materialized view fid.mv alter column n set (n_distinct = 10);
do $$ begin
  if current_setting('server_version_num')::int >= 180000 then
    execute 'alter default privileges for role pgy_fid_owner grant select on large objects to pgy_fid_reader';
    execute 'alter default privileges grant select, update on large objects to pgy_fid_reader';
  end if;
end $$;
