package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func indexCommentTable(ix *schema.Index) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"app.t": {
		Name: "t", Columns: map[string]*schema.Column{"a": {Type: "int"}}, Indexes: []*schema.Index{ix},
	}}}
}

func TestIndexCommentFollowsConcurrentBuild(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	live.Schemas["app"] = true
	p := Plan(live, indexCommentTable(&schema.Index{Name: "t_a", Columns: []string{"a"}, Concurrently: true, Comment: "by a"}), false)
	if strings.Contains(RenderMain(p), "comment on index") {
		t.Errorf("the comment must not run before the concurrent build:\n%s", RenderMain(p))
	}
	conc := RenderConcurrent(p)
	build, note := strings.Index(conc, "create index concurrently"), strings.Index(conc, `comment on index "app"."t_a" is 'by a';`)
	if build < 0 || note < build {
		t.Errorf("want the comment after the concurrent build:\n%s", conc)
	}
}

func TestIndexCommentReappliedAfterRebuild(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"a": {Type: "integer"}})
	live.Schemas["app"] = true
	lt := live.Tables["app.t"]
	lt.Indexes["t_a"] = true
	lt.IndexDefs = map[string]*LiveIndex{}
	lt.IndexComments = map[string]string{}
	lt.IndexDefs["t_a"] = &LiveIndex{Method: "btree", Keys: []string{"a"}, Descending: []bool{false}, NullsFirst: []bool{false}}
	lt.IndexComments["t_a"] = "by a"
	ix := &schema.Index{Name: "t_a", Columns: []string{"a"}, Comment: "by a"}
	if sql := Render(Plan(live, indexCommentTable(ix), true)); sql != "" {
		t.Fatalf("unchanged index must not diff, got:\n%s", sql)
	}
	ix.Columns = []string{"a desc"}
	sql := Render(Plan(live, indexCommentTable(ix), true))
	if !strings.Contains(sql, `drop index "app"."t_a";`) || !strings.Contains(sql, `comment on index "app"."t_a" is 'by a';`) {
		t.Errorf("a rebuilt index must get its comment back, got:\n%s", sql)
	}
	ix.Comment = "by a, descending"
	if sql := Render(Plan(live, indexCommentTable(ix), false)); !strings.Contains(sql, `comment on index "app"."t_a" is 'by a, descending';`) || strings.Contains(sql, "drop index") {
		t.Errorf("without --unsafe only the comment changes, got:\n%s", sql)
	}
}

func TestMaterializedViewIndexCommentOnce(t *testing.T) {
	d := &schema.Database{Views: map[string]*schema.View{"app.mv": {
		Name: "mv", Materialized: true, Query: "select 1 as a",
		Indexes: []*schema.Index{{Name: "mv_a", Columns: []string{"a"}, Comment: "by a"}},
	}}}
	if n := strings.Count(Render(Plan(emptyLive(), d, false)), `comment on index "app"."mv_a"`); n != 1 {
		t.Errorf("want one index comment, got %d", n)
	}
}
