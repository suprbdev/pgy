create schema sts;
create schema sts_other;
create table sts.t (a int, b int, c text);
create statistics sts.own (ndistinct) on a, b from sts.t;
create statistics sts_other.s (dependencies) on a, b from sts.t;
create statistics public.sts_p on a, lower(c) from sts.t;
comment on statistics sts_other.s is 'elsewhere';
alter statistics sts_other.s set statistics 50;
