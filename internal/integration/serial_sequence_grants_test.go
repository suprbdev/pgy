package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationSerialSequenceGrants(t *testing.T) {
	pool := connect(t)
	role := freshRole(t, pool, "seqgrant")
	assertYAMLIdempotent(t, map[string]string{"serial_grants": strings.ReplaceAll(`
schema SCH:
  table audit:
    columns:
      id: {type: bigserial, primaryKey: true}
      note: {type: text, nullable: true}
    grants: {ROLE: [insert]}
  sequence audit_id_seq:
    grants: {ROLE: [usage]}
    comment: audit ids
    dependsOn: [table SCH.audit]
`, "ROLE", role)})
}

// TestIntegrationIdentitySequenceGrants declares grants on the sequences of
// identity and serial columns without dependsOn: pgy must not create them
// ahead of their column, which would leave the column a sequence of another
// name, and the grants must be pulled again.
func TestIntegrationIdentitySequenceGrants(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	role := freshRole(t, pool, "idseq")
	sch := freshSchema(t, pool)
	d, err := schema.LoadAndMerge([]string{writeYAML(t, strings.NewReplacer("SCH", sch, "ROLE", role).Replace(`
schema SCH:
  sequence t_id_seq:
    grants: {ROLE: [usage, select]}
    comment: ids
  sequence named_ids:
    grants: {ROLE: [usage]}
  sequence t_s_seq:
    grants: {ROLE: [update]}
  table t:
    columns:
      id: {type: bigint, identity: by default, primaryKey: true}
      other: {type: bigint, identity: {generated: always, sequenceName: named_ids}}
      s: {type: bigserial}
`))})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := applyAsMigrations(ctx, pool, diff.Plan(introspect(t, pool, d), d, true)); err != nil {
		t.Fatalf("apply %q: %v", s, err)
	}
	var sequences, columns string
	if err := pool.QueryRow(ctx, `select string_agg(relname, ',' order by relname) from pg_class where relkind = 'S' and relnamespace = $1::regnamespace`, sch).Scan(&sequences); err != nil {
		t.Fatal(err)
	}
	if sequences != "named_ids,t_id_seq,t_s_seq" {
		t.Errorf("the columns must create the declared sequences and nothing else, got %s", sequences)
	}
	if err := pool.QueryRow(ctx, `select concat_ws(',', pg_get_serial_sequence($1, 'id'), pg_get_serial_sequence($1, 'other'), pg_get_serial_sequence($1, 's'),
	        has_sequence_privilege($2, $3 || '.t_id_seq', 'select'), has_sequence_privilege($2, $3 || '.named_ids', 'usage'), has_sequence_privilege($2, $3 || '.t_s_seq', 'update'))`,
		sch+".t", role, sch).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if want := strings.ReplaceAll("SCH.t_id_seq,SCH.named_ids,SCH.t_s_seq,t,t,t", "SCH", sch); columns != want {
		t.Errorf("want the columns' own sequences with the declared grants, got %s, want %s", columns, want)
	}
	live := introspect(t, pool, d)
	if sql := diff.Render(diff.Plan(live, d, true)); sql != "" {
		t.Errorf("second plan not empty:\n%s", sql)
	}
	assertPullRoundTrip(t, ctx, pool, live, sch, d)
}

func TestIntegrationDefaultPrivilegesDoNotOverGrant(t *testing.T) {
	pool := connect(t)
	role := freshRole(t, pool, "defpriv")
	assertYAMLIdempotent(t, map[string]string{"defaults": strings.ReplaceAll(`
defaultPrivileges:
  - in: [SCH]
    tables: {ROLE: [select]}
    sequences: {ROLE: [usage]}
schema SCH:
  table older:
    columns: {id: {type: int, primaryKey: true}}
    grants: {}
  table newer:
    columns: {id: {type: bigserial, primaryKey: true}}
    grants: {ROLE: [select, insert]}
  sequence newer_id_seq:
    grants: {}
    dependsOn: [table SCH.newer]
  table loose:
    columns: {id: {type: int}}
`, "ROLE", role)})
}
