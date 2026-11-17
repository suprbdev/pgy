create schema pidx;
create table pidx.ev (id int, region text, yr int, note text) partition by list (region);
create table pidx.ev_eu partition of pidx.ev for values in ('eu') partition by range (yr);
create table pidx.ev_eu_2024 partition of pidx.ev_eu for values from (2024) to (2025);
create table pidx.ev_eu_2025 partition of pidx.ev_eu for values from (2025) to (2026);
create table pidx.ev_us partition of pidx.ev for values in ('us');
create table pidx.ev_other partition of pidx.ev default;

create index ev_yr on only pidx.ev (yr);
create index ev_eu_yr_named on only pidx.ev_eu (yr);
create index ev_eu_2024_yr_named on pidx.ev_eu_2024 (yr);
alter index pidx.ev_eu_yr_named attach partition pidx.ev_eu_2024_yr_named;
create index ev_eu_2025_yr_named on pidx.ev_eu_2025 (yr);
alter index pidx.ev_eu_yr_named attach partition pidx.ev_eu_2025_yr_named;
alter index pidx.ev_yr attach partition pidx.ev_eu_yr_named;
create index ev_us_yr_named on pidx.ev_us (yr);
alter index pidx.ev_yr attach partition pidx.ev_us_yr_named;
create index ev_other_yr_named on pidx.ev_other (yr);
alter index pidx.ev_yr attach partition pidx.ev_other_yr_named;

create index ev_id on pidx.ev (id);
create index ev_id_hash on pidx.ev using hash (id);

create index ev_us_lower_note on pidx.ev_us (lower(note));
create index ev_lower_note on pidx.ev (lower(note));
comment on index pidx.ev_other_lower_idx is 'automatic name, own comment';

create index ev_region on only pidx.ev (region);
create index ev_eu_ev_region on only pidx.ev_eu (region);
create index ev_eu_2024_ev_eu_ev_region on pidx.ev_eu_2024 (region);
create index ev_eu_2025_ev_eu_ev_region on pidx.ev_eu_2025 (region);
alter index pidx.ev_eu_ev_region attach partition pidx.ev_eu_2024_ev_eu_ev_region;
alter index pidx.ev_eu_ev_region attach partition pidx.ev_eu_2025_ev_eu_ev_region;
create index ev_us_ev_region on pidx.ev_us (region);
create index ev_other_ev_region on pidx.ev_other (region);
alter index pidx.ev_region attach partition pidx.ev_eu_ev_region;
alter index pidx.ev_region attach partition pidx.ev_us_ev_region;
alter index pidx.ev_region attach partition pidx.ev_other_ev_region;
