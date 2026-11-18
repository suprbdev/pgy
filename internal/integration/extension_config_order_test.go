package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
)

// TestIntegrationConfigRowsBeforeTheExtensionFillsThem declares the rows
// of an extension's configuration table that the extension also fills
// itself when an index is created, as pg_search fills its type modifier
// cache: made by hand here, with an event trigger standing for the
// extension. The rows and the sequence past them go first, so creating the
// index finds its row instead of taking key 1 from the sequence, and the
// plan converges.
func TestIntegrationConfigRowsBeforeTheExtensionFillsThem(t *testing.T) {
	pool := freshDatabase(t, connect(t), "pgytest_config_rows_order")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		create extension hstore schema public;
		create table public.cache (id serial primary key, tag text not null unique);
		alter extension hstore add table public.cache;
		alter extension hstore add sequence public.cache_id_seq;
		update pg_extension set extconfig = array['public.cache'::regclass::oid, 'public.cache_id_seq'::regclass::oid], extcondition = array['', ''] where extname = 'hstore';
		create function public.note_index() returns event_trigger language plpgsql as $$
		begin
			insert into public.cache (tag) select 'idx:' || c.object_identity from pg_event_trigger_ddl_commands() c
			where c.command_tag = 'CREATE INDEX' and not exists (select from public.cache k where k.tag = 'idx:' || c.object_identity);
		end $$;
		create event trigger note_index on ddl_command_end when tag in ('CREATE INDEX') execute function public.note_index();`); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(`
extensions:
  - {name: hstore, schema: public}
schema public:
  table cache:
    data:
      key: [id]
      rows:
        - {id: 1, tag: "idx:public.docs_body_idx"}
        - {id: 2, tag: gone}
  table docs:
    columns:
      id: {type: integer, primaryKey: true}
      body: {type: text}
    indexes:
      docs_body_idx: {columns: [body]}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	plan := diff.Plan(introspect(t, pool, d), d, false)
	sql := diff.Render(plan)
	if rows, table := strings.Index(sql, `insert into "public"."cache"`), strings.Index(sql, `create table if not exists "public"."docs"`); rows < 0 || table < 0 || rows > table {
		t.Fatalf("the configuration rows go before the rest of the plan:\n%s", sql)
	}
	if s, err := applyAsMigrations(ctx, pool, plan); err != nil {
		t.Fatalf("apply %q: %v\n%s", s, err, sql)
	}
	if again := diff.Render(diff.Plan(introspect(t, pool, d), d, false)); again != "" {
		t.Errorf("plan did not converge:\n%s", again)
	}
	var tags []string
	if err := pool.QueryRow(ctx, `select array_agg(id || '=' || tag order by id) from public.cache`).Scan(&tags); err != nil || strings.Join(tags, " ") != "1=idx:public.docs_body_idx 2=gone" {
		t.Errorf("cache = %v (%v)", tags, err)
	}
	var id int
	if err := pool.QueryRow(ctx, `insert into public.cache (tag) values ('next') returning id`).Scan(&id); err != nil || id != 3 {
		t.Errorf("the next row the extension adds gets id %d (%v), want 3", id, err)
	}
}
