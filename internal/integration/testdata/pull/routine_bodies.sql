create schema rb;

create function rb.framed() returns integer language plpgsql as $$
begin
  return 1;
end;
$$;

create function rb.framed_blank_line() returns integer language sql as $$
select 1

$$;

create function rb.indented() returns integer language plpgsql as $$
    begin
      return 1;
    end;
$$;

create function rb.tabbed() returns integer language plpgsql as E'\n\tbegin\n\t\treturn 1;\n\tend;\n';

create function rb.inner_tabs() returns integer language plpgsql as E'\nbegin\n\treturn 1;\nend;\n';

create function rb.padded() returns integer language sql as $$ select 1 $$;

create function rb.quoted() returns integer language sql as 'select 1';

create function rb.unframed() returns integer language sql as $$select 1
  + 1$$;

create function rb.closing_on_last_line() returns integer language sql as $$
select 1
  + 1$$;

create function rb.opening_on_first_line() returns integer language sql as $$select 1
$$;

create function rb.blank_first_line() returns integer language sql as $$

select 1
$$;

create function rb.trailing_spaces() returns integer language sql as E'\nselect 1   \n  + 1\n';

create function rb.dollars() returns text language sql as $b$select '$$'$b$;

create function rb.ends_with_dollar() returns integer language sql as $b$select 1 as "a$"$b$;

create function rb.directive() returns integer language plpgsql as $$
#variable_conflict use_variable
declare
  x integer := 1;
begin
  return x;
end
$$;

create function rb.empty() returns void language sql as '';

create function rb.blank() returns void language sql as '  ';

create function rb.newline_only() returns void language sql as E'\n';

create procedure rb.empty_proc() language sql as '';

create procedure rb.padded_proc() language sql as $$ select 1 $$;

create function rb.cat(text, text) returns text language sql immutable as 'select $1 || $2';

create function rb.uncat(text, text) returns text language sql immutable as 'select left($1, length($1) - length($2))';

create aggregate rb.cat_all(text) (sfunc = rb.cat, stype = text, initcond = '');

create aggregate rb.cat_null(text) (sfunc = rb.cat, stype = text);

create aggregate rb.cat_moving(text) (
  sfunc = rb.cat, stype = text, initcond = '',
  msfunc = rb.cat, minvfunc = rb.uncat, mstype = text, minitcond = ''
);
