package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

// TestIntegrationExtensionNotRelocatable declares extensions that cannot be
// moved (xml2 is not relocatable, plpgsql lives in pg_catalog) in another
// schema, with a default that calls xml2 qualified with the new schema: pgy
// warns and plans nothing that needs the move.
func TestIntegrationExtensionNotRelocatable(t *testing.T) {
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_ext_unmovable")
	ctx := context.Background()
	if _, err := db.Exec(ctx, `create extension xml2 schema public`); err != nil {
		t.Skipf("xml2 is not available: %v", err)
	}
	if _, err := db.Exec(ctx, `
		create schema app;
		create table app.t (id int primary key, ok boolean default xml_valid('<a/>'));`); err != nil {
		t.Fatal(err)
	}
	y := `
extensions:
  - {name: xml2, schema: other}
  - {name: plpgsql, schema: public}
schema app:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      ok: {type: boolean, nullable: true, default: "other.xml_valid('<a/>')"}
`
	for _, opts := range []diff.Options{{}, {Unsafe: true, Prune: true}} {
		p := planExtensionsLive(t, db, y, opts)
		if sql := strings.TrimSpace(diff.Render(p)); sql != `create schema if not exists "other";` {
			t.Errorf("%+v: nothing that needs the move is planned, got:\n%s", opts, sql)
		}
		got := warningMessages(p)
		for _, want := range []string{
			"extension xml2 is not relocatable, so it stays in schema public with its objects instead of moving to schema other, and references to them qualified with other are planned as references to public",
			"extension plpgsql is not relocatable, so it stays in schema pg_catalog with its objects instead of moving to schema public",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%+v: want warning %q, got:\n%s", opts, want, got)
			}
		}
	}
}

// TestIntegrationExtensionFixedSchema declares plpgsql, whose control file
// fixes its schema to pg_catalog, in another schema: pgy warns before
// CREATE EXTENSION fails.
func TestIntegrationExtensionFixedSchema(t *testing.T) {
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_ext_fixed_schema")
	if _, err := db.Exec(context.Background(), `drop extension plpgsql`); err != nil {
		t.Fatal(err)
	}
	want := "extension plpgsql can only be installed in schema pg_catalog, which its control file names, so CREATE EXTENSION with schema public will fail; declare schema: pg_catalog, or leave schema out"
	if got := warningMessages(planExtensionsLive(t, db, "extensions:\n  - {name: plpgsql, schema: public}\n", diff.Options{})); !strings.Contains(got, want) {
		t.Errorf("want warning %q, got:\n%s", want, got)
	}
	for _, y := range []string{"extensions:\n  - {name: plpgsql, schema: pg_catalog}\n", "extensions:\n  - {name: plpgsql}\n"} {
		p := planExtensionsLive(t, db, y, diff.Options{})
		if got := warningMessages(p); got != "" {
			t.Errorf("%s: unexpected warnings:\n%s", y, got)
		}
		applyPlan(t, db, p)
		if _, err := db.Exec(context.Background(), `drop extension plpgsql`); err != nil {
			t.Fatal(err)
		}
	}
}
