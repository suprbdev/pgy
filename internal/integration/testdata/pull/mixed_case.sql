create schema "Mixed";
create type "Mixed"."Mood" as enum ('Happy', 'sad');
create type "Mixed"."Pair" as ("Left" int, "Right" text);
create domain "Mixed"."PosInt" as int constraint "PosCheck" check (value > 0);
create collation "Mixed"."NumSort" (provider = icu, locale = 'en-u-kn-true');
create sequence "Mixed"."Counter";
create table "Mixed"."Items" (
  "ItemId" int generated always as identity (sequence name "Mixed"."ItemSeq") primary key,
  "Name" text collate "Mixed"."NumSort",
  "Qty" "Mixed"."PosInt",
  "Mood" "Mixed"."Mood" default 'Happy',
  "Total" int generated always as ("Qty" * 2) stored,
  "Range" int4range,
  "Ext" int default nextval('"Mixed"."Counter"'),
  constraint "NoOverlap" exclude using gist ("Range" with &&),
  constraint "QtyCheck" check ("Qty" < 1000)
);
alter sequence "Mixed"."Counter" owned by "Mixed"."Items"."Ext";
create index "ItemsByName" on "Mixed"."Items" ("Name") include ("Qty");
create statistics "Mixed"."ItemStats" on "Qty", "Mood" from "Mixed"."Items";
create function "Mixed"."Touch"() returns trigger language plpgsql as $$ begin return new; end $$;
create trigger "TouchIt" before update of "Name", "Qty" on "Mixed"."Items" for each row execute function "Mixed"."Touch"();
alter table "Mixed"."Items" enable row level security;
create policy "ReadAll" on "Mixed"."Items" for select using ("Qty" > 0);
create view "Mixed"."ItemView" as select "ItemId", "Name" from "Mixed"."Items";
create rule "NoDelete" as on delete to "Mixed"."ItemView" do instead nothing;
create table "Mixed"."Events" ("At" date not null, "Kind" text) partition by range ("At");
create table "Mixed"."Events2025" partition of "Mixed"."Events" for values from ('2025-01-01') to ('2026-01-01');
create publication "MixedPub" for table "Mixed"."Items" ("ItemId", "Name") where ("ItemId" > 1);
