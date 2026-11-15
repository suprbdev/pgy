package integration

import (
	"context"
	"os"
	"regexp"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

// pgDumpSchema returns the normalized pg_dump --schema-only output for one
// schema of the test database, with the schema's name replaced by SCH, or
// "" when PGY_TEST_PGDUMP is unset.
func pgDumpSchema(t *testing.T, sch string) string {
	t.Helper()
	cmdline := strings.Fields(os.Getenv("PGY_TEST_PGDUMP"))
	if len(cmdline) == 0 {
		return ""
	}
	args := append(cmdline[1:], "--schema-only", "--schema="+sch, "pgytest")
	out, err := exec.Command(cmdline[0], args...).Output()
	if err != nil {
		t.Fatalf("pg_dump %s: %v", sch, err)
	}
	return strings.ReplaceAll(normalizeDump(string(out)), sch, "SCH")
}

// assertUpgradeMatchesFresh migrates a fresh schema through each YAML in
// steps and builds another fresh schema from the last one alone; with
// PGY_TEST_PGDUMP set, both must dump identically.
func assertUpgradeMatchesFresh(t *testing.T, steps ...string) {
	t.Helper()
	pool := connect(t)
	opts := diff.Options{Unsafe: true, Prune: true}
	upgraded := freshSchema(t, pool)
	for _, y := range steps {
		convergeYAMLWith(t, pool, upgraded, y, opts)
	}
	fresh := upgraded[:len(upgraded)-1] + "9"
	dropSchema := func() { pool.Exec(context.Background(), `drop schema if exists "`+fresh+`" cascade`) } //nolint
	dropSchema()
	t.Cleanup(dropSchema)
	convergeYAMLWith(t, pool, fresh, steps[len(steps)-1], opts)
	a := pgDumpSchema(t, upgraded)
	if a == "" {
		return
	}
	if d := dumpDifferences(a, pgDumpSchema(t, fresh)); len(d) > 0 {
		sort.Strings(d)
		t.Errorf("migrated schema differs from one built fresh (- migrated, + fresh):\n%s", strings.Join(d, "\n"))
	}
}

// TestIntegrationUpgradeMatchesFresh migrates each matrix case from its
// before to its after YAML and requires the result to dump like a schema
// built from the after YAML alone. Range type options are create-only and
// a plain column cannot become generated in place (pgy warns), so those
// cases are left out.
func TestIntegrationUpgradeMatchesFresh(t *testing.T) {
	if os.Getenv("PGY_TEST_PGDUMP") == "" {
		t.Skip("PGY_TEST_PGDUMP not set")
	}
	for _, n := range sortedCaseNames(objectChangeCases) {
		if n == "range" {
			continue
		}
		c := objectChangeCases[n]
		t.Run("object_"+n, func(t *testing.T) {
			assertUpgradeMatchesFresh(t, objectMatrixBase+c[0]+"\n", objectMatrixBase+c[1]+"\n")
		})
	}
	typeRe := regexp.MustCompile(`type: ("[^"]+"|[^,}]+)`)
	for _, n := range sortedCaseNames(columnAttributeCases) {
		if n == "generated" {
			continue
		}
		attr := columnAttributeCases[n]
		t.Run("column_"+n, func(t *testing.T) {
			typ := typeRe.FindStringSubmatch(attr)[1]
			if typ == "serial" {
				typ = "int"
			}
			base := "\nschema SCH:\n  table t:\n    columns:\n      id: {type: int, primaryKey: true}\n"
			assertUpgradeMatchesFresh(t, base+"      b: {type: "+typ+", nullable: true}\n", base+"      b: "+attr+"\n")
		})
	}
	for _, n := range sortedCaseNames(tableAttributeCases) {
		c := tableAttributeCases[n]
		t.Run("table_"+n, func(t *testing.T) {
			assertUpgradeMatchesFresh(t, tableMatrixBase, tableMatrixBase+c+"\n")
		})
	}
}

func sortedCaseNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
