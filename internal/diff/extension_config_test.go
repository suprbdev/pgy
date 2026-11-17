package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestPruneKeepsRulesOfAttributeOnlyAndExtensionViews(t *testing.T) {
	live := memberLive()
	live.ViewTables = map[string]*LiveTable{}
	for _, v := range []string{"public.geometry_columns", "public.mine"} {
		live.Views[v] = true
		vt := newLiveTable()
		vt.Rules = map[string]*LiveRule{"keep_insert": {Event: "insert", Instead: true}}
		vt.Triggers = map[string]string{"keep_trigger": ""}
		live.ViewTables[v] = vt
	}
	live.ExtensionMembers["rel:public.geometry_columns"] = true
	d := &schema.Database{Views: map[string]*schema.View{
		"public.geometry_columns": {Schema: "public", Name: "geometry_columns", Query: "select 1", Grants: map[string][]string{"public": {"select"}}},
		"public.mine":             {Schema: "public", Name: "mine", Grants: map[string][]string{"public": {"select"}}},
	}}
	live.TableGrants["public.geometry_columns"] = map[string]map[string]bool{"public": {"select": true}}
	live.TableGrants["public.mine"] = map[string]map[string]bool{"public": {"select": true}}
	early, late, _ := planPrune(live, d, "", nil)
	if len(early)+len(late) != 0 {
		t.Errorf("rules and triggers of an extension's view or a view declared without a query are never pruned, got %v %v", early, late)
	}

	d.Views["public.mine"].Query = "select 1"
	early, _, _ = planPrune(live, d, "", nil)
	if want := []string{`drop trigger "keep_trigger" on "public"."mine";`, `drop rule "keep_insert" on "public"."mine";`}; !reflect.DeepEqual(early, want) {
		t.Errorf("a view pgy defines still has its undeclared rules and triggers pruned, got %v", early)
	}
}

func TestPruneKeepsObjectsOfExtensionTables(t *testing.T) {
	live := memberLive()
	addMemberTable(live, "public.spatial_ref_sys", "postgis")
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.spatial_ref_sys": {Name: "public.spatial_ref_sys", ColumnsSet: true, Columns: map[string]*schema.Column{"srid": {Type: "integer"}}},
	}}
	if early, late, _ := planPrune(live, d, "", nil); len(early)+len(late) != 0 {
		t.Errorf("an extension's table keeps its constraints, indexes and rules, got %v %v", early, late)
	}
}

func TestExtensionConfigTableData(t *testing.T) {
	live := memberLive()
	addMemberTable(live, "public.spatial_ref_sys", "postgis")
	live.ExtensionConfig = map[string]string{"public.spatial_ref_sys": "WHERE NOT (srid BETWEEN 2000 AND 6999)"}
	live.TableData = map[string]*LiveTableData{"public.spatial_ref_sys": {
		Missing: []int{0},
		Changed: map[int][]string{},
		Extra:   []map[string]string{{"srid": "990002"}},
	}}
	d := &schema.Database{Tables: map[string]*schema.Table{
		"public.spatial_ref_sys": {Name: "public.spatial_ref_sys", Data: &schema.TableData{Key: []string{"srid"}, Rows: []map[string]any{{"srid": 990001, "auth_name": "local"}}}},
	}}
	sql, warns := planSQL(t, live, d, Options{Unsafe: true, Prune: true})
	wantSQL(t, sql,
		`insert into "public"."spatial_ref_sys" ("auth_name", "srid") values ('local', 990001);`,
		`delete from only "public"."spatial_ref_sys" where "srid" = '990002';`,
	)
	if len(warns) > 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestExtensionConfigTableDataPending(t *testing.T) {
	live := memberLive()
	d := &schema.Database{
		Extensions: []*schema.Extension{{Name: "postgis_topology"}},
		Tables: map[string]*schema.Table{
			"topology.layer": {Name: "topology.layer", DependsOn: []string{"table topology.topology"},
				Data: &schema.TableData{Key: []string{"topology_id", "layer_id"}, Rows: []map[string]any{{"topology_id": 1, "layer_id": 1}}}},
			"topology.topology": {Name: "topology.topology", Data: &schema.TableData{Key: []string{"id"}, Rows: []map[string]any{{"id": 1, "name": "city"}}}},
		},
	}
	sql, _ := planSQL(t, live, d, Options{})
	if strings.Contains(sql, "create table") {
		t.Errorf("tables declared without columns are never created:\n%s", sql)
	}
	topo, layer := strings.Index(sql, `insert into "topology"."topology"`), strings.Index(sql, `insert into "topology"."layer"`)
	if topo < 0 || layer < 0 || topo > layer || strings.Index(sql, "create extension") > topo {
		t.Errorf("the rows follow the extension and the tables a table without columns depends on:\n%s", sql)
	}
}
