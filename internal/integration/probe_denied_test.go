package integration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
)

// TestIntegrationProbesDenied compares as a role without USAGE on a schema,
// as on a hosted platform: the views and the table there can't be compared
// through the server, so they are compared as text and reported, a view
// whose query differs as text is left as it is instead of replaced, and
// the definitions the role may read are still compared through the server.
func TestIntegrationProbesDenied(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	const role = "pgytest_probe_denied"
	dropRole := func() {
		admin.Exec(context.Background(), `drop role if exists `+role) //nolint
	}
	dropRole()
	t.Cleanup(dropRole)
	pool := freshDatabase(t, admin, "pgytest_probe_denied")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `drop owned by `+role) //nolint
	})
	for _, stmt := range []string{
		`create role ` + role + ` login password 'pgy'`,
		`create schema hidden`,
		`revoke all on schema hidden from public`,
		`create table hidden.beat (ts timestamptz)`,
		`create view hidden.same as select max(ts) as ts from hidden.beat`,
		`create view hidden.edited as select max(ts) as ts from hidden.beat`,
		`create materialized view hidden.mv as select count(*) as n from hidden.beat`,
		`create table hidden.p (id int primary key, ttl interval default '1 hour')`,
		`create schema app`,
		`grant usage on schema app to ` + role,
		`create table app.q (id int primary key, ttl interval default '1 hour')`,
		`alter table app.q owner to ` + role,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	d := pulledYAML(t, pool)
	d.Views["hidden.edited"].Query = "select max(ts) + interval '1 day' as ts from hidden.beat"
	d.Views["hidden.mv"].Query = "select count(*) + 1 as n from hidden.beat"
	d.Tables["hidden.p"].Columns["ttl"].Default = "'1 hour'::interval"
	d.Tables["app.q"].Columns["ttl"].Default = "'1 hour'"

	cfg := pool.Config().Copy()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = role, "pgy"
	limited, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	live := introspect(t, limited, d)

	var denied *diff.DeniedError
	if err := diff.ProbeViews(ctx, limited, live, d); !errors.As(err, &denied) || !reflect.DeepEqual(denied.Objects, []string{"hidden.edited", "hidden.mv"}) || !strings.Contains(denied.Reason, "permission denied for schema hidden") {
		t.Errorf("want the views whose query differs as text reported, got %v", err)
	}
	for _, k := range []string{"hidden.same", "hidden.edited", "hidden.mv"} {
		if p := live.ViewProbes[k]; p == nil || !p.Denied || p.Changed() {
			t.Errorf("%s: want a denied probe that isn't a change, got %+v", k, p)
		}
	}
	denied = nil
	if err := diff.ProbeExpressions(ctx, limited, live, d); !errors.As(err, &denied) || !reflect.DeepEqual(denied.Objects, []string{"hidden.p"}) {
		t.Errorf("want the table the role may not copy reported, got %v", err)
	}
	if p := live.ExprProbes[diff.ExprKey{Kind: diff.ExprDefault, Object: "app.q", Name: "ttl"}]; p == nil || p.Err != "" {
		t.Errorf("the role's own table is still probed, got %+v", p)
	}

	want := []string{`alter table "hidden"."p" alter column "ttl" set default '1 hour'::interval;`}
	for _, opts := range []diff.Options{{}, {Unsafe: true}} {
		if p := diff.PlanWith(live, d, opts); len(p.Alters) > 0 || len(p.Creates) > 0 || !strings.Contains(strings.Join(notOwnerMessages(p), "\n"), "table hidden.p is owned by ") {
			t.Errorf("the role doesn't own hidden.p, so the change is left out with a not_owner warning (%+v), got %v %v", opts, p.Alters, p.Warnings)
		}
		p := diff.PlanWith(asOwner(live), d, opts)
		if got := p.Creates; len(got) > 0 {
			t.Errorf("nothing is replaced because a comparison couldn't run (%+v), got %v", opts, got)
		}
		if got := p.Alters; !reflect.DeepEqual(got, want) {
			t.Errorf("want only the default the text comparison finds changed (%+v), got %v", opts, got)
		}
		for _, w := range p.Warnings {
			if strings.Contains(w.Message, "view") {
				t.Errorf("the views are reported once, by the probe, got %+v", w)
			}
		}
	}

	pulled := pulledYAML(t, pool)
	live = introspect(t, limited, pulled)
	if err := diff.ProbeViews(ctx, limited, live, pulled); err != nil {
		t.Errorf("views as pgy pull writes them match as text, so nothing is reported: %v", err)
	}
	if err := diff.ProbeExpressions(ctx, limited, live, pulled); err != nil || live.ExprProbes != nil {
		t.Errorf("pulled YAML needs no expression probe, got %v, %v", err, exprProbeKeys(live))
	}
	if p := diff.PlanReportingUnsafe(live, pulled, diff.Options{}); diff.Render(p) != "" || len(p.Warnings) > 0 {
		t.Errorf("pulled YAML diffs clean for the role, got:\n%s%+v", diff.Render(p), p.Warnings)
	}
}
