-- requires server_version_num 160000
create schema gp;
create table gp.m (id int not null, k text not null, s int generated always as (id * 2) stored) partition by list (k);
create table gp.m_a partition of gp.m (s generated always as (id * 20) stored) for values in ('a');
create table gp.m_b partition of gp.m for values in ('b');
create table gp.m_c partition of gp.m (s generated always as (id + 1) stored) for values in ('c') partition by list (id);
create table gp.m_c1 partition of gp.m_c for values in (1);
create table gp.m_c2 partition of gp.m_c (s generated always as (id - 1) stored) for values in (2);
create table gp.inh_p (id int, g int generated always as (id + 1) stored, t text collate "C" generated always as (id::text) stored);
create table gp.inh_c (g int generated always as (id + 100) stored, t text collate "C" generated always as ('c' || id) stored) inherits (gp.inh_p);
create table gp.inh_d () inherits (gp.inh_p);
