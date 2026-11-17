package schema

import (
	"strings"
	"testing"
)

func TestForeignTableAndServerParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
servers:
  warehouse:
    wrapper: postgres_fdw
    version: 2
    options: {host: wh.internal, port: 5432}
    owner: admin
    comment: analytics
schema reporting:
  foreign table orders:
    server: warehouse
    options: {schema_name: public, table_name: orders}
    columns:
      id: {type: bigint}
      total: {type: "numeric(12,2)", nullable: true, options: {column_name: amount}}
    comment: remote orders
  table local:
    columns: {id: {type: int}}
`))
	if err != nil {
		t.Fatal(err)
	}
	s := d.Servers["warehouse"]
	if s == nil || s.Wrapper != "postgres_fdw" || s.Version != "2" || s.Options["port"] != "5432" || s.Owner != "admin" || s.Comment != "analytics" {
		t.Errorf("server = %+v", s)
	}
	ft := d.Tables["reporting.orders"]
	if ft == nil || ft.Foreign == nil || ft.Foreign.Server != "warehouse" || ft.Foreign.Options["table_name"] != "orders" || ft.Comment != "remote orders" {
		t.Fatalf("foreign table = %+v", ft)
	}
	if ft.Columns["total"].FDWOptions["column_name"] != "amount" || ft.Columns["id"].FDWOptions != nil {
		t.Errorf("column options = %v / %v", ft.Columns["total"].FDWOptions, ft.Columns["id"].FDWOptions)
	}
	if order := ft.ColumnOrder; len(order) != 2 || order[0] != "id" {
		t.Errorf("column order = %v", order)
	}
	if d.Tables["reporting.local"].Foreign != nil {
		t.Error("a plain table is not foreign")
	}
	if d.Servers == nil || (&Database{}).Servers != nil {
		t.Error("servers block presence must be tracked")
	}
}

func TestForeignScalarsKeepSourceText(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
servers:
  wh:
    wrapper: postgres_fdw
    version: 15.10
    options: {port: 05432, fetch_size: 1_000, connect_timeout: 0x10, keep_connections: on, sslmode: "require", application_name: ~}
  plain:
    wrapper: file_fdw
    version: "9.60"
schema rep:
  foreign table t:
    server: wh
    options: {fetch_size: 2_000, table_name: 0010}
    columns:
      a: {type: int, options: {column_name: 007}}
      b: {type: text, options: {column_name: b}}
`))
	if err != nil {
		t.Fatal(err)
	}
	s := d.Servers["wh"]
	if s.Version != "15.10" {
		t.Errorf("version = %q, want the text as written", s.Version)
	}
	want := map[string]string{"port": "05432", "fetch_size": "1_000", "connect_timeout": "0x10", "keep_connections": "on", "sslmode": "require", "application_name": ""}
	for k, v := range want {
		if s.Options[k] != v {
			t.Errorf("server option %s = %q, want %q", k, s.Options[k], v)
		}
	}
	if v := d.Servers["plain"].Version; v != "9.60" {
		t.Errorf("quoted version = %q", v)
	}
	ft := d.Tables["rep.t"]
	if ft.Foreign.Options["fetch_size"] != "2_000" || ft.Foreign.Options["table_name"] != "0010" {
		t.Errorf("table options = %v", ft.Foreign.Options)
	}
	if ft.Columns["a"].FDWOptions["column_name"] != "007" || ft.Columns["b"].FDWOptions["column_name"] != "b" {
		t.Errorf("column options = %v / %v", ft.Columns["a"].FDWOptions, ft.Columns["b"].FDWOptions)
	}
}

func TestServerRenamedFromAndForeignInheritsParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
servers:
  warehouse: {wrapper: postgres_fdw, renamedFrom: " wh "}
schema rep:
  table parent:
    columns: {id: {type: int}}
  foreign table child:
    server: warehouse
    inherits: [parent]
    columns:
      id: {options: {column_name: ident}}
      extra: {type: int, generated: id * 2, storage: plain}
`))
	if err != nil {
		t.Fatal(err)
	}
	if s := d.Servers["warehouse"]; s.RenamedFrom != "wh" {
		t.Errorf("renamedFrom = %q", s.RenamedFrom)
	}
	ft := d.Tables["rep.child"]
	if ft.Foreign == nil || len(ft.Inherits) != 1 || ft.Inherits[0] != "rep.parent" {
		t.Fatalf("foreign table = %+v", ft)
	}
	if c := ft.Columns["id"]; c.Type != "" || c.FDWOptions["column_name"] != "ident" {
		t.Errorf("inherited column = %+v", c)
	}
	if c := ft.Columns["extra"]; c.Generated != "id * 2" || c.Storage != "plain" {
		t.Errorf("own column = %+v", c)
	}
}

func TestPublicationGeneratedColumnsParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
publications:
  cdc: {allTables: true, generatedColumns: STORED}
`))
	if err != nil {
		t.Fatal(err)
	}
	if g := d.Publications["cdc"].GeneratedColumns; g != "stored" {
		t.Errorf("generatedColumns = %q", g)
	}
}

func TestPrimaryKeyNameForceRLSAndDomainCheckCommentsParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
schema app:
  domain pct:
    type: int
    checks:
      pct_min: {check: VALUE >= 0, comment: not negative}
      pct_max: VALUE <= 100
  table t:
    columns: {id: {type: int}}
    primaryKey:
      t_pk: {columns: [id]}
    forceRowLevelSecurity: true
`))
	if err != nil {
		t.Fatal(err)
	}
	tb := d.Tables["app.t"]
	if tb.PrimaryKeyName != "t_pk" || len(tb.PrimaryKey) != 1 || tb.ForceRowLevelSecurity == nil || !*tb.ForceRowLevelSecurity {
		t.Errorf("table = %+v", tb)
	}
	dm := d.Domains["app.pct"]
	if dm.Checks["pct_min"] != "VALUE >= 0" || dm.Checks["pct_max"] != "VALUE <= 100" || dm.CheckComments["pct_min"] != "not negative" {
		t.Errorf("domain = %+v", dm)
	}
}

func TestGrantOptionParse(t *testing.T) {
	g := parseGrants(map[string]any{"r": []any{"SELECT", "insert  WITH grant option"}})
	if strings.Join(g["r"], ",") != "insert*,select" {
		t.Errorf("grants = %v", g)
	}
}
