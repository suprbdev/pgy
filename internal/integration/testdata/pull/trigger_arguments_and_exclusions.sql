create extension if not exists btree_gist;
create extension if not exists moddatetime;
create extension if not exists dict_int;
create schema sp;
create text search dictionary sp.plain (template = intdict_template);
create text search dictionary sp.short_ints (template = intdict_template, maxlen = 4, rejectlong = true);
create function sp.note() returns trigger language plpgsql as $$ begin return new; end $$;
create table sp.booking (
  id bigint primary key,
  room_id int not null,
  label text not null,
  during tstzrange not null,
  price numeric not null default 0,
  updated_at timestamp,
  "UpdatedAt" timestamp,
  constraint booking_no_overlap exclude using gist (room_id with =, during with &&) with (fillfactor = 70) where (price between -1 and 100),
  constraint booking_label exclude (label with =) deferrable initially deferred,
  constraint booking_lower exclude using gist (lower(label) with =, (room_id + 1) with <>) include (price),
  constraint booking_label_check check (label <> 'public.label')
);
create trigger booking_mdt before update on sp.booking for each row execute function moddatetime(updated_at);
create trigger booking_mdt_quoted before update on sp.booking for each row execute function moddatetime("UpdatedAt");
create trigger booking_note after insert on sp.booking for each row execute function sp.note('public.booking', 7, $$it's$$, E'a\tb');
create trigger booking_noop before update on sp.booking for each row execute function suppress_redundant_updates_trigger();
