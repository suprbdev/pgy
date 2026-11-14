package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func pruneLive() *Live {
	l := emptyLive()
	l.Schemas["app"], l.Schemas["other"] = true, true
	l.ExtensionMembers = map[string]bool{"rel:app.ext_table": true, "proc:app.ext_fn()": true, "type:app.ext_type": true}
	l.OwnedSequences = map[string]bool{"app.keep_id_seq": true}
	l.MatViewTables = map[string]*LiveTable{}
	for _, k := range []string{"app.keep", "app.gone", "app.ext_table", "app.part_child", "other.untouched", "pgy.migrations"} {
		l.Tables[k] = newLiveTable()
		l.Tables[k].Columns["id"] = &LiveColumn{Type: "integer"}
	}
	l.Tables["app.part_child"].IsPartition = true
	keep := l.Tables["app.keep"]
	keep.HasPK = true
	for name, def := range map[string]string{
		"keep_pkey":        "PRIMARY KEY (id)",
		"keep_fk":          "FOREIGN KEY (id) REFERENCES app.gone(id)",
		"keep_old_fk":      "FOREIGN KEY (id) REFERENCES app.gone(id)",
		"keep_check":       "CHECK ((id > 0))",
		"keep_old_check":   "CHECK ((id > 1))",
		"app_keep_id_key":  "UNIQUE (id)",
		"keep_inherited_c": "CHECK ((id > 2))",
	} {
		keep.Constraints[name] = true
		keep.ConstraintDefs[name] = def
	}
	keep.ConstraintInherited["keep_inherited_c"] = true
	for name, li := range map[string]*LiveIndex{
		"keep_pkey":       {Constraint: true},
		"app_keep_id_key": {Constraint: true},
		"keep_idx":        {},
		"keep_old_idx":    {},
		"keep_inh_idx":    {Inherited: true},
	} {
		keep.Indexes[name] = true
		keep.IndexDefs[name] = li
	}
	keep.Triggers["keep_trg"] = ""
	keep.Triggers["keep_old_trg"] = ""
	keep.Triggers["keep_inh_trg"] = ""
	keep.TriggerInherited["keep_inh_trg"] = true

	l.Views["app.v_keep"], l.Views["app.v_gone"], l.Views["other.v"] = true, true, true
	l.MatViews["app.mv_keep"], l.MatViews["app.mv_gone"] = true, true
	mv := newLiveTable()
	mv.Indexes["mv_keep_idx"], mv.Indexes["mv_old_idx"] = true, true
	mv.IndexDefs["mv_keep_idx"], mv.IndexDefs["mv_old_idx"] = &LiveIndex{}, &LiveIndex{}
	l.MatViewTables["app.mv_keep"] = mv

	for sig, lf := range map[string]*LiveFunction{
		"app.f_keep(a integer)": {Kind: "f"}, "app.f_gone(a integer, b text)": {Kind: "f"},
		"app.agg(integer)": {Kind: "a"}, "app.ext_fn()": {Kind: "f"}, "other.f()": {Kind: "f"},
	} {
		lf.Signature = sig
		l.Functions[sig] = true
		l.FunctionDefs[normalizeFunctionSignature(sig)] = lf
	}
	l.Procedures["app.p_gone()"] = true
	l.ProcedureDefs["app.p_gone()"] = &LiveProcedure{Signature: "app.p_gone()"}
	for _, k := range []string{"app.seq_keep", "app.seq_gone", "app.keep_id_seq", "other.seq"} { l.Sequences[k] = true }
	for _, k := range []string{"app.dom_keep", "app.dom_gone"} { l.Domains[k] = true }
	for _, k := range []string{"app.t_keep", "app.t_gone", "app.ext_type"} { l.Types[k] = true }
	return l
}

func pruneDesired() *schema.Database {
	return &schema.Database{
		Tables: map[string]*schema.Table{
			"app.keep": {Name: "keep",
				Columns:     map[string]*schema.Column{"id": {Type: "int", Unique: true}},
				PrimaryKey:  []string{"id"},
				ForeignKeys: []*schema.ForeignKey{{Name: "keep_fk", Columns: []string{"id"}, RefTable: "app.gone", RefColumns: []string{"id"}}},
				Constraints: []*schema.Constraint{{Name: "keep_check", Type: "check", Expression: "id > 0"}},
				Indexes:     []*schema.Index{{Name: "keep_idx", Columns: []string{"id"}}},
				Triggers:    []*schema.Trigger{{Name: "keep_trg", Timing: "before", Events: []string{"insert"}, Level: "row", Procedure: "app.f_keep()"}},
			},
		},
		Views: map[string]*schema.View{
			"app.v_keep":  {Name: "v_keep", Query: "select 1"},
			"app.mv_keep": {Name: "mv_keep", Materialized: true, Query: "select 1", Indexes: []*schema.Index{{Name: "mv_keep_idx", Columns: []string{"x"}}}},
		},
		Functions: map[string]*schema.Function{"app.f_keep(a int)": {Name: "f_keep", Schema: "app", ArgsSig: "(a int)", Returns: "int", Language: "sql"}},
		Sequences: map[string]*schema.Sequence{"app.seq_keep": {Name: "seq_keep"}},
		Domains:   map[string]*schema.Domain{"app.dom_keep": {Name: "dom_keep"}},
		Types:     map[string]*schema.TypeDef{"app.t_keep": {Name: "t_keep"}},
	}
}

func TestPruneRequiresUnsafeAndPrune(t *testing.T) {
	for _, opts := range []Options{{}, {Prune: true}, {Unsafe: true}} {
		p := PlanWith(pruneLive(), pruneDesired(), opts)
		if strings.Contains(strings.Join(p.Drops, "\n"), "drop table") {
			t.Errorf("%+v must not prune, drops: %v", opts, p.Drops)
		}
	}
}

func TestPruneDropsUndeclaredObjects(t *testing.T) {
	p := PlanWith(pruneLive(), pruneDesired(), Options{Unsafe: true, Prune: true})
	want := []string{
		`drop trigger "keep_old_trg" on "app"."keep";`,
		`alter table "app"."keep" drop constraint "keep_old_fk";`,
		`alter table "app"."keep" drop constraint "keep_old_check";`,
		`drop index "app"."keep_old_idx";`,
		`drop index "app"."mv_old_idx";`,
		`drop view "app"."v_gone";`,
		`drop materialized view "app"."mv_gone";`,
		`drop table "app"."gone";`,
		`drop function "app"."f_gone"(a integer, b text);`,
		`drop procedure "app"."p_gone"();`,
		`drop sequence "app"."seq_gone";`,
		`drop domain "app"."dom_gone";`,
		`drop type "app"."t_gone";`,
	}
	if got := strings.Join(p.Drops, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("drops:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestPruneEarlyDropsPrecedeColumnDrops(t *testing.T) {
	live := pruneLive()
	live.Tables["app.keep"].Columns["legacy"] = &LiveColumn{Type: "text"}
	p := PlanWith(live, pruneDesired(), Options{Unsafe: true, Prune: true})
	idx := func(s string) int {
		for i, d := range p.Drops {
			if strings.Contains(d, s) { return i }
		}
		return -1
	}
	view, col, table := idx(`drop view`), idx(`drop column "legacy"`), idx(`drop table`)
	if view < 0 || col < 0 || table < 0 || view > col || col > table {
		t.Errorf("want view drop < column drop < table drop, got %d %d %d: %v", view, col, table, p.Drops)
	}
}

func TestPruneKeepsConfiguredMigrationsTable(t *testing.T) {
	live := pruneLive()
	live.Tables["app.applied"] = newLiveTable()
	p := PlanWith(live, pruneDesired(), Options{Unsafe: true, Prune: true, MigrationsTable: "app.applied"})
	for _, d := range p.Drops {
		if strings.Contains(d, `"app"."applied"`) {
			t.Errorf("configured migrations table must not be pruned: %s", d)
		}
	}
}
