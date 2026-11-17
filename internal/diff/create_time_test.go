package diff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func TestCreateTimeOnlyParam(t *testing.T) {
	for k, want := range map[string]bool{
		"tsdb.hypertable":          true,
		"timescaledb.continuous":   true,
		"toast.autovacuum_enabled": false,
		"TOAST.fillfactor":         false,
		"fillfactor":               false,
	} {
		if got := createTimeOnlyParam(k); got != want {
			t.Errorf("createTimeOnlyParam(%q) = %v, want %v", k, got, want)
		}
	}
}

func createTimeDesired() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"app.m": {Name: "m",
		Columns: map[string]*schema.Column{"at": {Type: "timestamptz"}},
		With:    map[string]string{"tsdb.hypertable": "true", "tsdb.partition_column": "'at'", "fillfactor": "70"},
	}}}
}

func TestCreateTimeOnlyParamsCreate(t *testing.T) {
	sql := Render(Plan(emptyLive(), createTimeDesired(), false))
	if !strings.Contains(sql, `with (fillfactor = 70, tsdb.hypertable = true, tsdb.partition_column = 'at');`) {
		t.Errorf("CREATE TABLE passes every parameter:\n%s", sql)
	}
}

func TestCreateTimeOnlyParamsInYAMLOrderQuoted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(`
schema app:
  table m:
    columns: {at: {type: timestamptz}}
    with:
      tsdb.hypertable: true
      tsdb.partition_column: at
      tsdb.chunk_interval: 1 day
      fillfactor: 70
`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	sql := Render(Plan(emptyLive(), d, false))
	if !strings.Contains(sql, `with (tsdb.hypertable = true, tsdb.partition_column = at, tsdb.chunk_interval = '1 day', fillfactor = 70);`) {
		t.Errorf("CREATE TABLE passes the parameters in YAML order, quoting values that need it:\n%s", sql)
	}
	live := liveWithTable("app.m", map[string]*LiveColumn{"at": {Type: "timestamp with time zone"}})
	live.Schemas["app"] = true
	live.Tables["app.m"].Options = map[string]string{"fillfactor": "70"}
	if sql := Render(Plan(live, d, true)); sql != "" {
		t.Errorf("the namespaced parameters are create-time only, got:\n%s", sql)
	}
}

func TestCreateTimeOnlyParamsNotReemitted(t *testing.T) {
	live := liveWithTable("app.m", map[string]*LiveColumn{"at": {Type: "timestamp with time zone"}})
	live.Schemas["app"] = true
	live.Tables["app.m"].Options = map[string]string{"fillfactor": "70"}
	if sql := Render(Plan(live, createTimeDesired(), true)); sql != "" {
		t.Errorf("namespaced parameters the table doesn't show are create-time only, got:\n%s", sql)
	}
	live.Tables["app.m"].Options = map[string]string{"fillfactor": "80"}
	if sql := Render(Plan(live, createTimeDesired(), true)); sql != `alter table "app"."m" set (fillfactor = 70);`+"\n" {
		t.Errorf("other parameters are still compared, got:\n%s", sql)
	}
	live.Tables["app.m"].Options = map[string]string{"fillfactor": "70", "tsdb.hypertable": "false"}
	if sql := Render(Plan(live, createTimeDesired(), true)); sql != `alter table "app"."m" set (tsdb.hypertable = true);`+"\n" {
		t.Errorf("a namespaced parameter the table shows is compared, got:\n%s", sql)
	}
}

func TestMaterializedViewCreatedAsViewByExtension(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Views["app.hourly"] = true
	d := &schema.Database{Views: map[string]*schema.View{"app.hourly": {Name: "hourly", Materialized: true, Query: "select 1",
		With: map[string]string{"timescaledb.continuous": "true"}, Comment: "hourly", Indexes: []*schema.Index{{Name: "hourly_x", Columns: []string{"x"}}}}}}
	if sql := Render(Plan(live, d, true)); sql != `comment on view "app"."hourly" is 'hourly';`+"\n" {
		t.Errorf("an extension made the materialized view a view at creation, so only its attributes are managed, got:\n%s", sql)
	}
	if !d.Views["app.hourly"].Materialized {
		t.Error("the desired schema must not be modified")
	}
	d.Views["app.hourly"].With = map[string]string{"fillfactor": "70"}
	if sql := Render(Plan(live, d, true)); !strings.Contains(sql, "create materialized view") {
		t.Errorf("without a create-time-only parameter the view is not taken for the materialized view, got:\n%s", sql)
	}
}
