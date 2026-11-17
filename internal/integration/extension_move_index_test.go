package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
)

// TestIntegrationExtensionMoveIndexOptions moves an extension whose type and
// operator class an expression index uses, with the operator class, the
// collation and the statistics target set per index column: the index
// follows the extension, so only ALTER EXTENSION is planned, also for a
// table with a generated column on the indexed one.
func TestIntegrationExtensionMoveIndexOptions(t *testing.T) {
	admin := connect(t)
	db := freshDatabase(t, admin, "pgytest_ext_move_index")
	if _, err := db.Exec(context.Background(), `
		create extension citext schema public;
		create schema app;
		create table app.t (id int primary key, c citext, g text generated always as (btrim(c)) stored);
		create index t_c on app.t ((btrim(c)::citext) citext_pattern_ops);
		create index t_d on app.t ((lower(c)::citext) collate "C");
		alter index app.t_d alter column 1 set statistics 50;`); err != nil {
		t.Fatal(err)
	}
	sql := convergeLive(t, db, `
extensions:
  - {name: citext, schema: ext}
schema app:
  table t:
    columns:
      id: {type: int, primaryKey: true}
      c: {type: ext.citext, nullable: true}
      g: {type: text, nullable: true, generated: btrim(c)}
    indexes:
      t_c: {columns: ["(btrim(c)::ext.citext)"], opclasses: {"(btrim(c)::ext.citext)": ext.citext_pattern_ops}}
      t_d: {columns: ["(lower(c)::ext.citext)"], collations: {"(lower(c)::ext.citext)": C}, statisticsTargets: {"(lower(c)::ext.citext)": 50}}
`, diff.Options{Unsafe: true, Prune: true})
	if want := "create schema if not exists \"ext\";\nalter extension \"citext\" set schema \"ext\";"; strings.TrimSpace(sql) != want {
		t.Errorf("the indexes follow the extension, got:\n%s\nwant:\n%s", sql, want)
	}
}
