create extension if not exists btree_gist;
create schema so;
create table so.t (
    id int,
    k int,
    room int,
    during tstzrange,
    constraint t_pkey primary key (id) with (fillfactor = 80, deduplicate_items = off),
    constraint t_k_key unique (k) with (fillfactor = 70, deduplicate_items = off),
    constraint t_excl exclude using gist (room with =, during with &&) with (fillfactor = 60, buffering = on)
) with (toast_tuple_target = 4000, fillfactor = 90, autovacuum_enabled = false);
create index t_k on so.t (k) with (fillfactor = 50, deduplicate_items = off);
create table so.p (id int, k int) partition by range (k);
create table so.p1 partition of so.p for values from (0) to (10) with (fillfactor = 70, autovacuum_enabled = false);
create materialized view so.mv with (fillfactor = 60, autovacuum_enabled = false) as select id from so.t;
create index mv_id on so.mv (id) with (fillfactor = 40, deduplicate_items = off);
