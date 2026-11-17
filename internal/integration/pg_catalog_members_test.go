package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

// TestIntegrationPgCatalogMembers manages the privileges of objects an
// extension installed in pg_catalog, as plv8 does and as plpgsql does with
// its handler functions: they are introspected, pulled and rebuilt, an
// attribute-only declaration of one is planned without a warning, and drift
// on one is repaired.
func TestIntegrationPgCatalogMembers(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	app := freshRole(t, admin, "app")
	src := freshDatabase(t, admin, "pgytest_catalog_members")
	dst := freshDatabase(t, admin, "pgytest_catalog_members_copy")
	if _, err := src.Exec(ctx, strings.ReplaceAll(`
		revoke execute on function plpgsql_call_handler() from public;
		grant execute on function plpgsql_validator(oid) to "APP";
		comment on function plpgsql_inline_handler(internal) is 'inline';`, "APP", app)); err != nil {
		t.Fatal(err)
	}
	live := introspectAll(t, src)
	for _, sig := range []string{"pg_catalog.plpgsql_call_handler()", "pg_catalog.plpgsql_validator(oid)"} {
		if live.FunctionDefs[sig] == nil || !live.ExtensionMembers["proc:"+sig] {
			t.Errorf("%s is introspected as a member of plpgsql", sig)
		}
	}
	if live.FunctionDefs["pg_catalog.now()"] != nil {
		t.Errorf("built-in functions stay out of the snapshot")
	}
	if live.FunctionPublicExec["pg_catalog.plpgsql_call_handler()"] || !live.FunctionGrants["pg_catalog.plpgsql_validator(oid)"][app]["execute"] {
		t.Errorf("the privileges of members in pg_catalog are read: %v %v", live.FunctionPublicExec, live.FunctionGrants)
	}

	y := pullAndRebuild(t, src, dst)
	for _, want := range []string{
		"schema pg_catalog:\n",
		"  function plpgsql_call_handler():\n    revokePublic: true\n",
		"  function plpgsql_validator(oid):\n    grants: {" + app + ": [execute]}\n",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("missing %q in the pulled YAML:\n%s", want, y)
		}
	}
	if strings.Contains(y, "plpgsql_inline_handler") {
		t.Errorf("a member's comment is not pulled, as for members elsewhere:\n%s", y)
	}

	const declared = `
schema pg_catalog:
  function plpgsql_call_handler():
    revokePublic: true
  function plpgsql_validator(oid):
    grants: {APP: [execute]}
`
	d := loadYAML(t, "public", strings.ReplaceAll(declared, "APP", app))
	plan := diff.PlanWith(introspectAll(t, src), d, diff.Options{})
	if sql := diff.Render(plan); sql != "" {
		t.Errorf("declarations of members in pg_catalog match the database, got:\n%s", sql)
	}
	if msgs := warningMessages(plan); msgs != "" {
		t.Errorf("declarations of members in pg_catalog don't warn, got:\n%s", msgs)
	}
	if _, err := src.Exec(ctx, `grant execute on function plpgsql_call_handler() to public; revoke execute on function plpgsql_validator(oid) from `+app); err != nil {
		t.Fatal(err)
	}
	plan = diff.PlanWith(introspectAll(t, src), d, diff.Options{})
	want := `revoke all on function "pg_catalog"."plpgsql_call_handler"() from public;` + "\n" + `grant execute on function "pg_catalog"."plpgsql_validator"(oid) to "` + app + `";`
	if sql := strings.TrimSpace(diff.Render(plan)); sql != want {
		t.Errorf("drift on members in pg_catalog is repaired, got:\n%s\nwant:\n%s", sql, want)
	}
	applyPlan(t, src, plan)
	if sql := diff.Render(diff.PlanWith(introspectAll(t, src), d, diff.Options{})); sql != "" {
		t.Errorf("a second diff must be empty, got:\n%s", sql)
	}
}
