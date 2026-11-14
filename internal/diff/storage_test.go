package diff

import (
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func storageTable() *schema.Table {
	return &schema.Table{
		Name:        "t",
		ColumnOrder: []string{"a", "b"},
		Unlogged:    schema.BoolPtr(true),
		With:        map[string]string{"fillfactor": "70", "autovacuum_enabled": "false"},
		Columns: map[string]*schema.Column{
			"a": {Type: "text", Collate: "C", Compression: "lz4"},
			"b": {Type: "text", Storage: "external"},
		},
	}
}

func TestTableStorageOptionsCreate(t *testing.T) {
	p := Plan(emptyLive(), &schema.Database{Tables: map[string]*schema.Table{"public.t": storageTable()}}, false)
	want := `create unlogged table if not exists "public"."t" ("a" text compression lz4 collate "C" not null, "b" text not null) with (autovacuum_enabled = false, fillfactor = 70);`
	if !findCreate(p, want) {
		t.Errorf("want %q, got %+v", want, p.Creates)
	}
	if !findAlter(p, `alter table "public"."t" alter column "b" set storage external;`) {
		t.Errorf("expected set storage, got %+v", p.Alters)
	}
}

func storageLive() *Live {
	live := liveWithTable("public.t", map[string]*LiveColumn{
		"a": {Type: "text", Collation: "C", Compression: "lz4", Storage: "extended"},
		"b": {Type: "text", Storage: "external"},
	})
	lt := live.Tables["public.t"]
	lt.Unlogged = true
	lt.Options = map[string]string{"fillfactor": "70", "autovacuum_enabled": "false"}
	return live
}

func TestTableStorageOptionsSameSkipped(t *testing.T) {
	if sql := Render(Plan(storageLive(), &schema.Database{Tables: map[string]*schema.Table{"public.t": storageTable()}}, true)); sql != "" {
		t.Errorf("same storage options must not diff, got:\n%s", sql)
	}
}

func TestTableStorageOptionsAltered(t *testing.T) {
	dt := storageTable()
	dt.Unlogged = schema.BoolPtr(false)
	dt.With = map[string]string{"fillfactor": "80"}
	dt.Columns["a"] = &schema.Column{Type: "text", Collate: "en-x-icu", Compression: "default", Storage: "main"}
	dt.Columns["b"] = &schema.Column{Type: "text"}
	d := &schema.Database{Tables: map[string]*schema.Table{"public.t": dt}}

	safe := Plan(storageLive(), d, false)
	for _, s := range safe.Alters {
		if findAlter(&PlanDiff{Alters: []string{s}}, "set logged") || findAlter(&PlanDiff{Alters: []string{s}}, " type ") {
			t.Errorf("SET LOGGED and collation changes are gated behind --unsafe, got %q", s)
		}
	}
	p := Plan(storageLive(), d, true)
	for _, want := range []string{
		`alter table "public"."t" alter column "a" type text collate "en-x-icu";`,
		`alter table "public"."t" alter column "a" set compression default;`,
		`alter table "public"."t" alter column "a" set storage main;`,
		`alter table "public"."t" set logged;`,
		`alter table "public"."t" set (fillfactor = 80);`,
		`alter table "public"."t" reset (autovacuum_enabled);`,
	} {
		if !findAlter(p, want) {
			t.Errorf("missing %q in %+v", want, p.Alters)
		}
	}
	if len(p.Alters) != 6 {
		t.Errorf("unmanaged storage on b must not diff; alters: %+v", p.Alters)
	}
}
