create schema s16;
create table s16.t (id int primary key, a int, b text);
create view s16.v (ident, total) with (security_barrier) as select id, a from s16.t;
alter view s16.v alter column total set default 0;
create view s16.chk as select id, a from s16.t where a > 0 with cascaded check option;
create recursive view s16.nums (n) as values (1) union all select n + 1 from nums where n < 5;
create materialized view s16.mv with (fillfactor = 70) as select id from s16.t with no data;
create view s16.inv with (security_invoker = true) as select * from s16.t;
comment on view s16.inv is 'invoker view';
