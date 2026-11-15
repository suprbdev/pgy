package integration

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

const tableMatrixBase = `
schema SCH:
  function touch():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table parent:
    columns: {id: {type: int, primaryKey: true}}
  table t:
    columns:
      id: {type: int, primaryKey: true}
      a: {type: int, nullable: true}
      b: {type: text, nullable: true}
      p: {type: int, nullable: true}
`

// tableAttributeCases maps a case name to YAML lines added to table t.
// Each must converge in one migration when t is created with it, when it
// is added to an existing t, and when it is removed again under --unsafe
// --prune, and must survive a pull round trip.
var tableAttributeCases = map[string]string{
	"comment":          `    comment: "t's table"`,
	"check":            `    constraints: {t_a_pos: {type: check, expression: a > 0}}`,
	"check_not_valid":  `    constraints: {t_a_pos: {type: check, expression: a > 0, notValid: true}}`,
	"check_no_inherit": `    constraints: {t_a_pos: {type: check, expression: a > 0, noInherit: true}}`,
	"unique":           `    constraints: {t_ab_key: {type: unique, columns: [a, b], deferrable: true}}`,
	"unique_nnd":       `    constraints: {t_ab_key: {type: unique, columns: [a, b], nullsNotDistinct: true}}`,
	"fk":               `    foreignKeys: {t_p_fkey: {columns: [p], references: {table: SCH.parent, columns: [id]}, onDelete: cascade, onUpdate: restrict}}`,
	"fk_deferred":      `    foreignKeys: {t_p_fkey: {columns: [p], references: {table: SCH.parent, columns: [id]}, initiallyDeferred: true, comment: to parent}}`,
	"fk_not_valid":     `    foreignKeys: {t_p_fkey: {columns: [p], references: {table: SCH.parent, columns: [id]}, notValid: true}}`,
	"index":            `    indexes: {t_b_idx: {columns: [b], comment: by b}}`,
	"index_partial":    `    indexes: {t_lower_b: {columns: [lower(b)], where: a > 0, unique: true}}`,
	"index_desc":       `    indexes: {t_ab_idx: {columns: [a desc nulls last, b], include: [p]}}`,
	"index_with":       `    indexes: {t_b_idx: {columns: [b], method: hash, with: {fillfactor: 70}}}`,
	"index_statistics": `    indexes: {t_lower_b: {columns: [a, lower(b)], statisticsTargets: {lower(b): 300}}}`,
	"trigger":          `    triggers: {t_touch: {timing: before, events: [update], level: row, procedure: SCH.touch()}}`,
	"trigger_when":     `    triggers: {t_touch: {timing: before, events: [insert, update], level: row, when: new.a is not null, procedure: SCH.touch(), enabled: false}}`,
	"policy":           "    rowLevelSecurity: true\n    policies: {t_sel: {for: select, to: [public], using: a > 0}, t_ins: {for: insert, as: restrictive, withCheck: a < 100}}",
	"rule":             `    rules: {t_no_delete: {event: delete, instead: true, do: nothing}}`,
	"statistics":       `    statistics: {t_ab_stats: {kinds: [ndistinct, dependencies], columns: [a, b]}}`,
	"statistics_target": `    statistics: {t_ab_stats: {columns: [a, b], statisticsTarget: 500}}`,
	"grants":           `    grants: {public: [select, insert]}`,
	"replica_full":     `    replicaIdentity: full`,
	"replica_index":    `    replicaIdentity: index t_pkey`,
	"cluster":          `    clusterOn: t_pkey`,
	"with":             `    with: {fillfactor: 80, autovacuum_enabled: false}`,
	"unlogged":         `    unlogged: true`,
	"inherits":         `    inherits: [SCH.parent]`,
}

func TestIntegrationTableAttributeMatrix(t *testing.T) {
	names := make([]string, 0, len(tableAttributeCases))
	for n := range tableAttributeCases {
		names = append(names, n)
	}
	sort.Strings(names)
	created := map[string]string{}
	for _, n := range names {
		created["new_table_"+n] = tableMatrixBase + tableAttributeCases[n] + "\n"
	}
	assertYAMLIdempotent(t, created)

	pool := connect(t)
	for _, n := range names {
		t.Run("added_and_removed_"+n, func(t *testing.T) {
			sch := freshSchema(t, pool)
			opts := diff.Options{Unsafe: true, Prune: true}
			convergeYAMLWith(t, pool, sch, tableMatrixBase, opts)
			convergeYAMLWith(t, pool, sch, tableMatrixBase+tableAttributeCases[n]+"\n", opts)
			convergeYAMLWith(t, pool, sch, tableMatrixBase, opts)
		})
	}
}

// convergeYAMLWith is convergeYAML with explicit plan options.
func convergeYAMLWith(t *testing.T, pool *pgxpool.Pool, sch, y string, opts diff.Options) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(y, "SCH", sch)), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.PlanWith(introspect(t, pool, d), d, opts)
	applyPlan(t, pool, plan)
	if again := diff.PlanWith(introspect(t, pool, d), d, opts); diff.Render(again)+diff.RenderConcurrent(again) != "" {
		t.Fatalf("plan did not converge:\n%s%s\nafter applying:\n%s%s", diff.Render(again), diff.RenderConcurrent(again), diff.Render(plan), diff.RenderConcurrent(plan))
	}
	return diff.Render(plan) + diff.RenderConcurrent(plan)
}
