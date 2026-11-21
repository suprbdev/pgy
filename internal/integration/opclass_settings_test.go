package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
)

// TestIntegrationPullContestedOpclasses pulls indexes and an exclusion
// constraint built with btree_gist's default classes and requires each class
// to be written out, since core's inet_ops replaces gist_inet_ops as the
// default for inet on PostgreSQL 19.
func TestIntegrationPullContestedOpclasses(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	db := freshDatabase(t, admin, "pgytest_contested_opclasses")
	if _, err := db.Exec(ctx, `
		create extension if not exists btree_gist;
		create schema netw;
		create table netw.host (id int primary key, tenant int not null, addr inet not null,
			constraint host_excl exclude using gist (tenant with =, addr with =));
		create index host_default on netw.host using gist (tenant, addr);
		create index host_text on netw.host (id);
	`); err != nil {
		t.Fatal(err)
	}
	var inetDefault string
	if err := db.QueryRow(ctx, `select opcname from pg_opclass o join pg_am a on a.oid = o.opcmethod where a.amname = 'gist' and o.opcintype = 'inet'::regtype and o.opcdefault`).Scan(&inetDefault); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(introspectAll(t, db), pull.Options{Single: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(res.Files[0].Content)
	for _, want := range []string{
		"opclasses: {tenant: gist_int4_ops, addr: " + inetDefault + "}",
		"expression: USING gist (tenant gist_int4_ops WITH =, addr " + inetDefault + " WITH =)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{id: int4_ops") {
		t.Errorf("a btree default no extension contests must be left out:\n%s", got)
	}
}

// TestIntegrationUnknownSettingsLeftOut declares a setting the server does
// not know next to known and placeholder ones and requires the plan to leave
// out only the unknown one, with a warning, and to converge.
func TestIntegrationUnknownSettingsLeftOut(t *testing.T) {
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_unknown_settings")
	y := `
database:
  set: {pgy_no_such_setting: "on", app.pgy_custom: "1", work_mem: 8MB}
schema SCH:
  function f():
    returns: int
    language: sql
    body: select 1
    set: {pgy_no_such_setting: "on", search_path: SCH}
`
	d := loadYAML(t, "app", y)
	plan := diff.Plan(introspect(t, db, d), d, true)
	sql := diff.Render(plan)
	if strings.Contains(sql, "pgy_no_such_setting") {
		t.Errorf("the unknown setting must be left out:\n%s", sql)
	}
	for _, want := range []string{"set app.pgy_custom = 1", "set work_mem = ''8MB''", "set search_path = app"} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	warned := 0
	for _, w := range plan.Warnings {
		if strings.Contains(w.Message, "pgy_no_such_setting is not a parameter") {
			warned++
		}
	}
	if warned != 2 {
		t.Errorf("want a warning for each unknown setting, got %+v", plan.Warnings)
	}
	convergeYAML(t, db, "app", y)
}
