create extension if not exists pg_trgm;
create extension if not exists intarray;
create extension if not exists bloom;
create schema ocp;
create table ocp.doc (
    id int primary key,
    name text not null,
    last text not null,
    tags int[] not null,
    tsv tsvector,
    n int not null,
    words text[] not null
);
create index doc_name_sig on ocp.doc using gist (name gist_trgm_ops (siglen = 32));
create index doc_names on ocp.doc using gist (name gist_trgm_ops (siglen = 64), last gist_trgm_ops);
create index doc_tags_ranges on ocp.doc using gist (tags gist__int_ops (numranges = 100));
create index doc_tags_big on ocp.doc using gist (tags gist__intbig_ops (siglen = 32));
create index doc_tsv on ocp.doc using gist (tsv tsvector_ops (siglen = 100));
create index doc_n_multi on ocp.doc using brin (n int4_minmax_multi_ops (values_per_range = 16));
create index doc_n_bloom on ocp.doc using brin (n int4_bloom_ops (n_distinct_per_range = 100, false_positive_rate = 0.05));
create index doc_lower_sig on ocp.doc using gist (lower(name) gist_trgm_ops (siglen = 48)) where n > 0;
create index doc_bloom on ocp.doc using bloom (id, n) with (length = 80, col1 = 2, col2 = 4);
create index doc_bloom_default on ocp.doc using bloom (n);
create index doc_name_words on ocp.doc using gin (string_to_array(name, ' '));
create index doc_words_removed on ocp.doc using gin (array_remove(words, 'x'));
create index doc_words_c on ocp.doc using gin ((string_to_array(name, ' ')) collate "C");
create materialized view ocp.doc_summary as select id, name, tags from ocp.doc;
create index doc_summary_sig on ocp.doc_summary using gist (name gist_trgm_ops (siglen = 40));
create index doc_summary_tags on ocp.doc_summary using gist (tags gist__int_ops (numranges = 50));
