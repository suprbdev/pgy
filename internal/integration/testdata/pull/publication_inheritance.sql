create schema pubinh;
create table pubinh.base (id int primary key, note text);
create table pubinh.ext (extra int) inherits (pubinh.base);
create table pubinh.other (id int primary key);
create publication pubinh_filtered for table pubinh.base (id, note) where (id > 10), pubinh.other;
create publication pubinh_plain for table pubinh.base;
create publication pubinh_only for table only pubinh.base;
