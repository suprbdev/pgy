package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/validate"
)

func TestStrictSettings(t *testing.T) {
	failOn, forbid, err := strictSettings(nil)
	if err != nil || !failOn["lock"] || !failOn["public_revoke"] || !failOn["validate"] || failOn["unsupported"] || !forbid {
		t.Errorf("preset: %v %v %v", failOn, forbid, err)
	}
	no := false
	failOn, forbid, err = strictSettings(&CIConfig{FailOn: []string{"unsupported"}, ForbidUnsafe: &no})
	if err != nil || len(failOn) != 1 || !failOn["unsupported"] || forbid {
		t.Errorf("override: %v %v %v", failOn, forbid, err)
	}
	failOn, forbid, _ = strictSettings(&CIConfig{ForbidUnsafe: &no})
	if !failOn["lock"] || forbid {
		t.Errorf("keys left out keep the preset: %v %v", failOn, forbid)
	}
	if _, _, err := strictSettings(&CIConfig{FailOn: []string{"locks"}}); err == nil || !strings.Contains(err.Error(), `unknown kind "locks"`) {
		t.Errorf("unknown kind: %v", err)
	}
	if failOn, _, err := strictSettings(&CIConfig{FailOn: []string{"not_owner"}}); err != nil || !failOn["not_owner"] {
		t.Errorf("not_owner is a kind ci.fail_on accepts: %v %v", failOn, err)
	}
}

func strictConfig(t *testing.T, dir string, ci *CIConfig, dsn string) *GlobalConfig {
	t.Helper()
	failOn, forbid, err := strictSettings(ci)
	if err != nil {
		t.Fatal(err)
	}
	return &GlobalConfig{DSN: dsn, SchemaRoot: dir, Schemas: []string{"s.yml"}, BufferPath: filepath.Join(dir, "buffer.sql"), MigrationsDir: filepath.Join(dir, "migrations"), MigrationsTable: defaultMigrationsTable, Quiet: true, Strict: true, FailOn: failOn, ForbidUnsafe: forbid}
}

func runWith(cfg *GlobalConfig, c interface {
	SetArgs([]string)
	ExecuteContext(context.Context) error
}, args ...string) error {
	c.SetArgs(args)
	return c.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, cfg))
}

func TestStrictRefusesUnsafe(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.yml"), "schema app:\n  table t:\n    columns: {id: {type: int}}\n")
	err := runWith(strictConfig(t, dir, nil, ""), cmdDiff(), "--from-empty", "--unsafe")
	if err == nil || !strings.Contains(err.Error(), "--strict refuses --unsafe") {
		t.Errorf("want --unsafe refused, got %v", err)
	}
	no := false
	err = runWith(strictConfig(t, dir, &CIConfig{ForbidUnsafe: &no}, ""), cmdDiff(), "--from-empty", "--unsafe")
	if code, ok := AsExitCode(err); !ok || code != 2 {
		t.Errorf("forbid_unsafe: false allows it, got %v", err)
	}
}

func TestStrictValidateFindings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.yml"), "schema app:\n  table t:\n    columns: {id: {type: int, primaryKey: true}}\n    replicaIdentity: index missing_idx\n")
	if err := runWith(strictConfig(t, dir, nil, ""), cmdValidate()); err == nil || !strings.Contains(err.Error(), "--strict: 1 finding(s) of kind validate") {
		t.Errorf("validate --strict must fail on a warning, got %v", err)
	}
	cfg := strictConfig(t, dir, nil, "")
	cfg.Strict = false
	if err := runWith(cfg, cmdValidate()); err != nil {
		t.Errorf("without --strict a warning passes, got %v", err)
	}
	if err := runWith(strictConfig(t, dir, nil, ""), cmdDiff(), "--from-empty"); err == nil || !strings.Contains(err.Error(), "kind validate") {
		t.Errorf("diff --strict must fail on validate findings, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "buffer.sql")); err != nil {
		t.Errorf("the plan is still written: %v", err)
	}
	err := runWith(strictConfig(t, dir, &CIConfig{FailOn: []string{"lock"}}, ""), cmdDiff(), "--from-empty")
	if code, ok := AsExitCode(err); !ok || code != 2 {
		t.Errorf("fail_on without validate: want exit 2 for changes, got %v", err)
	}
}

func TestStrictLockWarnings(t *testing.T) {
	dsn := os.Getenv("PGY_TEST_DSN")
	if dsn == "" {
		t.Skip("PGY_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sch := "pgytest_strict_locks"
	if _, err := pool.Exec(ctx, "drop schema if exists "+sch+" cascade; create schema "+sch+"; create table "+sch+".t (id int primary key, b int)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "drop schema if exists "+sch+" cascade") })
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.yml"), "schema "+sch+":\n  table t:\n    columns: {id: {type: int, primaryKey: true}, b: {type: int, nullable: true}}\n    indexes: {t_b: {columns: [b]}}\n")
	if err := runWith(strictConfig(t, dir, nil, dsn), cmdDiff()); err == nil || !strings.Contains(err.Error(), "kind lock") {
		t.Errorf("a blocking index build must fail --strict, got %v", err)
	}
	err = runWith(strictConfig(t, dir, &CIConfig{FailOn: []string{"validate"}}, dsn), cmdDiff())
	if code, ok := AsExitCode(err); !ok || code != 2 {
		t.Errorf("fail_on without lock: want exit 2, got %v", err)
	}
}

func TestResolvePolicy(t *testing.T) {
	p, err := resolvePolicy(&PolicyConfig{SecurityDefiner: "hardened", RequireGrants: []string{"tables"}, RequireOwners: true, RowLevelSecurity: []string{"app"}})
	if err != nil || !p.SecurityDefinerHardened || !p.RequireGrants["tables"] || len(p.RequireOwners) != len(validateKinds()) || !p.RowLevelSecurity["app"] {
		t.Errorf("resolved: %+v %v", p, err)
	}
	if p, err := resolvePolicy(&PolicyConfig{RequireOwners: []any{"views"}}); err != nil || len(p.RequireOwners) != 1 || !p.RequireOwners["views"] {
		t.Errorf("owners list: %+v %v", p, err)
	}
	for _, bad := range []*PolicyConfig{{SecurityDefiner: "strict"}, {RequireGrants: []string{"table"}}, {RequireOwners: "yes"}} {
		if _, err := resolvePolicy(bad); err == nil {
			t.Errorf("want an error for %+v", bad)
		}
	}
	if p, _ := resolvePolicy(nil); !p.Empty() {
		t.Error("no policy block enables nothing")
	}
}

func TestStrictPolicyFindings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "s.yml"), "schema app:\n  table t:\n    columns: {id: {type: int, primaryKey: true}}\n")
	cfg := strictConfig(t, dir, nil, "")
	cfg.Policy, _ = resolvePolicy(&PolicyConfig{RequireGrants: []string{"tables"}})
	if err := runWith(cfg, cmdValidate()); err == nil || !strings.Contains(err.Error(), "kind policy") {
		t.Errorf("validate --strict must fail on a policy finding, got %v", err)
	}
	if err := runWith(cfg, cmdDiff(), "--from-empty"); err == nil || !strings.Contains(err.Error(), "kind policy") {
		t.Errorf("diff --strict must fail on a policy finding, got %v", err)
	}
	cfg = strictConfig(t, dir, &CIConfig{FailOn: []string{"validate"}}, "")
	cfg.Policy, _ = resolvePolicy(&PolicyConfig{RequireGrants: []string{"tables"}})
	if err := runWith(cfg, cmdValidate()); err != nil {
		t.Errorf("fail_on without policy: policy findings only warn, got %v", err)
	}
}

func validateKinds() []string { return validate.PolicyKinds }

func TestCapUnsafe(t *testing.T) {
	ws := []diff.Warning{{Kind: "lock", Message: "lock"}}
	for i := 0; i < maxUnsafeWarnings+5; i++ {
		ws = append(ws, diff.Warning{Kind: "unsafe", Message: "u"})
	}
	got := capUnsafe(ws)
	if len(got) != maxUnsafeWarnings+2 || got[0].Message != "lock" || !strings.Contains(got[len(got)-1].Message, "and 5 more statements") {
		t.Errorf("capUnsafe kept %d: last %+v", len(got), got[len(got)-1])
	}
	if failOn, _, err := strictSettings(&CIConfig{FailOn: []string{"unsafe"}}); err != nil || !failOn["unsafe"] {
		t.Errorf("unsafe is a fail_on kind: %v %v", failOn, err)
	}
}
