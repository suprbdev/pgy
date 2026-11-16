package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationProbeViews(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_view_probe")
	if _, err := db.Exec(ctx, `
		create schema app;
		create table app.t (id int primary key, a int, b text);
		create view app.star as select * from app.t;
		create view app.cast as select id, b::text as b from app.t where a > 0;
		create view app.uses as select id from app.star;
		create materialized view app.mv as select count(*) as n from app.t;`); err != nil {
		t.Fatal(err)
	}
	desired := &schema.Database{Views: map[string]*schema.View{
		"app.star": {Name: "star", Query: "SELECT id, a, b FROM app.t"},
		"app.cast": {Name: "cast", Query: "select id, b as b from app.t where (a > 0);"},
		"app.uses": {Name: "uses", Query: "select id, 1 as one from app.star"},
		"app.mv":   {Name: "mv", Materialized: true, Query: "select count(*) as n from app.t where a > 1"},
		"app.new":  {Name: "new", Query: "select 1"},
	}}
	live := introspectAll(t, db)
	if err := diff.ProbeViews(ctx, db, live, desired); err != nil {
		t.Fatal(err)
	}
	for k, changed := range map[string]bool{"app.star": false, "app.cast": false, "app.uses": true, "app.mv": true} {
		p := live.ViewProbes[k]
		if p == nil {
			t.Fatalf("%s was not probed", k)
		}
		if p.Changed() != changed {
			t.Errorf("%s: changed = %v, want %v (declared %q, live %q, err %q)", k, p.Changed(), changed, p.DesiredDef, p.LiveDef, p.Err)
		}
	}
	if p := live.ViewProbes["app.uses"]; !p.ColumnsCompatible() || strings.Join(p.DesiredCols, ",") != "id integer,one integer" {
		t.Errorf("adding a column at the end keeps the view replaceable: %+v", p)
	}
	if live.ViewProbes["app.new"] != nil {
		t.Error("a view that doesn't exist yet is not probed")
	}
	if deps := live.ViewDependents["app.star"]; len(deps) != 1 || deps[0] != "app.uses" {
		t.Errorf("app.uses selects from app.star: %v", live.ViewDependents)
	}

	cfg := db.Config().Copy()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	ro, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	roLive := introspectAll(t, ro)
	if err := diff.ProbeViews(ctx, ro, roLive, desired); err == nil || roLive.ViewProbes != nil {
		t.Errorf("a read-only session can't probe, and views stay create-only: err %v, probes %v", err, roLive.ViewProbes)
	}
}
