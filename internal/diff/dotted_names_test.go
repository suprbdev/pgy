package diff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pgy/internal/schema"
)

func loadYAML(t *testing.T, text string) *schema.Database {
	t.Helper()
	p := filepath.Join(t.TempDir(), "schema.yml")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := schema.LoadAndMerge([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

const dottedNamesYAML = `
schema app:
  type typ:
    type: composite
    attributes:
      at.tr: {type: integer, comment: attr}
      b: {type: text}
  domain dom:
    type: integer
    check: VALUE > 0
    constraintName: do.chk
  sequence sq:
    ownedBy: app.t.a.b
  function trg():
    returns: trigger
    language: plpgsql
    body: begin return new; end
  table parent:
    columns:
      p.id: {type: integer}
      p.name: {type: text, nullable: true, unique: true}
    primaryKey: [p.id]
  table pt:
    columns:
      r.k: {type: integer}
    partitionBy: {type: range, columns: [r.k]}
  table t:
    columns:
      id: {type: integer}
      a.b: {type: integer, default: "1", grants: {public: [select]}, comment: dotted}
      c.d: {type: text, nullable: true}
      g.h: {type: integer, nullable: true}
    primaryKey:
      t.pk: {columns: [id, a.b]}
    indexes:
      t.idx:
        columns: [c.d, a.b desc]
        include: [g.h]
        comment: idx
    foreignKeys:
      t.fk:
        columns: [g.h]
        references: {table: app.parent, columns: [p.id]}
        comment: fk
    constraints:
      t.chk:
        type: check
        expression: '"a.b" > 0'
        comment: chk
      t.uq:
        type: unique
        columns: [c.d]
    triggers:
      t.trg:
        timing: before
        events: [update of a.b]
        level: row
        procedure: app.trg()
        comment: trg
      tt.trg:
        timing: after
        events: [update]
        level: statement
        referencing: {old: old.rows, new: new.rows}
        procedure: app.trg()
    rules:
      t.rule:
        event: delete
        do: NOTHING
        comment: rule
    statistics:
      t.stat:
        columns: [a.b, c.d]
    clusterOn: t.idx
    replicaIdentity: index t.idx
    rowLevelSecurity: true
    policies:
      t.pol:
        using: ("a.b" > 0)
        comment: pol
  view vw:
    query: select "a.b" from app.t
    columnComments:
      a.b: view col
`

func TestDottedNamesAreSingleIdentifiers(t *testing.T) {
	sql := Render(Plan(emptyLive(), loadYAML(t, dottedNamesYAML), false))
	for _, want := range []string{
		`create type "app"."typ" as ("at.tr" integer, "b" text);`,
		`create domain "app"."dom" as integer constraint "do.chk" check (VALUE > 0);`,
		`alter sequence "app"."sq" owned by "app"."t"."a.b";`,
		`create table if not exists "app"."parent" ("p.id" integer not null, "p.name" text);`,
		`("r.k" integer not null) partition by range ("r.k");`,
		`create table if not exists "app"."t" ("id" integer not null, "a.b" integer not null default 1, "c.d" text, "g.h" integer);`,
		`create index if not exists "t.idx" on "app"."t"("c.d", "a.b" desc) include ("g.h");`,
		`create trigger "t.trg" BEFORE UPDATE OF "a.b" on "app"."t"`,
		`create trigger "tt.trg" AFTER UPDATE on "app"."t" referencing old table as "old.rows" new table as "new.rows"`,
		`create or replace rule "t.rule" as on delete to "app"."t" do NOTHING;`,
		`alter table "app"."parent" add primary key ("p.id");`,
		`unique ("p.name");`,
		`alter table "app"."t" add constraint "t.pk" primary key ("id", "a.b");`,
		`alter table "app"."t" add constraint "t.chk" check ("a.b" > 0);`,
		`alter table "app"."t" add constraint "t.uq" unique ("c.d");`,
		`create statistics "app"."t.stat" on "a.b", "c.d" from "app"."t";`,
		`alter table "app"."t" cluster on "t.idx";`,
		`alter table "app"."t" replica identity using index "t.idx";`,
		`alter table "app"."t" add constraint "t.fk" foreign key ("g.h") references "app"."parent"("p.id");`,
		`create policy "t.pol" on "app"."t" using (("a.b" > 0));`,
		`grant select ("a.b") on table "app"."t" to public;`,
		`comment on column "app"."typ"."at.tr" is 'attr';`,
		`comment on column "app"."t"."a.b" is 'dotted';`,
		`comment on index "app"."t.idx" is 'idx';`,
		`comment on constraint "t.fk" on "app"."t" is 'fk';`,
		`comment on constraint "t.chk" on "app"."t" is 'chk';`,
		`comment on trigger "t.trg" on "app"."t" is 'trg';`,
		`comment on rule "t.rule" on "app"."t" is 'rule';`,
		`comment on policy "t.pol" on "app"."t" is 'pol';`,
		`comment on column "app"."vw"."a.b" is 'view col';`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
	for _, bad := range []string{`"a"."b"`, `"c"."d"`, `"g"."h"`, `"p"."id"`, `"r"."k"`, `"at"."tr"`, `"t"."pk"`, `"t"."chk"`, `"t"."fk"`, `"t"."trg"`, `"t"."pol"`, `"t"."rule"`, `"do"."chk"`, `"old"."rows"`} {
		if strings.Contains(sql, bad) {
			t.Errorf("%s: a single name was split at its dot in\n%s", bad, sql)
		}
	}
}

func TestDottedNamesInAltersAndDrops(t *testing.T) {
	live := liveWithTablePK("app.t", map[string]*LiveColumn{
		"id":  {Type: "integer"},
		"o.c": {Type: "integer", Nullable: true},
		"x.y": {Type: "text", Nullable: true},
		"z.z": {Type: "integer", Nullable: true},
	})
	live.Schemas["app"] = true
	lt := live.Tables["app.t"]
	lt.ConstraintDefs = map[string]string{"old.chk": "CHECK ((id > 0))", "gone.chk": "CHECK ((id > 1))"}
	lt.Constraints["old.chk"], lt.Constraints["gone.chk"] = true, true
	lt.Triggers["gone.trg"] = ""
	lt.RLSEnabled = true
	lt.Policies = map[string]*LivePolicy{"gone.pol": {Cmd: "all", Using: "true"}}
	lt.Indexes["old.idx"] = true
	lt.IndexDefs = map[string]*LiveIndex{"old.idx": {Method: "btree", Keys: []string{"id"}}}
	d := loadYAML(t, `
schema app:
  table t:
    columns:
      id: {type: integer}
      n.c: {type: integer, nullable: true, renamedFrom: o.c}
      x.y: {type: bigint, nullable: true, default: "0", comment: changed}
      a.d: {type: text, nullable: true}
    primaryKey: [id]
    indexes:
      new.idx: {columns: [id], renamedFrom: old.idx}
    constraints:
      new.chk: {type: check, expression: id > 0, renamedFrom: old.chk}
    rowLevelSecurity: true
    policies:
      new.pol: {using: id > 0}
`)
	sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true}))
	for _, want := range []string{
		`alter table "app"."t" rename column "o.c" to "n.c";`,
		`alter index "app"."old.idx" rename to "new.idx";`,
		`alter table "app"."t" rename constraint "old.chk" to "new.chk";`,
		`alter table "app"."t" add column "a.d" text;`,
		`alter table "app"."t" alter column "x.y" type bigint`,
		`alter table "app"."t" alter column "x.y" set default 0;`,
		`comment on column "app"."t"."x.y" is 'changed';`,
		`alter table "app"."t" drop column "z.z";`,
		`alter table "app"."t" drop constraint "gone.chk";`,
		`drop trigger "gone.trg" on "app"."t";`,
		`create policy "new.pol" on "app"."t" using (id > 0);`,
		`drop policy "gone.pol" on "app"."t";`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
}

func TestRecreateDependentsOfDottedColumn(t *testing.T) {
	live := liveWithTable("app.t", map[string]*LiveColumn{"a.b": {Type: "integer"}})
	live.Schemas["app"] = true
	live.Views = map[string]bool{"app.v": true, "app.vv": true}
	live.ColumnDependents = map[string][]ColumnDependent{
		"app.t.a.b": {{Kind: "view", Name: "app.v", Table: "app.v"}},
		"app.v.a.b": {{Kind: "view", Name: "app.vv", Table: "app.vv"}},
	}
	d := &schema.Database{
		Tables: map[string]*schema.Table{"app.t": {Name: "t", Columns: map[string]*schema.Column{"a.b": {Type: "bigint"}}}},
		Views: map[string]*schema.View{
			"app.v":  {Name: "v", Query: `select "a.b" from app.t`},
			"app.vv": {Name: "vv", Query: `select "a.b" from app.v`},
		},
	}
	_, _, pre, _, _, warns := recreateColumnDependents(live, d, Options{Unsafe: true})
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	want := `drop view "app"."vv";|drop view "app"."v";`
	if got := strings.Join(pre["app.t"], "|"); got != want {
		t.Errorf("pre:\n got %s\nwant %s", got, want)
	}
}

func TestColumnRelation(t *testing.T) {
	for ref, want := range map[string]string{
		"app.t.id":    "app.t",
		"app.t.a.b":   "app.t",
		"app.t.a.b.c": "app.t",
	} {
		if got := ColumnRelation(ref); got != want {
			t.Errorf("ColumnRelation(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestNumericColumnKeysAreNotPruned(t *testing.T) {
	live := liveWithTablePK("app.pivot", map[string]*LiveColumn{
		"id":   {Type: "integer"},
		"2024": {Type: "numeric", Nullable: true},
		"2025": {Type: "numeric", Nullable: true},
		"null": {Type: "text", Nullable: true},
		"true": {Type: "boolean", Nullable: true},
	})
	live.Schemas["app"] = true
	d := loadYAML(t, `
schema app:
  table pivot:
    columns:
      id: {type: integer}
      2024: {type: numeric, nullable: true}
      2025: {type: numeric, nullable: true}
      null: {type: text, nullable: true}
      true: {type: boolean, nullable: true}
    primaryKey: [id]
`)
	if sql := Render(PlanWith(live, d, Options{Unsafe: true, Prune: true})); sql != "" {
		t.Errorf("unquoted numeric, null and boolean column keys must match the live columns:\n%s", sql)
	}
	if sql := Render(Plan(emptyLive(), d, false)); !strings.Contains(sql, `("id" integer not null, "2024" numeric, "2025" numeric, "null" text, "true" boolean)`) {
		t.Errorf("columns missing from the create:\n%s", sql)
	}
}

func TestOptionKeyNullKeepsTheOptions(t *testing.T) {
	d := loadYAML(t, `
servers:
  files: {wrapper: file_fdw}
schema f:
  foreign table csv:
    server: files
    options: {filename: /tmp/a.csv, format: csv, null: "NULL"}
    columns:
      a: {type: text, nullable: true, options: {null: x}}
`)
	sql := Render(Plan(emptyLive(), d, false))
	for _, want := range []string{
		`options ("filename" '/tmp/a.csv', "format" 'csv', "null" 'NULL');`,
		`alter column "a" options (add "null" 'x');`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
}
