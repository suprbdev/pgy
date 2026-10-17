package group

import (
    "os"
    "path/filepath"
    "reflect"
    "strings"
    "testing"

    "github.com/suprbdev/pgy/internal/diff"
    "github.com/suprbdev/pgy/internal/schema"
)

func writeSchemas(t *testing.T, files map[string]string) (string, func(names ...string) []string) {
    t.Helper()
    dir := t.TempDir()
    for name, body := range files {
        p := filepath.Join(dir, name)
        if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { t.Fatal(err) }
        if err := os.WriteFile(p, []byte(body), 0o644); err != nil { t.Fatal(err) }
    }
    return dir, func(names ...string) []string {
        out := make([]string, 0, len(names))
        for _, n := range names { out = append(out, filepath.Join(dir, n)) }
        return out
    }
}

func planFiles(t *testing.T, live *diff.Live, paths []string, mode string, unsafe bool) []Step {
    t.Helper()
    groups, err := Split(paths, mode)
    if err != nil { t.Fatal(err) }
    steps, err := Plan(live, paths, groups, unsafe)
    if err != nil { t.Fatal(err) }
    return steps
}

func stepNames(steps []Step) []string {
    out := make([]string, 0, len(steps))
    for _, s := range steps { out = append(out, s.Name) }
    return out
}

func stepSQL(t *testing.T, steps []Step, name string) string {
    t.Helper()
    for _, s := range steps {
        if s.Name == name { return diff.Render(s.Plan) }
    }
    t.Fatalf("no step %q in %v", name, stepNames(steps))
    return ""
}

func assertEmittedOnce(t *testing.T, steps []Step) {
    t.Helper()
    seen := map[string]string{}
    for _, s := range steps {
        for _, stmt := range append(append(append([]string{}, s.Plan.Creates...), s.Plan.Alters...), s.Plan.Drops...) {
            if prev, ok := seen[stmt]; ok { t.Fatalf("statement emitted in both %q and %q: %s", prev, s.Name, stmt) }
            seen[stmt] = s.Name
        }
    }
}

func TestPlanExamplesEmitEachStatementOnce(t *testing.T) {
    var paths []string
    for _, pattern := range []string{"modules/*/*.yml", "links/*.yml", "schema.yml"} {
        m, err := filepath.Glob(filepath.Join("..", "..", "examples", pattern))
        if err != nil { t.Fatal(err) }
        paths = append(paths, m...)
    }
    steps := planFiles(t, nil, paths, ModeFile, false)
    assertEmittedOnce(t, steps)
    full, err := schema.LoadAndMerge(paths)
    if err != nil { t.Fatal(err) }
    single := diff.Plan(&diff.Live{}, full, false)
    if len(steps) < 2 || len(steps) > len(paths) { t.Fatalf("unexpected step count %d for %d files", len(steps), len(paths)) }
    if strings.Count(Render(steps), "create table") != strings.Count(diff.Render(single), "create table") {
        t.Fatal("split and single plans must create the same tables")
    }
}

const userYML = `
schema public:
  function set_updated_at():
    returns: trigger
    language: plpgsql
    body: "BEGIN NEW.updated_at = NOW(); RETURN NEW; END;"
  type user_status:
    type: enum
    labels: [active, deleted]
  table user:
    columns:
      id: {type: uuid, primaryKey: true}
      status: {type: public.user_status}
      updated_at: {type: timestamptz}
    triggers:
      set_updated_at:
        timing: before
        events: [update]
        level: row
        procedure: public.set_updated_at()
`

const postYML = `
schema public:
  table post:
    columns:
      id: {type: uuid, primaryKey: true}
      title: {type: text}
`

const postAuthorYML = `
schema public:
  table post:
    columns:
      author_id: {type: uuid, nullable: true}
    foreignKeys:
      post_author_id_fkey:
        columns: [author_id]
        references: {table: public.user, columns: [id]}
    indexes:
      post_author_id_idx:
        columns: [author_id]
    dependsOn:
      - table public.user
`

func TestSplitNames(t *testing.T) {
    paths := []string{"schemas/modules/core/user.yml", "schemas/modules/core/org.yaml", "schemas/links/membership.yml"}
    g, err := Split(paths, ModeFile)
    if err != nil { t.Fatal(err) }
    want := []Group{
        {Name: "modules/core/user", Files: paths[:1]},
        {Name: "modules/core/org", Files: paths[1:2]},
        {Name: "links/membership", Files: paths[2:]},
    }
    if !reflect.DeepEqual(g, want) { t.Fatalf("file mode:\n got %+v\nwant %+v", g, want) }

    g, err = Split(paths, ModeDir)
    if err != nil { t.Fatal(err) }
    want = []Group{
        {Name: "modules/core", Files: paths[:2]},
        {Name: "links", Files: paths[2:]},
    }
    if !reflect.DeepEqual(g, want) { t.Fatalf("dir mode:\n got %+v\nwant %+v", g, want) }

    g, err = Split([]string{"schemas/a.yml", "schemas/b.yml"}, ModeDir)
    if err != nil { t.Fatal(err) }
    if len(g) != 1 || g[0].Name != "schemas" || len(g[0].Files) != 2 { t.Fatalf("flat dir mode: %+v", g) }

    g, err = Split([]string{"a.yml"}, ModeFile)
    if err != nil { t.Fatal(err) }
    if g[0].Name != "a" { t.Fatalf("single file: %+v", g) }

    if _, err := Split(paths, "table"); err == nil { t.Fatal("expected invalid mode error") }
}

func TestPlanLinkFileBecomesOwnMigration(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "modules/user.yml":      userYML,
        "modules/post.yml":      postYML,
        "links/post_author.yml": postAuthorYML,
    })
    for name, paths := range map[string][]string{
        "modules first": p("modules/user.yml", "modules/post.yml", "links/post_author.yml"),
        "links first":   p("links/post_author.yml", "modules/post.yml", "modules/user.yml"),
    } {
        t.Run(name, func(t *testing.T) {
            steps := planFiles(t, nil, paths, ModeFile, false)
            names := stepNames(steps)
            if names[len(names)-1] != "links/post_author" || len(names) != 3 {
                t.Fatalf("link must come after both modules, got %v", names)
            }
            post := stepSQL(t, steps, "modules/post")
            if strings.Contains(post, "author_id") { t.Fatalf("post module step should not contain link columns:\n%s", post) }
            link := stepSQL(t, steps, "links/post_author")
            for _, want := range []string{
                `alter table "public"."post" add column "author_id" uuid;`,
                `create index if not exists "post_author_id_idx"`,
                `add constraint "post_author_id_fkey" foreign key ("author_id") references "public"."user"("id")`,
            } {
                if !strings.Contains(link, want) { t.Fatalf("link step missing %q:\n%s", want, link) }
            }
            if strings.Contains(link, "create table") { t.Fatalf("link step must not create tables:\n%s", link) }
            assertEmittedOnce(t, steps)
        })
    }
}

func TestPlanDeclaredOrderKeptWithoutDependencies(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "b.yml": "tables:\n  b:\n    columns:\n      id: {type: int}\n",
        "a.yml": "tables:\n  a:\n    columns:\n      id: {type: int}\n",
    })
    steps := planFiles(t, nil, p("b.yml", "a.yml"), ModeFile, false)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"b", "a"}) { t.Fatalf("got %v", got) }
}

func TestPlanForeignKeyPrefersTargetFirst(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "order.yml": `
tables:
  order:
    columns:
      id: {type: int, primaryKey: true}
      customer_id: {type: int}
    foreignKeys:
      order_customer_fkey:
        columns: [customer_id]
        references: {table: customer, columns: [id]}
`,
        "customer.yml": "tables:\n  customer:\n    columns:\n      id: {type: int, primaryKey: true}\n",
    })
    paths := p("order.yml", "customer.yml")
    steps := planFiles(t, nil, paths, ModeFile, false)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"customer", "order"}) { t.Fatalf("got %v", got) }
    if !strings.Contains(stepSQL(t, steps, "order"), "order_customer_fkey") { t.Fatal("fk should stay with its table") }
}

func TestPlanCircularForeignKeysDeferred(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "a.yml": `
tables:
  a:
    columns:
      id: {type: int, primaryKey: true}
      b_id: {type: int, nullable: true}
    foreignKeys:
      a_b_fkey:
        columns: [b_id]
        references: {table: b, columns: [id]}
`,
        "b.yml": `
tables:
  b:
    columns:
      id: {type: int, primaryKey: true}
      a_id: {type: int, nullable: true}
    foreignKeys:
      b_a_fkey:
        columns: [a_id]
        references: {table: a, columns: [id]}
`,
    })
    paths := p("a.yml", "b.yml")
    steps := planFiles(t, nil, paths, ModeFile, false)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"a", "b"}) { t.Fatalf("got %v", got) }
    a, b := stepSQL(t, steps, "a"), stepSQL(t, steps, "b")
    if strings.Contains(a, "a_b_fkey") { t.Fatalf("fk to a later group must be deferred:\n%s", a) }
    if !strings.Contains(b, "a_b_fkey") || !strings.Contains(b, "b_a_fkey") { t.Fatalf("both fks expected in b:\n%s", b) }
    assertEmittedOnce(t, steps)
}

func TestPlanHardCycleMergesGroups(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "a.yml": `
schema public:
  type a_kind:
    type: enum
    labels: [x]
  table a:
    columns:
      id: {type: int, primaryKey: true}
      kind: {type: public.b_kind}
`,
        "b.yml": `
schema public:
  type b_kind:
    type: enum
    labels: [y]
  table b:
    columns:
      id: {type: int, primaryKey: true}
      kind: {type: public.a_kind}
`,
        "c.yml": "tables:\n  c:\n    columns:\n      id: {type: int}\n",
    })
    paths := p("a.yml", "c.yml", "b.yml")
    steps := planFiles(t, nil, paths, ModeFile, false)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"a+b", "c"}) { t.Fatalf("got %v", got) }
    assertEmittedOnce(t, steps)
}

func TestPlanDefersTriggerAndPolicyUntilFunctionExists(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "doc.yml": `
schema public:
  table doc:
    columns:
      id: {type: int, primaryKey: true}
      owner: {type: text}
    rowLevelSecurity: true
    policies:
      doc_owner:
        using: "public.can_read(id)"
    triggers:
      doc_audit:
        timing: after
        events: [insert]
        level: row
        procedure: public.doc_audit()
`,
        "fns.yml": `
schema public:
  function can_read(doc_id int):
    returns: boolean
    language: sql
    body: "select exists (select 1 from public.doc where id = doc_id)"
    dependsOn: [table public.doc]
  function doc_audit():
    returns: trigger
    language: plpgsql
    body: "BEGIN RETURN NEW; END;"
`,
    })
    paths := p("fns.yml", "doc.yml")
    steps := planFiles(t, nil, paths, ModeFile, false)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"doc", "fns"}) { t.Fatalf("got %v", got) }
    doc, fns := stepSQL(t, steps, "doc"), stepSQL(t, steps, "fns")
    if strings.Contains(doc, "create policy") || strings.Contains(doc, "create trigger") {
        t.Fatalf("policy and trigger must wait for their functions:\n%s", doc)
    }
    if strings.Contains(doc, "enable row level security") { t.Fatalf("rls must be enabled together with its deferred policies:\n%s", doc) }
    if !strings.Contains(fns, "enable row level security") { t.Fatalf("rls expected with the policies:\n%s", fns) }
    if !strings.Contains(fns, `create policy "doc_owner"`) || !strings.Contains(fns, `create trigger "doc_audit"`) {
        t.Fatalf("deferred policy and trigger expected in function step:\n%s", fns)
    }
    if strings.Index(fns, "create function \"public\".\"can_read\"") > strings.Index(fns, "create policy") {
        t.Fatalf("function must be created before the policy:\n%s", fns)
    }
    assertEmittedOnce(t, steps)
}

func TestPlanSequenceOwnedByOtherFile(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "order.yml": `
schema public:
  table order:
    columns:
      id: {type: int, primaryKey: true}
      number: {type: bigint, default: "nextval('public.order_number_seq')"}
`,
        "seq.yml": `
schema public:
  sequence order_number_seq:
    as: bigint
    ownedBy: order.number
`,
    })
    paths := p("seq.yml", "order.yml")
    steps := planFiles(t, nil, paths, ModeFile, false)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"order", "seq"}) { t.Fatalf("got %v", got) }
    if order := stepSQL(t, steps, "order"); strings.Contains(order, "nextval") {
        t.Fatalf("default referencing a later sequence must be deferred:\n%s", order)
    }
    seq := stepSQL(t, steps, "seq")
    for _, want := range []string{"create sequence", `alter column "number" set default nextval('public.order_number_seq')`, "owned by"} {
        if !strings.Contains(seq, want) { t.Fatalf("seq step missing %q:\n%s", want, seq) }
    }
    assertEmittedOnce(t, steps)
}

func TestPlanHoistsExtensionsAndRoles(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "a.yml": "tables:\n  a:\n    columns:\n      id: {type: int}\n",
        "z.yml": "extensions: [pg_trgm]\nroles:\n  app_user:\n    login: true\n",
    })
    steps := planFiles(t, nil, p("a.yml", "z.yml"), ModeFile, false)
    if len(steps) != 1 || steps[0].Name != "a" { t.Fatalf("got %v", stepNames(steps)) }
    sql := diff.Render(steps[0].Plan)
    if !strings.Contains(sql, `create extension if not exists "pg_trgm"`) || !strings.Contains(sql, `create role "app_user"`) {
        t.Fatalf("extensions and roles belong in the first step:\n%s", sql)
    }
}

func TestPlanDefersSchemaGrantsUntilSchemaExists(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "access.yml": "roles:\n  reader: {}\nschema app:\n  comment: App objects.\n  grants:\n    reader: [usage]\n",
        "app.yml":    "schema app:\n  table thing:\n    columns:\n      id: {type: int}\n",
    })
    steps := planFiles(t, nil, p("access.yml", "app.yml"), ModeFile, false)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"access", "app"}) { t.Fatalf("got %v", got) }
    if access := stepSQL(t, steps, "access"); strings.Contains(access, `schema "app"`) {
        t.Fatalf("schema grants and comments must wait for the schema:\n%s", access)
    }
    app := stepSQL(t, steps, "app")
    for _, want := range []string{`create schema if not exists "app"`, `grant usage on schema "app" to "reader"`, `comment on schema "app"`} {
        if !strings.Contains(app, want) { t.Fatalf("app step missing %q:\n%s", want, app) }
    }
}

func TestPlanViewWaitsForTablesDespiteSoftCycle(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "reports.yml": `
schema public:
  table report:
    columns:
      id: {type: int, primaryKey: true}
  view active_users:
    query: select id from public.users
`,
        "users.yml": `
schema public:
  table users:
    columns:
      id: {type: int, primaryKey: true}
      report_id: {type: int, nullable: true}
    foreignKeys:
      users_report_fkey:
        columns: [report_id]
        references: {table: public.report, columns: [id]}
`,
    })
    paths := p("reports.yml", "users.yml")
    steps := planFiles(t, nil, paths, ModeFile, false)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"users", "reports"}) { t.Fatalf("got %v", got) }
    if users := stepSQL(t, steps, "users"); strings.Contains(users, "users_report_fkey") {
        t.Fatalf("fk to a later group must be deferred:\n%s", users)
    }
    reports := stepSQL(t, steps, "reports")
    if !strings.Contains(reports, "create or replace view") || !strings.Contains(reports, "users_report_fkey") {
        t.Fatalf("view and deferred fk expected in reports step:\n%s", reports)
    }
    assertEmittedOnce(t, steps)
}

func TestPlanDefersDefaultCastToLaterType(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "orders.yml": `
schema public:
  table orders:
    columns:
      id: {type: int, primaryKey: true}
      status: {type: text, default: "'new'::public.order_status"}
    constraints:
      orders_status_check:
        type: check
        expression: "status::public.order_status <> 'void'"
`,
        "enums.yml": `
schema public:
  type order_status:
    type: enum
    labels: [new, void]
  table order_note:
    columns:
      id: {type: int}
    dependsOn: [table public.orders]
`,
    })
    paths := p("orders.yml", "enums.yml")
    steps := planFiles(t, nil, paths, ModeFile, false)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"orders", "enums"}) { t.Fatalf("got %v", got) }
    if orders := stepSQL(t, steps, "orders"); strings.Contains(orders, "order_status") {
        t.Fatalf("default and check casting to a later type must be deferred:\n%s", orders)
    }
    enums := stepSQL(t, steps, "enums")
    for _, want := range []string{"create type", `set default 'new'::public.order_status`, `add constraint "orders_status_check"`} {
        if !strings.Contains(enums, want) { t.Fatalf("enums step missing %q:\n%s", want, enums) }
    }
    assertEmittedOnce(t, steps)
}

func TestPlanFunctionReturningTableComesAfterIt(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "fns.yml": `
schema public:
  function all_rows():
    returns: setof public.things
    language: plpgsql
    body: "BEGIN RETURN; END;"
`,
        "things.yml": "schema public:\n  table things:\n    columns:\n      id: {type: int}\n",
    })
    steps := planFiles(t, nil, p("fns.yml", "things.yml"), ModeFile, false)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"things", "fns"}) { t.Fatalf("got %v", got) }
}

func TestPlanForeignKeyWaitsForReferencedKey(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "users.yml": "schema public:\n  table users:\n    columns:\n      id: {type: int, primaryKey: true}\n      email: {type: text}\n",
        "invites.yml": `
schema public:
  table invites:
    columns:
      id: {type: int, primaryKey: true}
      email: {type: text}
    foreignKeys:
      invites_email_fkey:
        columns: [email]
        references: {table: public.users, columns: [email]}
`,
        "users_email.yml": "schema public:\n  table users:\n    indexes:\n      users_email_key:\n        columns: [email]\n        unique: true\n",
    })
    paths := p("users.yml", "invites.yml", "users_email.yml")
    steps := planFiles(t, nil, paths, ModeFile, false)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"users", "invites", "users_email"}) { t.Fatalf("got %v", got) }
    if invites := stepSQL(t, steps, "invites"); strings.Contains(invites, "invites_email_fkey") {
        t.Fatalf("fk must wait for the unique key it references:\n%s", invites)
    }
    last := stepSQL(t, steps, "users_email")
    if !strings.Contains(last, "users_email_key") ||strings.Index(last, "invites_email_fkey") < strings.Index(last, "users_email_key") {
        t.Fatalf("unique index must precede the deferred fk:\n%s", last)
    }
}

func TestPlanAttributeOnlyRevokeEmittedOnce(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "ext.yml": "extensions: [pgcrypto]\nschema public:\n  function gen_salt(text):\n    revokePublic: true\n",
        "a.yml":   "tables:\n  a:\n    columns:\n      id: {type: int}\n",
        "b.yml":   "tables:\n  b:\n    columns:\n      id: {type: int}\n",
    })
    steps := planFiles(t, nil, p("ext.yml", "a.yml", "b.yml"), ModeFile, false)
    if got := strings.Count(Render(steps), "revoke all on function"); got != 1 { t.Fatalf("expected one revoke, got %d:\n%s", got, Render(steps)) }
}

func TestPlanReplaceViewEmittedOnce(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "a.yml": "schema public:\n  table a:\n    columns:\n      id: {type: int}\n  view a_ids:\n    query: select id from public.a\n    replace: true\n",
        "b.yml": "tables:\n  b:\n    columns:\n      id: {type: int}\n",
    })
    steps := planFiles(t, nil, p("a.yml", "b.yml"), ModeFile, false)
    if got := strings.Count(Render(steps), "create or replace view"); got != 1 { t.Fatalf("expected one view replace, got %d:\n%s", got, Render(steps)) }
}

func TestPlanAgainstLiveDatabase(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "post.yml": `
schema public:
  table post:
    columns:
      id: {type: int, primaryKey: true}
      title: {type: text}
`,
        "author.yml": `
schema public:
  table post:
    columns:
      author: {type: text, nullable: true}
  table author:
    columns:
      id: {type: int, primaryKey: true}
`,
    })
    live := &diff.Live{Tables: map[string]*diff.LiveTable{
        "public.post": {
            Columns: map[string]*diff.LiveColumn{
                "id":     {Type: "integer"},
                "title":  {Type: "text"},
                "author": {Type: "text", Nullable: true},
                "legacy": {Type: "text", Nullable: true},
            },
            HasPK: true,
        },
    }}
    paths := p("post.yml", "author.yml")
    steps := planFiles(t, live, paths, ModeFile, true)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"author"}) {
        t.Fatalf("only the group with changes should produce a step, got %v", got)
    }
    sql := stepSQL(t, steps, "author")
    if strings.Count(sql, "drop column") != 1 || !strings.Contains(sql, `drop column "legacy"`) {
        t.Fatalf("only the truly removed column may be dropped, once, in the final step:\n%s", sql)
    }
    if strings.Contains(sql, `drop column "author"`) { t.Fatalf("column declared by a later file must not be dropped:\n%s", sql) }
}

func TestPlanUnsafeDropsOnlyInFinalStep(t *testing.T) {
    _, p := writeSchemas(t, map[string]string{
        "a.yml": "tables:\n  a:\n    columns:\n      id: {type: int}\n",
        "b.yml": "tables:\n  b:\n    columns:\n      id: {type: int}\n",
    })
    live := &diff.Live{Tables: map[string]*diff.LiveTable{
        "public.a": {Columns: map[string]*diff.LiveColumn{"id": {Type: "integer"}, "old": {Type: "text"}}},
    }}
    steps := planFiles(t, live, p("a.yml", "b.yml"), ModeFile, true)
    if got := stepNames(steps); !reflect.DeepEqual(got, []string{"b"}) { t.Fatalf("got %v", got) }
    if sql := stepSQL(t, steps, "b"); !strings.Contains(sql, `alter table "public"."a" drop column "old";`) {
        t.Fatalf("expected drop in final step:\n%s", sql)
    }
}

func TestRenderParseRoundTrip(t *testing.T) {
    steps := []Step{
        {Name: "modules/core/user", Plan: &diff.PlanDiff{Creates: []string{"create table a (id int);"}}},
        {Name: "links/x", Plan: &diff.PlanDiff{Alters: []string{"alter table a add column b int;"}}},
    }
    got := Parse(Render(steps))
    want := []Section{
        {Name: "modules/core/user", SQL: "create table a (id int);\n"},
        {Name: "links/x", SQL: "alter table a add column b int;\n"},
    }
    if !reflect.DeepEqual(got, want) { t.Fatalf("got %+v", got) }

    if Parse("create table a (id int);\n-- pgy:groups is not a marker\n") != nil { t.Fatal("unmarked buffer should not parse into sections") }

    got = Parse("select 1;\n\n-- pgy:group b\nselect 2;\n-- pgy:group empty\n\n")
    want = []Section{{SQL: "select 1;\n"}, {Name: "b", SQL: "select 2;\n"}, {Name: "empty"}}
    if !reflect.DeepEqual(got, want) { t.Fatalf("got %+v", got) }
}

func TestTokens(t *testing.T) {
    got := tokens(`nextval('public.seq_a'::regclass) + app."Order".total + 'it''s' 3x`)
    want := []token{
        {name: "nextval"},
        {name: "public", lit: true}, {qual: "public", name: "seq_a", lit: true},
        {name: "regclass"},
        {name: "app"}, {qual: "app", name: "order"}, {qual: "order", name: "total"},
        {name: "it", lit: true}, {name: "s", lit: true},
    }
    if !reflect.DeepEqual(got, want) { t.Fatalf("got %+v", got) }
}
