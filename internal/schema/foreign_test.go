package schema

import "testing"

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
