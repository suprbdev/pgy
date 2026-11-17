create extension if not exists postgres_fdw;
create extension if not exists file_fdw;
create server bare foreign data wrapper postgres_fdw;
create server wh type 'pg' version '15.10' foreign data wrapper postgres_fdw options (host 'wh.internal', port '05432', dbname 'analytics', fetch_size '1000');
comment on server wh is 'analytics warehouse';
create server files foreign data wrapper file_fdw;
create schema ft;
create table ft.events (id bigint not null, region text not null, payload text) partition by list (region);
create table ft.parent (id integer not null, name text);
create foreign table ft.orders (
    id integer not null,
    qty integer options (column_name 'quantity'),
    dbl integer generated always as (qty * 2) stored,
    big text
) server wh options (schema_name 'public', table_name 'orders');
alter foreign table ft.orders alter column big set storage external;
alter foreign table ft.orders add constraint orders_qty check (qty >= 0);
comment on foreign table ft.orders is 'remote orders';
create foreign table ft.plain (id integer, v text) server bare;
create foreign table ft.events_us partition of ft.events for values in ('us') server wh options (table_name 'events');
alter foreign table ft.events_us alter column id options (add column_name 'event_id');
alter foreign table ft.events_us alter column payload set default 'none';
create foreign table ft.child (extra integer) inherits (ft.parent) server wh options (table_name 'child');
alter foreign table ft.child alter column name options (add column_name 'label');
create table ft.grandchild (note text) inherits (ft.child);
create foreign table ft.lines (line text) server files options (filename '/tmp/pgy-lines.csv', format 'csv', header 'true');
alter foreign table ft.lines alter column line options (add force_not_null 'true');
