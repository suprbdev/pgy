package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationOwnersAndDefaultPrivileges(t *testing.T) {
	pool := connect(t)
	sch := freshSchema(t, pool)
	ctx := context.Background()
	owner := freshRole(t, pool, "owner")
	reader := freshRole(t, pool, "reader")
	d := &schema.Database{
		SchemaOwners: map[string]string{sch: owner},
		DefaultPrivileges: []*schema.DefaultPrivilege{
			{Schemas: []string{sch}, Grants: map[string]map[string][]string{"tables": {reader: {"select"}}}},
			{ForRole: owner, Grants: map[string]map[string][]string{"functions": {"public": {}}, "types": {reader: {"usage"}}}},
		},
		Tables:    map[string]*schema.Table{sch + ".t": {Name: "t", Columns: map[string]*schema.Column{"id": {Type: "int"}}, Owner: owner}},
		Functions: map[string]*schema.Function{sch + ".f()": {Schema: sch, Name: "f", ArgsSig: "()", Returns: "int", Language: "sql", Body: "select 1", Owner: owner}},
		Types:     map[string]*schema.TypeDef{sch + ".mood": {Name: "mood", Kind: "enum", Labels: []string{"ok"}, Owner: owner}},
	}
	plan := func() *diff.PlanDiff {
		t.Helper()
		live, err := diff.Introspect(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return diff.Plan(live, d, false)
	}
	applyPlan(t, pool, plan())
	if sql := diff.Render(plan()); sql != "" {
		t.Fatalf("expected no changes after apply, got:\n%s", sql)
	}
	var tblOwner, fnOwner string
	if err := pool.QueryRow(ctx, fmt.Sprintf(`select pg_get_userbyid(relowner) from pg_class where oid = '%q.t'::regclass`, sch)).Scan(&tblOwner); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, fmt.Sprintf(`select pg_get_userbyid(proowner) from pg_proc where oid = '%q.f()'::regprocedure`, sch)).Scan(&fnOwner); err != nil {
		t.Fatal(err)
	}
	if tblOwner != owner || fnOwner != owner {
		t.Errorf("owners: table=%s function=%s, want %s", tblOwner, fnOwner, owner)
	}
	var canSelect bool
	if err := pool.QueryRow(ctx, fmt.Sprintf(`select has_table_privilege('%s', '%q.t', 'select')`, reader, sch)).Scan(&canSelect); err != nil || !canSelect {
		t.Errorf("default privileges should give %s select on a table created in the same plan (got %v, %v)", reader, canSelect, err)
	}
}
