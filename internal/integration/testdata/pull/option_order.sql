create schema oo;
create text search dictionary oo.stem (template = snowball, stopwords = 'english', language = 'english');
create text search dictionary oo.plain (template = pg_catalog.simple, stopwords = 'english', accept = false);
create function oo.f() returns int language sql set work_mem = '1MB' set search_path = oo, public set lock_timeout = '1s' as 'select 1';
create function oo.g() returns int language sql security definer set statement_timeout = '5s' set lock_timeout = '1s' as 'select 1';
create procedure oo.p() language sql set statement_timeout = '5s' set lock_timeout = '1s' as 'select 1';
