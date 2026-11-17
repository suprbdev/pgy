package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
)

func TestIntegrationDefinitionSpellingsIdempotent(t *testing.T) {
	assertYAMLIdempotent(t, map[string]string{
		"trigger_arguments": `
extensions: [moddatetime]
schema SCH:
  function note():
    returns: trigger
    language: plpgsql
    body: begin return new; end;
  table doc:
    columns:
      id: {type: bigint, primaryKey: true}
      title: {type: text}
      body: {type: text}
      tsv: {type: tsvector, nullable: true}
      updated_at: {type: timestamp, nullable: true}
      UpdatedAt: {type: timestamp, nullable: true}
    triggers:
      a_bare:
        timing: before
        events: [update]
        level: row
        procedure: moddatetime(updated_at)
      b_quoted_identifier:
        timing: before
        events: [update]
        level: row
        procedure: moddatetime("UpdatedAt")
      c_mixed:
        timing: before
        events: [insert, update]
        level: row
        procedure: tsvector_update_trigger(tsv, 'pg_catalog.english', title, body)
      d_dollar_and_numbers:
        timing: after
        events: [insert]
        level: row
        procedure: SCH.note($$doc$$, 007, 1.50, Upper)
      e_escape:
        timing: after
        events: [insert]
        level: row
        procedure: SCH.note(E'doc\tchanges', 'it''s', 'public.doc')
      f_catalog:
        timing: before
        events: [update]
        level: row
        procedure: pg_catalog.suppress_redundant_updates_trigger()
`,
		"exclusion_constraints": `
extensions: [btree_gist]
schema SCH:
  table booking:
    columns:
      id: {type: bigint, primaryKey: true}
      room_id: {type: int}
      label: {type: text}
      during: {type: tstzrange}
      price: {type: numeric, default: 0}
    constraints:
      a_no_using: {type: exclude, expression: "(room_id with =)"}
      b_default_opclass: {type: exclude, expression: "using gist (room_id gist_int4_ops with =, during with &&)"}
      c_default_range_opclass: {type: exclude, expression: "using gist (room_id with =, during range_ops with &&)"}
      d_catalog_operator: {type: exclude, expression: "using gist (room_id with operator(pg_catalog.=), during with &&)"}
      e_spacing: {type: exclude, expression: "USING GIST ( room_id WITH = , during WITH && ) WHERE ( price > 2 )"}
      f_with_key: {type: exclude, expression: "using gist (room_id with =, label with <>)", with: {fillfactor: 80}}
      g_with_expression: {type: exclude, expression: "using gist (label with =, during with &&) with (fillfactor = 70)", deferrable: true}
      h_expression_key: {type: exclude, expression: "using gist (lower(label) with =, (room_id + 1) with =) where (price between -1 and 5)"}
`,
		"expressions": `
schema SCH:
  table t:
    columns:
      id: {type: uuid, primaryKey: true, default: pg_catalog.gen_random_uuid()}
      name: {type: text}
      lat: {type: float8, nullable: true}
      n: {type: int, default: "cast('5' as integer)"}
      day: {type: date, default: "cast(now() as date)"}
      n_text: {type: text, generated: "cast(n as text)"}
    constraints:
      t_cast: {type: check, expression: "cast(n as text) <> name"}
      t_between: {type: check, expression: "lat between -90 and 90"}
      t_not_between: {type: check, expression: "n not between -5 and -1"}
      t_catalog: {type: check, expression: "pg_catalog.length(name) > 0"}
      t_literal: {type: check, expression: "name <> 'public.name'"}
    indexes:
      t_cast_idx: {columns: ["cast(n as text)"], where: "lat between -90 and 90"}
    policies:
      t_visible: {using: "cast(n as bigint) > 0 and lat between -1 and 1"}
`,
	})
}

func loadSpellingYAML(t *testing.T, y string) *schema.Database {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.yml")
	if err := os.WriteFile(p, []byte(y), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestIntegrationDictionaryTemplateFromExtension(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	d := loadSpellingYAML(t, strings.ReplaceAll(`
extensions: [unaccent, dict_int]
schema SCH:
  text search dictionary my_unaccent:
    template: unaccent
    options: {rules: unaccent}
  text search dictionary int4:
    template: intdict_template
    options: {maxlen: 4}
  text search dictionary plain:
    template: intdict_template
`, "SCH", sch))
	if s, err := applyAsMigrations(ctx, pool, diff.Plan(introspect(t, pool, d), d, true)); err != nil {
		t.Fatalf("apply %q: %v", s, err)
	}
	plan := diff.Plan(introspect(t, pool, d), d, true)
	if sql := diff.Render(plan); sql != "" {
		t.Errorf("second plan not empty:\n%s", sql)
	}
	for _, w := range plan.Warnings {
		if strings.Contains(w.Message, "template changed") {
			t.Errorf("a template named without its schema is not a changed template: %s", w.Message)
		}
	}

	live := introspect(t, pool, d)
	if err := diff.IntrospectDetails(ctx, pool, live); err != nil {
		t.Fatal(err)
	}
	res, err := pull.Build(live, pull.Options{Schemas: []string{sch}, Single: true})
	if err != nil {
		t.Fatal(err)
	}
	pulled := loadSpellingYAML(t, string(res.Files[0].Content))
	if sql := diff.Render(diff.Plan(live, pulled, true)); sql != "" {
		t.Errorf("pulled YAML must diff clean:\n%s\n%s", res.Files[0].Content, sql)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`alter text search dictionary %q.plain (maxlen = 3, rejectlong = true)`, sch)); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`alter text search dictionary %q."plain" (maxlen, rejectlong);`, sch)
	if sql := diff.Render(diff.Plan(introspect(t, pool, pulled), pulled, true)); !strings.Contains(sql, want) {
		t.Errorf("options added to a dictionary pulled without any must be removed; want %s in:\n%s\n%s", want, sql, res.Files[0].Content)
	}
}

func TestIntegrationDefinitionDriftRepaired(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	sch := freshSchema(t, pool)
	d := loadSpellingYAML(t, strings.ReplaceAll(`
extensions: [btree_gist]
schema SCH:
  function note():
    returns: trigger
    language: plpgsql
    body: begin return new; end;
  table booking:
    columns:
      id: {type: bigint, primaryKey: true}
      room_id: {type: int}
      kind: {type: text}
    constraints:
      booking_room: {type: exclude, expression: "using gist (room_id gist_int4_ops with =) with (fillfactor = 80)"}
      booking_kind: {type: check, expression: "kind <> 'public.kind'"}
    triggers:
      booking_note:
        timing: after
        events: [insert]
        level: row
        procedure: SCH.note(room_id, 'public.booking')
`, "SCH", sch))
	apply := func() {
		t.Helper()
		if s, err := applyAsMigrations(ctx, pool, diff.Plan(introspect(t, pool, d), d, true)); err != nil {
			t.Fatalf("apply %q: %v", s, err)
		}
		if sql := diff.Render(diff.Plan(introspect(t, pool, d), d, true)); sql != "" {
			t.Fatalf("plan after applying is not empty:\n%s", sql)
		}
	}
	apply()

	for _, drift := range []string{
		`alter index %[1]q.booking_room set (fillfactor = 50)`,
		`drop trigger booking_note on %[1]q.booking`,
		`create trigger booking_note after insert on %[1]q.booking for each row execute function %[1]q.note('room_id', 'booking')`,
		`alter table %[1]q.booking drop constraint booking_kind`,
		`alter table %[1]q.booking add constraint booking_kind check (kind <> 'kind')`,
	} {
		if _, err := pool.Exec(ctx, fmt.Sprintf(drift, sch)); err != nil {
			t.Fatal(err)
		}
	}
	sql := diff.Render(diff.Plan(introspect(t, pool, d), d, true))
	for _, want := range []string{
		fmt.Sprintf(`alter index %q."booking_room" set (fillfactor = 80);`, sch),
		`drop trigger "booking_note"`,
		`execute procedure ` + sch + `.note(room_id, 'public.booking');`,
		`drop constraint "booking_kind"`,
		`check (kind <> 'public.kind')`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, `drop constraint "booking_room"`) {
		t.Errorf("a storage parameter must change in place:\n%s", sql)
	}
	apply()
}
