package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestTablespaceCreate(t *testing.T) {
	d := &schema.Database{
		Tables: map[string]*schema.Table{"app.t": {Name: "t", Tablespace: "fast",
			Columns:              map[string]*schema.Column{"id": {Type: "int"}, "v": {Type: "int"}, "r": {Type: "int4range"}},
			PrimaryKey:           []string{"id"},
			PrimaryKeyTablespace: "fast",
			Indexes:              []*schema.Index{{Name: "t_v", Columns: []string{"v"}, Tablespace: "archive", Where: "v > 0"}},
			Constraints: []*schema.Constraint{
				{Name: "t_v_uq", Type: "unique", Columns: []string{"v"}, Tablespace: "fast"},
				{Name: "t_r_excl", Type: "exclude", Expression: "using gist (r with &&) where (v > 0)", Tablespace: "fast"},
			}}},
		Views: map[string]*schema.View{"app.mv": {Name: "mv", Materialized: true, Query: "select 1", Tablespace: "archive"}},
	}
	sql := Render(Plan(emptyLive(), d, false))
	for _, want := range []string{
		`create table if not exists "app"."t" (` + "",
		`) tablespace "fast";`,
		`add primary key ("id") using index tablespace "fast";`,
		`create index if not exists "t_v" on "app"."t"("v") tablespace "archive" where (v > 0);`,
		`unique ("v") using index tablespace "fast";`,
		`exclude using gist (r with &&) using index tablespace "fast" where (v > 0);`,
		`create materialized view if not exists "app"."mv" tablespace "archive" as select 1`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestTablespaceMoves(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"id": {Type: "integer"}, "v": {Type: "integer"}})
	live.Schemas["app"] = true
	live.DefaultTablespace = "pg_default"
	lt := live.Tables["app.t"]
	lt.Indexes = map[string]bool{"t_v": true, "t_c": true}
	lt.IndexDefs = map[string]*LiveIndex{"t_v": {Method: "btree", Keys: []string{"v"}, Descending: []bool{false}, NullsFirst: []bool{false}}, "t_c": {Method: "btree", Keys: []string{"v"}, Descending: []bool{false}, NullsFirst: []bool{false}, Tablespace: "fast"}}
	d := &schema.Database{Tables: map[string]*schema.Table{"app.t": {Name: "t", Tablespace: "fast",
		Columns: map[string]*schema.Column{"id": {Type: "int"}, "v": {Type: "int"}},
		Indexes: []*schema.Index{{Name: "t_v", Columns: []string{"v"}, Tablespace: "fast"}, {Name: "t_c", Columns: []string{"v"}, Tablespace: "archive", Concurrently: true}},
	}}}
	p := Plan(live, d, false)
	sql := Render(p)
	if !strings.Contains(sql, `alter table "app"."t" set tablespace "fast";`) || !strings.Contains(sql, `alter index "app"."t_v" set tablespace "fast";`) {
		t.Errorf("want the table and index moved:\n%s", sql)
	}
	if got := strings.Join(p.Concurrent, "\n"); !strings.Contains(got, `reindex (tablespace "archive") index concurrently "app"."t_c";`) {
		t.Errorf("a concurrently index moves with REINDEX CONCURRENTLY, got %v", p.Concurrent)
	}
	locks := 0
	for _, w := range p.Warnings {
		if w.Kind == "lock" && strings.Contains(w.Message, "another tablespace") {
			locks++
		}
	}
	if locks != 2 {
		t.Errorf("want lock warnings for the table and t_v: %+v", p.Warnings)
	}
	lt.Tablespace = "fast"
	lt.IndexDefs["t_v"].Tablespace = "fast"
	lt.IndexDefs["t_c"].Tablespace = "archive"
	if p := Plan(live, d, false); Render(p) != "" || len(p.Concurrent) != 0 {
		t.Errorf("matching tablespaces must be left alone, got:\n%s%v", Render(p), p.Concurrent)
	}
	d.Tables["app.t"].Tablespace = "pg_default"
	lt.Tablespace = ""
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("pg_default matches a relation in the database's default tablespace, got:\n%s", sql)
	}
	d.Tables["app.t"].Tablespace = ""
	lt.Tablespace = "fast"
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("an undeclared tablespace is unmanaged, got:\n%s", sql)
	}
}
