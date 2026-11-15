create schema ext;
create extension if not exists citext with schema ext;
create extension if not exists pg_trgm with schema ext;
create schema eso;
create table eso.t (id int primary key, name ext.citext, body text);
create index t_body_trgm on eso.t using gin (body ext.gin_trgm_ops);
create index t_name_trgm on eso.t using gist (name ext.gist_trgm_ops);
