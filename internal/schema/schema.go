package schema

import (
    "container/heap"
    "fmt"
    "io/fs"
    "os"
    "regexp"
    "sort"
    "strings"
    "time"
    "unicode/utf8"

    yaml "gopkg.in/yaml.v3"
)

// Minimal schema model: schemas -> tables -> columns
type Database struct {
    Tables map[string]*Table `yaml:"tables"`
    // Extensions is nil when the YAML has no extensions block (installed
    // extensions are never dropped) and authoritative for pruning when
    // present, even when empty.
    Extensions []*Extension `yaml:"extensions"`
    Types map[string]*TypeDef `yaml:"-"`
    Functions map[string]*Function `yaml:"-"`
    Procedures map[string]*Procedure `yaml:"-"`
    Aggregates map[string]*Aggregate `yaml:"-"` // keyed like Functions: schema.name(args)
    Views map[string]*View `yaml:"-"`
    Sequences map[string]*Sequence `yaml:"-"`
    Domains map[string]*Domain `yaml:"-"`
    Roles map[string]*Role `yaml:"-"`
    Collations map[string]*Collation `yaml:"-"`
    TSDictionaries map[string]*TSDictionary `yaml:"-"`
    TSConfigs      map[string]*TSConfig     `yaml:"-"`
    // EventTriggers is nil when the YAML has no eventTriggers block (event
    // triggers unmanaged) and authoritative for pruning when present.
    EventTriggers map[string]*EventTrigger `yaml:"-"`
    // Publications is nil when the YAML has no publications block
    // (unmanaged) and authoritative for pruning when present.
    Publications map[string]*Publication `yaml:"-"`
    // Servers is nil when the YAML has no servers block (foreign servers
    // unmanaged) and authoritative for pruning when present.
    Servers map[string]*Server `yaml:"-"`
    // ForeignDataWrappers is nil when the YAML has no foreignDataWrappers
    // block (wrappers unmanaged) and authoritative for pruning when present.
    ForeignDataWrappers map[string]*ForeignDataWrapper `yaml:"-"`
    // Languages holds the procedural languages whose privileges the YAML
    // manages, by name. Languages are never created or dropped.
    Languages map[string]*Language `yaml:"-"`
    SchemaGrants map[string]map[string][]string `yaml:"-"` // schema name -> role -> privileges
    SchemaComments map[string]string `yaml:"-"`            // schema name -> comment
    SchemaOwners map[string]string `yaml:"-"`              // schema name -> owner role
    DefaultPrivileges []*DefaultPrivilege `yaml:"-"`
    DatabaseSettings  map[string]string   `yaml:"-"` // ALTER DATABASE ... SET for the target database; nil = unmanaged
    DatabaseRoleSettings map[string]map[string]string `yaml:"-"` // role -> ALTER ROLE ... IN DATABASE ... SET settings
}

// Role is a cluster-level role (CREATE ROLE). Passwords are intentionally
// unsupported: secrets do not belong in schema YAML.
type Role struct {
    Name            string
    Login           bool
    Superuser       bool
    CreateDB        bool
    CreateRole      bool
    Replication     bool
    BypassRLS       bool
    NoInherit       bool     // yaml `inherit: false`
    ConnectionLimit int      // -1 = unset (no clause emitted)
    InRoles         []string // memberships: GRANT <parent> TO <role>
    // MemberOptions holds the options an inRoles entry sets (admin, inherit,
    // set), keyed by parent role; options left out are unmanaged.
    MemberOptions   map[string]*MembershipOptions
    ValidUntil      string   // VALID UNTIL timestamp or infinity; managed when Managed["validUntil"]
    Set             map[string]string // ALTER ROLE ... SET parameters; nil = unmanaged
    Comment         string
    // Managed records which attributes the YAML sets explicitly (login,
    // superuser, createdb, createrole, replication, bypassRLS, inherit,
    // connectionLimit); only those are reconciled on an existing role.
    Managed map[string]bool
}

// MembershipOptions are the options of one role membership: WITH ADMIN, and
// from PostgreSQL 16 WITH INHERIT and WITH SET. nil fields are unmanaged.
type MembershipOptions struct {
    Admin, Inherit, Set *bool
}

type View struct {
    Schema       string
    Name         string
    Query        string
    Materialized bool
    DependsOn    []string `yaml:"dependsOn"`
    Comment      string
    Replace      bool // always emit CREATE OR REPLACE (live view definitions cannot be reliably text-compared)
    Grants       map[string][]string // role -> privileges; nil means unmanaged
    Owner        string // "" = unmanaged
    SecurityInvoker *bool  // views: security_invoker option (PostgreSQL 15+); nil = unmanaged
    SecurityBarrier *bool  // views: security_barrier option; nil = unmanaged
    CheckOption     string // views: local | cascaded | none; "" = unmanaged
    WithData        *bool  // materialized views: false = WITH NO DATA at create; nil = WITH DATA
    With            map[string]string // materialized views: storage parameters; nil = unmanaged, present = authoritative
    WithOrder       []string          // materialized views: order of the With keys in the YAML (filled from node tree)
    Tablespace      string            // materialized views: TABLESPACE; "" = unmanaged
    AccessMethod    string            // materialized views: USING table access method; "" = unmanaged
    Indexes         []*Index // materialized views only
    Triggers        []*Trigger // plain views: INSTEAD OF triggers
    Rules           []*Rule    // plain views
    ColumnComments  map[string]string // view column -> COMMENT ON COLUMN text
    ColumnDefaults  map[string]string // plain views: column -> DEFAULT expression, used by inserts through the view
    RenamedFrom string `yaml:"-"` // previous name; emitted as ALTER ... RENAME when only the old name exists live
}

// Server is a CREATE SERVER declaration: a foreign server reached through a
// foreign-data wrapper, usually one an extension such as postgres_fdw
// provides. User mappings are out of scope, since their options hold
// credentials.
type Server struct {
    Name    string
    Wrapper string            // FOREIGN DATA WRAPPER
    Type    string            // TYPE; "" = none
    Version string            // VERSION; "" = unmanaged
    Options map[string]string // OPTIONS; nil = unmanaged, present = authoritative
    Owner   string            // "" = unmanaged
    Grants  map[string][]string // role -> privileges (usage); nil = unmanaged
    Comment string
    RenamedFrom string // previous name; emitted as ALTER SERVER ... RENAME TO when only the old name exists live
}

// ForeignDataWrapper is a foreignDataWrappers entry. A wrapper that neither
// exists nor comes with an extension is created from it. For a wrapper an
// extension owns, such as postgres_fdw, only Options, Owner, Grants and
// Comment are managed.
type ForeignDataWrapper struct {
    Name      string
    Handler   string            // HANDLER function, optionally schema-qualified; see ManagesHandler
    Validator string            // VALIDATOR function, optionally schema-qualified; see ManagesValidator
    // HandlerSet and ValidatorSet report a handler or validator key in the
    // YAML, so that an empty value (NO HANDLER, NO VALIDATOR) can be told
    // from a key left out.
    HandlerSet, ValidatorSet bool
    Options   map[string]string // OPTIONS; nil = unmanaged, present = authoritative
    Owner     string            // "" = unmanaged
    Grants    map[string][]string // role -> privileges (usage); nil = unmanaged
    Comment   string
    RenamedFrom string // previous name; emitted as ALTER FOREIGN DATA WRAPPER ... RENAME TO when only the old name exists live
}

// ManagesHandler reports whether w declares its handler, which may be
// none. Without a declaration an existing wrapper keeps the handler it has,
// and a new one is created without.
func (w *ForeignDataWrapper) ManagesHandler() bool { return w.HandlerSet || w.Handler != "" }

// ManagesValidator reports whether w declares its validator, which may be
// none; see ManagesHandler.
func (w *ForeignDataWrapper) ManagesValidator() bool { return w.ValidatorSet || w.Validator != "" }

// Language is a languages entry: the privileges of a procedural language
// that exists already or that an extension creates.
type Language struct {
    Name   string
    Grants map[string][]string // role -> privileges (usage); nil = unmanaged
}

// contribWrappers maps the foreign-data wrappers that PostgreSQL's own
// extensions create to their extension.
var contribWrappers = map[string]string{"postgres_fdw": "postgres_fdw", "file_fdw": "file_fdw", "dblink_fdw": "dblink"}

// WrapperExtension returns the extension that creates the foreign-data
// wrapper name, or "" when none is known to: the PostgreSQL extension that
// ships it, or a declared extension called like the wrapper, which is how
// extensions name the wrapper they provide.
func (d *Database) WrapperExtension(name string) string {
    if ext, ok := contribWrappers[name]; ok { return ext }
    if d.DeclaresExtension(name) { return name }
    return ""
}

// DeclaresExtension reports whether the extensions block lists name.
func (d *Database) DeclaresExtension(name string) bool {
    for _, ext := range d.Extensions {
        if ext != nil && ext.Name == name { return true }
    }
    return false
}

// SequenceColumn is a serial or identity column: the table's key
// ("schema.table") and the column's name.
type SequenceColumn struct {
    Table, Column string
}

// ColumnSequences maps the sequences that declared serial and identity
// columns create ("schema.sequence") to their column. A sequence has the
// name PostgreSQL gives it: the identity's sequenceName (in the table's
// schema when unqualified), or <table>_<column>_seq with both names
// shortened to fit 63 bytes. A name PostgreSQL numbers because it is taken
// is not predicted.
func (d *Database) ColumnSequences() map[string]SequenceColumn {
    out := map[string]SequenceColumn{}
    for fq, t := range d.Tables {
        if t == nil { continue }
        nsp, table, ok := strings.Cut(fq, ".")
        if !ok { nsp, table = "public", fq }
        for cn, c := range t.Columns {
            if c == nil || c.Identity == "" && !IsSerialType(c.Type) { continue }
            seq := nsp + "." + ColumnSequenceName(table, cn)
            if c.Identity != "" && c.IdentityOptions != nil && c.IdentityOptions.Name != "" {
                seq = c.IdentityOptions.Name
                if !strings.Contains(seq, ".") { seq = nsp + "." + seq }
            }
            out[seq] = SequenceColumn{Table: nsp + "." + table, Column: cn}
        }
    }
    return out
}

// ColumnSequenceName returns the name PostgreSQL chooses for the sequence
// of a serial or identity column: <table>_<column>_seq, where the longer of
// the two names is shortened, a byte at a time and never inside a
// multibyte character, until the whole fits in 63 bytes.
func ColumnSequenceName(table, column string) string {
    const avail = 63 - len("_seq") - len("_")
    t, c := len(table), len(column)
    for t+c > avail {
        if t > c { t-- } else { c-- }
    }
    return clipBytes(table, t) + "_" + clipBytes(column, c) + "_seq"
}

func clipBytes(s string, n int) string {
    if len(s) <= n { return s }
    for n > 0 && !utf8.RuneStart(s[n]) { n-- }
    return s[:n]
}

// IsSerialType reports whether t is one of the serial pseudo-types.
func IsSerialType(t string) bool {
    switch strings.ToLower(strings.TrimSpace(t)) {
    case "serial", "smallserial", "bigserial", "serial2", "serial4", "serial8":
        return true
    }
    return false
}

// ForeignTable holds what makes a table a CREATE FOREIGN TABLE: the server
// it reads through and the wrapper's table options.
type ForeignTable struct {
    Server  string
    Options map[string]string // OPTIONS; nil = unmanaged, present = authoritative
}

// Publication is a logical replication CREATE PUBLICATION declaration.
type Publication struct {
    Name             string
    AllTables        bool
    Tables           []*PublicationTable
    Schemas          []string // FOR TABLES IN SCHEMA
    Publish          []string // insert | update | delete | truncate; nil = all
    ViaPartitionRoot *bool    // publish_via_partition_root; nil = unmanaged
    GeneratedColumns string   // publish_generated_columns (PostgreSQL 18+): none | stored; "" = unmanaged
    Owner            string   // "" = unmanaged
    Comment          string
}

// PublicationTable is one FOR TABLE entry, optionally limited to some
// columns and filtered by a row filter (Where).
type PublicationTable struct {
    Table   string // schema-qualified
    Columns []string
    Where   string
    Only    bool // TABLE ONLY: leave out the table's inheritance children
}

// DefaultPrivilege is one ALTER DEFAULT PRIVILEGES scope: objects created
// later by ForRole ("" = the connecting role), in Schemas (empty = in any
// schema), get Grants: object kind (tables, sequences, functions, types,
// schemas) -> role -> privileges. Roles not listed are unmanaged; an empty
// list revokes everything from that role.
type DefaultPrivilege struct {
    ForRole string
    Schemas []string
    Grants  map[string]map[string][]string
}

// EventTrigger is a database-wide CREATE EVENT TRIGGER declaration.
type EventTrigger struct {
    Name     string
    Event    string   // ddl_command_start | ddl_command_end | sql_drop | table_rewrite | login
    Tags     []string // WHEN TAG IN (...); empty = every command
    Function string   // function returning event_trigger, e.g. public.log_ddl()
    Enabled  string   // origin (default) | disabled | replica | always
    Comment  string
}

// TSDictionary is a CREATE TEXT SEARCH DICTIONARY declaration (create-only).
type TSDictionary struct {
    Schema    string
    Name      string
    Template  string
    Options   map[string]string
    DependsOn []string `yaml:"dependsOn"`
    Comment   string
}

// TSConfig is a CREATE TEXT SEARCH CONFIGURATION declaration: a copy of an
// existing configuration or a parser, plus token type -> dictionary mapping
// overrides. Mappings lists only the token types the YAML manages; an empty
// dictionary list drops that token type's mapping.
type TSConfig struct {
    Schema    string
    Name      string
    Copy      string
    Parser    string
    Mappings  map[string][]string
    DependsOn []string `yaml:"dependsOn"`
    Comment   string
}

// Collation is a CREATE COLLATION declaration, either from provider options
// or copied from an existing collation (From). PostgreSQL cannot alter a
// collation's definition, so collations are create-only.
type Collation struct {
    Schema        string
    Name          string
    Provider      string // icu | libc | builtin
    Locale        string
    LcCollate     string
    LcCtype       string
    Deterministic *bool
    Rules         string // ICU tailoring rules (PostgreSQL 16+)
    From          string // existing collation to copy
    DependsOn     []string `yaml:"dependsOn"`
    Comment       string
}

// TableData declares a table's managed rows (data:). Key names the columns
// that identify a row; when empty, the table's primary key is used. Each row
// maps column names to YAML values (string, int, float64, bool, nil, []any or
// map[string]any).
type TableData struct {
    Key  []string
    Rows []map[string]any
}

// DataKey returns the columns identifying t's data rows: Data.Key, or the
// primary key (table-level, else column-level in column order).
func (t *Table) DataKey() []string {
    if t.Data != nil && len(t.Data.Key) > 0 { return t.Data.Key }
    if len(t.PrimaryKey) > 0 {
        out := make([]string, len(t.PrimaryKey))
        for i, c := range t.PrimaryKey { out[i] = strings.Fields(c)[0] }
        return out
    }
    out := []string{}
    names := append([]string(nil), t.ColumnOrder...)
    if len(names) == 0 {
        for cn := range t.Columns { names = append(names, cn) }
        sort.Strings(names)
    }
    for _, cn := range names {
        if c := t.Columns[cn]; c != nil && c.PrimaryKey { out = append(out, cn) }
    }
    return out
}

// parseTableData parses a table's data: block, or returns nil.
func parseTableData(v any) *TableData {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    td := &TableData{Rows: []map[string]any{}}
    if k, ok := m["key"]; ok { td.Key = parseStringListFromNode(k) }
    if rows, ok := m["rows"].([]any); ok {
        for _, r := range rows {
            if rm, ok := r.(map[string]any); ok { td.Rows = append(td.Rows, rm) }
        }
    }
    return td
}

// Sequence is an explicit CREATE SEQUENCE declaration. Numeric options are
// stored as strings so zero/negative values are distinguishable from unset.
type Sequence struct {
    Schema    string
    Name      string
    As        string // smallint | integer | bigint
    Increment string
    MinValue  string
    MaxValue  string
    Start     string
    Cache     string
    Cycle     *bool  // nil = unmanaged
    Unlogged  *bool  // UNLOGGED (PostgreSQL 15+); nil = unmanaged
    OwnedBy   string // table.column (or schema.table.column)
    Owner     string // "" = unmanaged
    Grants    map[string][]string // role -> privileges (usage, select, update); nil = unmanaged
    DependsOn []string `yaml:"dependsOn"`
    Comment   string
    RenamedFrom string `yaml:"-"` // previous name; emitted as ALTER ... RENAME when only the old name exists live
}

// Domain is a CREATE DOMAIN declaration: a named base type with optional
// default, NOT NULL, and CHECK constraint (which sees the value as VALUE).
type Domain struct {
    Schema         string
    Name           string
    Type           string // underlying data type
    Collate        string
    Default        string
    NotNull        bool
    NotNullName    string // name of the NOT NULL constraint (PostgreSQL 17+); "" = PostgreSQL's <domain>_not_null
    Check          string // CHECK expression (without surrounding parens)
    ConstraintName string // optional name for the CHECK constraint
    Checks         map[string]string // constraint name -> CHECK expression; nil = only Check is managed
    CheckComments  map[string]string // constraint name -> COMMENT ON CONSTRAINT text
    Owner          string // "" = unmanaged
    Grants         map[string][]string // role -> privileges (usage); nil = unmanaged
    RevokePublic   bool   // revoke PUBLIC's default USAGE
    DependsOn      []string `yaml:"dependsOn"`
    Comment        string
    RenamedFrom string `yaml:"-"` // previous name; emitted as ALTER ... RENAME when only the old name exists live
}

type Table struct {
    Name    string             `yaml:"name"`
    Columns map[string]*Column `yaml:"columns"`
    Foreign *ForeignTable      `yaml:"-"` // set for `foreign table <name>:` declarations
    PrimaryKey []string        `yaml:"-"`
    Of             string      `yaml:"-"` // composite type of a typed table (CREATE TABLE ... OF); its columns come from the type
    PrimaryKeyName string      `yaml:"-"` // constraint name; "" = PostgreSQL's default <table>_pkey
    PrimaryKeyComment string   `yaml:"-"` // COMMENT ON CONSTRAINT for the primary key; "" = unmanaged
    PrimaryKeyInclude []string `yaml:"-"` // INCLUDE (non-key) columns of the primary key's index
    PrimaryKeyWith map[string]string `yaml:"-"` // storage parameters of the primary key's index
    PrimaryKeyWithOrder []string  `yaml:"-"` // order of the PrimaryKeyWith keys in the YAML (filled from node tree)
    PrimaryKeyTablespace string     `yaml:"-"` // tablespace of the primary key's index; "" = unmanaged
    ForceRowLevelSecurity *bool `yaml:"-"` // FORCE ROW LEVEL SECURITY; nil = unmanaged
    Indexes    []*Index        `yaml:"-"`
    ForeignKeys []*ForeignKey  `yaml:"-"`
    Triggers   []*Trigger      `yaml:"-"`
    Rules      []*Rule         `yaml:"-"`
    Statistics []*Statistic    `yaml:"-"`
    ClusterOn  string          `yaml:"-"` // index to CLUSTER ON; "none" = SET WITHOUT CLUSTER; "" = unmanaged
    // ReplicaIdentity is default | full | nothing | index <name>; "" = unmanaged.
    ReplicaIdentity string     `yaml:"-"`
    Constraints []*Constraint  `yaml:"-"`
    ColumnOrder []string       `yaml:"-"`
    DependsOn []string         `yaml:"dependsOn"`
    Grants map[string][]string `yaml:"-"` // role -> privileges; nil means unmanaged
    Owner  string              `yaml:"-"` // "" = unmanaged
    // RowLevelSecurity is tri-state: nil means unmanaged (RLS state is left
    // alone), true means ENABLE, false means DISABLE (gated behind --unsafe,
    // since disabling removes row filtering from every policy on the table).
    RowLevelSecurity *bool     `yaml:"-"`
    Policies []*Policy         `yaml:"-"` // nil means unmanaged
    Comment string             `yaml:"-"` // empty means unmanaged
    PartitionBy *PartitionBy   `yaml:"-"` // declarative partitioning key (parent tables)
    PartitionOf string         `yaml:"-"` // fully qualified parent table (partition children)
    Inherits    []string       `yaml:"-"` // fully qualified parent tables (INHERITS); nil = unmanaged
    Data        *TableData     `yaml:"-"` // managed rows; nil = unmanaged
    Partition   *PartitionSpec `yaml:"-"` // bound spec for partition children
    Unlogged    *bool          `yaml:"-"` // UNLOGGED; nil = unmanaged
    With        map[string]string `yaml:"-"` // storage parameters; nil = unmanaged, present = authoritative
    WithOrder   []string          `yaml:"-"` // order of the With keys in the YAML (filled from node tree)
    Tablespace  string            `yaml:"-"` // TABLESPACE; "" = unmanaged
    AccessMethod string           `yaml:"-"` // USING: table access method; "" = unmanaged
    Validate    string            `yaml:"-"` // how check constraints, foreign keys and NOT NULL added to the existing table are validated: ValidateImmediate or ValidateLater; "" = ValidateLater
    RenamedFrom string `yaml:"-"` // previous name; emitted as ALTER ... RENAME when only the old name exists live
}

// Values of the validate key of tables, check constraints, foreign keys and
// columns: how a constraint added to a table that already exists is
// validated.
const (
    // ValidateLater adds the constraint NOT VALID and validates it in the
    // no-transaction migration, which doesn't block writes. The default.
    ValidateLater = "later"
    // ValidateImmediate adds the constraint validated in one step, which
    // checks the existing rows under the stronger lock of ADD CONSTRAINT
    // (SET NOT NULL for a column), for tables that refuse VALIDATE
    // CONSTRAINT.
    ValidateImmediate = "immediate"
)

// ValidatesImmediately reports whether a constraint with validate value own
// on table t is added validated in one step: own, or else t's validate, is
// ValidateImmediate.
func ValidatesImmediately(t *Table, own string) bool {
    if own == "" && t != nil { own = t.Validate }
    return own == ValidateImmediate
}

// PartitionBy declares a table's partitioning strategy (PARTITION BY).
// Columns may be plain column names or expressions.
type PartitionBy struct {
    Type    string   // range | list | hash
    Columns []string
}

// PartitionSpec is a partition child's bound (FOR VALUES ... / DEFAULT).
// Values are stored as raw strings; the diff layer quotes non-numeric,
// non-keyword values as SQL string literals.
type PartitionSpec struct {
    From      []string // range: FOR VALUES FROM (...)
    To        []string // range: ... TO (...)
    In        []string // list: FOR VALUES IN (...)
    Modulus   int      // hash: -1 = unset
    Remainder int      // hash: -1 = unset
    Default   bool     // DEFAULT partition (no FOR VALUES)
}

type Policy struct {
    Name      string
    As        string   // permissive|restrictive (empty = permissive)
    For       string   // select|insert|update|delete|all (empty = all)
    To        []string // roles (empty = all roles)
    Using     string
    WithCheck string
    Comment   string // empty means unmanaged
}

type Constraint struct {
    Name              string
    Type              string   // check, unique, exclude
    Expression        string   // for check or exclude
    Columns           []string // for unique
    Include           []string // unique: INCLUDE (non-key) columns
    With              map[string]string // unique: index storage parameters, WITH (name = value)
    WithOrder         []string          // order of the With keys in the YAML (filled from node tree)
    Tablespace        string            // unique and exclude: USING INDEX TABLESPACE; "" = unmanaged
    NullsNotDistinct  bool     // unique: NULLS NOT DISTINCT (PostgreSQL 15+)
    Deferrable        bool     // unique/exclude
    InitiallyDeferred bool     // unique/exclude; implies Deferrable
    NoInherit         bool     // check: NO INHERIT
    NotValid          bool     // check: added NOT VALID (existing rows not checked)
    Validate          string   // check: ValidateImmediate or ValidateLater; "" = the table's Validate
    NotEnforced       bool     // check: NOT ENFORCED (PostgreSQL 18+); yaml `enforced: false`
    Comment           string   // empty means unmanaged
    RenamedFrom string `yaml:"-"` // previous name; emitted as ALTER ... RENAME when only the old name exists live
}

type Column struct {
    Type     string `yaml:"type"`
    Nullable bool   `yaml:"nullable"`
    NullableSet bool `yaml:"-"` // nullable or notNull was written; on a partition's or child's inherited column, nullability is managed only then
    Default  string `yaml:"default"`
    Unique   bool   `yaml:"unique"`
    PrimaryKey bool `yaml:"primaryKey"`
    Comment  string `yaml:"comment"`
    Identity string `yaml:"identity"` // "" (none), "always", or "by default"
    IdentityOptions *Sequence `yaml:"-"` // identity sequence options (increment, minValue, maxValue, start, cache, cycle, and Name: sequenceName, unqualified = the table's schema); nil = unmanaged
    Generated string `yaml:"generated"` // GENERATED ALWAYS AS (expr) STORED expression
    Virtual  bool   `yaml:"virtual"`  // generated column is VIRTUAL rather than STORED (PostgreSQL 18+)
    NotValid bool   `yaml:"notValid"` // the NOT NULL constraint is NOT VALID, so existing rows aren't checked (PostgreSQL 18+)
    Validate string `yaml:"validate"` // NOT NULL on an existing column: ValidateImmediate or ValidateLater; "" = the table's Validate
    NotNullName string `yaml:"notNullName"` // name of the NOT NULL constraint (PostgreSQL 18+); "" = unmanaged, and PostgreSQL's <table>_<column>_not_null for a new one
    Collate     string `yaml:"collate"`     // COLLATE; "" = type default
    Compression string `yaml:"compression"` // pglz | lz4 | default; "" = unmanaged
    Storage     string `yaml:"storage"`     // plain | external | extended | main; "" = unmanaged
    StatisticsTarget *int `yaml:"statisticsTarget"` // SET STATISTICS; -1 = default; nil = unmanaged
    NDistinct          *float64 `yaml:"-"` // SET (n_distinct = ...); 0 = no override (RESET); nil = unmanaged
    NDistinctInherited *float64 `yaml:"-"` // SET (n_distinct_inherited = ...); 0 = no override (RESET); nil = unmanaged
    FDWOptions map[string]string `yaml:"-"` // foreign table columns: OPTIONS (...); nil = unmanaged
    Using    string `yaml:"using"`    // USING expression for ALTER COLUMN ... TYPE
    Grants   map[string][]string `yaml:"-"` // role -> column privileges; nil means unmanaged
    RenamedFrom string `yaml:"-"` // previous name; emitted as ALTER ... RENAME when only the old name exists live
}

type Index struct {
    Name      string
    Columns   []string
    Unique    bool
    Using     string            // index method: btree (default), gist, gin, brin, hash, spgist
    Where     string            // partial index predicate
    Opclass   string            // operator class applied to every column (e.g. gin_trgm_ops)
    Opclasses map[string]string // per-column operator class overrides; key matches the columns entry
    Collations map[string]string // per-column COLLATE; key matches the columns entry
    StatisticsTargets map[string]int // per-expression ALTER INDEX ... ALTER COLUMN n SET STATISTICS; key matches the columns entry
    Include   []string          // INCLUDE (non-key) columns
    NullsNotDistinct bool       // NULLS NOT DISTINCT (unique indexes, PostgreSQL 15+)
    With      map[string]string // storage parameters, WITH (name = value)
    WithOrder []string          // order of the With keys in the YAML (filled from node tree)
    Tablespace string           // TABLESPACE; "" = unmanaged
    Concurrently bool           // build on existing tables with CREATE INDEX CONCURRENTLY
    Comment   string            // empty means unmanaged
    RenamedFrom string `yaml:"-"` // previous name; emitted as ALTER ... RENAME when only the old name exists live
}

type ForeignKey struct {
    Name              string
    Columns           []string
    RefTable          string
    RefColumns        []string
    OnDelete          string
    OnUpdate          string
    Match             string // full | simple | partial (empty = simple)
    Deferrable        bool
    InitiallyDeferred bool   // implies Deferrable
    NotValid          bool   // added NOT VALID (existing rows not checked)
    Validate          string // ValidateImmediate or ValidateLater; "" = the table's Validate
    NotEnforced       bool   // NOT ENFORCED (PostgreSQL 18+); yaml `enforced: false`
    Comment           string // empty means unmanaged
    RenamedFrom string `yaml:"-"` // previous name; emitted as ALTER ... RENAME when only the old name exists live
}

type Trigger struct {
    Name    string
    Timing  string // before/after
    Events  []string
    Level   string // row/statement
    Procedure string
    When    string // WHEN (condition) guard, e.g. "old.created_at <= new.created_at"
    Constraint bool        // CREATE CONSTRAINT TRIGGER (must be AFTER ... FOR EACH ROW)
    Deferrable bool
    InitiallyDeferred bool // implies deferrable
    OldTable string        // REFERENCING OLD TABLE AS (AFTER triggers)
    NewTable string        // REFERENCING NEW TABLE AS (AFTER triggers)
    Comment  string        // empty means unmanaged
    Enabled  string        // origin | disabled | replica | always; "" = unmanaged
}

// Statistic is a CREATE STATISTICS object on a table. Kinds is a subset of
// ndistinct, dependencies, mcv (empty = all); Columns are column names or
// expressions.
type Statistic struct {
    Name    string
    Kinds   []string
    Columns []string
    Comment string
    StatisticsTarget *int // ALTER STATISTICS ... SET STATISTICS; -1 = default; nil = unmanaged
}

// Rule is a CREATE RULE on a table or view. PostgreSQL rewrites the action
// when storing it, so only the event, INSTEAD flag and WHERE condition are
// compared against live; Replace re-emits CREATE OR REPLACE RULE on every
// diff for action changes.
type Rule struct {
    Name    string
    Event   string // select | insert | update | delete
    Instead bool
    Where   string
    Do      string // action: nothing, a statement, or (stmt; stmt)
    Replace bool
    Comment string
}

type Extension struct {
    Name        string   `yaml:"name"`
    IfNotExists bool     `yaml:"ifNotExists"`
    Schema      string   `yaml:"schema"`  // WITH SCHEMA; empty = server default
    Version     string   `yaml:"version"` // VERSION; empty = default version, never updated
    Cascade     bool     `yaml:"cascade"`
    DependsOn   []string `yaml:"dependsOn"`
    Comment     string   `yaml:"comment"` // COMMENT ON EXTENSION; "" = unmanaged
}

type TypeDef struct {
    Name   string
    Schema string
    Kind   string   // enum|composite|range
    Labels []string // enum
    Range  *RangeDef // range
    Attributes map[string]string // composite: name->type
    AttributeCollations map[string]string // composite: name -> COLLATE; unset = the type's default
    AttributeComments map[string]string // composite: name -> COMMENT ON COLUMN text
    AttributeOrder []string      // composite: YAML declaration order (filled from node tree)
    DependsOn []string `yaml:"dependsOn"`
    Comment string
    Owner   string // "" = unmanaged
    Grants  map[string][]string // role -> privileges (usage); nil = unmanaged
    RevokePublic bool // revoke PUBLIC's default USAGE
    RenamedFrom string `yaml:"-"` // previous name; emitted as ALTER ... RENAME when only the old name exists live
    RenamedLabels map[string]string `yaml:"-"` // enum label -> previous label (ALTER TYPE ... RENAME VALUE)
}

// RangeDef holds a range type's CREATE TYPE ... AS RANGE options. Subtype is
// required; the rest are optional.
type RangeDef struct {
    Subtype            string
    SubtypeOpclass     string
    Collation          string
    Canonical          string
    SubtypeDiff        string
    MultirangeTypeName string
}

type Function struct {
    Schema  string
    Name    string
    ArgsSig string
    Returns string
    Language string
    Security string // definer/invoker
    Volatility string // stable/volatile/immutable
    Strict *bool    // nil = unmanaged
    Leakproof *bool // nil = unmanaged
    Parallel string // safe | restricted | unsafe; "" = unmanaged
    Cost *float64   // nil = unmanaged
    Rows *float64   // set-returning functions only; nil = unmanaged
    Set map[string]string
    Body string
    // BodySet reports a body key in the YAML, so that an empty body can be
    // told from a declaration without one (which manages only attributes).
    BodySet bool `yaml:"-"`
    // Transform lists the types of the TRANSFORM FOR TYPE clause. nil leaves
    // the routine's transforms unmanaged; an empty list declares none.
    Transform []string
    // Replace re-emits a SQL-standard body (BEGIN ATOMIC / RETURN), which
    // PostgreSQL rewrites when it stores it, so it cannot be compared.
    Replace bool
    DependsOn []string `yaml:"dependsOn"`
    Grants map[string][]string // role -> privileges; nil means unmanaged
    RevokePublic bool          // revoke default PUBLIC execute (security-definer pattern)
    Comment string             // empty means unmanaged
    Owner   string             // "" = unmanaged
    RenamedFrom string `yaml:"-"` // previous name; emitted as ALTER ... RENAME when only the old name exists live
}

// Aggregate is a CREATE AGGREGATE declaration. SFunc and SType are
// required; the other fields are optional.
type Aggregate struct {
    Schema      string
    Name        string
    ArgsSig     string
    SFunc       string
    SType       string
    FinalFunc   string
    InitCond    string
    // InitCondSet reports an initcond key in the YAML, so that the empty
    // string can be told from no initial condition.
    InitCondSet bool `yaml:"-"`
    CombineFunc string
    Parallel    string // safe | restricted | unsafe
    // Options holds the other CREATE AGGREGATE options by SQL name (sspace,
    // finalfunc_extra, finalfunc_modify, serialfunc, deserialfunc, msfunc,
    // minvfunc, mstype, msspace, mfinalfunc, mfinalfunc_extra,
    // mfinalfunc_modify, minitcond, sortop, hypothetical). Options left out
    // are unmanaged. An empty value means unset, except for minitcond, whose
    // empty string is an initial condition.
    Options     map[string]string
    DependsOn   []string `yaml:"dependsOn"`
    Comment     string
    Owner       string
}

// AggregateOptions maps the YAML keys of the optional CREATE AGGREGATE
// options to their SQL names, in the order they are emitted.
var AggregateOptions = [][2]string{
    {"sspace", "sspace"}, {"finalfuncExtra", "finalfunc_extra"}, {"finalfuncModify", "finalfunc_modify"},
    {"serialfunc", "serialfunc"}, {"deserialfunc", "deserialfunc"},
    {"msfunc", "msfunc"}, {"minvfunc", "minvfunc"}, {"mstype", "mstype"}, {"msspace", "msspace"},
    {"mfinalfunc", "mfinalfunc"}, {"mfinalfuncExtra", "mfinalfunc_extra"}, {"mfinalfuncModify", "mfinalfunc_modify"},
    {"minitcond", "minitcond"}, {"sortop", "sortop"}, {"hypothetical", "hypothetical"},
}

// Procedure is a CREATE PROCEDURE declaration (PostgreSQL 11+). Unlike
// functions, procedures have no return type, volatility, or strictness;
// they are invoked with CALL and may control transactions.
type Procedure struct {
    Schema   string
    Name     string
    ArgsSig  string
    Language string
    Security string // definer/invoker
    Set      map[string]string
    Body     string
    BodySet  bool `yaml:"-"` // a body key is present; see Function.BodySet
    Transform []string // TRANSFORM FOR TYPE types; see Function.Transform
    Replace  bool // re-emit a SQL-standard body; see Function.Replace
    DependsOn []string `yaml:"dependsOn"`
    Grants map[string][]string // role -> privileges; nil means unmanaged
    RevokePublic bool          // revoke default PUBLIC execute
    Comment string             // empty means unmanaged
    Owner   string             // "" = unmanaged
    RenamedFrom string `yaml:"-"` // previous name; emitted as ALTER ... RENAME when only the old name exists live
}

// HasBody reports whether f declares a body, which may be empty. Without
// one, f manages only the attributes of a function that already exists.
func (f *Function) HasBody() bool { return f.BodySet || strings.TrimSpace(f.Body) != "" }

// HasBody reports whether p declares a body, which may be empty.
func (p *Procedure) HasBody() bool { return p.BodySet || strings.TrimSpace(p.Body) != "" }

// HasInitCond reports whether ag declares an initial condition, which may
// be the empty string.
func (ag *Aggregate) HasInitCond() bool { return ag.InitCondSet || ag.InitCond != "" }

var languageTransformRe = regexp.MustCompile(`(?is)^\s*(\S+)\s+transform\s+(.*)$`)

var transformTypeRe = regexp.MustCompile(`(?is)^\s*for\s+type\s+(.+?)\s*$`)

// SplitLanguage splits a routine's language into the language name and the
// types of a TRANSFORM FOR TYPE clause written after it ("plpython3u
// transform for type hstore, for type jsonb"). transform is nil when the
// language carries no such clause.
func SplitLanguage(language string) (name string, transform []string) {
    m := languageTransformRe.FindStringSubmatch(language)
    if m == nil { return strings.TrimSpace(language), nil }
    transform = []string{}
    for _, part := range strings.Split(m[2], ",") {
        if t := transformTypeRe.FindStringSubmatch(part); t != nil { transform = append(transform, t[1]) }
    }
    return m[1], transform
}

// RoutineTransform returns the transform types a routine declares: its
// transform list when set, otherwise the types of a TRANSFORM clause in its
// language. nil means the routine's transforms are unmanaged.
func RoutineTransform(language string, transform []string) []string {
    if transform != nil { return transform }
    _, fromLanguage := SplitLanguage(language)
    return fromLanguage
}

// IsSQLStandardBody reports whether body, for a routine in language, is a
// SQL-standard body (BEGIN ATOMIC ... END or RETURN expr) written after the
// routine's attributes rather than as a string literal.
func IsSQLStandardBody(language, body string) bool {
    if !strings.EqualFold(strings.TrimSpace(language), "sql") { return false }
    words := strings.Fields(strings.ToLower(body))
    if len(words) == 0 { return false }
    return words[0] == "return" || (len(words) > 1 && words[0] == "begin" && words[1] == "atomic")
}

func LoadAndMerge(paths []string) (*Database, error) {
    dbs := make([]*Database, 0, len(paths))
    for _, p := range paths {
        d, err := Load(p)
        if err != nil { return nil, err }
        if d != nil { dbs = append(dbs, d) }
    }
    return Merge(dbs), nil
}

func Load(path string) (*Database, error) {
    b, err := os.ReadFile(path)
    if err != nil {
        if errorsIsNotExist(err) { return nil, nil }
        return nil, err
    }
    d, err := parseFlexibleDatabase(b)
    if err != nil { return nil, fmt.Errorf("%s: %w", path, err) }
    return d, nil
}

func Merge(dbs []*Database) *Database {
    merged := &Database{Tables: map[string]*Table{}}
    for _, d := range dbs {
        if d == nil { continue }
        for name, t := range d.Tables {
            if t != nil {
                t.Name = name
            }
            if existing, ok := merged.Tables[name]; ok {
                if ownsColumns(t) && !ownsColumns(existing) {
                    existing.ColumnOrder = appendUnique(append([]string(nil), t.ColumnOrder...), existing.ColumnOrder)
                } else {
                    existing.ColumnOrder = appendUnique(existing.ColumnOrder, t.ColumnOrder)
                }
                if existing.Columns == nil {
                    existing.Columns = map[string]*Column{}
                }
                for cn, c := range t.Columns {
                    existing.Columns[cn] = c
                }
                if t.Grants != nil {
                    existing.Grants = t.Grants
                }
                if t.RowLevelSecurity != nil {
                    existing.RowLevelSecurity = t.RowLevelSecurity
                }
                if t.Policies != nil {
                    existing.Policies = t.Policies
                }
                if t.Comment != "" {
                    existing.Comment = t.Comment
                }
                if t.Owner != "" {
                    existing.Owner = t.Owner
                }
                if t.PartitionBy != nil {
                    existing.PartitionBy = t.PartitionBy
                }
                if t.PartitionOf != "" {
                    existing.PartitionOf = t.PartitionOf
                }
                if t.Of != "" {
                    existing.Of = t.Of
                }
                if t.Inherits != nil {
                    existing.Inherits = t.Inherits
                }
                if t.Data != nil {
                    existing.Data = t.Data
                }
                if t.Partition != nil {
                    existing.Partition = t.Partition
                }
                if t.Unlogged != nil {
                    existing.Unlogged = t.Unlogged
                }
                if t.With != nil {
                    existing.With, existing.WithOrder = t.With, t.WithOrder
                }
                if t.Tablespace != "" {
                    existing.Tablespace = t.Tablespace
                }
                if t.AccessMethod != "" {
                    existing.AccessMethod = t.AccessMethod
                }
                if t.Validate != "" {
                    existing.Validate = t.Validate
                }
                if t.Foreign != nil {
                    existing.Foreign = t.Foreign
                }
                if len(existing.PrimaryKey) == 0 {
                    existing.PrimaryKey, existing.PrimaryKeyName, existing.PrimaryKeyComment = t.PrimaryKey, t.PrimaryKeyName, t.PrimaryKeyComment
                    existing.PrimaryKeyInclude, existing.PrimaryKeyWith, existing.PrimaryKeyTablespace = t.PrimaryKeyInclude, t.PrimaryKeyWith, t.PrimaryKeyTablespace
                    existing.PrimaryKeyWithOrder = t.PrimaryKeyWithOrder
                }
                if t.ForceRowLevelSecurity != nil {
                    existing.ForceRowLevelSecurity = t.ForceRowLevelSecurity
                }
                existing.Indexes = mergeNamed(existing.Indexes, t.Indexes, func(x *Index) string { return x.Name })
                existing.ForeignKeys = mergeNamed(existing.ForeignKeys, t.ForeignKeys, func(x *ForeignKey) string { return x.Name })
                existing.Constraints = mergeNamed(existing.Constraints, t.Constraints, func(x *Constraint) string { return x.Name })
                existing.Triggers = mergeNamed(existing.Triggers, t.Triggers, func(x *Trigger) string { return x.Name })
                existing.Rules = mergeNamed(existing.Rules, t.Rules, func(x *Rule) string { return x.Name })
                existing.Statistics = mergeNamed(existing.Statistics, t.Statistics, func(x *Statistic) string { return x.Name })
                if t.ReplicaIdentity != "" {
                    existing.ReplicaIdentity = t.ReplicaIdentity
                }
                if t.ClusterOn != "" {
                    existing.ClusterOn = t.ClusterOn
                }
                if t.RenamedFrom != "" {
                    existing.RenamedFrom = t.RenamedFrom
                }
                existing.DependsOn = appendUnique(existing.DependsOn, t.DependsOn)
            } else {
                merged.Tables[name] = cloneTable(t)
            }
        }
        // merge extensions (dedupe by name; first declaration wins)
        if d.Extensions != nil && merged.Extensions == nil { merged.Extensions = []*Extension{} }
        for _, ext := range d.Extensions {
            if ext == nil { continue }
            dup := false
            for _, e := range merged.Extensions {
                if e != nil && e.Name == ext.Name { dup = true; break }
            }
            if !dup {
                merged.Extensions = append(merged.Extensions, ext)
            }
        }
        // merge types
        if len(d.Types) > 0 {
            if merged.Types == nil { merged.Types = map[string]*TypeDef{} }
            for k, v := range d.Types { merged.Types[k] = v }
        }
        // merge functions
        if len(d.Functions) > 0 {
            if merged.Functions == nil { merged.Functions = map[string]*Function{} }
            for k, v := range d.Functions { merged.Functions[k] = v }
        }
        // merge procedures
        if len(d.Procedures) > 0 {
            if merged.Procedures == nil { merged.Procedures = map[string]*Procedure{} }
            for k, v := range d.Procedures { merged.Procedures[k] = v }
        }
        if len(d.Aggregates) > 0 {
            if merged.Aggregates == nil { merged.Aggregates = map[string]*Aggregate{} }
            for k, v := range d.Aggregates { merged.Aggregates[k] = v }
        }
        // merge views
        if len(d.Views) > 0 {
            if merged.Views == nil { merged.Views = map[string]*View{} }
            for k, v := range d.Views { merged.Views[k] = v }
        }
        // merge sequences
        if len(d.Sequences) > 0 {
            if merged.Sequences == nil { merged.Sequences = map[string]*Sequence{} }
            for k, v := range d.Sequences { merged.Sequences[k] = v }
        }
        if len(d.TSDictionaries) > 0 {
            if merged.TSDictionaries == nil { merged.TSDictionaries = map[string]*TSDictionary{} }
            for k, v := range d.TSDictionaries { merged.TSDictionaries[k] = v }
        }
        if len(d.TSConfigs) > 0 {
            if merged.TSConfigs == nil { merged.TSConfigs = map[string]*TSConfig{} }
            for k, v := range d.TSConfigs { merged.TSConfigs[k] = v }
        }
        if len(d.Collations) > 0 {
            if merged.Collations == nil { merged.Collations = map[string]*Collation{} }
            for k, v := range d.Collations { merged.Collations[k] = v }
        }
        if d.Publications != nil {
            if merged.Publications == nil { merged.Publications = map[string]*Publication{} }
            for k, v := range d.Publications { merged.Publications[k] = v }
        }
        if d.Servers != nil {
            if merged.Servers == nil { merged.Servers = map[string]*Server{} }
            for k, v := range d.Servers { merged.Servers[k] = v }
        }
        if d.ForeignDataWrappers != nil {
            if merged.ForeignDataWrappers == nil { merged.ForeignDataWrappers = map[string]*ForeignDataWrapper{} }
            for k, v := range d.ForeignDataWrappers { merged.ForeignDataWrappers[k] = v }
        }
        if d.Languages != nil {
            if merged.Languages == nil { merged.Languages = map[string]*Language{} }
            for k, v := range d.Languages { merged.Languages[k] = v }
        }
        if d.EventTriggers != nil {
            if merged.EventTriggers == nil { merged.EventTriggers = map[string]*EventTrigger{} }
            for k, v := range d.EventTriggers { merged.EventTriggers[k] = v }
        }
        // merge domains
        if len(d.Domains) > 0 {
            if merged.Domains == nil { merged.Domains = map[string]*Domain{} }
            for k, v := range d.Domains { merged.Domains[k] = v }
        }
        // merge roles
        if len(d.Roles) > 0 {
            if merged.Roles == nil { merged.Roles = map[string]*Role{} }
            for k, v := range d.Roles { merged.Roles[k] = v }
        }
        // merge schema grants
        if len(d.SchemaGrants) > 0 {
            if merged.SchemaGrants == nil { merged.SchemaGrants = map[string]map[string][]string{} }
            for k, v := range d.SchemaGrants { merged.SchemaGrants[k] = v }
        }
        if len(d.SchemaOwners) > 0 {
            if merged.SchemaOwners == nil { merged.SchemaOwners = map[string]string{} }
            for k, v := range d.SchemaOwners { merged.SchemaOwners[k] = v }
        }
        merged.DefaultPrivileges = append(merged.DefaultPrivileges, d.DefaultPrivileges...)
        if d.DatabaseSettings != nil {
            if merged.DatabaseSettings == nil { merged.DatabaseSettings = map[string]string{} }
            for k, v := range d.DatabaseSettings { merged.DatabaseSettings[k] = v }
        }
        for role, set := range d.DatabaseRoleSettings {
            if merged.DatabaseRoleSettings == nil { merged.DatabaseRoleSettings = map[string]map[string]string{} }
            merged.DatabaseRoleSettings[role] = set
        }
        // merge schema comments
        if len(d.SchemaComments) > 0 {
            if merged.SchemaComments == nil { merged.SchemaComments = map[string]string{} }
            for k, v := range d.SchemaComments { merged.SchemaComments[k] = v }
        }
    }
    return merged
}

func cloneTable(t *Table) *Table {
    if t == nil { return nil }
    c := *t
    c.Columns = make(map[string]*Column, len(t.Columns))
    for k, v := range t.Columns { c.Columns[k] = v }
    c.PrimaryKey = append([]string(nil), t.PrimaryKey...)
    c.Indexes = append([]*Index(nil), t.Indexes...)
    c.ForeignKeys = append([]*ForeignKey(nil), t.ForeignKeys...)
    c.Constraints = append([]*Constraint(nil), t.Constraints...)
    c.Triggers = append([]*Trigger(nil), t.Triggers...)
    c.Rules = append([]*Rule(nil), t.Rules...)
    c.Statistics = append([]*Statistic(nil), t.Statistics...)
    c.DependsOn = append([]string(nil), t.DependsOn...)
    c.ColumnOrder = append([]string(nil), t.ColumnOrder...)
    return &c
}

func ownsColumns(t *Table) bool {
    if len(t.PrimaryKey) > 0 { return true }
    for _, c := range t.Columns {
        if c != nil && c.PrimaryKey { return true }
    }
    return false
}

// parseFlexibleDatabase accepts multiple structures:
// 1) { tables: { t: { columns: { c: {type,...} } } } }
// 2) { tables: [ { name: t, schema: public, columns: [ {name: c, type:...} ] } ] }
// 3) { schemas: { public: { tables: {... or [...] } } } }
func parseFlexibleDatabase(b []byte) (*Database, error) {
    // Generic map for flexible parsing
    var root map[string]any
    // Also capture YAML node tree to preserve order information
    node, err := DecodeYAML(b, &root)
    if err != nil {
        return nil, err
    }
    out := &Database{Tables: map[string]*Table{}}
    out.Types = map[string]*TypeDef{}
    out.Functions = map[string]*Function{}
    out.Procedures = map[string]*Procedure{}
    out.Aggregates = map[string]*Aggregate{}
    out.Views = map[string]*View{}
    out.Sequences = map[string]*Sequence{}
    out.Domains = map[string]*Domain{}
    out.Collations = map[string]*Collation{}
    out.TSDictionaries = map[string]*TSDictionary{}
    out.TSConfigs = map[string]*TSConfig{}
    // extensions top-level
    if extsRaw, ok := root["extensions"]; ok {
        out.Extensions = []*Extension{}
        if arr, ok := extsRaw.([]any); ok {
            for _, it := range arr {
                switch v := it.(type) {
                case string:
                    // shorthand: `extensions: [pg_trgm, pgcrypto]` — implies
                    // ifNotExists, matching the tool's if-not-exists style
                    if v != "" {
                        out.Extensions = append(out.Extensions, &Extension{Name: v, IfNotExists: true})
                    }
                case map[string]any:
                    name, _ := v["name"].(string)
                    if name == "" { continue }
                    ext := &Extension{Name: name}
                    if b, ok := v["ifNotExists"].(bool); ok { ext.IfNotExists = b }
                    if sc, ok := v["schema"].(string); ok { ext.Schema = strings.TrimSpace(sc) }
                    if ver, ok := v["version"]; ok { ext.Version = scalarToString(ver) }
                    if b, ok := v["cascade"].(bool); ok { ext.Cascade = b }
                    if c, ok := v["comment"].(string); ok { ext.Comment = c }
                    if dep, ok := v["dependsOn"]; ok {
                        ext.DependsOn = parseStringListFromNode(dep)
                    }
                    out.Extensions = append(out.Extensions, ext)
                }
            }
        }
    }
    // Handle schemas form
    if schRaw, ok := root["schemas"]; ok {
        if m, ok := schRaw.(map[string]any); ok {
            for schemaName, v := range m {
                // intercept schema-level grants/comment so they are not parsed as tables
                if inner, ok := v.(map[string]any); ok {
                    if gRaw, ok := inner["grants"]; ok {
                        if g := parseGrants(gRaw); g != nil {
                            if out.SchemaGrants == nil { out.SchemaGrants = map[string]map[string][]string{} }
                            out.SchemaGrants[schemaName] = g
                            delete(inner, "grants")
                        }
                    }
                    if cm, ok := inner["comment"].(string); ok {
                        if cm != "" {
                            if out.SchemaComments == nil { out.SchemaComments = map[string]string{} }
                            out.SchemaComments[schemaName] = cm
                        }
                        delete(inner, "comment")
                    }
                    if o, ok := inner["owner"].(string); ok {
                        if o = strings.TrimSpace(o); o != "" {
                            if out.SchemaOwners == nil { out.SchemaOwners = map[string]string{} }
                            out.SchemaOwners[schemaName] = o
                        }
                        delete(inner, "owner")
                    }
                }
                mergeTablesInto(out, schemaName, v)
            }
        }
    }
    if dpRaw, ok := root["defaultPrivileges"]; ok {
        out.DefaultPrivileges = parseDefaultPrivileges(dpRaw)
    }
    if dbRaw, ok := root["database"].(map[string]any); ok {
        if set, ok := dbRaw["set"].(map[string]any); ok {
            out.DatabaseSettings = map[string]string{}
            for k, v := range set { out.DatabaseSettings[k] = scalarToString(v) }
        }
        if roles, ok := dbRaw["roles"].(map[string]any); ok {
            out.DatabaseRoleSettings = map[string]map[string]string{}
            for role, def := range roles {
                set := map[string]string{}
                if m, ok := def.(map[string]any); ok {
                    for k, v := range m { set[k] = scalarToString(v) }
                }
                out.DatabaseRoleSettings[role] = set
            }
        }
    }
    if srvRaw, ok := root["servers"]; ok {
        out.Servers = parseServers(srvRaw)
    }
    if fdwRaw, ok := root["foreignDataWrappers"]; ok {
        out.ForeignDataWrappers = parseForeignDataWrappers(fdwRaw)
    }
    if langRaw, ok := root["languages"]; ok {
        out.Languages = parseLanguages(langRaw)
    }
    if pubRaw, ok := root["publications"]; ok {
        out.Publications = parsePublications(pubRaw)
    }
    if etRaw, ok := root["eventTriggers"]; ok {
        out.EventTriggers = parseEventTriggers(etRaw)
    }
    // roles top-level (cluster-scoped, no schema qualification)
    if rolesRaw, ok := root["roles"]; ok {
        if r := parseRoles(rolesRaw); len(r) > 0 {
            out.Roles = r
        }
    }
    // Handle top-level tables
    if tblRaw, ok := root["tables"]; ok {
        mergeTablesInto(out, "", tblRaw)
    }
    // Handle keys like "schema public:" blocks
    for k, v := range root {
        if strings.HasPrefix(k, "schema ") {
            schemaName := bareName(strings.TrimSpace(strings.TrimPrefix(k, "schema ")))
            mergeSchemaBlock(out, schemaName, v)
            // fill column order from node if available
            fillColumnOrderFromNode(node, out, schemaName)
        }
    }
    fillFDWScalarsFromNode(node, out)
    fillWithOrderFromNode(node, out)
    return out, nil
}

// mergeNamed appends items from add to base, replacing base entries whose
// name matches (later files win). Unnamed entries are always appended.
func mergeNamed[T any](base, add []T, name func(T) string) []T {
    for _, a := range add {
        n := name(a)
        replaced := false
        if n != "" {
            for i, b := range base {
                if name(b) == n {
                    base[i] = a
                    replaced = true
                    break
                }
            }
        }
        if !replaced {
            base = append(base, a)
        }
    }
    return base
}

// appendUnique appends items from add that are not already in base.
func appendUnique(base, add []string) []string {
    for _, a := range add {
        found := false
        for _, b := range base {
            if b == a {
                found = true
                break
            }
        }
        if !found {
            base = append(base, a)
        }
    }
    return base
}

func mergeTablesInto(db *Database, defaultSchema string, v any) {
    switch tt := v.(type) {
    case map[string]any:
        // tables map: name -> spec
        for tname, tv := range tt {
            fq := qualify(defaultSchema, tname)
            t := &Table{Name: fq, Columns: map[string]*Column{}}
            if m, ok := tv.(map[string]any); ok {
                if cRaw, ok := m["columns"]; ok {
                    t.Columns = parseColumns(cRaw)
                }
                if pkRaw, ok := m["primaryKey"]; ok {
                    t.parsePrimaryKey(pkRaw)
                }
                if idxRaw, ok := m["indexes"]; ok {
                    t.Indexes = parseIndexes(idxRaw)
                }
                if fkRaw, ok := m["foreignKeys"]; ok {
                    t.ForeignKeys = parseForeignKeys(fkRaw)
                }
                if trgRaw, ok := m["triggers"]; ok {
                    t.Triggers = parseTriggers(trgRaw)
                }
                if rRaw, ok := m["rules"]; ok {
                    t.Rules = parseRules(rRaw)
                }
                if sRaw, ok := m["statistics"]; ok {
                    t.Statistics = parseStatistics(sRaw)
                }
                if ri, ok := m["replicaIdentity"]; ok {
                    t.ReplicaIdentity = parseReplicaIdentity(ri)
                }
                if co, ok := m["clusterOn"].(string); ok { t.ClusterOn = strings.TrimSpace(co) }
                if v, ok := m["renamedFrom"].(string); ok { t.RenamedFrom = strings.TrimSpace(v) }
                if conRaw, ok := m["constraints"]; ok {
                    t.Constraints = parseConstraints(conRaw)
                }
                if dep, ok := m["dependsOn"]; ok {
                    t.DependsOn = parseStringListFromNode(dep)
                }
                if gRaw, ok := m["grants"]; ok {
                    t.Grants = parseGrants(gRaw)
                }
                if rls, ok := m["rowLevelSecurity"].(bool); ok {
                    t.RowLevelSecurity = &rls
                }
                if polRaw, ok := m["policies"]; ok {
                    t.Policies = parsePolicies(polRaw)
                }
                if cm, ok := m["comment"].(string); ok { t.Comment = cm }
                if o, ok := m["owner"].(string); ok { t.Owner = strings.TrimSpace(o) }
                parseTablePartitioning(t, defaultSchema, m)
                t.Data = parseTableData(m["data"])
                parseTableStorage(t, m)
            }
            t.RenamedFrom = qualifyQuoted(defaultSchema, t.RenamedFrom)
            db.Tables[fq] = t
        }
    case []any:
        // tables array: each element has name/schema/columns
        for _, item := range tt {
            m, ok := item.(map[string]any)
            if !ok { continue }
            name, _ := nameScalar(m["name"])
            schemaName := defaultSchema
            if sc, ok := m["schema"].(string); ok && sc != "" {
                schemaName = sc
            }
            fq := qualify(schemaName, name)
            t := &Table{Name: fq, Columns: parseColumns(m["columns"]) }
            if pkRaw, ok := m["primaryKey"]; ok {
                t.parsePrimaryKey(pkRaw)
            }
            if idxRaw, ok := m["indexes"]; ok {
                t.Indexes = parseIndexes(idxRaw)
            }
            if fkRaw, ok := m["foreignKeys"]; ok {
                t.ForeignKeys = parseForeignKeys(fkRaw)
            }
            if trgRaw, ok := m["triggers"]; ok {
                t.Triggers = parseTriggers(trgRaw)
            }
            if rRaw, ok := m["rules"]; ok {
                t.Rules = parseRules(rRaw)
            }
            if sRaw, ok := m["statistics"]; ok {
                t.Statistics = parseStatistics(sRaw)
            }
            if ri, ok := m["replicaIdentity"]; ok {
                t.ReplicaIdentity = parseReplicaIdentity(ri)
            }
            if co, ok := m["clusterOn"].(string); ok { t.ClusterOn = strings.TrimSpace(co) }
            if v, ok := m["renamedFrom"].(string); ok { t.RenamedFrom = strings.TrimSpace(v) }
            if conRaw, ok := m["constraints"]; ok {
                t.Constraints = parseConstraints(conRaw)
            }
            if dep, ok := m["dependsOn"]; ok {
                t.DependsOn = parseStringListFromNode(dep)
            }
            if gRaw, ok := m["grants"]; ok {
                t.Grants = parseGrants(gRaw)
            }
            if rls, ok := m["rowLevelSecurity"].(bool); ok {
                t.RowLevelSecurity = &rls
            }
            if polRaw, ok := m["policies"]; ok {
                t.Policies = parsePolicies(polRaw)
            }
            if cm, ok := m["comment"].(string); ok { t.Comment = cm }
            if o, ok := m["owner"].(string); ok { t.Owner = strings.TrimSpace(o) }
            parseTablePartitioning(t, schemaName, m)
            t.Data = parseTableData(m["data"])
            parseTableStorage(t, m)
            t.RenamedFrom = qualifyQuoted(schemaName, t.RenamedFrom)
            db.Tables[fq] = t
        }
    }
}

// mergeSchemaBlock parses blocks of the form:
// schema <name>:
//   table <t>:
//     columns: { ... }
//   table <t2>:
//     columns: [...]
func mergeSchemaBlock(db *Database, schemaName string, v any) {
    m, ok := v.(map[string]any)
    if !ok { return }
    for key, body := range m {
        if tname, foreign := tableKey(key); tname != "" {
            fq := qualify(schemaName, tname)
            t := &Table{Name: fq, Columns: map[string]*Column{}}
            if foreign { t.Foreign = &ForeignTable{} }
            if inner, ok := body.(map[string]any); ok {
                if foreign {
                    if s, ok := inner["server"].(string); ok { t.Foreign.Server = strings.TrimSpace(s) }
                    if o, ok := inner["options"].(map[string]any); ok { t.Foreign.Options = stringMap(o) }
                }
                if cRaw, ok := inner["columns"]; ok {
                    t.Columns = parseColumns(cRaw)
                }
                if pkRaw, ok := inner["primaryKey"]; ok {
                    t.parsePrimaryKey(pkRaw)
                }
                if idxRaw, ok := inner["indexes"]; ok {
                    t.Indexes = parseIndexes(idxRaw)
                }
                if fkRaw, ok := inner["foreignKeys"]; ok {
                    t.ForeignKeys = parseForeignKeys(fkRaw)
                }
                if trgRaw, ok := inner["triggers"]; ok {
                    t.Triggers = parseTriggers(trgRaw)
                }
                if rRaw, ok := inner["rules"]; ok {
                    t.Rules = parseRules(rRaw)
                }
                if sRaw, ok := inner["statistics"]; ok {
                    t.Statistics = parseStatistics(sRaw)
                }
                if ri, ok := inner["replicaIdentity"]; ok {
                    t.ReplicaIdentity = parseReplicaIdentity(ri)
                }
                if co, ok := inner["clusterOn"].(string); ok { t.ClusterOn = strings.TrimSpace(co) }
                if v, ok := inner["renamedFrom"].(string); ok { t.RenamedFrom = strings.TrimSpace(v) }
                if conRaw, ok := inner["constraints"]; ok {
                    t.Constraints = parseConstraints(conRaw)
                }
                if dep, ok := inner["dependsOn"]; ok {
                    t.DependsOn = parseStringListFromNode(dep)
                }
                if gRaw, ok := inner["grants"]; ok {
                    t.Grants = parseGrants(gRaw)
                }
                if rls, ok := inner["rowLevelSecurity"].(bool); ok {
                    t.RowLevelSecurity = &rls
                }
                if f, ok := inner["forceRowLevelSecurity"].(bool); ok {
                    t.ForceRowLevelSecurity = &f
                }
                if polRaw, ok := inner["policies"]; ok {
                    t.Policies = parsePolicies(polRaw)
                }
                if cm, ok := inner["comment"].(string); ok { t.Comment = cm }
                if o, ok := inner["owner"].(string); ok { t.Owner = strings.TrimSpace(o) }
                parseTablePartitioning(t, schemaName, inner)
                t.Data = parseTableData(inner["data"])
                parseTableStorage(t, inner)
            }
            t.RenamedFrom = qualifyQuoted(schemaName, t.RenamedFrom)
            db.Tables[fq] = t
        } else if strings.HasPrefix(key, "function ") {
            fn := parseFunction(schemaName, key, body)
            if fn != nil {
                full := qualify(schemaName, fn.Name)
                fn.Name, fn.RenamedFrom = bareName(fn.Name), qualifyQuoted(schemaName, fn.RenamedFrom)
                db.Functions[FunctionKey(full, fn.ArgsSig)] = fn
            }
        } else if strings.HasPrefix(key, "procedure ") {
            pr := parseProcedure(schemaName, key, body)
            if pr != nil {
                full := qualify(schemaName, pr.Name)
                pr.Name, pr.RenamedFrom = bareName(pr.Name), qualifyQuoted(schemaName, pr.RenamedFrom)
                db.Procedures[FunctionKey(full, pr.ArgsSig)] = pr
            }
        } else if strings.HasPrefix(key, "aggregate ") {
            if ag := parseAggregate(schemaName, key, body); ag != nil {
                full := qualify(schemaName, ag.Name)
                ag.Name = bareName(ag.Name)
                db.Aggregates[FunctionKey(full, ag.ArgsSig)] = ag
            }
        } else if strings.HasPrefix(key, "type ") {
            td := parseType(schemaName, key, body)
            if td != nil {
                full := qualify(schemaName, td.Name)
                td.Name, td.RenamedFrom = bareName(td.Name), qualifyQuoted(schemaName, td.RenamedFrom)
                db.Types[full] = td
            }
        } else if strings.HasPrefix(key, "view ") || strings.HasPrefix(key, "materialized view ") {
            vw := parseView(schemaName, key, body)
            if vw != nil {
                full := qualify(schemaName, vw.Name)
                vw.Name, vw.RenamedFrom = bareName(vw.Name), qualifyQuoted(schemaName, vw.RenamedFrom)
                db.Views[full] = vw
            }
        } else if strings.HasPrefix(key, "sequence ") {
            sq := parseSequence(schemaName, key, body)
            if sq != nil {
                full := qualify(schemaName, sq.Name)
                sq.Name, sq.RenamedFrom = bareName(sq.Name), qualifyQuoted(schemaName, sq.RenamedFrom)
                db.Sequences[full] = sq
            }
        } else if strings.HasPrefix(key, "text search configuration ") {
            if c := parseTSConfig(schemaName, key, body); c != nil {
                full := qualify(schemaName, c.Name)
                c.Name = bareName(c.Name)
                db.TSConfigs[full] = c
            }
        } else if strings.HasPrefix(key, "text search dictionary ") {
            if d := parseTSDictionary(schemaName, key, body); d != nil {
                full := qualify(schemaName, d.Name)
                d.Name = bareName(d.Name)
                db.TSDictionaries[full] = d
            }
        } else if strings.HasPrefix(key, "collation ") {
            if co := parseCollation(schemaName, key, body); co != nil {
                full := qualify(schemaName, co.Name)
                co.Name = bareName(co.Name)
                db.Collations[full] = co
            }
        } else if strings.HasPrefix(key, "domain ") {
            dm := parseDomain(schemaName, key, body)
            if dm != nil {
                full := qualify(schemaName, dm.Name)
                dm.Name, dm.RenamedFrom = bareName(dm.Name), qualifyQuoted(schemaName, dm.RenamedFrom)
                db.Domains[full] = dm
            }
        } else if key == "grants" {
            if g := parseGrants(body); g != nil {
                if db.SchemaGrants == nil { db.SchemaGrants = map[string]map[string][]string{} }
                db.SchemaGrants[schemaName] = g
            }
        } else if key == "owner" {
            if o, ok := body.(string); ok && strings.TrimSpace(o) != "" {
                if db.SchemaOwners == nil { db.SchemaOwners = map[string]string{} }
                db.SchemaOwners[schemaName] = strings.TrimSpace(o)
            }
        } else if key == "comment" {
            if cm, ok := body.(string); ok && cm != "" {
                if db.SchemaComments == nil { db.SchemaComments = map[string]string{} }
                db.SchemaComments[schemaName] = cm
            }
        }
    }
}

func parseColumns(v any) map[string]*Column {
    cols := map[string]*Column{}
    switch cc := v.(type) {
    case map[string]any:
        for name, spec := range cc {
            cols[name] = parseColumnSpec(spec)
        }
    case []any:
        for _, item := range cc {
            if m, ok := item.(map[string]any); ok {
                name, _ := nameScalar(m["name"])
                cols[name] = parseColumnSpec(m)
            }
        }
    }
    return cols
}

func parseColumnSpec(spec any) *Column {
    c := &Column{}
    if m, ok := spec.(map[string]any); ok {
        if t, ok := m["type"].(string); ok { c.Type = t }
        if v, ok := m["renamedFrom"].(string); ok { c.RenamedFrom = strings.TrimSpace(v) }
        if n, ok := m["nullable"].(bool); ok { c.Nullable, c.NullableSet = n, true }
        if nn, ok := m["notNull"].(bool); ok { c.Nullable, c.NullableSet = !nn, true }
        if d, ok := m["default"]; ok { c.Default = defaultToString(d) }
        if u, ok := m["unique"].(bool); ok { c.Unique = u }
        if pk, ok := m["primaryKey"].(bool); ok { c.PrimaryKey = pk }
        if cm, ok := m["comment"].(string); ok { c.Comment = cm }
        if id, ok := m["identity"]; ok { c.Identity, c.IdentityOptions = parseIdentity(id), parseIdentityOptions(id) }
        if g, ok := m["generated"].(string); ok { c.Generated = strings.TrimSpace(g) }
        if v, ok := m["virtual"].(bool); ok { c.Virtual = v }
        if v, ok := m["notValid"].(bool); ok { c.NotValid = v }
        if v, ok := m["validate"].(string); ok { c.Validate = strings.ToLower(strings.TrimSpace(v)) }
        if v, ok := m["notNullName"].(string); ok { c.NotNullName = strings.TrimSpace(v) }
        if v, ok := m["collate"].(string); ok { c.Collate = strings.TrimSpace(v) }
        if v, ok := m["compression"].(string); ok { c.Compression = strings.ToLower(strings.TrimSpace(v)) }
        if v, ok := m["storage"].(string); ok { c.Storage = strings.ToLower(strings.TrimSpace(v)) }
    if v, ok := m["statisticsTarget"].(int); ok { c.StatisticsTarget = &v }
    c.NDistinct = floatPtr(m["nDistinct"])
    c.NDistinctInherited = floatPtr(m["nDistinctInherited"])
    if o, ok := m["options"].(map[string]any); ok { c.FDWOptions = stringMap(o) }
        if u, ok := m["using"].(string); ok { c.Using = u }
        if gRaw, ok := m["grants"]; ok { c.Grants = parseGrants(gRaw) }
    }
    return c
}

// parseIdentity normalizes the `identity` column property to "always" or
// "by default". Accepts `always`, `byDefault`, `by default`, `default`, and
// bare `true` (treated as ALWAYS, the stricter form). Unknown values = unset.
func parseIdentity(v any) string {
    switch x := v.(type) {
    case map[string]any:
        if g, ok := x["generated"]; ok { return parseIdentity(g) }
        return "always"
    case bool:
        if x { return "always" }
    case string:
        switch strings.ToLower(strings.TrimSpace(x)) {
        case "always":
            return "always"
        case "bydefault", "by default", "default":
            return "by default"
        }
    }
    return ""
}

// parseIdentityOptions reads the sequence options of an identity: {generated,
// increment, minValue, maxValue, start, cache, cycle}. It returns nil for the
// scalar forms.
func parseIdentityOptions(v any) *Sequence {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    sq := &Sequence{}
    for key, dst := range map[string]*string{"increment": &sq.Increment, "minValue": &sq.MinValue, "maxValue": &sq.MaxValue, "start": &sq.Start, "cache": &sq.Cache} {
        if x, ok := m[key]; ok { *dst = defaultToString(x) }
    }
    if b, ok := m["cycle"].(bool); ok { sq.Cycle = &b }
    if s, ok := m["sequenceName"].(string); ok { sq.Name = strings.TrimSpace(s) }
    return sq
}

// defaultToString coerces a YAML scalar default (bool, int, float) to its SQL
// literal form so `default: false` is not silently dropped.
func defaultToString(v any) string {
    switch x := v.(type) {
    case string:
        return x
    case bool, int, int64, uint64, float64:
        return fmt.Sprintf("%v", x)
    }
    return ""
}

// parsePolicies parses { name: {as, for, to, using, withCheck}, ... } sorted by name.
func parsePolicies(v any) []*Policy {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    out := []*Policy{}
    for name, def := range m {
        p := &Policy{Name: name}
        if dm, ok := def.(map[string]any); ok {
            if f, ok := dm["for"].(string); ok { p.For = strings.ToLower(f) }
            if as, ok := dm["as"].(string); ok { p.As = strings.ToLower(strings.TrimSpace(as)) }
            if to, ok := dm["to"].(string); ok {
                p.To = []string{to}
            } else if to, ok := dm["to"]; ok {
                p.To = parseStringListFromNode(to)
            }
            if u, ok := dm["using"].(string); ok { p.Using = u }
            if wc, ok := dm["withCheck"].(string); ok { p.WithCheck = wc }
            if cm, ok := dm["comment"].(string); ok { p.Comment = cm }
        }
        out = append(out, p)
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

// parseRoles parses { name: {login, superuser, createdb, createrole,
// replication, bypassRLS, inherit, connectionLimit, inRoles, comment}, ... }.
func parseRoles(v any) map[string]*Role {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    out := map[string]*Role{}
    for name, def := range m {
        r := &Role{Name: name, ConnectionLimit: -1, Managed: map[string]bool{}}
        if dm, ok := def.(map[string]any); ok {
            for _, k := range []string{"login", "superuser", "createdb", "createrole", "replication", "bypassRLS", "inherit", "connectionLimit", "validUntil"} {
                if _, ok := dm[k]; ok { r.Managed[k] = true }
            }
            if b, ok := dm["login"].(bool); ok { r.Login = b }
            if b, ok := dm["superuser"].(bool); ok { r.Superuser = b }
            if b, ok := dm["createdb"].(bool); ok { r.CreateDB = b }
            if b, ok := dm["createrole"].(bool); ok { r.CreateRole = b }
            if b, ok := dm["replication"].(bool); ok { r.Replication = b }
            if b, ok := dm["bypassRLS"].(bool); ok { r.BypassRLS = b }
            if b, ok := dm["inherit"].(bool); ok { r.NoInherit = !b }
            if n, ok := dm["connectionLimit"].(int); ok { r.ConnectionLimit = n }
            if ir, ok := dm["inRoles"].([]any); ok { r.InRoles, r.MemberOptions = parseInRoles(ir) }
            switch v := dm["validUntil"].(type) {
            case time.Time:
                r.ValidUntil = v.UTC().Format(time.RFC3339)
            case string:
                r.ValidUntil = strings.TrimSpace(v)
            }
            if set, ok := dm["set"].(map[string]any); ok {
                r.Set = map[string]string{}
                for k, v := range set { r.Set[k] = scalarToString(v) }
            }
            if cm, ok := dm["comment"].(string); ok { r.Comment = cm }
        }
        out[name] = r
    }
    return out
}

// parseInRoles parses a role's inRoles: parent role names, or maps
// {role, admin, inherit, set} that also set the membership's options.
func parseInRoles(items []any) ([]string, map[string]*MembershipOptions) {
    names := []string{}
    var opts map[string]*MembershipOptions
    for _, it := range items {
        switch x := it.(type) {
        case string:
            names = append(names, x)
        case map[string]any:
            name, _ := x["role"].(string)
            if name = strings.TrimSpace(name); name == "" { continue }
            names = append(names, name)
            o := &MembershipOptions{}
            for key, dst := range map[string]**bool{"admin": &o.Admin, "inherit": &o.Inherit, "set": &o.Set} {
                if b, ok := x[key].(bool); ok { *dst = &b }
            }
            if opts == nil { opts = map[string]*MembershipOptions{} }
            opts[name] = o
        }
    }
    return names, opts
}

// parseGrants parses { role: [priv, ...], ... }. Privileges are lowercased.
// Returns nil (unmanaged) if the value has no valid role -> list entries.
// parseGrants builds the role -> privileges map for a grants block. A role
// mapped to an empty list is kept as an empty (non-nil) entry rather than
// dropped: it means "this role should hold no privileges here", which the diff
// layer turns into a REVOKE. Likewise a present-but-empty block returns an
// empty map, not nil — nil is reserved for "no grants block at all", i.e.
// unmanaged, where live privileges are left untouched.
func parseGrants(v any) map[string][]string {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    out := map[string][]string{}
    for role, privs := range m {
        list := parseStringListFromNode(privs)
        for i := range list {
            p := strings.Join(strings.Fields(strings.ToLower(list[i])), " ")
            if base, ok := strings.CutSuffix(p, " with grant option"); ok { p = base + "*" }
            list[i] = p
        }
        sort.Strings(list)
        out[role] = list
    }
    return out
}

// BoolPtr returns a pointer to b, for setting tri-state fields such as
// Table.RowLevelSecurity where nil means unmanaged.
func BoolPtr(b bool) *bool { return &b }

// qualify returns the model key, schema.name, of a name written in YAML for
// an object of schemaName ("" for public). A name with a dot is taken as
// schema-qualified, unless the object's name is in double quotes (see
// SplitName): "x.y" and app."x.y" both name the object x.y.
func qualify(schemaName, tableName string) string {
    if tableName == "" { return tableName }
    if schemaName == "" { schemaName = "public" }
    if parts, ok := SplitName(tableName); ok && len(parts) <= 2 {
        if len(parts) == 2 { return parts[0] + "." + parts[1] }
        return schemaName + "." + parts[0]
    }
    if strings.Contains(tableName, ".") { return tableName }
    return schemaName + "." + tableName
}

// SplitName splits a name written in YAML at the dots between its parts
// (schema, object, column), reading a part in double quotes as one name,
// dots included, with "" standing for a double quote: app."x.y" is
// [app x.y]. ok is false when name has no double quote, in which case each
// place keeps its own rule for its dots, or when it is not a dot-separated
// list of names.
func SplitName(name string) (parts []string, ok bool) {
    s := strings.TrimSpace(name)
    if !strings.Contains(s, `"`) { return nil, false }
    for {
        var part string
        if strings.HasPrefix(s, `"`) {
            var b strings.Builder
            i, closed := 1, false
            for i < len(s) && !closed {
                switch {
                case s[i] != '"':
                    b.WriteByte(s[i])
                    i++
                case i+1 < len(s) && s[i+1] == '"':
                    b.WriteByte('"')
                    i += 2
                default:
                    closed = true
                    i++
                }
            }
            if !closed || b.Len() == 0 { return nil, false }
            part, s = b.String(), s[i:]
        } else {
            i := strings.IndexByte(s, '.')
            if i < 0 { i = len(s) }
            part, s = s[:i], s[i:]
            if part == "" || strings.Contains(part, `"`) { return nil, false }
        }
        parts = append(parts, part)
        if s == "" { return parts, true }
        if s[0] != '.' { return nil, false }
        s = s[1:]
    }
}

// QuoteName returns an object's name as YAML has to write it where a dot
// separates a schema from it: in double quotes, with "" for a double quote,
// when it contains a dot or a double quote, and as it is otherwise.
// SplitName and the YAML keys of schema blocks read it back.
func QuoteName(name string) string {
    if !strings.ContainsAny(name, `."`) { return name }
    return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QualifiedName writes the model key fq (schema.name) as a reference in
// YAML, its name quoted where QuoteName quotes it: app."x.y".
func QualifiedName(fq string) string {
    s, name, ok := strings.Cut(fq, ".")
    if !ok { return QuoteName(fq) }
    return s + "." + QuoteName(name)
}

// bareName returns name without the double quotes it is written in, as
// SplitName reads a single quoted name, or as it is.
func bareName(name string) string {
    if parts, ok := SplitName(name); ok && len(parts) == 1 { return parts[0] }
    return name
}

// qualifyQuoted returns a reference written with double quotes (see
// SplitName) as the model key it names, in schemaName when it has no
// schema, so that the places that read references keep their rule for
// dots in the others. Anything else is returned as it is.
func qualifyQuoted(schemaName, ref string) string {
    if !strings.Contains(ref, `"`) { return ref }
    return qualify(schemaName, ref)
}

// OwnedByParts returns the schema, table and column a sequence's ownedBy
// names. seq is the sequence's key, whose schema an unqualified table.column
// is in. Parts in double quotes are read with SplitName; otherwise a single
// dot separates table and column, and with more dots the first separates
// the schema and everything after the table is the column. ok is false for
// none and for a value that names no column.
func OwnedByParts(seq, ownedBy string) (schemaName, table, column string, ok bool) {
    s := strings.TrimSpace(ownedBy)
    if s == "" || strings.EqualFold(s, "none") { return "", "", "", false }
    seqSchema, _, _ := strings.Cut(seq, ".")
    parts, quoted := SplitName(s)
    if !quoted {
        if strings.Count(s, ".") == 1 {
            parts = strings.SplitN(s, ".", 2)
        } else {
            parts = strings.SplitN(s, ".", 3)
        }
    }
    switch len(parts) {
    case 2:
        return seqSchema, parts[0], parts[1], true
    case 3:
        return parts[0], parts[1], parts[2], true
    }
    return "", "", "", false
}

// parseStringListFromNode reads a list of names. An entry YAML resolved to
// a number, a boolean or null, such as the column 2024 in
// `primaryKey: [id, 2024]`, is taken as the name it spells.
func parseStringListFromNode(v any) []string {
    out := []string{}
    switch x := v.(type) {
    case []any:
        for _, it := range x {
            if s, ok := listName(it); ok { out = append(out, s) }
        }
    case map[string]any:
        // map name -> {columns:[...]}
        for _, def := range x {
            if m, ok := def.(map[string]any); ok {
                if cols, ok := m["columns"]; ok {
                    out = append(out, parseStringListFromNode(cols)...)
                }
            }
        }
    }
    return out
}

func parseIndexes(v any) []*Index {
    out := []*Index{}
    if m, ok := v.(map[string]any); ok {
        for name, def := range m {
            ix := &Index{Name: name}
            if dm, ok := def.(map[string]any); ok {
                ix.Columns = parseStringListFromNode(dm["columns"])
                if v, ok := dm["renamedFrom"].(string); ok { ix.RenamedFrom = strings.TrimSpace(v) }
                if u, ok := dm["unique"].(bool); ok { ix.Unique = u }
                if m, ok := dm["using"].(string); ok { ix.Using = m }
                if w, ok := dm["where"].(string); ok { ix.Where = w }
                if oc, ok := dm["opclass"].(string); ok { ix.Opclass = oc }
                if cm, ok := dm["comment"].(string); ok { ix.Comment = cm }
                if inc, ok := dm["include"]; ok { ix.Include = parseStringListFromNode(inc) }
                if b, ok := dm["nullsNotDistinct"].(bool); ok { ix.NullsNotDistinct = b }
                if b, ok := dm["concurrently"].(bool); ok { ix.Concurrently = b }
                if ts, ok := dm["tablespace"].(string); ok { ix.Tablespace = strings.TrimSpace(ts) }
                if w, ok := dm["with"].(map[string]any); ok {
                    ix.With = map[string]string{}
                    for k, v := range w { ix.With[k] = scalarToString(v) }
                }
                if ocm, ok := dm["opclasses"].(map[string]any); ok {
                    ix.Opclasses = map[string]string{}
                    for col, v := range ocm {
                        if s, ok := v.(string); ok { ix.Opclasses[col] = s }
                    }
                }
                if cm, ok := dm["collations"].(map[string]any); ok { ix.Collations = stringMap(cm) }
                if sm, ok := dm["statisticsTargets"].(map[string]any); ok {
                    ix.StatisticsTargets = map[string]int{}
                    for col, v := range sm {
                        if n, ok := v.(int); ok { ix.StatisticsTargets[col] = n }
                    }
                }
            }
            out = append(out, ix)
        }
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

func parseForeignKeys(v any) []*ForeignKey {
    out := []*ForeignKey{}
    if m, ok := v.(map[string]any); ok {
        for name, def := range m {
            fk := &ForeignKey{Name: name}
            if dm, ok := def.(map[string]any); ok {
                fk.Columns = parseStringListFromNode(dm["columns"])
                if v, ok := dm["renamedFrom"].(string); ok { fk.RenamedFrom = strings.TrimSpace(v) }
                if ref, ok := dm["references"].(map[string]any); ok {
                    if t, ok := ref["table"].(string); ok { fk.RefTable = qualifyQuoted("", t) }
                    fk.RefColumns = parseStringListFromNode(ref["columns"])
                }
                if od, ok := dm["onDelete"].(string); ok { fk.OnDelete = od }
                if ou, ok := dm["onUpdate"].(string); ok { fk.OnUpdate = ou }
                if mt, ok := dm["match"].(string); ok { fk.Match = strings.ToLower(strings.TrimSpace(mt)) }
                if b, ok := dm["deferrable"].(bool); ok { fk.Deferrable = b }
                if b, ok := dm["initiallyDeferred"].(bool); ok { fk.InitiallyDeferred = b }
                if b, ok := dm["notValid"].(bool); ok { fk.NotValid = b }
                if v, ok := dm["validate"].(string); ok { fk.Validate = strings.ToLower(strings.TrimSpace(v)) }
                if b, ok := dm["enforced"].(bool); ok { fk.NotEnforced = !b }
                if cm, ok := dm["comment"].(string); ok { fk.Comment = cm }
            }
            out = append(out, fk)
        }
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

func parseTriggers(v any) []*Trigger {
    out := []*Trigger{}
    if m, ok := v.(map[string]any); ok {
        for name, def := range m {
            tr := &Trigger{Name: name}
            if dm, ok := def.(map[string]any); ok {
                if t, ok := dm["timing"].(string); ok { tr.Timing = t }
                tr.Events = parseStringListFromNode(dm["events"])
                if l, ok := dm["level"].(string); ok { tr.Level = l }
                if p, ok := dm["procedure"].(string); ok { tr.Procedure = p }
                if w, ok := dm["when"].(string); ok { tr.When = w }
                if c, ok := dm["constraint"].(bool); ok { tr.Constraint = c }
                if d, ok := dm["deferrable"].(bool); ok { tr.Deferrable = d }
                if id, ok := dm["initiallyDeferred"].(bool); ok { tr.InitiallyDeferred = id }
                if cm, ok := dm["comment"].(string); ok { tr.Comment = cm }
                switch e := dm["enabled"].(type) {
                case bool:
                    tr.Enabled = "origin"
                    if !e { tr.Enabled = "disabled" }
                case string:
                    tr.Enabled = strings.ToLower(strings.TrimSpace(e))
                }
                if ref, ok := dm["referencing"].(map[string]any); ok {
                    if s, ok := ref["old"].(string); ok { tr.OldTable = strings.TrimSpace(s) }
                    if s, ok := ref["new"].(string); ok { tr.NewTable = strings.TrimSpace(s) }
                }
            }
            out = append(out, tr)
        }
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

func parseStatistics(v any) []*Statistic {
    out := []*Statistic{}
    m, ok := v.(map[string]any)
    if !ok { return out }
    for name, def := range m {
        st := &Statistic{Name: name}
        if dm, ok := def.(map[string]any); ok {
            if k, ok := dm["kinds"]; ok {
                for _, kind := range parseStringListFromNode(k) { st.Kinds = append(st.Kinds, strings.ToLower(strings.TrimSpace(kind))) }
            }
            if c, ok := dm["columns"]; ok { st.Columns = parseStringListFromNode(c) }
            if s, ok := dm["comment"].(string); ok { st.Comment = s }
            if n, ok := dm["statisticsTarget"].(int); ok { st.StatisticsTarget = &n }
        }
        out = append(out, st)
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

// parseReplicaIdentity accepts default, full, nothing, or index <name> /
// using index <name> / {index: <name>}.
func parseReplicaIdentity(v any) string {
    switch x := v.(type) {
    case string:
        s := strings.Join(strings.Fields(x), " ")
        lower := strings.ToLower(s)
        for _, prefix := range []string{"using index ", "index "} {
            if strings.HasPrefix(lower, prefix) { return "index " + strings.TrimSpace(s[len(prefix):]) }
        }
        return lower
    case map[string]any:
        if idx, ok := x["index"].(string); ok { return "index " + strings.TrimSpace(idx) }
    }
    return ""
}

func parseRules(v any) []*Rule {
    out := []*Rule{}
    m, ok := v.(map[string]any)
    if !ok { return out }
    for name, def := range m {
        r := &Rule{Name: name}
        if dm, ok := def.(map[string]any); ok {
            if s, ok := dm["event"].(string); ok { r.Event = strings.ToLower(strings.TrimSpace(s)) }
            if b, ok := dm["instead"].(bool); ok { r.Instead = b }
            if s, ok := dm["where"].(string); ok { r.Where = strings.TrimSpace(s) }
            if s, ok := dm["do"].(string); ok { r.Do = strings.TrimSpace(s) }
            if b, ok := dm["replace"].(bool); ok { r.Replace = b }
            if s, ok := dm["comment"].(string); ok { r.Comment = s }
        }
        out = append(out, r)
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

func parseConstraints(v any) []*Constraint {
    out := []*Constraint{}
    if m, ok := v.(map[string]any); ok {
        for name, def := range m {
            c := &Constraint{Name: name}
            if dm, ok := def.(map[string]any); ok {
                if t, ok := dm["type"].(string); ok { c.Type = t }
                if v, ok := dm["renamedFrom"].(string); ok { c.RenamedFrom = strings.TrimSpace(v) }
                if e, ok := dm["expression"].(string); ok { c.Expression = e }
                if e, ok := dm["def"].(string); ok { c.Expression = e } // alias
                if cols, ok := dm["columns"]; ok { c.Columns = parseStringListFromNode(cols) }
                if b, ok := dm["nullsNotDistinct"].(bool); ok { c.NullsNotDistinct = b }
                if v, ok := dm["include"]; ok { c.Include = parseStringListFromNode(v) }
                if w, ok := dm["with"].(map[string]any); ok { c.With = stringMap(w) }
                if ts, ok := dm["tablespace"].(string); ok { c.Tablespace = strings.TrimSpace(ts) }
                if b, ok := dm["deferrable"].(bool); ok { c.Deferrable = b }
                if b, ok := dm["initiallyDeferred"].(bool); ok { c.InitiallyDeferred = b }
                if b, ok := dm["noInherit"].(bool); ok { c.NoInherit = b }
                if b, ok := dm["notValid"].(bool); ok { c.NotValid = b }
                if v, ok := dm["validate"].(string); ok { c.Validate = strings.ToLower(strings.TrimSpace(v)) }
                if b, ok := dm["enforced"].(bool); ok { c.NotEnforced = !b }
                if cm, ok := dm["comment"].(string); ok { c.Comment = cm }
            }
            out = append(out, c)
        }
    }
    sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
    return out
}

// parseTablePartitioning fills partitioning properties from a table's YAML map:
// `partitionBy` (parent), `partitionOf` + `forValues`/`default` (child).
// parseTableStorage fills a table's unlogged flag, tablespace, access method,
// `with` storage parameters and validate mode from its YAML map.
func parseTableStorage(t *Table, m map[string]any) {
    if b, ok := m["unlogged"].(bool); ok { t.Unlogged = &b }
    if ts, ok := m["tablespace"].(string); ok { t.Tablespace = strings.TrimSpace(ts) }
    if am, ok := m["accessMethod"].(string); ok { t.AccessMethod = strings.TrimSpace(am) }
    if v, ok := m["validate"].(string); ok { t.Validate = strings.ToLower(strings.TrimSpace(v)) }
    if w, ok := m["with"].(map[string]any); ok {
        t.With = map[string]string{}
        for k, v := range w { t.With[k] = scalarToString(v) }
    }
}

func parseTablePartitioning(t *Table, schemaName string, m map[string]any) {
    if of, ok := m["of"].(string); ok && strings.TrimSpace(of) != "" { t.Of = qualify(schemaName, strings.TrimSpace(of)) }
    if inh, ok := m["inherits"]; ok {
        t.Inherits = []string{}
        for _, p := range parseStringListFromNode(inh) { t.Inherits = append(t.Inherits, qualify(schemaName, p)) }
    }
    if pbRaw, ok := m["partitionBy"]; ok {
        t.PartitionBy = parsePartitionBy(pbRaw)
    }
    if po, ok := m["partitionOf"].(string); ok && po != "" {
        t.PartitionOf = qualify(schemaName, po)
        spec := &PartitionSpec{Modulus: -1, Remainder: -1}
        if fv, ok := m["forValues"]; ok {
            if parsed := parsePartitionSpec(fv); parsed != nil { spec = parsed }
        }
        if b, ok := m["default"].(bool); ok { spec.Default = b }
        t.Partition = spec
    }
}

// parsePartitionBy accepts either the explicit form
//   { type: range, columns: [c1, c2] }
// or the shorthand form keyed by strategy:
//   { range: [c1] } / { list: [c1] } / { hash: [c1] }
func parsePartitionBy(v any) *PartitionBy {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    pb := &PartitionBy{}
    if t, ok := m["type"].(string); ok { pb.Type = strings.ToLower(strings.TrimSpace(t)) }
    if cols, ok := m["columns"]; ok { pb.Columns = parseStringListFromNode(cols) }
    for _, k := range []string{"range", "list", "hash"} {
        if cols, ok := m[k]; ok {
            pb.Type = k
            pb.Columns = parseStringListFromNode(cols)
        }
    }
    if pb.Type == "" || len(pb.Columns) == 0 { return nil }
    return pb
}

// parsePartitionSpec parses a `forValues` block:
//   { from: [...], to: [...] }         range
//   { in: [...] }                      list
//   { modulus: 4, remainder: 0 }       hash
func parsePartitionSpec(v any) *PartitionSpec {
    m, ok := v.(map[string]any)
    if !ok { return nil }
    ps := &PartitionSpec{Modulus: -1, Remainder: -1}
    if f, ok := m["from"]; ok { ps.From = parseScalarList(f) }
    if t, ok := m["to"]; ok { ps.To = parseScalarList(t) }
    if in, ok := m["in"]; ok { ps.In = parseScalarList(in) }
    if n, ok := m["modulus"].(int); ok { ps.Modulus = n }
    if n, ok := m["remainder"].(int); ok { ps.Remainder = n }
    return ps
}

// parseScalarList coerces a YAML list of scalars (strings, numbers, bools,
// timestamps) to strings, preserving order. Unlike parseStringListFromNode it
// keeps non-string scalars, which partition bounds routinely contain.
func parseScalarList(v any) []string {
    arr, ok := v.([]any)
    if !ok { return nil }
    out := []string{}
    for _, it := range arr {
        if s := scalarToString(it); s != "" { out = append(out, s) }
    }
    return out
}

// scalarToString renders a YAML scalar as a string. yaml.v3 resolves bare
// dates like 2024-01-01 to time.Time; format those back as date/timestamp.
// floatPtr returns a YAML integer or float as a *float64, or nil for
// anything else.
func floatPtr(v any) *float64 {
    switch n := v.(type) {
    case int:
        f := float64(n)
        return &f
    case float64:
        return &n
    }
    return nil
}

// nameScalar returns the name a YAML scalar spells: a string as it is, a
// number or a boolean in its plain form. Anything else is not a name.
func nameScalar(v any) (string, bool) {
    switch x := v.(type) {
    case string:
        return x, true
    case bool, int, int64, uint64, float64:
        return scalarToString(x), true
    }
    return "", false
}

// listName is nameScalar for an entry of a list of names, where a null
// entry is the name null rather than a missing value.
func listName(v any) (string, bool) {
    if v == nil { return "null", true }
    return nameScalar(v)
}

func scalarToString(v any) string {
    switch x := v.(type) {
    case string:
        return x
    case bool, int, int64, uint64, float64:
        return fmt.Sprintf("%v", x)
    case time.Time:
        if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
            return x.Format("2006-01-02")
        }
        return x.Format("2006-01-02 15:04:05")
    }
    return ""
}

func parseFunction(schemaName, key string, body any) *Function {
    // key format: "function <name>(args):"
    nameAndSig := strings.TrimSpace(strings.TrimPrefix(key, "function "))
    fn := &Function{Schema: schemaName}
    if i := argsStart(nameAndSig); i >= 0 {
        fn.Name = strings.TrimSpace(nameAndSig[:i])
        fn.ArgsSig = strings.TrimSuffix(strings.TrimSpace(nameAndSig[i:]), ":")
    } else {
        fn.Name = strings.TrimSuffix(nameAndSig, ":")
        fn.ArgsSig = "()"
    }
    m, ok := body.(map[string]any)
    if !ok { return fn }
    if v, ok := m["renamedFrom"].(string); ok { fn.RenamedFrom = strings.TrimSpace(v) }
    if r, ok := m["returns"].(string); ok { fn.Returns = r }
    if l, ok := m["language"].(string); ok { fn.Language = l }
    if s, ok := m["security"].(string); ok { fn.Security = s }
    if _, ok := m["stable"].(bool); ok { fn.Volatility = "stable" }
    if _, ok := m["volatile"].(bool); ok { fn.Volatility = "volatile" }
    if _, ok := m["immutable"].(bool); ok { fn.Volatility = "immutable" }
    if st, ok := m["strict"].(bool); ok { fn.Strict = &st }
    if lp, ok := m["leakproof"].(bool); ok { fn.Leakproof = &lp }
    if p, ok := m["parallel"].(string); ok { fn.Parallel = strings.ToLower(strings.TrimSpace(p)) }
    fn.Cost = floatPtr(m["cost"])
    fn.Rows = floatPtr(m["rows"])
    if set, ok := m["set"].(map[string]any); ok {
        fn.Set = map[string]string{}
        for k, v := range set { fn.Set[k] = scalarToString(v) }
    }
    if b, ok := m["body"].(string); ok { fn.Body, fn.BodySet = b, true }
    if t, ok := m["transform"]; ok && t != nil { fn.Transform = parseTransform(t) }
    if r, ok := m["replace"].(bool); ok { fn.Replace = r }
    if dep, ok := m["dependsOn"]; ok {
        fn.DependsOn = parseStringListFromNode(dep)
    }
    if gRaw, ok := m["grants"]; ok { fn.Grants = parseGrants(gRaw) }
    if rp, ok := m["revokePublic"].(bool); ok { fn.RevokePublic = rp }
    if cm, ok := m["comment"].(string); ok { fn.Comment = cm }
    if o, ok := m["owner"].(string); ok { fn.Owner = strings.TrimSpace(o) }
    return fn
}

// parseTransform reads a transform list, or one comma-separated string, of
// type names, each optionally written as "for type <name>".
func parseTransform(v any) []string {
    out := []string{}
    items := parseStringListFromNode(v)
    if s, ok := v.(string); ok { items = strings.Split(s, ",") }
    for _, t := range items {
        if m := transformTypeRe.FindStringSubmatch(t); m != nil { t = m[1] }
        if t = strings.TrimSpace(t); t != "" { out = append(out, t) }
    }
    return out
}

func parseAggregate(schemaName, key string, body any) *Aggregate {
    nameAndSig := strings.TrimSpace(strings.TrimPrefix(key, "aggregate "))
    ag := &Aggregate{Schema: schemaName, ArgsSig: "(*)"}
    if i := argsStart(nameAndSig); i >= 0 {
        ag.Name = strings.TrimSpace(nameAndSig[:i])
        ag.ArgsSig = strings.TrimSpace(nameAndSig[i:])
    } else {
        ag.Name = nameAndSig
    }
    m, ok := body.(map[string]any)
    if !ok { return ag }
    str := func(k string) string { s, _ := m[k].(string); return strings.TrimSpace(s) }
    ag.SFunc, ag.SType, ag.FinalFunc, ag.CombineFunc = str("sfunc"), str("stype"), str("finalfunc"), str("combinefunc")
    ag.Parallel = strings.ToLower(str("parallel"))
    if v, ok := m["initcond"]; ok && v != nil { ag.InitCond, ag.InitCondSet = scalarToString(v), true }
    for _, o := range AggregateOptions {
        if v, ok := m[o[0]]; ok && v != nil {
            if ag.Options == nil { ag.Options = map[string]string{} }
            val := scalarToString(v)
            switch {
            case strings.HasSuffix(o[1], "_modify"):
                val = strings.ToLower(strings.TrimSpace(val))
            case o[1] != "minitcond":
                val = strings.TrimSpace(val)
            }
            ag.Options[o[1]] = val
        }
    }
    if dep, ok := m["dependsOn"]; ok { ag.DependsOn = parseStringListFromNode(dep) }
    ag.Comment = str("comment")
    ag.Owner = str("owner")
    return ag
}

func parseProcedure(schemaName, key string, body any) *Procedure {
    // key format: "procedure <name>(args):"
    nameAndSig := strings.TrimSpace(strings.TrimPrefix(key, "procedure "))
    pr := &Procedure{Schema: schemaName}
    if i := argsStart(nameAndSig); i >= 0 {
        pr.Name = strings.TrimSpace(nameAndSig[:i])
        pr.ArgsSig = strings.TrimSuffix(strings.TrimSpace(nameAndSig[i:]), ":")
    } else {
        pr.Name = strings.TrimSuffix(nameAndSig, ":")
        pr.ArgsSig = "()"
    }
    m, ok := body.(map[string]any)
    if !ok { return pr }
    if v, ok := m["renamedFrom"].(string); ok { pr.RenamedFrom = strings.TrimSpace(v) }
    if l, ok := m["language"].(string); ok { pr.Language = l }
    if s, ok := m["security"].(string); ok { pr.Security = s }
    if set, ok := m["set"].(map[string]any); ok {
        pr.Set = map[string]string{}
        for k, v := range set { pr.Set[k] = scalarToString(v) }
    }
    if b, ok := m["body"].(string); ok { pr.Body, pr.BodySet = b, true }
    if t, ok := m["transform"]; ok && t != nil { pr.Transform = parseTransform(t) }
    if r, ok := m["replace"].(bool); ok { pr.Replace = r }
    if dep, ok := m["dependsOn"]; ok {
        pr.DependsOn = parseStringListFromNode(dep)
    }
    if gRaw, ok := m["grants"]; ok { pr.Grants = parseGrants(gRaw) }
    if rp, ok := m["revokePublic"].(bool); ok { pr.RevokePublic = rp }
    if cm, ok := m["comment"].(string); ok { pr.Comment = cm }
    if o, ok := m["owner"].(string); ok { pr.Owner = strings.TrimSpace(o) }
    return pr
}

func parseType(schemaName, key string, body any) *TypeDef {
    name := strings.TrimSpace(strings.TrimPrefix(key, "type "))
    td := &TypeDef{Name: name, Schema: schemaName}
    m, ok := body.(map[string]any)
    if !ok { return td }
    if kind, ok := m["type"].(string); ok { td.Kind = kind }
    if v, ok := m["renamedFrom"].(string); ok { td.RenamedFrom = strings.TrimSpace(v) }
    if rl, ok := m["renamedLabels"].(map[string]any); ok {
        td.RenamedLabels = map[string]string{}
        for label, old := range rl {
            if o, ok := nameScalar(old); ok { td.RenamedLabels[label] = o }
        }
    }
    if labels, ok := m["labels"].([]any); ok {
        for _, it := range labels { if s, ok := listName(it); ok { td.Labels = append(td.Labels, s) } }
    }
    if attrs, ok := m["attributes"].(map[string]any); ok {
        td.Attributes = map[string]string{}
        for k, v := range attrs {
            if mm, ok := v.(map[string]any); ok {
                if t, ok := mm["type"].(string); ok { td.Attributes[k] = t }
                if c, ok := mm["collate"].(string); ok && strings.TrimSpace(c) != "" {
                    if td.AttributeCollations == nil { td.AttributeCollations = map[string]string{} }
                    td.AttributeCollations[k] = strings.TrimSpace(c)
                }
                if c, ok := mm["comment"].(string); ok && c != "" {
                    if td.AttributeComments == nil { td.AttributeComments = map[string]string{} }
                    td.AttributeComments[k] = c
                }
            } else if t, ok := v.(string); ok && strings.TrimSpace(t) != "" {
                td.Attributes[k] = strings.TrimSpace(t)
            }
        }
    }
    if td.Kind == "range" {
        str := func(k string) string { s, _ := m[k].(string); return strings.TrimSpace(s) }
        td.Range = &RangeDef{
            Subtype: str("subtype"), SubtypeOpclass: str("subtypeOpclass"), Collation: str("collation"),
            Canonical: str("canonical"), SubtypeDiff: str("subtypeDiff"), MultirangeTypeName: str("multirangeTypeName"),
        }
    }
    if dep, ok := m["dependsOn"]; ok {
        td.DependsOn = parseStringListFromNode(dep)
    }
    if cm, ok := m["comment"].(string); ok { td.Comment = cm }
    if o, ok := m["owner"].(string); ok { td.Owner = strings.TrimSpace(o) }
    if g, ok := m["grants"]; ok { td.Grants = parseGrants(g) }
    if rp, ok := m["revokePublic"].(bool); ok { td.RevokePublic = rp }
    return td
}

// fillColumnOrderFromNode walks the yaml.Node tree to extract column key order
// for tables inside a specific schema block (schema <schemaName>: ...).
func fillColumnOrderFromNode(root *yaml.Node, db *Database, schemaName string) {
    if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 { return }
    top := root.Content[0]
    if top.Kind != yaml.MappingNode { return }
    // find mapping entry key == "schema <schemaName>"
    for i := 0; i+1 < len(top.Content); i += 2 {
        k := top.Content[i]
        v := top.Content[i+1]
        if name, ok := strings.CutPrefix(k.Value, "schema "); ok && bareName(strings.TrimSpace(name)) == schemaName && v.Kind == yaml.MappingNode {
            // inside schema mapping, find table blocks
            for j := 0; j+1 < len(v.Content); j += 2 {
                tk := v.Content[j]
                tv := v.Content[j+1]
                if tname, _ := tableKey(tk.Value); tname != "" && tv.Kind == yaml.MappingNode {
                    fq := qualify(schemaName, tname)
                    // find columns mapping
                    for k2 := 0; k2+1 < len(tv.Content); k2 += 2 {
                        ck := tv.Content[k2]
                        cv := tv.Content[k2+1]
                        if ck.Value == "columns" && cv.Kind == yaml.MappingNode {
                            order := []string{}
                            for x := 0; x+1 < len(cv.Content); x += 2 {
                                colName := cv.Content[x].Value
                                order = append(order, colName)
                            }
                            if t, ok := db.Tables[fq]; ok {
                                t.ColumnOrder = order
                            }
                        }
                    }
                }
                if strings.HasPrefix(tk.Value, "type ") && tv.Kind == yaml.MappingNode {
                    tname := strings.TrimSpace(strings.TrimPrefix(tk.Value, "type "))
                    fq := qualify(schemaName, tname)
                    for k2 := 0; k2+1 < len(tv.Content); k2 += 2 {
                        ak := tv.Content[k2]
                        av := tv.Content[k2+1]
                        if ak.Value == "attributes" && av.Kind == yaml.MappingNode {
                            order := []string{}
                            for x := 0; x+1 < len(av.Content); x += 2 {
                                order = append(order, av.Content[x].Value)
                            }
                            if td, ok := db.Types[fq]; ok {
                                td.AttributeOrder = order
                            }
                        }
                    }
                }
            }
        }
    }
}

func SortedTableNames(d *Database) []string {
    out := make([]string, 0, len(d.Tables))
    for k := range d.Tables {
        out = append(out, k)
    }
    sort.Strings(out)
    return out
}

// Entity represents any orderable entity (extension, type, function, table)
type Entity struct {
    Key      string   // fully qualified name
    Kind     string   // "extension", "type", "function", "table"
    DependsOn []string // dependencies as written in YAML
}

// TopologicalSort returns all entities in dependency order
func TopologicalSort(d *Database) ([]Entity, error) {
    entities := []Entity{}
    entityMap := map[string]Entity{}
    callable := callableFunctions(d)
    
    // Collect all entities
    for _, ext := range d.Extensions {
        if ext == nil { continue }
        key := ext.Name
        e := Entity{Key: key, Kind: "extension", DependsOn: ext.DependsOn}
        entities = append(entities, e)
        entityMap[key] = e
    }
    for k, td := range d.Types {
        if td == nil { continue }
        deps := td.DependsOn
        if td.Range != nil && td.Range.SubtypeDiff != "" && len(resolveRoutine(td.Range.SubtypeDiff, d.Functions)) > 0 {
            deps = append(append([]string{}, deps...), "function "+td.Range.SubtypeDiff)
        }
        used := []string{}
        for _, a := range td.Attributes { used = append(used, a) }
        if td.Range != nil { used = append(used, td.Range.Subtype) }
        deps = append(append([]string{}, deps...), typeRefDeps(d, k, used...)...)
        e := Entity{Key: k, Kind: "type", DependsOn: deps}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, co := range d.Collations {
        if co == nil { continue }
        e := Entity{Key: k, Kind: "collation", DependsOn: co.DependsOn}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, td := range d.TSDictionaries {
        if td == nil { continue }
        e := Entity{Key: k, Kind: "tsdictionary", DependsOn: td.DependsOn}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, tc := range d.TSConfigs {
        if tc == nil { continue }
        deps := tc.DependsOn
        for _, dicts := range tc.Mappings {
            for _, dict := range dicts {
                if dk := qualify("", dict); d.TSDictionaries[dk] != nil { deps = append(append([]string{}, deps...), "text search dictionary "+dk) }
            }
        }
        e := Entity{Key: k, Kind: "tsconfig", DependsOn: deps}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, dm := range d.Domains {
        if dm == nil { continue }
        exprs := []string{dm.Default, dm.Check}
        for _, c := range dm.Checks { exprs = append(exprs, c) }
        deps := append(append([]string{}, dm.DependsOn...), calledFunctionDeps(callable, k, exprs...)...)
        e := Entity{Key: k, Kind: "domain", DependsOn: append(deps, typeRefDeps(d, k, dm.Type)...)}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, sq := range d.Sequences {
        if sq == nil { continue }
        e := Entity{Key: k, Kind: "sequence", DependsOn: sq.DependsOn}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, fn := range d.Functions {
        if fn == nil { continue }
        deps := append(append([]string{}, fn.DependsOn...), typeRefDeps(d, k, append(argTypes(fn.ArgsSig), returnTypes(fn.Returns)...)...)...)
        deps = append(deps, calledFunctionDeps(callable, k, fn.ArgsSig)...)
        if IsSQLStandardBody(fn.Language, fn.Body) { deps = append(deps, queryDeps(d, callable, k, fn.Body)...) }
        e := Entity{Key: k, Kind: "function", DependsOn: deps}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, ag := range d.Aggregates {
        if ag == nil { continue }
        deps := ag.DependsOn
        for _, fn := range []string{ag.SFunc, ag.FinalFunc, ag.CombineFunc} {
            if fn != "" && len(resolveRoutine(fn, d.Functions)) > 0 { deps = append(append([]string{}, deps...), "function "+fn) }
        }
        e := Entity{Key: k, Kind: "aggregate", DependsOn: append(deps, typeRefDeps(d, k, append(argTypes(strings.ReplaceAll(strings.ToLower(ag.ArgsSig), " order by ", ", ")), ag.SType)...)...)}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, pr := range d.Procedures {
        if pr == nil { continue }
        deps := append(append([]string{}, pr.DependsOn...), typeRefDeps(d, k, argTypes(pr.ArgsSig)...)...)
        if IsSQLStandardBody(pr.Language, pr.Body) { deps = append(deps, queryDeps(d, callable, k, pr.Body)...) }
        e := Entity{Key: k, Kind: "procedure", DependsOn: deps}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, t := range d.Tables {
        if t == nil { continue }
        deps := t.DependsOn
        if t.PartitionOf != "" {
            // partition children must be created after their parent
            deps = append(append([]string{}, deps...), "table "+t.PartitionOf)
        }
        for _, p := range t.Inherits { deps = append(append([]string{}, deps...), "table "+p) }
        if t.Of != "" && d.Types[t.Of] != nil { deps = append(append([]string{}, deps...), "type "+t.Of) }
        colTypes, exprs := []string{}, []string{}
        for _, c := range t.Columns {
            if c != nil { colTypes, exprs = append(colTypes, c.Type), append(exprs, c.Default, c.Generated) }
        }
        for _, ct := range t.Constraints {
            if ct != nil { exprs = append(exprs, ct.Expression) }
        }
        for _, ix := range t.Indexes {
            if ix != nil { exprs = append(append(exprs, ix.Columns...), ix.Where) }
        }
        deps = append(append([]string{}, deps...), typeRefDeps(d, k, colTypes...)...)
        deps = append(deps, calledFunctionDeps(callable, k, exprs...)...)
        e := Entity{Key: k, Kind: "table", DependsOn: deps}
        entities = append(entities, e)
        entityMap[k] = e
    }
    for k, vw := range d.Views {
        if vw == nil { continue }
        e := Entity{Key: k, Kind: "view", DependsOn: append(append([]string{}, vw.DependsOn...), queryDeps(d, callable, k, vw.Query)...)}
        entities = append(entities, e)
        entityMap[k] = e
    }
    
    // Resolve dependencies: convert "table private.account" -> "private.account"
    // Build dependency graph
    graph := map[string][]string{} // node -> list of dependencies
    fnIndex := routineIndex(d.Functions)
    resolve := func(raw string) []string {
        raw = strings.TrimSpace(raw)
        if ref, ok := strings.CutPrefix(raw, "function "); ok && strings.Contains(ref, ".") {
            return resolveIndexedRoutine(strings.TrimSpace(ref), fnIndex)
        }
        return ResolveDependencyKeys(raw, d)
    }
    for _, e := range entities {
        graph[e.Key] = []string{}
        for _, rawDep := range e.DependsOn {
            for _, resolvedKey := range resolve(rawDep) {
                if resolvedKey == e.Key { continue }
                // Edges to trigger-returning functions are dropped: triggers are
                // emitted after every other create, so no CREATE statement can
                // reference such a function, and keeping the edge manufactures
                // false cycles (table -> its trigger function -> the tables the
                // function body reads).
                if fn, ok := d.Functions[resolvedKey]; ok && fn != nil && strings.EqualFold(strings.TrimSpace(fn.Returns), "trigger") {
                    continue
                }
                graph[e.Key] = append(graph[e.Key], resolvedKey)
            }
        }
    }

    // Deterministic ordering among ready nodes: kind first (matching the
    // natural create order), then name. Without this, Kahn's queue would
    // follow Go's randomized map iteration and the buffer would differ
    // between runs on identical input.
    kindRank := map[string]int{"extension": 0, "collation": 1, "tsdictionary": 2, "tsconfig": 3, "type": 4, "domain": 5, "sequence": 6, "function": 7, "procedure": 8, "aggregate": 9, "table": 10, "view": 11}
    entityLess := func(a, b string) bool {
        ra, rb := kindRank[entityMap[a].Kind], kindRank[entityMap[b].Kind]
        if ra != rb { return ra < rb }
        return a < b
    }

    // Topological sort (Kahn's algorithm)
    inDegree := map[string]int{}
    for k := range graph {
        inDegree[k] = 0
    }
    dependents := map[string][]string{}
    for k, deps := range graph {
        for _, dep := range deps {
            if _, exists := graph[dep]; exists {
                inDegree[k]++
                dependents[dep] = append(dependents[dep], k)
            }
        }
    }

    queue := &entityQueue{less: entityLess}
    for k, deg := range inDegree {
        if deg == 0 {
            heap.Push(queue, k)
        }
    }

    result := []Entity{}
    visited := map[string]bool{}

    for queue.Len() > 0 {
        node := heap.Pop(queue).(string)
        if visited[node] { continue }
        visited[node] = true
        if e, ok := entityMap[node]; ok {
            result = append(result, e)
        }
        // Find nodes that depend on this one
        for _, k := range dependents[node] {
            if visited[k] { continue }
            inDegree[k]--
            if inDegree[k] == 0 {
                heap.Push(queue, k)
            }
        }
    }
    
    // Any unvisited node is part of a dependency cycle (or depends on one).
    // Append them deterministically so the result is still usable, but return
    // an error so callers can fail loudly — their relative order is NOT
    // dependency-correct.
    if len(visited) < len(entityMap) {
        remaining := []string{}
        for k := range entityMap {
            if !visited[k] { remaining = append(remaining, k) }
        }
        sort.Strings(remaining)
        for _, k := range remaining {
            result = append(result, entityMap[k])
        }
        cycleMsg := ""
        if cycle := findCycle(graph, remaining); len(cycle) > 0 {
            cycleMsg = strings.Join(cycle, " -> ")
        } else {
            cycleMsg = "among: " + strings.Join(remaining, ", ")
        }
        return result, fmt.Errorf("dependency cycle detected: %s — check dependsOn declarations (note: triggers, policies, and grants are all emitted after every create, so dependsOn is never needed for trigger functions or functions referenced only by policies)", cycleMsg)
    }

    return result, nil
}

type entityQueue struct {
    items []string
    less  func(a, b string) bool
}

func (q *entityQueue) Len() int           { return len(q.items) }
func (q *entityQueue) Less(i, j int) bool { return q.less(q.items[i], q.items[j]) }
func (q *entityQueue) Swap(i, j int)      { q.items[i], q.items[j] = q.items[j], q.items[i] }
func (q *entityQueue) Push(x any)         { q.items = append(q.items, x.(string)) }
func (q *entityQueue) Pop() any {
    n := len(q.items)
    x := q.items[n-1]
    q.items = q.items[:n-1]
    return x
}

// findCycle returns one concrete dependency cycle (as a path ending on its
// first node) among the given unvisited nodes, or nil if none is reachable.
// Deterministic: nodes and edges are explored in sorted order.
func findCycle(graph map[string][]string, unvisited []string) []string {
    const (
        inProgress = 1
        done       = 2
    )
    color := map[string]int{}
    path := []string{}
    var cycle []string
    var visit func(n string) bool
    visit = func(n string) bool {
        color[n] = inProgress
        path = append(path, n)
        deps := append([]string{}, graph[n]...)
        sort.Strings(deps)
        for _, dep := range deps {
            if _, ok := graph[dep]; !ok { continue }
            switch color[dep] {
            case inProgress:
                for i, p := range path {
                    if p == dep {
                        cycle = append(append([]string{}, path[i:]...), dep)
                        return true
                    }
                }
            case done:
            default:
                if visit(dep) { return true }
            }
        }
        color[n] = done
        path = path[:len(path)-1]
        return false
    }
    for _, k := range unvisited {
        if color[k] == 0 && visit(k) { return cycle }
    }
    return nil
}

// ResolveDependency resolves a dependsOn entry to the key of the entity it
// names, or "" when nothing matches. For a routine reference that matches
// several overloads it returns the first; use ResolveDependencyKeys for all.
func ResolveDependency(raw string, d *Database) string {
    if keys := ResolveDependencyKeys(raw, d); len(keys) > 0 { return keys[0] }
    return ""
}

// ResolveDependencyKeys resolves a dependsOn entry such as "table
// private.account", "function app.f(int)" or a bare name to the keys of the
// entities it names. A routine reference without an argument list names every
// overload; one whose argument list matches no overload falls back to all of
// them. "schema <name>" entries resolve to nothing.
func ResolveDependencyKeys(raw string, d *Database) []string {
    raw = strings.TrimSpace(raw)
    if raw == "" || strings.HasPrefix(raw, "schema ") { return nil }

    if strings.HasPrefix(raw, "function ") {
        return resolveRoutine(strings.TrimPrefix(raw, "function "), d.Functions)
    }
    if strings.HasPrefix(raw, "procedure ") {
        return resolveRoutine(strings.TrimPrefix(raw, "procedure "), d.Procedures)
    }
    if strings.HasPrefix(raw, "aggregate ") {
        return resolveRoutine(strings.TrimPrefix(raw, "aggregate "), d.Aggregates)
    }
    for _, p := range []struct {
        prefix string
        has    func(string) bool
    }{
        {"foreign table ", func(k string) bool { return d.Tables[k] != nil && d.Tables[k].Foreign != nil }},
        {"table ", func(k string) bool { return d.Tables[k] != nil }},
        {"type ", func(k string) bool { return d.Types[k] != nil }},
        {"domain ", func(k string) bool { return d.Domains[k] != nil }},
        {"collation ", func(k string) bool { return d.Collations[k] != nil }},
        {"text search configuration ", func(k string) bool { return d.TSConfigs[k] != nil }},
        {"text search dictionary ", func(k string) bool { return d.TSDictionaries[k] != nil }},
        {"sequence ", func(k string) bool { return d.Sequences[k] != nil }},
        {"materialized view ", func(k string) bool { return d.Views[k] != nil }},
        {"view ", func(k string) bool { return d.Views[k] != nil }},
    } {
        if strings.HasPrefix(raw, p.prefix) {
            key := qualify("", strings.TrimSpace(strings.TrimPrefix(raw, p.prefix)))
            if p.has(key) { return []string{key} }
            return nil
        }
    }

    for _, key := range []string{raw, qualify("", raw)} {
        if d.Tables[key] != nil || d.Types[key] != nil || d.Views[key] != nil ||
            d.Sequences[key] != nil || d.Domains[key] != nil || d.Collations[key] != nil {
            return []string{key}
        }
    }
    if keys := resolveRoutine(raw, d.Functions); len(keys) > 0 { return keys }
    return resolveRoutine(raw, d.Procedures)
}

// resolveRoutine finds the Functions/Procedures keys a routine reference
// names: an exact qualified name, else (for an unqualified reference) the
// same name in public, else the same name in any schema.
func resolveRoutine[T any](ref string, m map[string]T) []string {
    ref = strings.TrimSpace(ref)
    name, args := ref, ""
    if i := argsStart(ref); i >= 0 { name, args = strings.TrimSpace(ref[:i]), ref[i:] }
    if parts, ok := SplitName(name); ok && len(parts) <= 2 {
        name = strings.Join(parts, ".")
        if len(parts) == 1 { return resolveRoutineName(name, args, m, true) }
    }
    return resolveRoutineName(name, args, m, false)
}

// resolveRoutineName is resolveRoutine for a name already read: schema.name,
// or a name alone when bare is set or it has no dot.
func resolveRoutineName[T any](name, args string, m map[string]T, bare bool) []string {
    match := func(want func(string) bool) []string {
        out := []string{}
        for k := range m {
            if want(RoutineName(k)) { out = append(out, k) }
        }
        sort.Strings(out)
        return out
    }
    var keys []string
    if !bare { keys = match(func(n string) bool { return n == name }) }
    if len(keys) == 0 && (bare || !strings.Contains(name, ".")) {
        keys = match(func(n string) bool { return n == "public."+name })
        if len(keys) == 0 {
            keys = match(func(n string) bool { _, rest, _ := strings.Cut(n, "."); return rest == name })
        }
    }
    if args != "" {
        want := compactArgs(args)
        for _, k := range keys {
            if compactArgs(k[len(RoutineName(k)):]) == want { return []string{k} }
        }
    }
    return keys
}

// FunctionKey returns the Database.Functions / Database.Procedures map key
// for a routine: its qualified name followed by its argument list, so
// overloads (same name, different arguments) are distinct entries.
func FunctionKey(qualifiedName, argsSig string) string {
    return qualifiedName + compactArgs(argsSig)
}

// RoutineName returns the qualified name part of a Functions/Procedures key,
// i.e. the key without its argument list.
func RoutineName(key string) string {
    if i := strings.IndexByte(key, '('); i >= 0 { return key[:i] }
    return key
}

// argsStart returns the index of the parenthesis that opens the argument
// list in a routine's name and arguments, outside the double quotes of a
// quoted name, or -1.
func argsStart(nameAndSig string) int {
    quoted := false
    for i := 0; i < len(nameAndSig); i++ {
        switch c := nameAndSig[i]; {
        case c == '"':
            quoted = !quoted
        case c == '(' && !quoted:
            return i
        }
    }
    return -1
}

var (
    argsCommaRe       = regexp.MustCompile(`\s*,\s*`)
    argsSpaceReplacer = strings.NewReplacer("( ", "(", " )", ")")
)

func compactArgs(args string) string {
    args = strings.Join(strings.Fields(strings.TrimSpace(args)), " ")
    if args == "" { return "()" }
    return argsSpaceReplacer.Replace(argsCommaRe.ReplaceAllString(args, ", "))
}

func parseView(schemaName, key string, body any) *View {
    materialized := strings.HasPrefix(key, "materialized view ")
    prefix := "view "
    if materialized {
        prefix = "materialized view "
    }
    name := strings.TrimSpace(strings.TrimPrefix(key, prefix))
    vw := &View{Schema: schemaName, Name: name, Materialized: materialized}
    m, ok := body.(map[string]any)
    if !ok { return vw }
    if q, ok := m["query"].(string); ok { vw.Query = q }
    if v, ok := m["renamedFrom"].(string); ok { vw.RenamedFrom = strings.TrimSpace(v) }
    if dep, ok := m["dependsOn"]; ok {
        vw.DependsOn = parseStringListFromNode(dep)
    }
    if cm, ok := m["comment"].(string); ok { vw.Comment = cm }
    if rp, ok := m["replace"].(bool); ok { vw.Replace = rp }
    if gRaw, ok := m["grants"]; ok { vw.Grants = parseGrants(gRaw) }
    if o, ok := m["owner"].(string); ok { vw.Owner = strings.TrimSpace(o) }
    if b, ok := m["securityInvoker"].(bool); ok { vw.SecurityInvoker = &b }
    if b, ok := m["securityBarrier"].(bool); ok { vw.SecurityBarrier = &b }
    if co, ok := m["checkOption"].(string); ok { vw.CheckOption = strings.ToLower(strings.TrimSpace(co)) }
    if b, ok := m["withData"].(bool); ok { vw.WithData = &b }
    if ts, ok := m["tablespace"].(string); ok { vw.Tablespace = strings.TrimSpace(ts) }
    if am, ok := m["accessMethod"].(string); ok { vw.AccessMethod = strings.TrimSpace(am) }
    if w, ok := m["with"].(map[string]any); ok {
        vw.With = map[string]string{}
        for k, v := range w { vw.With[k] = scalarToString(v) }
    }
    if ixRaw, ok := m["indexes"]; ok { vw.Indexes = parseIndexes(ixRaw) }
    if trRaw, ok := m["triggers"]; ok { vw.Triggers = parseTriggers(trRaw) }
    if rRaw, ok := m["rules"]; ok { vw.Rules = parseRules(rRaw) }
    if cc, ok := m["columnComments"].(map[string]any); ok {
        vw.ColumnComments = map[string]string{}
        for col, v := range cc {
            if s, ok := v.(string); ok { vw.ColumnComments[col] = s }
        }
    }
    if cd, ok := m["columnDefaults"].(map[string]any); ok {
        vw.ColumnDefaults = map[string]string{}
        for col, v := range cd {
            if s := defaultToString(v); s != "" { vw.ColumnDefaults[col] = s }
        }
    }
    return vw
}

func parseSequence(schemaName, key string, body any) *Sequence {
    name := strings.TrimSpace(strings.TrimPrefix(key, "sequence "))
    sq := &Sequence{Schema: schemaName, Name: name}
    m, ok := body.(map[string]any)
    if !ok { return sq }
    if a, ok := m["as"].(string); ok { sq.As = a }
    if v, ok := m["renamedFrom"].(string); ok { sq.RenamedFrom = strings.TrimSpace(v) }
    if v, ok := m["increment"]; ok { sq.Increment = defaultToString(v) }
    if v, ok := m["minValue"]; ok { sq.MinValue = defaultToString(v) }
    if v, ok := m["maxValue"]; ok { sq.MaxValue = defaultToString(v) }
    if v, ok := m["start"]; ok { sq.Start = defaultToString(v) }
    if v, ok := m["cache"]; ok { sq.Cache = defaultToString(v) }
    if b, ok := m["cycle"].(bool); ok { sq.Cycle = &b }
    if ob, ok := m["ownedBy"].(string); ok { sq.OwnedBy = ob }
    if u, ok := m["unlogged"].(bool); ok { sq.Unlogged = &u }
    if dep, ok := m["dependsOn"]; ok {
        sq.DependsOn = parseStringListFromNode(dep)
    }
    if cm, ok := m["comment"].(string); ok { sq.Comment = cm }
    if o, ok := m["owner"].(string); ok { sq.Owner = strings.TrimSpace(o) }
    if g, ok := m["grants"]; ok { sq.Grants = parseGrants(g) }
    return sq
}

// parseDefaultPrivileges parses a list of {for, in, tables, sequences,
// functions, types, schemas} entries; each kind maps role -> privileges.
func parseDefaultPrivileges(v any) []*DefaultPrivilege {
    arr, ok := v.([]any)
    if !ok { return nil }
    out := []*DefaultPrivilege{}
    for _, it := range arr {
        m, ok := it.(map[string]any)
        if !ok { continue }
        dp := &DefaultPrivilege{Grants: map[string]map[string][]string{}}
        if s, ok := m["for"].(string); ok { dp.ForRole = strings.TrimSpace(s) }
        switch in := m["in"].(type) {
        case string:
            dp.Schemas = []string{strings.TrimSpace(in)}
        case []any:
            dp.Schemas = parseStringListFromNode(in)
        }
        for _, kind := range []string{"tables", "sequences", "functions", "types", "schemas"} {
            if g, ok := m[kind]; ok {
                if parsed := parseGrants(g); parsed != nil { dp.Grants[kind] = parsed }
            }
        }
        out = append(out, dp)
    }
    return out
}

// parsePrimaryKey sets t's primary key from its primaryKey value: a column
// list, or a block {<constraint name>: {columns, include, with, comment}}
// that also names the constraint.
func (t *Table) parsePrimaryKey(v any) {
    t.PrimaryKey = parseStringListFromNode(v)
    m, ok := v.(map[string]any)
    if !ok || len(m) != 1 { return }
    for name, def := range m {
        t.PrimaryKeyName = strings.TrimSpace(name)
        dm, ok := def.(map[string]any)
        if !ok { continue }
        t.PrimaryKeyComment, _ = dm["comment"].(string)
        if inc, ok := dm["include"]; ok { t.PrimaryKeyInclude = parseStringListFromNode(inc) }
        if w, ok := dm["with"].(map[string]any); ok { t.PrimaryKeyWith = stringMap(w) }
        if ts, ok := dm["tablespace"].(string); ok { t.PrimaryKeyTablespace = strings.TrimSpace(ts) }
    }
}

// tableKey returns the table name a schema block key declares, and whether
// it is a `foreign table`, or "" for keys of other kinds.
func tableKey(key string) (string, bool) {
    if rest, ok := strings.CutPrefix(key, "foreign table "); ok { return strings.TrimSpace(rest), true }
    if rest, ok := strings.CutPrefix(key, "table "); ok { return strings.TrimSpace(rest), false }
    return "", false
}

// stringMap renders a YAML mapping of scalars as strings.
func stringMap(m map[string]any) map[string]string {
    out := make(map[string]string, len(m))
    for k, v := range m { out[k] = scalarToString(v) }
    return out
}

// parseServers parses { name: {wrapper, type, version, options, owner,
// comment} }.
func parseServers(v any) map[string]*Server {
    out := map[string]*Server{}
    m, ok := v.(map[string]any)
    if !ok { return out }
    for name, raw := range m {
        s := &Server{Name: name}
        if b, ok := raw.(map[string]any); ok {
            if w, ok := b["wrapper"].(string); ok { s.Wrapper = strings.TrimSpace(w) }
            if t, ok := b["type"].(string); ok { s.Type = t }
            if ver, ok := b["version"]; ok && ver != nil { s.Version = scalarToString(ver) }
            if o, ok := b["options"].(map[string]any); ok { s.Options = stringMap(o) }
            if o, ok := b["owner"].(string); ok { s.Owner = strings.TrimSpace(o) }
            if g, ok := b["grants"]; ok { s.Grants = parseGrants(g) }
            if c, ok := b["comment"].(string); ok { s.Comment = c }
            if v, ok := b["renamedFrom"].(string); ok { s.RenamedFrom = strings.TrimSpace(v) }
        }
        out[name] = s
    }
    return out
}

// parseForeignDataWrappers parses { name: {handler, validator, options,
// owner, grants, comment, renamedFrom} }. A handler or validator may be
// written with the empty argument list of the function, and as the empty
// string for none.
func parseForeignDataWrappers(v any) map[string]*ForeignDataWrapper {
    out := map[string]*ForeignDataWrapper{}
    m, ok := v.(map[string]any)
    if !ok { return out }
    function := func(v any) (string, bool) {
        s, ok := v.(string)
        return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "()")), ok
    }
    for name, raw := range m {
        w := &ForeignDataWrapper{Name: name}
        if b, ok := raw.(map[string]any); ok {
            w.Handler, w.HandlerSet = function(b["handler"])
            w.Validator, w.ValidatorSet = function(b["validator"])
            if o, ok := b["options"].(map[string]any); ok { w.Options = stringMap(o) }
            if o, ok := b["owner"].(string); ok { w.Owner = strings.TrimSpace(o) }
            if g, ok := b["grants"]; ok { w.Grants = parseGrants(g) }
            if c, ok := b["comment"].(string); ok { w.Comment = c }
            if v, ok := b["renamedFrom"].(string); ok { w.RenamedFrom = strings.TrimSpace(v) }
        }
        out[name] = w
    }
    return out
}

// parseLanguages parses { name: {grants} }.
func parseLanguages(v any) map[string]*Language {
    out := map[string]*Language{}
    m, ok := v.(map[string]any)
    if !ok { return out }
    for name, raw := range m {
        l := &Language{Name: name}
        if b, ok := raw.(map[string]any); ok {
            if g, ok := b["grants"]; ok { l.Grants = parseGrants(g) }
        }
        out[name] = l
    }
    return out
}

// fillFDWScalarsFromNode replaces the values parsed for a server's version
// and for wrapper, server, foreign table and foreign column options with
// their YAML source text. These settings are strings to PostgreSQL, and
// decoding a bare scalar as a number loses what was written: 15.10 would
// become 15.1 and 05432 an octal integer.
func fillFDWScalarsFromNode(root *yaml.Node, db *Database) {
    if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 { return }
    top := root.Content[0]
    if top.Kind != yaml.MappingNode { return }
    for i := 0; i+1 < len(top.Content); i += 2 {
        key, body := top.Content[i].Value, resolveAlias(top.Content[i+1])
        if body.Kind != yaml.MappingNode { continue }
        if key == "servers" {
            for j := 0; j+1 < len(body.Content); j += 2 {
                s, def := db.Servers[body.Content[j].Value], resolveAlias(body.Content[j+1])
                if s == nil || def.Kind != yaml.MappingNode { continue }
                if v := mappingValue(def, "version"); v != nil && v.Kind == yaml.ScalarNode && s.Version != "" { s.Version = scalarText(v) }
                fillScalarTexts(mappingValue(def, "options"), s.Options)
            }
            continue
        }
        if key == "foreignDataWrappers" {
            for j := 0; j+1 < len(body.Content); j += 2 {
                w, def := db.ForeignDataWrappers[body.Content[j].Value], resolveAlias(body.Content[j+1])
                if w == nil || def.Kind != yaml.MappingNode { continue }
                fillScalarTexts(mappingValue(def, "options"), w.Options)
            }
            continue
        }
        schemaName, ok := strings.CutPrefix(key, "schema ")
        if !ok { continue }
        schemaName = bareName(strings.TrimSpace(schemaName))
        for j := 0; j+1 < len(body.Content); j += 2 {
            tname, foreign := tableKey(body.Content[j].Value)
            def := resolveAlias(body.Content[j+1])
            t := db.Tables[qualify(schemaName, tname)]
            if !foreign || t == nil || t.Foreign == nil || def.Kind != yaml.MappingNode { continue }
            fillScalarTexts(mappingValue(def, "options"), t.Foreign.Options)
            cols := mappingValue(def, "columns")
            if cols == nil || cols.Kind != yaml.MappingNode { continue }
            for x := 0; x+1 < len(cols.Content); x += 2 {
                c, spec := t.Columns[cols.Content[x].Value], resolveAlias(cols.Content[x+1])
                if c == nil || spec.Kind != yaml.MappingNode { continue }
                fillScalarTexts(mappingValue(spec, "options"), c.FDWOptions)
            }
        }
    }
}

// resolveAlias returns the node a YAML alias points at, or n itself.
func resolveAlias(n *yaml.Node) *yaml.Node {
    if n != nil && n.Kind == yaml.AliasNode && n.Alias != nil { return n.Alias }
    return n
}

// mappingValue returns the value node of key in mapping node m, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
    if m == nil || m.Kind != yaml.MappingNode { return nil }
    for i := 0; i+1 < len(m.Content); i += 2 {
        if m.Content[i].Value == key { return resolveAlias(m.Content[i+1]) }
    }
    return nil
}

// scalarText returns a scalar node's value as written, or "" for a null.
func scalarText(n *yaml.Node) string {
    if n.ShortTag() == "!!null" { return "" }
    return n.Value
}

// fillScalarTexts overwrites the entries of out that mapping node m sets to
// a scalar with that scalar's source text.
func fillScalarTexts(m *yaml.Node, out map[string]string) {
    if m == nil || m.Kind != yaml.MappingNode || out == nil { return }
    for i := 0; i+1 < len(m.Content); i += 2 {
        k, v := m.Content[i].Value, resolveAlias(m.Content[i+1])
        if _, ok := out[k]; ok && v.Kind == yaml.ScalarNode { out[k] = scalarText(v) }
    }
}

// parsePublications parses { name: {allTables, tables, schemas, publish,
// viaPartitionRoot, comment} }. A tables entry is a table name or
// {table, columns, where}.
func parsePublications(v any) map[string]*Publication {
    m, ok := v.(map[string]any)
    if !ok { return map[string]*Publication{} }
    out := map[string]*Publication{}
    for name, def := range m {
        p := &Publication{Name: name}
        if dm, ok := def.(map[string]any); ok {
            if b, ok := dm["allTables"].(bool); ok { p.AllTables = b }
            if arr, ok := dm["tables"].([]any); ok {
                for _, it := range arr {
                    switch x := it.(type) {
                    case string:
                        p.Tables = append(p.Tables, &PublicationTable{Table: qualify("", strings.TrimSpace(x))})
                    case map[string]any:
                        tn, _ := x["table"].(string)
                        if tn == "" { continue }
                        pt := &PublicationTable{Table: qualify("", strings.TrimSpace(tn))}
                        if c, ok := x["columns"]; ok { pt.Columns = parseStringListFromNode(c) }
                        if w, ok := x["where"].(string); ok { pt.Where = strings.TrimSpace(w) }
                        if o, ok := x["only"].(bool); ok { pt.Only = o }
                        p.Tables = append(p.Tables, pt)
                    }
                }
            }
            if sc, ok := dm["schemas"]; ok { p.Schemas = parseStringListFromNode(sc) }
            if pub, ok := dm["publish"]; ok {
                p.Publish = parseStringListFromNode(pub)
                for i := range p.Publish { p.Publish[i] = strings.ToLower(strings.TrimSpace(p.Publish[i])) }
            }
            if b, ok := dm["viaPartitionRoot"].(bool); ok { p.ViaPartitionRoot = &b }
            if g, ok := dm["generatedColumns"].(string); ok { p.GeneratedColumns = strings.ToLower(strings.TrimSpace(g)) }
            if s, ok := dm["comment"].(string); ok { p.Comment = s }
            if s, ok := dm["owner"].(string); ok { p.Owner = strings.TrimSpace(s) }
        }
        out[name] = p
    }
    return out
}

// parseEventTriggers parses { name: {event, tags, function, enabled, comment} }.
// enabled accepts true/false or origin/disabled/replica/always.
func parseEventTriggers(v any) map[string]*EventTrigger {
    m, ok := v.(map[string]any)
    if !ok { return map[string]*EventTrigger{} }
    out := map[string]*EventTrigger{}
    for name, def := range m {
        et := &EventTrigger{Name: name}
        if dm, ok := def.(map[string]any); ok {
            if s, ok := dm["event"].(string); ok { et.Event = strings.ToLower(strings.TrimSpace(s)) }
            if t, ok := dm["tags"]; ok { et.Tags = parseStringListFromNode(t) }
            if s, ok := dm["function"].(string); ok { et.Function = strings.TrimSpace(s) }
            switch e := dm["enabled"].(type) {
            case bool:
                et.Enabled = "origin"
                if !e { et.Enabled = "disabled" }
            case string:
                et.Enabled = strings.ToLower(strings.TrimSpace(e))
            }
            if s, ok := dm["comment"].(string); ok { et.Comment = s }
        }
        out[name] = et
    }
    return out
}

func parseTSConfig(schemaName, key string, body any) *TSConfig {
    c := &TSConfig{Schema: schemaName, Name: strings.TrimSpace(strings.TrimPrefix(key, "text search configuration "))}
    m, ok := body.(map[string]any)
    if !ok { return c }
    if s, ok := m["copy"].(string); ok { c.Copy = strings.TrimSpace(s) }
    if s, ok := m["parser"].(string); ok { c.Parser = strings.TrimSpace(s) }
    if mm, ok := m["mappings"].(map[string]any); ok {
        c.Mappings = map[string][]string{}
        for tok, dicts := range mm { c.Mappings[strings.ToLower(strings.TrimSpace(tok))] = parseStringListFromNode(dicts) }
    }
    if dep, ok := m["dependsOn"]; ok { c.DependsOn = parseStringListFromNode(dep) }
    if s, ok := m["comment"].(string); ok { c.Comment = s }
    return c
}

func parseTSDictionary(schemaName, key string, body any) *TSDictionary {
    d := &TSDictionary{Schema: schemaName, Name: strings.TrimSpace(strings.TrimPrefix(key, "text search dictionary "))}
    m, ok := body.(map[string]any)
    if !ok { return d }
    if s, ok := m["template"].(string); ok { d.Template = strings.TrimSpace(s) }
    if om, ok := m["options"].(map[string]any); ok {
        d.Options = map[string]string{}
        for k, v := range om { d.Options[k] = scalarToString(v) }
    }
    if dep, ok := m["dependsOn"]; ok { d.DependsOn = parseStringListFromNode(dep) }
    if s, ok := m["comment"].(string); ok { d.Comment = s }
    return d
}

func parseCollation(schemaName, key string, body any) *Collation {
    co := &Collation{Schema: schemaName, Name: strings.TrimSpace(strings.TrimPrefix(key, "collation "))}
    m, ok := body.(map[string]any)
    if !ok { return co }
    str := func(k string) string { s, _ := m[k].(string); return strings.TrimSpace(s) }
    co.Provider = strings.ToLower(str("provider"))
    co.Locale = str("locale")
    co.LcCollate = str("lcCollate")
    co.LcCtype = str("lcCtype")
    co.Rules = str("rules")
    co.From = str("from")
    co.Comment, _ = m["comment"].(string)
    if b, ok := m["deterministic"].(bool); ok { co.Deterministic = &b }
    if dep, ok := m["dependsOn"]; ok { co.DependsOn = parseStringListFromNode(dep) }
    return co
}

func parseDomain(schemaName, key string, body any) *Domain {
    name := strings.TrimSpace(strings.TrimPrefix(key, "domain "))
    dm := &Domain{Schema: schemaName, Name: name}
    m, ok := body.(map[string]any)
    if !ok { return dm }
    if t, ok := m["type"].(string); ok { dm.Type = t }
    if t, ok := m["as"].(string); ok && t != "" { dm.Type = t } // alias
    if v, ok := m["renamedFrom"].(string); ok { dm.RenamedFrom = strings.TrimSpace(v) }
    if c, ok := m["collate"].(string); ok { dm.Collate = c }
    if v, ok := m["default"]; ok { dm.Default = defaultToString(v) }
    if b, ok := m["notNull"].(bool); ok { dm.NotNull = b }
    if b, ok := m["nullable"].(bool); ok { dm.NotNull = !b }
    if n, ok := m["notNullName"].(string); ok { dm.NotNullName = strings.TrimSpace(n) }
    if c, ok := m["check"].(string); ok { dm.Check = c }
    if cn, ok := m["constraintName"].(string); ok { dm.ConstraintName = cn }
    if cs, ok := m["checks"].(map[string]any); ok {
        dm.Checks = map[string]string{}
        for n, v := range cs {
            switch e := v.(type) {
            case string:
                dm.Checks[n] = strings.TrimSpace(e)
            case map[string]any:
                if c, ok := e["check"].(string); ok { dm.Checks[n] = strings.TrimSpace(c) }
                if c, ok := e["comment"].(string); ok {
                    if dm.CheckComments == nil { dm.CheckComments = map[string]string{} }
                    dm.CheckComments[n] = c
                }
            }
        }
    }
    if dep, ok := m["dependsOn"]; ok {
        dm.DependsOn = parseStringListFromNode(dep)
    }
    if cm, ok := m["comment"].(string); ok { dm.Comment = cm }
    if o, ok := m["owner"].(string); ok { dm.Owner = strings.TrimSpace(o) }
    if g, ok := m["grants"]; ok { dm.Grants = parseGrants(g) }
    if rp, ok := m["revokePublic"].(bool); ok { dm.RevokePublic = rp }
    return dm
}

func errorsIsNotExist(err error) bool {
    return err != nil && (os.IsNotExist(err) || errorsIs(err, fs.ErrNotExist))
}

func errorsIs(err, target error) bool { // tiny helper to avoid importing errors
    type causer interface{ Is(error) bool }
    if err == nil {
        return target == nil
    }
    if err == target {
        return true
    }
    if c, ok := err.(causer); ok {
        return c.Is(target)
    }
    return false
}



// argTypes returns the type of each argument in a routine signature:
// its last word, after the mode, name and default are dropped. Only
// single-word types matter to typeRefDeps, so multi-word built-in types
// needn't be kept whole.
func argTypes(sig string) []string {
    inner := strings.TrimSpace(sig)
    inner = strings.TrimSuffix(strings.TrimPrefix(inner, "("), ")")
    out := []string{}
    depth, start := 0, 0
    flush := func(a string) {
        lower := strings.ToLower(a)
        if i := strings.Index(lower, " default "); i >= 0 { a = a[:i] }
        if i := strings.IndexByte(a, '='); i >= 0 { a = a[:i] }
        if w := strings.Fields(a); len(w) > 0 { out = append(out, w[len(w)-1]) }
    }
    for i, r := range inner {
        switch r {
        case '(':
            depth++
        case ')':
            depth--
        case ',':
            if depth == 0 { flush(inner[start:i]); start = i + 1 }
        }
    }
    flush(inner[start:])
    return out
}

// returnTypes returns the types a RETURNS clause names: the type of
// "[setof] type", or each column's of "table(...)".
func returnTypes(returns string) []string {
    r := strings.TrimSpace(returns)
    lower := strings.ToLower(r)
    if strings.HasPrefix(lower, "table") && strings.Contains(r, "(") {
        return argTypes(r[strings.IndexByte(r, '('):])
    }
    if strings.HasPrefix(lower, "setof ") { r = strings.TrimSpace(r[len("setof "):]) }
    return []string{r}
}

// typeRefDeps returns dependsOn entries for the declared types, domains,
// tables and views (row types) that the given types name (app.t,
// "App"."T", app.t[]), other than self, since PostgreSQL needs a type
// before anything that uses it. Types sort before domains and relations,
// and functions before tables, so without these a composite of a domain or
// a function returning a table's rows would be created too early.
func typeRefDeps(d *Database, self string, types ...string) []string {
    out := []string{}
    seen := map[string]bool{}
    for _, typ := range types {
        name := strings.ReplaceAll(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(typ), "[]")), `"`, "")
        if name == "" { continue }
        if !strings.Contains(name, ".") { name = "public." + name }
        if name == self || seen[name] { continue }
        if i := strings.IndexByte(name, '('); i >= 0 { name = strings.TrimSpace(name[:i]) }
        switch {
        case d.Tables[name] != nil:
            out = append(out, "table "+name)
        case d.Views[name] != nil:
            out = append(out, "view "+name)
        case d.Types[name] != nil:
            out = append(out, "type "+name)
        case d.Domains[name] != nil:
            out = append(out, "domain "+name)
        default:
            continue
        }
        seen[name] = true
    }
    return out
}

// calledFunctionRe matches a possibly schema-qualified function call.
var calledFunctionRe = regexp.MustCompile(`([A-Za-z_][\w$]*(?:\.[A-Za-z_][\w$]*)?)\s*\(`)

// calledFunctionDeps returns dependsOn entries for the declared functions,
// other than self, that the expressions call (domain defaults and checks,
// argument defaults), which PostgreSQL resolves when it creates the
// object. Unqualified calls only match functions in public, so a built-in
// such as length() isn't taken for an app.length().
func calledFunctionDeps(callable map[string]bool, self string, exprs ...string) []string {
    if len(callable) == 0 { return nil }
    out := []string{}
    seen := map[string]bool{}
    for _, e := range exprs {
        if strings.IndexByte(e, '(') < 0 { continue }
        for _, m := range calledFunctionRe.FindAllStringSubmatch(e, -1) {
            name := m[1]
            if !strings.Contains(name, ".") { name = "public." + name }
            if seen[name] || !callable[name] || name == RoutineName(self) { continue }
            seen[name] = true
            out = append(out, "function "+name)
        }
    }
    return out
}

var (
    // fromRelationRe matches the relation after FROM or JOIN.
    fromRelationRe = regexp.MustCompile(`(?i)\b(?:from|join)\s+((?:"[^"]+"|[A-Za-z_][\w$]*)(?:\.(?:"[^"]+"|[A-Za-z_][\w$]*))?)`)
    // qualifiedNameRe matches a schema-qualified name, and a following
    // opening parenthesis when it is a function call.
    qualifiedNameRe = regexp.MustCompile(`((?:"[^"]+"|[A-Za-z_][\w$]*)\.(?:"[^"]+"|[A-Za-z_][\w$]*))(\s*\()?`)
    quotedLiteralRe = regexp.MustCompile(`'(?:[^']|'')*'`)
)

// queryDeps returns dependsOn entries for the declared tables, views,
// sequences and functions, other than self, that a view query or
// SQL-standard routine body uses, since PostgreSQL resolves them when it
// creates the object: relations after FROM or JOIN, and any
// schema-qualified relation or function call. Unqualified relations
// elsewhere, as in a comma join, need dependsOn.
func queryDeps(d *Database, callable map[string]bool, self, sql string) []string {
    sql = quotedLiteralRe.ReplaceAllString(sql, "''")
    out := []string{}
    seen := map[string]bool{}
    relation := func(name string) {
        name = strings.ReplaceAll(name, `"`, "")
        if !strings.Contains(name, ".") { name = "public." + name }
        if name == self || seen[name] { return }
        seen[name] = true
        switch {
        case d.Tables[name] != nil:
            out = append(out, "table "+name)
        case d.Views[name] != nil:
            out = append(out, "view "+name)
        case d.Sequences[name] != nil:
            out = append(out, "sequence "+name)
        }
    }
    for _, m := range fromRelationRe.FindAllStringSubmatch(sql, -1) {
        relation(m[1])
    }
    for _, m := range qualifiedNameRe.FindAllStringSubmatch(sql, -1) {
        if m[2] == "" {
            relation(m[1])
            continue
        }
        name := strings.ReplaceAll(m[1], `"`, "")
        if seen["function "+name] || !callable[name] || name == RoutineName(self) { continue }
        seen["function "+name] = true
        out = append(out, "function "+name)
    }
    return out
}

// callableFunctions returns the names (schema.name) of the declared
// functions other objects can call: all but trigger functions.
func callableFunctions(d *Database) map[string]bool {
    out := map[string]bool{}
    for k, f := range d.Functions {
        if f != nil && !strings.EqualFold(strings.TrimSpace(f.Returns), "trigger") { out[RoutineName(k)] = true }
    }
    return out
}

// routineIndex maps each routine name (schema.name) in m to its keys, sorted.
func routineIndex[T any](m map[string]T) map[string][]string {
    out := map[string][]string{}
    for k := range m { out[RoutineName(k)] = append(out[RoutineName(k)], k) }
    for _, keys := range out { sort.Strings(keys) }
    return out
}

// resolveIndexedRoutine is resolveRoutine for a schema-qualified ref,
// looked up in an index from routineIndex.
func resolveIndexedRoutine(ref string, index map[string][]string) []string {
    name, args := ref, ""
    if i := strings.IndexByte(ref, '('); i >= 0 { name, args = strings.TrimSpace(ref[:i]), ref[i:] }
    keys := index[name]
    if args != "" {
        want := compactArgs(args)
        for _, k := range keys {
            if compactArgs(k[len(RoutineName(k)):]) == want { return []string{k} }
        }
    }
    return keys
}
