package schema

import (
	"reflect"
	"testing"
)

const extensionObjectsYAML = `
extensions:
  - {name: postgis, schema: public}
schema public:
  table spatial_ref_sys:
    grants: {app: [select, insert], public: []}
    comment: spatial reference systems
    owner: gis_owner
    rowLevelSecurity: true
    policies:
      own_rows: {using: "auth_name = current_user"}
    data:
      key: [srid]
      rows:
        - {srid: 990001, auth_name: local}
  table zero_columns:
    columns: {}
  table child:
    inherits: [public.parent]
  sequence topology_seq:
    grants: {app: [usage]}
  sequence counter:
    as: integer
  aggregate st_union(geometry):
    revokePublic: true
    grants: {app: [execute]}
    comment: union of geometries
  aggregate percentile(float8 order by anyelement):
    grants: {app: [execute]}
  aggregate total(integer):
    sfunc: int4pl
    stype: integer
    grants: {app: [execute]}
`

func TestTableAttributeOnlyParsed(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(extensionObjectsYAML))
	if err != nil {
		t.Fatal(err)
	}
	tb := db.Tables["public.spatial_ref_sys"]
	if tb == nil || !tb.AttributeOnly() || tb.ColumnsSet || tb.Comment != "spatial reference systems" || tb.Owner != "gis_owner" ||
		!reflect.DeepEqual(tb.Grants, map[string][]string{"app": {"insert", "select"}, "public": {}}) ||
		tb.RowLevelSecurity == nil || len(tb.Policies) != 1 || tb.Data == nil || !reflect.DeepEqual(tb.Data.Key, []string{"srid"}) {
		t.Errorf("table without columns: %+v", tb)
	}
	if tb := db.Tables["public.zero_columns"]; tb == nil || tb.AttributeOnly() || !tb.ColumnsSet {
		t.Errorf("a table with an empty columns key is a definition: %+v", tb)
	}
	if tb := db.Tables["public.child"]; tb == nil || tb.AttributeOnly() {
		t.Errorf("a table that only inherits is a definition: %+v", tb)
	}
	if sq := db.Sequences["public.topology_seq"]; sq == nil || sq.DeclaresOptions() || sq.Grants == nil {
		t.Errorf("sequence without options: %+v", sq)
	}
	if sq := db.Sequences["public.counter"]; sq == nil || !sq.DeclaresOptions() {
		t.Errorf("sequence with options: %+v", sq)
	}
	ag := db.Aggregates["public.st_union(geometry)"]
	if ag == nil || !ag.AttributeOnly() || !ag.RevokePublic || ag.Comment != "union of geometries" || !reflect.DeepEqual(ag.Grants, map[string][]string{"app": {"execute"}}) {
		t.Errorf("aggregate without sfunc and stype: %+v", ag)
	}
	if ag := db.Aggregates["public.percentile(float8 order by anyelement)"]; ag == nil || !ag.AttributeOnly() || ag.Grants == nil {
		t.Errorf("ordered-set aggregate without sfunc and stype: %+v", ag)
	}
	if ag := db.Aggregates["public.total(integer)"]; ag == nil || ag.AttributeOnly() || !reflect.DeepEqual(ag.Grants, map[string][]string{"app": {"execute"}}) {
		t.Errorf("aggregate with grants: %+v", ag)
	}
}

func TestDictionaryAttributeOnlyParsed(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
schema public:
  text search dictionary intdict:
    options: {maxlen: 4, rejectlong: true}
    owner: dict_owner
    comment: integers
  text search dictionary english_nostop:
    template: snowball
    options: {language: english}
`))
	if err != nil {
		t.Fatal(err)
	}
	td := db.TSDictionaries["public.intdict"]
	if td == nil || !td.AttributeOnly() || td.Owner != "dict_owner" || td.Comment != "integers" || !reflect.DeepEqual(td.Options, map[string]string{"maxlen": "4", "rejectlong": "true"}) {
		t.Errorf("dictionary without a template: %+v", td)
	}
	if td := db.TSDictionaries["public.english_nostop"]; td == nil || td.AttributeOnly() {
		t.Errorf("dictionary with a template: %+v", td)
	}
}

func TestExtensionCommentNullParsed(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
extensions:
  - {name: hstore, comment: null}
  - {name: citext, comment: ""}
  - {name: pgcrypto}
  - {name: pg_trgm, comment: trigrams}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]any{}
	for _, ext := range db.Extensions {
		got[ext.Name] = [2]any{ext.Comment, ext.NoComment}
	}
	want := map[string][2]any{"hstore": {"", true}, "citext": {"", false}, "pgcrypto": {"", false}, "pg_trgm": {"trigrams", false}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extension comments %v, want %v", got, want)
	}
}

func TestTableAttributeOnlyMergedWithColumns(t *testing.T) {
	a, err := parseFlexibleDatabase([]byte("schema public:\n  table t:\n    columns: {id: {type: int}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseFlexibleDatabase([]byte("schema public:\n  table t:\n    grants: {app: [select]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Tables["public.t"].AttributeOnly() {
		t.Fatal("a file declaring only grants is attribute-only on its own")
	}
	if m := Merge([]*Database{a, b}); m.Tables["public.t"].AttributeOnly() || m.Tables["public.t"].Grants == nil {
		t.Errorf("merged with a file that declares columns, the table is a definition: %+v", m.Tables["public.t"])
	}
	if m := Merge([]*Database{b, a}); m.Tables["public.t"].AttributeOnly() {
		t.Errorf("the order of the files does not matter: %+v", m.Tables["public.t"])
	}
}

func TestTableDefinitionKeys(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte("schema public:\n  table t:\n    indexes: {t_idx: {columns: [a]}}\n    unlogged: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	tb := db.Tables["public.t"]
	if tb.AttributeOnly() || !reflect.DeepEqual(tb.DefinitionKeys(), []string{"indexes", "unlogged"}) {
		t.Errorf("keys that need columns: %v", tb.DefinitionKeys())
	}
}
