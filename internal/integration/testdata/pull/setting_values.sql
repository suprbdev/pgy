create schema cfg;
do $$ begin
  execute format('alter database %I set myapp.tenant = ''''', current_database());
  execute format('alter database %I set plpgsql.extra_warnings = ''all''', current_database());
  execute format('alter database %I set myapp.motto = ''it''''s, really''', current_database());
  execute format('alter database %I set search_path = "$user", public', current_database());
end $$;
create function cfg.reserved() returns int language plpgsql
  set plpgsql.extra_warnings = 'all'
  set plpgsql.extra_errors = 'none'
  set myapp.word = 'default'
  as $$ begin return 1; end $$;
create function cfg.empty_path() returns int language sql security definer
  set search_path = ''
  set myapp.tenant = ''
  as $$ select 1 $$;
create function cfg.commas() returns int language sql
  set application_name = 'a,b'
  set myapp.code = 'my ($a,$b) = (1,2);'
  set myapp.quoted = 'use lib "/a,b"; $x = ''p,q'';'
  set myapp.literal = '''quoted'''
  set datestyle = 'ISO, MDY'
  as $$ select 1 $$;
create function cfg.lists() returns int language sql
  set search_path = "$user", "Mixed Case", cfg, public
  set timezone = 'UTC'
  as $$ select 1 $$;
create procedure cfg.numbers() language sql
  set work_mem = 64
  set jit = off
  set statement_timeout = 0
  set lock_timeout = '5s'
  set myapp.ratio = 0.25
  as $$ select 1 $$;
