package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func pubDB(p *schema.Publication) *schema.Database {
	return &schema.Database{Tables: map[string]*schema.Table{}, Publications: map[string]*schema.Publication{"app_pub": p}}
}

func TestPublicationCreate(t *testing.T) {
	p := Plan(emptyLive(), pubDB(&schema.Publication{
		Tables: []*schema.PublicationTable{{Table: "public.orders", Columns: []string{"id", "status"}, Where: "status <> 'draft'"}, {Table: "public.users"}},
		Schemas: []string{"billing"}, Publish: []string{"update", "insert"}, ViaPartitionRoot: schema.BoolPtr(true), Comment: "cdc",
	}), false)
	want := `create publication "app_pub" for table "public"."orders" ("id", "status") where (status <> 'draft'), table "public"."users", tables in schema "billing" with (publish = 'insert, update', publish_via_partition_root = true);`
	if !findAlter(p, want) {
		t.Errorf("want %q, got %v", want, p.Alters)
	}
	if !findAlter(p, `comment on publication "app_pub" is 'cdc';`) {
		t.Errorf("expected comment, got %v", p.Alters)
	}
	if p := Plan(emptyLive(), pubDB(&schema.Publication{AllTables: true}), false); !findAlter(p, `create publication "app_pub" for all tables;`) {
		t.Errorf("expected all tables publication, got %v", p.Alters)
	}
}

func livePub(lp *LivePublication) *Live {
	l := emptyLive()
	l.Publications = map[string]*LivePublication{"app_pub": lp}
	return l
}

func TestPublicationSameSkippedAndAltered(t *testing.T) {
	lp := func() *LivePublication {
		return &LivePublication{Publish: []string{"insert", "update"}, ViaRoot: true, Comment: "cdc", Schemas: []string{"billing"},
			Tables: map[string]LivePublicationTable{"public.orders": {Columns: []string{"id", "status"}, Where: "(status <> 'draft'::text)"}, "public.users": {}}}
	}
	desired := &schema.Publication{
		Tables: []*schema.PublicationTable{{Table: "public.orders", Columns: []string{"status", "id"}, Where: "status <> 'draft'"}, {Table: "public.users"}},
		Schemas: []string{"billing"}, Publish: []string{"insert", "update"}, ViaPartitionRoot: schema.BoolPtr(true), Comment: "cdc",
	}
	if sql := Render(Plan(livePub(lp()), pubDB(desired), false)); sql != "" {
		t.Errorf("same publication must not diff, got:\n%s", sql)
	}
	changed := &schema.Publication{Tables: []*schema.PublicationTable{{Table: "public.users"}}, Publish: []string{"insert", "update", "delete", "truncate"}}
	p := Plan(livePub(lp()), pubDB(changed), false)
	want := []string{
		`alter publication "app_pub" set table "public"."users";`,
		`alter publication "app_pub" set (publish = 'insert, update, delete, truncate');`,
	}
	if len(p.Alters) != 2 || p.Alters[0] != want[0] || p.Alters[1] != want[1] {
		t.Errorf("want %v, got %v", want, p.Alters)
	}
	p = Plan(livePub(lp()), pubDB(&schema.Publication{}), false)
	if len(p.Alters) != 2 || p.Alters[0] != `alter publication "app_pub" drop table "public"."orders", "public"."users";` || p.Alters[1] != `alter publication "app_pub" drop tables in schema "billing";` {
		t.Errorf("emptying a publication drops its members, got %v", p.Alters)
	}
}

func TestPublicationAllTablesSwitchAndPruneUnsafe(t *testing.T) {
	live := livePub(&LivePublication{Publish: []string{"insert", "update", "delete", "truncate"}, Tables: map[string]LivePublicationTable{"public.users": {}}})
	live.Publications["stray"] = &LivePublication{AllTables: true}
	d := pubDB(&schema.Publication{AllTables: true})
	if sql := Render(Plan(live, d, false)); sql != "" {
		t.Errorf("switching to FOR ALL TABLES requires --unsafe, got:\n%s", sql)
	}
	p := PlanWith(live, d, Options{Unsafe: true, Prune: true})
	if len(p.Alters) != 2 || p.Alters[0] != `drop publication "app_pub";` || p.Alters[1] != `create publication "app_pub" for all tables;` {
		t.Errorf("expected drop + create, got %v", p.Alters)
	}
	if len(p.Drops) != 1 || p.Drops[0] != `drop publication "stray";` {
		t.Errorf("prune should drop the undeclared publication, got %v", p.Drops)
	}
}
