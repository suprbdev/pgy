package schema

import (
    "bytes"
    "encoding/json"
    "os"
    "testing"

    "github.com/santhosh-tekuri/jsonschema/v5"
    yaml "gopkg.in/yaml.v3"
)

// compileEditorSchema compiles schemas/pgy.schema.json, the JSON Schema
// published for yaml-language-server editor support. Keeping it compiling and
// in sync with the parser is enforced by the tests below.
func compileEditorSchema(t *testing.T) *jsonschema.Schema {
    t.Helper()
    b, err := os.ReadFile("../../schemas/pgy.schema.json")
    if err != nil {
        t.Fatalf("read schema: %v", err)
    }
    c := jsonschema.NewCompiler()
    if err := c.AddResource("pgy.schema.json", bytes.NewReader(b)); err != nil {
        t.Fatalf("add resource: %v", err)
    }
    s, err := c.Compile("pgy.schema.json")
    if err != nil {
        t.Fatalf("compile schema: %v", err)
    }
    return s
}

// yamlToJSONValue converts YAML bytes to a JSON-compatible value for
// validation (round-trips through encoding/json so types match what a JSON
// Schema validator expects).
func yamlToJSONValue(t *testing.T, in []byte) any {
    t.Helper()
    var v any
    if err := yaml.Unmarshal(in, &v); err != nil {
        t.Fatalf("yaml unmarshal: %v", err)
    }
    jb, err := json.Marshal(v)
    if err != nil {
        t.Fatalf("json marshal: %v", err)
    }
    var out any
    if err := json.Unmarshal(jb, &out); err != nil {
        t.Fatalf("json unmarshal: %v", err)
    }
    return out
}

func TestJSONSchemaValidatesExampleFile(t *testing.T) {
    s := compileEditorSchema(t)
    b, err := os.ReadFile("../../examples/schema.yml")
    if err != nil {
        t.Fatalf("read example: %v", err)
    }
    if err := s.Validate(yamlToJSONValue(t, b)); err != nil {
        t.Fatalf("examples/schema.yml does not validate against schemas/pgy.schema.json:\n%v", err)
    }
}

func TestJSONSchemaAcceptsAllFormats(t *testing.T) {
    s := compileEditorSchema(t)
    valid := []string{
        // schema block format
        `
schema public:
  comment: main schema
  grants:
    app: [usage]
  table users:
    columns:
      id: {type: uuid, primaryKey: true}
      email: {type: citext, notNull: true}
    primaryKey:
      users_pkey:
        columns: [id]
    indexes:
      users_email_idx: {columns: [email], unique: true, using: btree}
    foreignKeys:
      users_org_fkey:
        columns: [org_id]
        references: {table: public.orgs, columns: [id]}
        onDelete: cascade
    triggers:
      set_updated_at: {timing: before, events: [update], level: row, procedure: public.set_updated_at()}
    constraints:
      email_check: {type: check, expression: "length(email) > 0"}
    policies:
      self: {for: select, to: app, using: "id = current_user_id()"}
    rowLevelSecurity: true
  function set_updated_at():
    returns: trigger
    language: plpgsql
    stable: true
    body: BEGIN RETURN NEW; END
  type mood:
    type: enum
    labels: [happy, sad]
  view active_users:
    query: SELECT 1
  materialized view stats:
    query: SELECT 1
  sequence order_seq:
    as: bigint
    start: 100
  domain email:
    type: citext
    check: "VALUE ~ '@'"
`,
        // schemas map format
        `
schemas:
  app:
    comment: app schema
    users:
      columns:
        id: {type: uuid}
`,
        // tables map and list formats, extensions, roles
        `
extensions:
  - pg_trgm
  - {name: pgcrypto, ifNotExists: true}
roles:
  app: {login: true, inRoles: [base]}
tables:
  - name: events
    schema: app
    columns:
      - {name: id, type: bigint, identity: always}
    partitionBy: {range: [created_at]}
`,
    }
    for i, y := range valid {
        if err := s.Validate(yamlToJSONValue(t, []byte(y))); err != nil {
            t.Errorf("valid doc %d rejected:\n%v", i, err)
        }
    }
}

func TestJSONSchemaRejectsInvalid(t *testing.T) {
    s := compileEditorSchema(t)
    invalid := []string{
        // typo in top-level key
        "tabels:\n  users:\n    columns: {id: {type: uuid}}\n",
        // typo in column property
        "schema public:\n  table t:\n    columns:\n      id: {type: uuid, primarykey: true}\n",
        // bad index method
        "schema public:\n  table t:\n    columns: {id: {type: uuid}}\n    indexes:\n      i: {columns: [id], using: btre}\n",
        // column missing type
        "schema public:\n  table t:\n    columns:\n      id: {primaryKey: true}\n",
        // bad trigger timing
        "schema public:\n  table t:\n    columns: {id: {type: uuid}}\n    triggers:\n      trg: {timing: befor, events: [update], procedure: f()}\n",
    }
    for i, y := range invalid {
        if err := s.Validate(yamlToJSONValue(t, []byte(y))); err == nil {
            t.Errorf("invalid doc %d accepted, want validation error", i)
        }
    }
}
