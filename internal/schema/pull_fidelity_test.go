package schema

import "testing"

func TestDomainCheckNotValidParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
schema app:
  domain pct:
    type: numeric(5,2)
    checks:
      pct_min: VALUE >= 0
      pct_not_99: {check: VALUE <> 99, notValid: true}
      pct_max: {check: VALUE <= 100, notValid: false, comment: upper bound}
`))
	if err != nil {
		t.Fatal(err)
	}
	dm := d.Domains["app.pct"]
	if dm.Checks["pct_not_99"] != "VALUE <> 99" || dm.Checks["pct_max"] != "VALUE <= 100" {
		t.Errorf("unexpected checks: %v", dm.Checks)
	}
	if !dm.CheckNotValid["pct_not_99"] || dm.CheckNotValid["pct_max"] || dm.CheckNotValid["pct_min"] {
		t.Errorf("only pct_not_99 is notValid: %v", dm.CheckNotValid)
	}
}

func TestDefaultPrivilegesLargeObjectsParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
defaultPrivileges:
  - for: app_owner
    largeObjects: {app_read: [select], app_rw: [select, update]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.DefaultPrivileges) != 1 {
		t.Fatalf("unexpected entries: %v", d.DefaultPrivileges)
	}
	g := d.DefaultPrivileges[0].Grants["largeObjects"]
	if len(g["app_read"]) != 1 || len(g["app_rw"]) != 2 {
		t.Errorf("unexpected largeObjects grants: %v", g)
	}
}

func TestMaterializedViewColumnsParse(t *testing.T) {
	d, err := parseFlexibleDatabase([]byte(`
schema app:
  materialized view mv:
    query: select 1 as a, 'x'::text as b
    columns:
      a: {statisticsTarget: 500, nDistinct: 10}
      b: {compression: lz4, storage: main}
`))
	if err != nil {
		t.Fatal(err)
	}
	vw := d.Views["app.mv"]
	if vw == nil || len(vw.Columns) != 2 {
		t.Fatalf("unexpected view: %+v", vw)
	}
	a, b := vw.Columns["a"], vw.Columns["b"]
	if a.StatisticsTarget == nil || *a.StatisticsTarget != 500 || a.NDistinct == nil || *a.NDistinct != 10 {
		t.Errorf("unexpected column a: %+v", a)
	}
	if b.Compression != "lz4" || b.Storage != "main" {
		t.Errorf("unexpected column b: %+v", b)
	}
}
