do $$ begin
  if not exists (select from pg_roles where rolname = 'pgy_fx_reader') then create role pgy_fx_reader; end if;
end $$;
create schema ext;
comment on schema ext is 'extensions live here';
grant usage on schema ext to pgy_fx_reader;
create extension earthdistance schema ext cascade;
create schema bare;
create extension hstore schema bare;
create extension citext schema pg_catalog;
create schema el;
create table el.places (id int primary key, name citext not null, pos ext.cube, attrs bare.hstore);
create index places_pos on el.places using gist (pos);
