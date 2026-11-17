package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func droppedColumnLive(deps ...ColumnDependent) *Live {
	live := liveWithTablePK("public.t", map[string]*LiveColumn{"id": {Type: "integer"}, "a": {Type: "text", Nullable: true}})
	live.ColumnDependents = map[string][]ColumnDependent{"public.t.a": deps}
	return live
}

func droppedColumnYAML() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{"public.t": {Name: "t", PrimaryKey: []string{"id"}, Columns: map[string]*schema.Column{"id": {Type: "integer"}}}}}
}

func columnDropWarning(p *PlanDiff) string {
	for _, w := range p.Warnings {
		if w.Kind == "dependents" && strings.HasPrefix(w.Message, "dropping column ") {
			return w.Message
		}
	}
	return ""
}

func TestColumnDropKeptWhileUndeclaredTriggerUsesIt(t *testing.T) {
	live := droppedColumnLive(ColumnDependent{Kind: "trigger", Name: "t_a", Table: "public.t"})
	live.Tables["public.t"].Triggers["t_a"] = "CREATE TRIGGER t_a BEFORE UPDATE OF a ON t FOR EACH ROW EXECUTE FUNCTION suppress_redundant_updates_trigger()"
	d := droppedColumnYAML()

	p := PlanWith(live, d, Options{Unsafe: true})
	msg := columnDropWarning(p)
	if strings.Contains(Render(p), "drop column") || msg != "dropping column public.t.a: trigger t_a on public.t still uses it, and PostgreSQL rejects the drop while it exists, so the column is kept; --prune drops it first" {
		t.Errorf("without --prune the trigger stays, so the drop is left out, got %q\n%s", msg, Render(p))
	}
	p = PlanWith(live, d, Options{Unsafe: true, Prune: true})
	statementOrder(t, Render(p), `drop trigger "t_a" on "public"."t";`, `alter table "public"."t" drop column "a";`)
	if msg := columnDropWarning(p); msg != "" {
		t.Errorf("--prune drops the trigger first, so nothing is reported: %s", msg)
	}
	if ws := PlanWith(live, d, Options{}).Warnings; len(ws) != 0 {
		t.Errorf("without --unsafe no column is dropped, so nothing is reported: %v", ws)
	}
}

func TestColumnDropKeptWhileUndeclaredViewUsesIt(t *testing.T) {
	live := droppedColumnLive(ColumnDependent{Kind: "view", Name: "public.v", Table: "public.v"}, ColumnDependent{Kind: "view", Name: "audit.w", Table: "audit.w"})
	live.Views = map[string]bool{"public.v": true, "audit.w": true}
	d := droppedColumnYAML()

	msg := columnDropWarning(PlanWith(live, d, Options{Unsafe: true}))
	if !strings.Contains(msg, "view audit.w, view public.v still use it") || !strings.Contains(msg, "drop them first, in a hand-written migration") {
		t.Errorf("a view outside the schemas the YAML manages is never pruned: %q", msg)
	}
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if msg := columnDropWarning(p); !strings.Contains(msg, "view audit.w still uses it") || strings.Contains(msg, "public.v") || strings.Contains(Render(p), "drop column") {
		t.Errorf("--prune drops public.v but not audit.w, got %q\n%s", msg, Render(p))
	}

	delete(live.Views, "audit.w")
	live.ColumnDependents["public.t.a"] = []ColumnDependent{{Kind: "view", Name: "public.v", Table: "public.v"}}
	if msg := columnDropWarning(PlanWith(live, d, Options{Unsafe: true})); !strings.HasSuffix(msg, "view public.v still uses it, and PostgreSQL rejects the drop while it exists, so the column is kept; --prune drops it first") {
		t.Errorf("got %q", msg)
	}
	statementOrder(t, Render(PlanWith(live, d, Options{Unsafe: true, Prune: true})), `drop view "public"."v";`, `alter table "public"."t" drop column "a";`)

	d.Views = map[string]*schema.View{"public.v": {Name: "v"}}
	if msg := columnDropWarning(PlanWith(live, d, Options{Unsafe: true, Prune: true})); !strings.Contains(msg, "view public.v still uses it") || !strings.Contains(msg, "hand-written") {
		t.Errorf("a view declared without a query is kept as it is: %q", msg)
	}
	d.Views["public.v"].Query = "select id from t"
	if msg := columnDropWarning(PlanWith(live, d, Options{Unsafe: true})); msg != "" {
		t.Errorf("a declared view is the plan's to change: %q", msg)
	}
}

func TestColumnDropOrderedAfterPolicyDrop(t *testing.T) {
	live := droppedColumnLive(ColumnDependent{Kind: "policy", Name: "t_p", Table: "public.t"})
	live.Tables["public.t"].Policies = map[string]*LivePolicy{"t_p": {Cmd: "all", Using: "(a IS NOT NULL)"}}
	d := droppedColumnYAML()

	for _, prune := range []bool{false, true} {
		p := PlanWith(live, d, Options{Unsafe: true, Prune: prune})
		if msg := columnDropWarning(p); strings.Contains(Render(p), "drop column") || !strings.Contains(msg, "policy t_p on public.t still uses it") || !strings.Contains(msg, "(a policies block drops the policies it doesn't list)") {
			t.Errorf("prune %v: without a policies block the policy is unmanaged and stays, got %q\n%s", prune, msg, Render(p))
		}
	}
	d.Tables["public.t"].Policies = []*schema.Policy{}
	p := PlanWith(live, d, Options{Unsafe: true})
	statementOrder(t, Render(p), `drop policy "t_p" on "public"."t";`, `alter table "public"."t" drop column "a";`)
	if msg := columnDropWarning(p); msg != "" {
		t.Errorf("a policies block without the policy drops it first: %q", msg)
	}
}

func TestColumnDropDropsGeneratedColumnsFirst(t *testing.T) {
	live := droppedColumnLive(ColumnDependent{Kind: "generated", Name: "a_len", Table: "public.t"})
	live.Tables["public.t"].Columns["a_len"] = &LiveColumn{Type: "integer", Generated: "length(a)", Nullable: true}
	d := droppedColumnYAML()
	statementOrder(t, Render(PlanWith(live, d, Options{Unsafe: true})), `alter table "public"."t" drop column "a_len";`, `alter table "public"."t" drop column "a";`)

	live.Views = map[string]bool{"public.v": true}
	live.ColumnDependents["public.t.a_len"] = []ColumnDependent{{Kind: "view", Name: "public.v", Table: "public.v"}}
	p := PlanWith(live, d, Options{Unsafe: true})
	warned := []string{}
	for _, w := range p.Warnings {
		warned = append(warned, w.Message)
	}
	all := strings.Join(warned, "\n")
	if strings.Contains(Render(p), "drop column") || !strings.Contains(all, "dropping column public.t.a_len: view public.v still uses it") || !strings.Contains(all, "dropping column public.t.a: generated column a_len still uses it, and PostgreSQL rejects the drop while it exists, so the column is kept; --prune drops it first") {
		t.Errorf("a generated column that stays keeps the column it uses, got:\n%s\n%s", all, Render(p))
	}
	statementOrder(t, Render(PlanWith(live, d, Options{Unsafe: true, Prune: true})), `drop view "public"."v";`, `drop column "a_len";`, `drop column "a";`)
}
