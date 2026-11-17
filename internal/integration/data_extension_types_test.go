package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/pull"
	"github.com/suprbdev/pgy/internal/schema"
	"github.com/suprbdev/pgy/internal/validate"
)

const dataExtensionTypes = `
extensions:
  - {name: citext, schema: public}
  - {name: hstore, schema: public}
  - {name: isn, schema: public}
  - {name: seg, schema: public}
schema app:
  domain book_id:
    type: isbn13
  domain span:
    type: seg
  domain must:
    type: text
    notNull: true
  domain tags:
    type: hstore
  domain price:
    type: "numeric(6,2)"
  type attr:
    type: composite
    attributes: {name: text, props: hstore}
  type edition:
    type: composite
    attributes: {n: int, code: app.book_id}
    dependsOn: [domain app.book_id]
  table book:
    dependsOn: [domain app.book_id, domain app.span, domain app.must, domain app.tags, domain app.price, type app.attr, type app.edition]
    columns:
      id: {type: isbn13}
      alias: {type: app.book_id, nullable: true}
      editions: {type: "app.book_id[]", nullable: true}
      shelf: {type: app.span, nullable: true}
      status: {type: app.must, default: "'new'"}
      cost: {type: app.price, nullable: true}
      attrs: {type: hstore, nullable: true}
      labels: {type: app.tags, nullable: true}
      history: {type: "hstore[]", nullable: true}
      extra: {type: app.attr, nullable: true}
      first: {type: app.edition, nullable: true}
      meta: {type: jsonb, nullable: true}
      title: {type: citext}
    primaryKey: [id]
    data:
      rows:
        - {id: 978-0-306-40615-7, title: First}
        - id: "9781861972712"
          alias: "9781861972712"
          editions: ["9780306406157", "9780131103627"]
          shelf: 1 .. 2
          status: old
          cost: 10
          attrs: seats=>1, price=>0
          labels: k=>v
          history: [a=>1, "b=>2, c=>\"x y\""]
          extra: {name: x, props: k=>v}
          first: (1,9780306406157)
          meta: {a: [1, 2]}
          title: Second
        - {id: 978-0-13-110362-7, alias: null, shelf: null, attrs: null, history: [], title: Third}
`

// convergeDatabase applies d to pool and requires the next plan, with
// --unsafe --prune, to be empty. It returns the SQL it applied.
func convergeDatabase(t *testing.T, pool *pgxpool.Pool, d *schema.Database) string {
	t.Helper()
	opts := diff.Options{Unsafe: true, Prune: true}
	plan := diff.PlanWith(introspect(t, pool, d), d, opts)
	applyPlan(t, pool, plan)
	if again := diff.Render(diff.PlanWith(introspect(t, pool, d), d, opts)); again != "" {
		t.Fatalf("plan did not converge:\n%s\nafter applying:\n%s", again, diff.Render(plan))
	}
	return diff.Render(plan)
}

func rowTexts(t *testing.T, pool *pgxpool.Pool, query string) []string {
	t.Helper()
	r, err := pool.Query(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out := []string{}
	for r.Next() {
		var s string
		if err := r.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func dataError(t *testing.T, pool *pgxpool.Pool, d *schema.Database) string {
	t.Helper()
	ctx := context.Background()
	live, err := diff.Introspect(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := diff.IntrospectData(ctx, pool, live, d); err != nil {
		return err.Error()
	}
	return ""
}

// TestIntegrationDataExtensionTypes declares rows in columns of extension
// types and of domains over them (isn and seg have no binary input
// function, which jsonb_populate_record's domain check needs), pulls the
// table with its rows and rebuilds it elsewhere.
func TestIntegrationDataExtensionTypes(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	src := freshDatabase(t, admin, "pgytest_data_ext_src")
	d := loadYAML(t, "app", dataExtensionTypes)
	convergeDatabase(t, src, d)

	rows, err := diff.TableRows(ctx, src, "app.book", false)
	if err != nil {
		t.Fatal(err)
	}
	live := introspectAll(t, src)
	res, err := pull.Build(live, pull.Options{Data: map[string][]map[string]any{"app.book": rows}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := []string{}
	all := ""
	for _, f := range res.Files {
		p := filepath.Join(dir, f.Name)
		if err := os.WriteFile(p, f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
		all += string(f.Content)
	}
	for _, want := range []string{
		`attrs: '"price"=>"0", "seats"=>"1"'`,
		`labels: '"k"=>"v"'`,
		`history: ['"a"=>"1"', '"b"=>"2", "c"=>"x y"']`,
		`extra: '(x,"""k""=>""v""")'`,
		`first: '(1,978-0-306-40615-7)'`,
		`meta: {a: [1, 2]}`,
		`alias: 978-1-86197-271-2`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("pulled YAML lacks %q:\n%s", want, all)
		}
	}
	for _, issue := range validate.Files(paths) {
		if issue.Severity == validate.Error {
			t.Errorf("pgy validate rejects pulled YAML: %s", issue)
		}
	}
	pulled, err := schema.LoadAndMerge(paths)
	if err != nil {
		t.Fatal(err)
	}
	opts := diff.Options{Unsafe: true, Prune: true}
	if sql := diff.Render(diff.PlanWith(introspect(t, src, pulled), pulled, opts)); sql != "" {
		t.Fatalf("pulled YAML must diff clean against its source:\n%s\n%s", sql, all)
	}
	dst := freshDatabase(t, admin, "pgytest_data_ext_dst")
	convergeDatabase(t, dst, pulled)
	const q = `select b::text from app.book b order by id`
	want, got := rowTexts(t, src, q), rowTexts(t, dst, q)
	if strings.Join(want, "\n") != strings.Join(got, "\n") {
		t.Errorf("rebuilt rows differ:\nsource:\n%s\nrebuilt:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
	if sql := diff.Render(diff.PlanWith(introspect(t, dst, d), d, opts)); sql != "" {
		t.Errorf("the original YAML must diff clean against the rebuilt database:\n%s", sql)
	}
}

func TestIntegrationDataExtensionTypeChanges(t *testing.T) {
	admin := connect(t)
	pool := freshDatabase(t, admin, "pgytest_data_ext_changes")
	convergeDatabase(t, pool, loadYAML(t, "app", dataExtensionTypes))
	changed := strings.NewReplacer(
		`alias: "9781861972712"`, `alias: "9780306406157"`,
		`shelf: 1 .. 2`, `shelf: 3 .. 4`,
		`attrs: seats=>1, price=>0`, `attrs: seats=>2, price=>0`,
		`extra: {name: x, props: k=>v}`, `extra: {name: y, props: k=>v}`,
		`meta: {a: [1, 2]}`, `meta: {a: [1, 3]}`,
	).Replace(dataExtensionTypes)
	sql := convergeDatabase(t, pool, loadYAML(t, "app", changed))
	for _, want := range []string{
		`"alias" = '9780306406157'`,
		`"shelf" = '3 .. 4'`,
		`"attrs" = 'seats=>2, price=>0'`,
		`"extra" = (select v from jsonb_to_record('{"v":{"name":"y","props":"k=\u003ev"}}'::jsonb) as x(v app.attr))`,
		`"meta" = '{"a":[1,3]}'::jsonb`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "insert into") || strings.Contains(sql, `"history"`) || strings.Contains(sql, `"title"`) {
		t.Errorf("only the changed columns are written:\n%s", sql)
	}
	const q = `select concat_ws(' | ', alias, shelf, attrs -> 'seats', (extra).name, (extra).props -> 'k', meta) from app.book where id = '9781861972712'`
	if got, want := strings.Join(rowTexts(t, pool, q), "\n"), `978-0-306-40615-7 | 3 .. 4 | 2 | y | v | {"a": [1, 3]}`; got != want {
		t.Errorf("row after the change:\n got %s\nwant %s", got, want)
	}
}

func TestIntegrationDataMapForScalarType(t *testing.T) {
	admin := connect(t)
	pool := freshDatabase(t, admin, "pgytest_data_ext_map")
	convergeDatabase(t, pool, loadYAML(t, "app", dataExtensionTypes))
	withMap := strings.Replace(dataExtensionTypes, `attrs: seats=>1, price=>0`, `attrs: {seats: "1", price: "0"}`, 1)
	for name, y := range map[string]string{
		"existing table": withMap,
		"new table":      strings.Replace(withMap, "table book:", "table volume:", 1),
	} {
		t.Run(name, func(t *testing.T) {
			msg := dataError(t, pool, loadYAML(t, "app", y))
			if !strings.Contains(msg, "row 2 gives column attrs a map, but its type hstore is not json, jsonb or a composite type") {
				t.Errorf("expected the map to be rejected with its row and column, got %q", msg)
			}
		})
	}
	if msg := dataError(t, pool, loadYAML(t, "app", strings.Replace(dataExtensionTypes, "table book:", "table volume:", 1))); msg != "" {
		t.Errorf("valid rows of a new table must pass: %s", msg)
	}
}

func TestIntegrationDataCaseInsensitiveKey(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	pool := freshDatabase(t, admin, "pgytest_data_ext_key")
	const base = `
extensions:
  - {name: citext, schema: public}
schema app:
  table plan:
    columns:
      code: {type: citext}
      name: {type: text}
    primaryKey: [code]
    data:
      rows: ROWS
`
	convergeDatabase(t, pool, loadYAML(t, "app", strings.Replace(base, "ROWS", `[{code: free, name: Free}, {code: pro, name: Pro}]`, 1)))
	sql := convergeDatabase(t, pool, loadYAML(t, "app", strings.Replace(base, "ROWS", `[{code: FREE, name: Free}, {code: pro, name: Pro}]`, 1)))
	if want := `update only "app"."plan" set "code" = 'FREE' where "code" = 'FREE';`; strings.TrimSpace(sql) != want {
		t.Errorf("a key written in another case is rewritten:\n got %s\nwant %s", sql, want)
	}
	if got := rowTexts(t, pool, `select code::text from app.plan order by 1`); strings.Join(got, ",") != "FREE,pro" {
		t.Errorf("keys = %v", got)
	}
	dup := strings.Replace(base, "ROWS", `[{code: FREE, name: Free}, {code: pro, name: Pro}, {code: Pro, name: Again}]`, 1)
	for name, y := range map[string]string{
		"existing table": dup,
		"new table":      strings.Replace(dup, "table plan:", "table tier:", 1),
	} {
		t.Run(name, func(t *testing.T) {
			msg := dataError(t, pool, loadYAML(t, "app", y))
			if !strings.Contains(msg, "rows 2 and 3 identify the same row: their keys (code: pro / Pro) are equal in the key's column types") {
				t.Errorf("expected rows with equal keys to be rejected, got %q", msg)
			}
		})
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from app.plan`).Scan(&n); err != nil || n != 2 {
		t.Errorf("expected 2 plans, got %d (%v)", n, err)
	}
}

// TestIntegrationDataKeyOffSearchPath matches rows on key types whose
// equality operator is in a schema the search_path does not list, where a
// bare = compares a citext key as text and finds no operator for an isbn13
// one. A session with that schema on its search_path gets the bare =.
func TestIntegrationDataKeyOffSearchPath(t *testing.T) {
	admin := connect(t)
	const name = "pgytest_data_key_path"
	pool := freshDatabase(t, admin, name)
	const base = `
extensions:
  - {name: citext, schema: ext}
  - {name: isn, schema: ext}
schema app:
  domain code:
    type: ext.citext
  table plan:
    dependsOn: [domain app.code]
    columns:
      code: {type: app.code}
      name: {type: text}
    primaryKey: [code]
    data:
      rows: PLANS
  table book:
    columns:
      id: {type: ext.isbn13}
      edition: {type: int}
      title: {type: text}
    primaryKey: [id, edition]
    data:
      rows: BOOKS
`
	load := func(plans, books string) *schema.Database {
		return loadYAML(t, "app", strings.NewReplacer("PLANS", plans, "BOOKS", books).Replace(base))
	}
	convergeDatabase(t, pool, load(
		`[{code: free, name: Free}, {code: pro, name: Pro}, {code: old, name: Old}]`,
		`[{id: 978-0-306-40615-7, edition: 1, title: First}, {id: 978-0-13-110362-7, edition: 2, title: Second}]`))
	changed := load(
		`[{code: FREE, name: Free}, {code: pro, name: Professional}]`,
		`[{id: "9780306406157", edition: 1, title: Renamed}]`)
	opts := diff.Options{Unsafe: true, Prune: true}

	onPath := sessionPool(t, name, map[string]string{"search_path": "app, ext"})
	sql := diff.Render(diff.PlanWith(introspect(t, onPath, changed), changed, opts))
	for _, want := range []string{
		`update only "app"."plan" set "code" = 'FREE' where "code" = 'FREE';`,
		`delete from only "app"."book" where "id" = '978-0-13-110362-7' and "edition" = '2';`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("with the operator's schema on the search_path, missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "operator(") {
		t.Errorf("an operator on the search_path needs no qualifier:\n%s", sql)
	}

	sql = convergeDatabase(t, pool, changed)
	for _, want := range []string{
		`update only "app"."plan" set "code" = 'FREE' where "code" operator("ext".=) 'FREE';`,
		`update only "app"."plan" set "name" = 'Professional' where "code" operator("ext".=) 'pro';`,
		`delete from only "app"."plan" where "code" operator("ext".=) 'old';`,
		`update only "app"."book" set "title" = 'Renamed' where "id" operator("ext".=) '9780306406157' and "edition" = 1;`,
		`delete from only "app"."book" where "id" operator("ext".=) '978-0-13-110362-7' and "edition" = '2';`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "insert into") {
		t.Errorf("rows that match in the key's type are updated, not inserted:\n%s", sql)
	}
	if got := rowTexts(t, pool, `select code::text || ' ' || name from app.plan order by 1`); strings.Join(got, ",") != "FREE Free,pro Professional" {
		t.Errorf("plans = %v", got)
	}
	if got := rowTexts(t, pool, `select id::text || ' ' || title from app.book order by 1`); strings.Join(got, ",") != "978-0-306-40615-7 Renamed" {
		t.Errorf("books = %v", got)
	}
	dup := load(`[{code: pro, name: Pro}, {code: Pro, name: Again}]`, `[]`)
	if msg := dataError(t, pool, dup); !strings.Contains(msg, "rows 1 and 2 identify the same row") {
		t.Errorf("expected rows with equal keys to be rejected, got %q", msg)
	}
}

// TestIntegrationDataKeyWithoutBtree rejects a key column whose type has no
// default btree operator class, for a table that exists and for one the
// plan creates, and accepts key types that use another type's class.
func TestIntegrationDataKeyWithoutBtree(t *testing.T) {
	admin := connect(t)
	ctx := context.Background()
	pool := freshDatabase(t, admin, "pgytest_data_key_btree")
	const base = `
schema app:
  table shapes:
    columns:
      k: {type: "TYPE"}
      label: {type: text}
    data:
      key: [k]
      rows:
        - {k: VALUE, label: unit}
`
	if _, err := pool.Exec(ctx, `create schema app`); err != nil {
		t.Fatal(err)
	}
	for typ, value := range map[string]string{
		"box":     `"(1,1),(0,0)"`,
		"point":   `"(1,2)"`,
		"json":    `{a: 1}`,
		"xml":     `"<a/>"`,
		"point[]": `["(1,2)"]`,
	} {
		t.Run(typ, func(t *testing.T) {
			d := loadYAML(t, "app", strings.NewReplacer("TYPE", typ, "VALUE", value).Replace(base))
			want := "data for app.shapes: key column k has type " + typ + ", which has no default btree operator class"
			if msg := dataError(t, pool, d); !strings.Contains(msg, want) {
				t.Errorf("new table: expected %q, got %q", want, msg)
			}
			if _, err := pool.Exec(ctx, `create table app.shapes (k `+typ+`, label text)`); err != nil {
				t.Fatal(err)
			}
			defer pool.Exec(ctx, `drop table app.shapes`) //nolint:errcheck
			if msg := dataError(t, pool, d); !strings.Contains(msg, want) {
				t.Errorf("existing table: expected %q, got %q", want, msg)
			}
		})
	}
	for typ, value := range map[string]string{
		"varchar(10)": `one`,
		"cidr":        `10.0.0.0/8`,
		"int4range":   `"[1,5)"`,
		"text[]":      `[a, b]`,
		"regclass":    `pg_class`,
	} {
		t.Run(typ, func(t *testing.T) {
			d := loadYAML(t, "app", strings.NewReplacer("TYPE", typ, "VALUE", value).Replace(base))
			defer pool.Exec(ctx, `drop table if exists app.shapes`) //nolint:errcheck
			if msg := dataError(t, pool, d); msg != "" {
				t.Fatalf("new table: %s", msg)
			}
			convergeDatabase(t, pool, d)
			if _, err := pool.Exec(ctx, `insert into app.shapes select k, 'copy' from app.shapes`); err != nil {
				t.Fatal(err)
			}
			if sql := diff.Render(diff.PlanWith(introspect(t, pool, d), d, diff.Options{Unsafe: true, Prune: true})); !strings.Contains(sql, `update only "app"."shapes" set "label" = 'unit'`) || strings.Contains(sql, "insert into") {
				t.Errorf("the rows match on the key, so the changed label is updated:\n%s", sql)
			}
		})
	}
}
