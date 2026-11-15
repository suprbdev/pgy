create schema s19;
create table s19.p (id int primary key);
create table s19.c (id int primary key, "ParentId" int, other int, constraint c_fk foreign key ("ParentId") references s19.p (id) match simple on delete set null ("ParentId") on update cascade);
create table s19.d (id int primary key, "Ref" int references s19.p on delete set default ("Ref"));
