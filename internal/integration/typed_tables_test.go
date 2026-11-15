package integration

import (
	"context"
	"testing"
)

func TestIntegrationTypedTables(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	before := `
schema SCH:
  type addr:
    type: composite
    attributes: {street: {type: text}, city: {type: text}}
  table addresses:
    of: SCH.addr
    columns:
      city: {default: "'Paris'", nullable: false, comment: city name}
    primaryKey: [street]
`
	convergeYAML(t, pool, sch, before)
	after := `
schema SCH:
  type addr:
    type: composite
    attributes: {street: {type: text}, city: {type: text}, zip: {type: int}}
  table addresses:
    of: SCH.addr
    columns:
      city: {default: "'Paris'", nullable: false, comment: city name}
      zip: {statisticsTarget: 50}
    primaryKey: [street]
`
	if sql := convergeYAML(t, pool, sch, after); !contains(sql, "add attribute") || !contains(sql, "cascade") {
		t.Errorf("want the attribute added with CASCADE, got:\n%s", sql)
	}
	if _, err := pool.Exec(ctx, `alter table `+sch+`.addresses not of`); err != nil {
		t.Fatal(err)
	}
	if sql := convergeYAML(t, pool, sch, after); !contains(sql, "of \""+sch+"\".\"addr\"") {
		t.Errorf("want the table linked to its type again, got:\n%s", sql)
	}
	var ofType string
	if err := pool.QueryRow(ctx, `select reloftype::regtype::text from pg_class where oid = $1::regclass`, sch+".addresses").Scan(&ofType); err != nil {
		t.Fatal(err)
	}
	if ofType != sch+".addr" {
		t.Errorf("addresses is of %q", ofType)
	}
}
