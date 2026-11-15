package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func genColsDB(g string) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, Publications: map[string]*schema.Publication{
		"cdc": {Name: "cdc", AllTables: true, GeneratedColumns: g},
	}}
}

func TestPublicationGeneratedColumnsCreate(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	if sql := Render(Plan(live, genColsDB("stored"), false)); !strings.Contains(sql, `create publication "cdc" for all tables with (publish_generated_columns = stored);`) {
		t.Errorf("expected publish_generated_columns on create:\n%s", sql)
	}
	if sql := Render(Plan(live, genColsDB("none"), false)); strings.Contains(sql, "publish_generated_columns") {
		t.Errorf("none is the default for a new publication:\n%s", sql)
	}
}

func TestPublicationGeneratedColumnsReconciled(t *testing.T) {
	live := emptyLive()
	live.ServerVersion = 180000
	live.Publications = map[string]*LivePublication{}
	live.Publications["cdc"] = &LivePublication{AllTables: true, Publish: []string{"insert", "update", "delete", "truncate"}, GeneratedColumns: "none", Tables: map[string]LivePublicationTable{}}
	if sql := Render(Plan(live, genColsDB("stored"), false)); !strings.Contains(sql, `alter publication "cdc" set (publish_generated_columns = stored);`) {
		t.Errorf("expected SET (publish_generated_columns = stored):\n%s", sql)
	}
	if sql := Render(Plan(live, genColsDB("none"), false)); sql != "" {
		t.Errorf("matching option must not diff:\n%s", sql)
	}
	live.Publications["cdc"].GeneratedColumns = ""
	live.ServerVersion = 170000
	if sql := Render(Plan(live, genColsDB("none"), false)); sql != "" {
		t.Errorf("before PostgreSQL 18 every publication behaves as none:\n%s", sql)
	}
	p := Plan(live, genColsDB("stored"), false)
	if sql := Render(p); sql != "" {
		t.Errorf("before PostgreSQL 18 stored cannot be set:\n%s", sql)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "needs PostgreSQL 18") {
		t.Errorf("expected a version warning, got %+v", p.Warnings)
	}
}
