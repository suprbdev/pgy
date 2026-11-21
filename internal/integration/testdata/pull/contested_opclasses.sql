create extension if not exists btree_gist;
create schema netw;
create table netw.host (
  id int primary key,
  tenant int not null,
  addr inet not null,
  subnet cidr not null,
  constraint host_tenant_addr_excl exclude using gist (tenant with =, addr with =)
);
create index host_addr_btg on netw.host using gist (addr gist_inet_ops);
create index host_addr_core on netw.host using gist (addr inet_ops);
create index host_subnet_btg on netw.host using gist (subnet gist_cidr_ops);
create index host_default on netw.host using gist (tenant, addr);
create table netw.alloc (
  id int primary key,
  block inet not null,
  constraint alloc_no_overlap exclude using gist (block inet_ops with &&)
);
