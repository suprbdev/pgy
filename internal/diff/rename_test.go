package diff

import (
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func renameLive() *Live {
	live := emptyLive()
	live.Schemas["app"] = true
	lt := newLiveTable()
	lt.Columns["id"] = &LiveColumn{Type: "integer"}
	lt.Columns["qty"] = &LiveColumn{Type: "integer", Default: "0"}
	lt.Constraints["items_qty_check"] = true
	lt.ConstraintDefs["items_qty_check"] = "CHECK ((qty >= 0))"
	lt.Indexes["items_qty_idx"] = true
	lt.IndexDefs = map[string]*LiveIndex{"items_qty_idx": {Method: "btree", Keys: []string{"qty"}, Descending: []bool{false}, NullsFirst: []bool{false}, Predicate: "(qty > 0)"}}
	lt.Triggers["items_trg"] = "CREATE TRIGGER items_trg BEFORE UPDATE OF qty ON app.items FOR EACH ROW EXECUTE FUNCTION app.touch()"
	live.Tables["app.items"] = lt
	ref := newLiveTable()
	ref.Columns["item_qty"] = &LiveColumn{Type: "integer"}
	ref.Constraints["ref_fkey"] = true
	ref.ConstraintDefs["ref_fkey"] = "FOREIGN KEY (item_qty) REFERENCES app.items(qty)"
	live.Tables["app.ref"] = ref
	live.RelComments = map[string]string{"app.items": "items"}
	return live
}

func renameDB() *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{
		"app.products": {Name: "products", RenamedFrom: "items", Comment: "items",
			Columns: map[string]*schema.Column{
				"id":       {Type: "int"},
				"quantity": {Type: "int", Default: "0", RenamedFrom: "qty"},
			},
			Indexes:     []*schema.Index{{Name: "products_qty_idx", RenamedFrom: "items_qty_idx", Columns: []string{"quantity"}, Where: "quantity > 0"}},
			Constraints: []*schema.Constraint{{Name: "products_qty_check", RenamedFrom: "items_qty_check", Type: "check", Expression: "quantity >= 0"}},
		},
	}}
}

func TestRenameEmitsOnlyRenames(t *testing.T) {
	live := renameLive()
	p := PlanWith(live, renameDB(), Options{Unsafe: true})
	want := []string{
		`alter table "app"."items" rename to "products";`,
		`alter table "app"."products" rename column "qty" to "quantity";`,
		`alter index "app"."items_qty_idx" rename to "products_qty_idx";`,
		`alter table "app"."products" rename constraint "items_qty_check" to "products_qty_check";`,
	}
	if got := strings.Join(p.Creates, "\n"); got != strings.Join(want, "\n") || len(p.Alters) != 0 || len(p.Drops) != 0 {
		t.Errorf("unexpected plan:\n%s", Render(p))
	}
	if live.Tables["app.items"] == nil || live.Tables["app.products"] != nil {
		t.Error("planning must not modify the live snapshot")
	}
	r, _ := applyRenames(live, renameDB())
	if def := r.Tables["app.ref"].ConstraintDefs["ref_fkey"]; def != "FOREIGN KEY (item_qty) REFERENCES app.products(quantity)" {
		t.Errorf("referencing foreign key not rewritten: %s", def)
	}
	if def := r.Tables["app.products"].Triggers["items_trg"]; def != "CREATE TRIGGER items_trg BEFORE UPDATE OF quantity ON app.products FOR EACH ROW EXECUTE FUNCTION app.touch()" {
		t.Errorf("trigger not rewritten: %s", def)
	}
}

func TestRenameNoOpOnceApplied(t *testing.T) {
	live := Simulate(renameLive(), renameDB())
	if live.Tables["app.products"] == nil || live.Tables["app.products"].Columns["quantity"] == nil {
		t.Fatal("Simulate must apply renames")
	}
	if sql := Render(PlanWith(live, renameDB(), Options{Unsafe: true})); sql != "" {
		t.Errorf("renamedFrom must be a no-op once applied, got:\n%s", sql)
	}
	both := renameLive()
	both.Tables["app.products"] = newLiveTable()
	if p := PlanWith(both, renameDB(), Options{}); strings.Contains(Render(p), "rename to") {
		t.Errorf("no rename when the new name already exists:\n%s", Render(p))
	}
}

func TestRenameEnumLabelsAndRoutines(t *testing.T) {
	live := emptyLive()
	live.Schemas["app"] = true
	live.Types["app.mood"] = true
	live.EnumLabels["app.mood"] = []string{"ok", "meh"}
	live.Functions["app.twice(x integer)"] = true
	live.FunctionDefs[normalizeFunctionSignature("app.twice(x integer)")] = &LiveFunction{Kind: "f", Signature: "app.twice(x integer)", Body: "select x * 2", Volatility: "immutable", Security: "invoker", Parallel: "unsafe", Cost: 100}
	d := &schema.Database{Tables: map[string]*schema.Table{},
		Types:     map[string]*schema.TypeDef{"app.feeling": {Name: "feeling", Kind: "enum", Labels: []string{"ok", "so_so"}, RenamedFrom: "mood", RenamedLabels: map[string]string{"so_so": "meh"}}},
		Functions: map[string]*schema.Function{"app.double_it(x int)": {Name: "double_it", ArgsSig: "(x int)", Returns: "int", Language: "sql", Volatility: "immutable", Body: "select x * 2", RenamedFrom: "twice"}},
	}
	got := Render(Plan(live, d, false))
	for _, want := range []string{
		`alter type "app"."mood" rename to "feeling";`,
		`alter type "app"."feeling" rename value 'meh' to 'so_so';`,
		`alter function "app"."twice"(x int) rename to "double_it";`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "create") || strings.Contains(got, "add value") {
		t.Errorf("renames must not recreate:\n%s", got)
	}
}

func TestRenameThenAlter(t *testing.T) {
	d := renameDB()
	d.Tables["app.products"].Columns["quantity"].Type = "bigint"
	p := PlanWith(renameLive(), d, Options{Unsafe: true})
	if !findCreate(p, `alter table "app"."products" rename column "qty" to "quantity";`) || !findAlter(p, `alter table "app"."products" alter column "quantity" type bigint;`) {
		t.Errorf("expected a rename followed by a type change:\n%s", Render(p))
	}
}
