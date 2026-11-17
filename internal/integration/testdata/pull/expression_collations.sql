create extension if not exists pg_trgm;
create extension if not exists btree_gist;
create extension if not exists citext;
create schema ec;
create table ec.zoo (
    id bigint primary key,
    ip inet,
    name text collate "C",
    title text,
    ci citext,
    flag boolean not null default true
);
create index zoo_hash on ec.zoo using hash ((ip::text)) where flag = true;
create index zoo_hash_host on ec.zoo using hash (host(ip));
create index zoo_hash_c_column on ec.zoo using hash (lower(name));
create index zoo_hash_citext on ec.zoo using hash (lower(ci));
create index zoo_hash_array on ec.zoo using hash (string_to_array(title, ' '));
create index zoo_hash_explicit on ec.zoo using hash ((ip::text) collate "C");
create index zoo_hash_default on ec.zoo using hash (lower(name) collate "default");
create index zoo_btree_c_column on ec.zoo (lower(name));
create index zoo_btree_inner on ec.zoo ((title collate "C"));
create index zoo_btree_two on ec.zoo (lower(title), upper(name) collate "default" desc);
create index zoo_gin_trgm on ec.zoo using gin (lower(name) gin_trgm_ops);
create index zoo_gist_trgm on ec.zoo using gist (lower(title) collate "C" gist_trgm_ops);
create index zoo_gist_text on ec.zoo using gist (lower(name));
create index zoo_spgist on ec.zoo using spgist (lower(name));
create index zoo_brin on ec.zoo using brin (lower(name));
create index zoo_brin_bloom on ec.zoo using brin (upper(title) text_bloom_ops);
