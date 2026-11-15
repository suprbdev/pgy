create schema d1;
create type d1.mood as enum ('sad', 'ok', 'it''s great');
create type d1.pair as (a int, b text);
create table d1.ref (
  id int primary key,
  n numeric(12,4),
  f float8,
  r real,
  b boolean,
  t text,
  vc varchar(20),
  ch char(5),
  ts timestamp,
  tstz timestamptz,
  d date,
  tm time,
  iv interval,
  by bytea,
  j jsonb,
  js json,
  arr text[],
  iarr int[],
  m d1.mood,
  rg int4range,
  tr tstzrange,
  p d1.pair,
  u uuid,
  ip inet,
  bits bit varying(8),
  money_ money
);
insert into d1.ref values
  (1, 1234.5600, 1.5, 2.25, true, 'plain', 'v', 'ab', '2025-01-02 03:04:05.123456', '2025-01-02 03:04:05+02', '2025-12-31', '23:59:59.5', '1 year 2 mons 3 days 04:05:06', '\xdeadbeef', '{"a": [1, 2, {"b": null}], "c": "x\"y"}', '{"keep": "spacing"}', '{"a","b c",NULL,"q\"uote","comma,inside"}', '{1,NULL,3}', 'it''s great', '[1,10)', '["2025-01-01 00:00:00+00","2025-02-01 00:00:00+00")', '(1,"two words")', 'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', '192.168.1.0/24', B'1011', 12.34),
  (2, 'NaN', 'Infinity', '-Infinity', false, '', '', '', '-infinity', 'infinity', '2000-02-29', '00:00', '-1 days', '\x', '[]', '[null]', '{}', '{}', 'sad', 'empty', 'empty', '(,)', '00000000-0000-0000-0000-000000000000', '::1', B'', 0),
  (3, null, 'NaN', 0.1, null, E'line1\nline2\ttab \\ backslash', 'ünïcödé ✓', null, null, null, null, null, null, null, null, null, null, null, null, null, null, null, null, null, null, null),
  (4, -0.0001, 1e-300, 3.4e38, true, 'yes', 'no', 'x', '1999-12-31 23:59:59', '1999-12-31 23:59:59-12', '0001-01-01', '12:00', '0', '\x00ff', '"just a string"', '123', '{""}', '{-1}', 'ok', '[-5,5]', '(,)', '(0,)', 'ffffffff-ffff-ffff-ffff-ffffffffffff', '10.0.0.1', B'11111111', -5);
