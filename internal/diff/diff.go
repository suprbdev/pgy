package diff

import (
    "context"
    "errors"
    "fmt"
    "math"
    "regexp"
    "sort"
    "strconv"
    "strings"
    "sync"
    "time"

    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgconn"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/suprbdev/pgy/internal/db"
    "github.com/suprbdev/pgy/internal/schema"
)

type Live struct{
    Schemas    map[string]bool
    Tables     map[string]*LiveTable
    Types      map[string]bool
    Functions  map[string]bool
    Extensions map[string]bool
    // ExtensionDefs: extension name -> installed schema/version
    ExtensionDefs map[string]*LiveExtension
    // AvailableExtensions: the extensions the server can install, by name.
    // nil when unknown: no connection, or IntrospectExtensions found
    // nothing to look up.
    AvailableExtensions map[string]*AvailableExtension
    Views      map[string]bool
    MatViews   map[string]bool
    // ViewOptions: "schema.view" -> reloptions (security_invoker, security_barrier, check_option)
    ViewOptions map[string]map[string]string
    // ViewColumnComments: "schema.view" -> column -> comment
    ViewColumnComments map[string]map[string]string
    // ViewColumnDefaults: "schema.view" -> column -> DEFAULT expression
    ViewColumnDefaults map[string]map[string]string
    // ViewProbes compares declared view queries with the live views (see
    // ProbeViews); nil when they weren't probed, and views are create-only.
    ViewProbes map[string]*ViewProbe
    // ViewDependents: view or materialized view -> the views and
    // materialized views selecting from it, whether they name its columns
    // or use it as a whole (count(*))
    ViewDependents map[string][]string
    // ExprProbes holds what PostgreSQL made of the declared definitions that
    // don't match the live ones as text, or match only by dropping a cast or
    // a schema from one side (see ProbeExpressions); nil when none was
    // probed, and they are compared as text only.
    ExprProbes map[ExprKey]*ExprProbe
    // MatViewTables: "schema.matview" -> index state (Indexes, IndexDefs, IndexOpclasses, IndexComments)
    MatViewTables map[string]*LiveTable
    // ViewTables: "schema.view" -> trigger state (Triggers, TriggerComments)
    ViewTables map[string]*LiveTable
    Sequences  map[string]bool
    Domains    map[string]bool
    Collations map[string]bool
    CollationComments map[string]string
    // TSConfigs: "schema.config" -> token type alias -> dictionaries (unqualified, in order)
    TSConfigs        map[string]map[string][]string
    TSDictionaries   map[string]bool
    // TSDictionaryDefs: "schema.dict" -> its template and options
    TSDictionaryDefs map[string]*schema.TSDictionary
    TSComments       map[string]string // "config:schema.name" / "dict:schema.name" -> comment
    EventTriggers map[string]*LiveEventTrigger
    Publications  map[string]*LivePublication
    // Servers: foreign servers that no extension owns, by name
    Servers       map[string]*LiveServer
    // Wrappers: every foreign-data wrapper, by name, those of extensions
    // included (see ExtensionMembers)
    Wrappers      map[string]*LiveWrapper
    // Languages: every language of pg_language, by name
    Languages     map[string]*LiveLanguage
    // SequenceDefs / DomainDefs: live definitions for ALTER SEQUENCE / DOMAIN
    SequenceDefs map[string]*LiveSequence
    DomainDefs   map[string]*LiveDomain
    Roles      map[string]bool
    RoleDefs   map[string]*LiveRole
    // Owners: "rel:schema.name" (tables, views, sequences), "type:schema.name"
    // (types, domains), "schema:name", "proc:<normalized signature>" -> role
    Owners     map[string]string
    // DefaultACLs: "role|schema|kind" (schema "" = global, kind tables |
    // sequences | functions | types | schemas) -> grantee -> privilege set,
    // owner entries excluded. DefaultACLRows marks keys with a pg_default_acl row.
    DefaultACLs    map[string]map[string]map[string]bool
    DefaultACLRows map[string]bool
    CurrentUser    string
    // DatabaseConfig: ALTER DATABASE ... SET settings of the connected
    // database (for all roles): lower-cased name -> value
    DatabaseConfig map[string]string
    // DatabaseRoleConfig: role -> ALTER ROLE ... IN DATABASE settings of the
    // connected database
    DatabaseRoleConfig map[string]map[string]string
    ServerVersion  int // server_version_num, e.g. 170002; 0 when unknown
    // Offline marks a Live that stands for an empty database rather than
    // one that was introspected (pgy diff --from-empty), so there is no
    // connected role and no server to report on.
    Offline bool
    // DefaultTablespace is the database's default tablespace, which a
    // relation with no tablespace of its own is stored in; "" when unknown.
    DefaultTablespace string
    // SearchPath lists the schemas on the search_path of the session that
    // read the snapshot (current_schemas(false)); nil when unknown. The
    // snapshot itself is read with search_path set to public, so its
    // definitions do not depend on it. When they are compared with the
    // YAML, the qualifier of a schema listed here is optional (see
    // nameScope).
    SearchPath []string
    // scope is the name scope the function signatures keying the maps
    // below are spelled in; nil means publicScope, as introspection keys
    // them (see scoped).
    scope *nameScope
    // Details is filled by IntrospectDetails (pgy pull) and nil otherwise.
    Details *LiveDetails
    // RoleMembers: member role -> parent role -> exists (pg_auth_members)
    RoleMembers map[string]map[string]bool
    // ColumnDependents: "schema.relation.column" -> the views, materialized
    // views, triggers, policies, rules and generated columns whose
    // definitions use the column, which PostgreSQL won't let ALTER COLUMN ...
    // TYPE change under them.
    ColumnDependents map[string][]ColumnDependent
    // RoleMemberOptions: member role -> parent role -> the membership's
    // options, combined over grantors.
    RoleMemberOptions map[string]map[string]*LiveMembership
    RoleComments map[string]string // role name -> comment (pg_shdescription)
    // Grants: object key -> role -> privilege -> exists. Object owners excluded.
    TableGrants    map[string]map[string]map[string]bool // key "schema.table" (also views and sequences)
    TypeGrants     map[string]map[string]map[string]bool // key "schema.type" (types and domains)
    // TypePublicRevoked: types and domains whose ACL no longer gives PUBLIC
    // the default USAGE.
    TypePublicRevoked map[string]bool
    FunctionGrants map[string]map[string]map[string]bool // key normalized "schema.name(args)"
    SchemaGrants   map[string]map[string]map[string]bool // key schema name
    // ColumnGrants: "schema.table" -> column -> role -> privilege -> exists
    // (pg_attribute.attacl), for tables, views and materialized views
    ColumnGrants map[string]map[string]map[string]map[string]bool
    // FunctionPublicExec: normalized signature -> PUBLIC still has EXECUTE
    // (true when proacl is NULL, i.e. default privileges, or PUBLIC=X entry present).
    FunctionPublicExec map[string]bool
    // Comments: current COMMENT ON values (absent key = no comment)
    RelComments      map[string]string // "schema.rel" -> comment (tables, views, matviews)
    FunctionComments map[string]string // normalized "schema.name(args)" -> comment
    TypeComments     map[string]string // "schema.type" -> comment
    SchemaComments   map[string]string // schema name -> comment
    // FunctionDefs: normalized signature -> live definition, for replace-on-change
    FunctionDefs map[string]*LiveFunction
    // Procedures: normalized signature -> exists (pg_proc prokind = 'p').
    // Grants/comments for procedures share FunctionGrants/FunctionComments/
    // FunctionPublicExec (same pg_proc key space).
    Procedures map[string]bool
    // ProcedureDefs: normalized signature -> live definition, for replace-on-change
    ProcedureDefs map[string]*LiveProcedure
    // AggregateDefs: normalized signature -> aggregate definition
    AggregateDefs map[string]*LiveAggregate
    // EnumLabels: "schema.type" -> labels in enumsortorder, for ALTER TYPE ... ADD VALUE
    EnumLabels map[string][]string
    // CompositeAttributes: "schema.type" -> attribute name -> format_type, for
    // ALTER TYPE ... ADD / ALTER / DROP ATTRIBUTE
    CompositeAttributes map[string]map[string]string
    // CompositeCollations: "schema.type" -> attribute -> COLLATE, for
    // attributes whose collation differs from their type's default
    CompositeCollations map[string]map[string]string
    // CompositeComments: "schema.type" -> attribute -> comment
    CompositeComments map[string]map[string]string
    // ExtensionMembers: objects belonging to an extension, which are never
    // pruned. Keys are "rel:schema.name", "type:schema.name",
    // "proc:<normalized signature>", "language:name" and "fdw:name".
    ExtensionMembers map[string]bool
    // MemberExtension: ExtensionMembers key -> the name of the extension
    // the object belongs to.
    MemberExtension map[string]string
    // InitPrivs: the privileges CREATE EXTENSION left on the extension's
    // objects (pg_init_privs), by the object's ExtensionMembers key ->
    // grantee -> privilege set, the owner's entries excluded. A member
    // without a key kept the default privileges of its kind. See
    // InitialGrants.
    InitPrivs map[string]map[string]map[string]bool
    // BaseTypes: base and multirange types ("schema.type"), which pgy never
    // creates; a type declaration without a kind manages their owner,
    // privileges and comment.
    BaseTypes map[string]bool
    // OwnedSequences: "schema.seq" -> owned by a column (serial / identity /
    // OWNED BY), so it goes away with its column
    OwnedSequences map[string]bool
    // Partitions: "schema.parent" -> its direct partitions, sorted
    Partitions map[string][]string
    // PartitionBounds: "schema.partition" -> its parent and bound clause
    // (pg_get_expr of relpartbound); nil or missing when unknown.
    PartitionBounds map[string]LivePartitionBound
    // TableData: "schema.table" -> comparison of its declared data rows
    // with the live rows; filled by IntrospectData
    TableData map[string]*LiveTableData
}

// LiveServer is a foreign server from pg_foreign_server.
type LiveServer struct {
    Wrapper string
    Type    string
    Version string
    Options map[string]string
    Owner   string
    Comment string
    Grants  map[string]map[string]bool // srvacl, owner excluded: role -> privilege ("usage", "usage*" with grant option)
    // UserMapping reports whether the connected role, or PUBLIC, has a
    // user mapping for the server.
    UserMapping bool
    // UserMappings lists the roles with a user mapping for the server,
    // "public" for PUBLIC. Their options are never read.
    UserMappings []string
}

// LiveForeignTable is what pg_foreign_table records for a foreign table.
type LiveForeignTable struct {
    Server  string
    Options map[string]string
}

// LivePublication is a publication from pg_publication and its table and
// schema memberships. Tables maps "schema.table" to its column list (nil =
// all columns, sorted) and row filter.
type LivePublication struct {
    AllTables bool
    Publish   []string // enabled actions in insert, update, delete, truncate order
    ViaRoot   bool
    GeneratedColumns string // publish_generated_columns: none | stored; "" before PostgreSQL 18
    Tables    map[string]LivePublicationTable
    Schemas   []string
    Comment   string
    Owner     string
}

type LivePublicationTable struct {
    Columns []string
    Where   string
    Only    bool // the table has inheritance children the publication leaves out
}

// LiveEventTrigger is an event trigger from pg_event_trigger. Function is
// "schema.name"; Enabled is origin | disabled | replica | always.
type LiveEventTrigger struct {
    Event     string
    Tags      []string
    Function  string
    Enabled   string
    Comment   string
    Extension bool // member of an extension
}

// LiveRole is a role's attributes from pg_roles.
type LiveRole struct {
    Login, Superuser, CreateDB, CreateRole, Replication, BypassRLS, Inherit bool
    ConnectionLimit int
    ValidUntil string // UTC RFC 3339, "infinity", or "" when unset
    Config map[string]string // ALTER ROLE ... SET settings for all databases: lower-cased name -> value
}

// ColumnDependent is an object whose definition uses a column: a view or
// materialized view (Name is "schema.view"), a trigger, policy or rule
// (Name is its name, Table the relation it is on), or a generated column of
// the same table (Kind "generated", Name is the column).
type ColumnDependent struct {
    Kind, Name, Table string
}

// LiveMembership is a role membership's options. Before PostgreSQL 16,
// Inherit is the member's INHERIT attribute and Set is always true.
type LiveMembership struct {
    Admin, Inherit, Set bool
    Grantors []string // roles that granted the membership; PostgreSQL 16+ keeps one grant per grantor
}

// LiveSequence is a sequence's options from pg_sequence. Numeric options are
// decimal strings. Owner is "schema.table.column" for a sequence owned by a
// column, else "".
type LiveSequence struct {
    Type      string
    Increment string
    MinValue  string
    MaxValue  string
    Start     string
    Cache     string
    Cycle     bool
    Owner     string
    Unlogged  bool // relpersistence = 'u'
    Name      string // "schema.sequence"
}

// LiveDomain is a domain's default, NOT NULL flag and CHECK constraints
// (constraint name -> pg_get_constraintdef output).
type LiveDomain struct {
    Type    string // format_type of the base type, with its modifier
    NotNullName string // the NOT NULL constraint's name (PostgreSQL 17+ records one)
    Default string
    NotNull bool
    Checks  map[string]string
    CheckComments map[string]string // constraint name -> comment
}

// LiveExtension is an installed extension's schema, version and whether it
// can be moved with ALTER EXTENSION ... SET SCHEMA.
type LiveExtension struct {
    Schema      string
    Version     string
    Relocatable bool
    Comment     string
    // DefaultComment is the comment the extension's control file gives it,
    // which CREATE EXTENSION sets.
    DefaultComment string
    // DefaultVersion is the version CREATE EXTENSION installs on this
    // server; "" when unknown.
    DefaultVersion string
    // Requires lists the installed extensions this one depends on, sorted.
    Requires []string
    // UpdateTargets holds the versions ALTER EXTENSION ... UPDATE TO can
    // reach from Version; nil when unknown.
    UpdateTargets map[string]bool
    // MovedFrom is the schema a simulated ALTER EXTENSION ... SET SCHEMA
    // moved the extension out of, while the other live definitions still
    // spell its objects the old way; "" otherwise.
    MovedFrom string
    // Members holds the unqualified names of the types, routines,
    // relations, operator classes and text search objects the extension
    // owns, and Operators those of its operators. Builtins holds the names
    // of its routines that pg_catalog has a routine of too, so that an
    // unqualified call may well mean the built-in one. All three are nil
    // until IntrospectExtensions loads them for an extension that has to
    // move.
    Members   map[string]bool
    Operators map[string]bool
    Builtins  map[string]bool
    // Created marks an extension that an earlier step of the same plan
    // creates (see Simulate), so the snapshot doesn't have its objects.
    Created bool
    // Dependents lists the objects outside the extension that use its
    // members, which DROP EXTENSION without CASCADE refuses to leave
    // behind. It is nil until IntrospectExtensions loads it for an
    // extension that pruning may drop.
    Dependents []ExtensionDependent
    // UpdateDependents lists the views, materialized views and routines
    // with a SQL-standard body outside the extension that use its objects,
    // directly or through other views, and whatever else uses those views.
    // An update that replaces what one of them uses fails while it exists.
    // It is nil until IntrospectExtensions loads it for an extension whose
    // version changes.
    UpdateDependents []UpdateDependent
}

// AvailableExtension is an extension the server can install: its default
// version and, for every version it can install or update to, the
// extensions that version requires.
type AvailableExtension struct {
    DefaultVersion string
    Requires       map[string][]string
}

type LiveFunction struct{
    Signature  string // "schema.name(identity args)" as introspected
    Args       string // pg_get_function_arguments: the arguments with their defaults
    Kind       string // pg_proc.prokind: f (function), w (window), a (aggregate)
    Internal   bool   // created implicitly by another object (e.g. a range type's constructors)
    Body       string
    Volatility string // volatile|stable|immutable
    Security   string // definer|invoker
    Strict     bool
    Leakproof  bool
    Parallel   string  // safe|restricted|unsafe
    Cost       float64 // procost
    Rows       float64 // prorows (0 unless set-returning)
    Config     map[string]string // proconfig: lower-cased parameter name -> value
    Returns    string // pg_get_function_result, e.g. "SETOF integer"; "" when unknown
    // SQLBody reports a SQL-standard body (BEGIN ATOMIC / RETURN); Body then
    // holds PostgreSQL's rewritten form, from pg_get_function_sqlbody.
    SQLBody bool
    Language  string   // pg_language.lanname; "" when unknown
    Transform []string // protrftypes as type names, in declaration order
}
type LiveProcedure struct{
    Signature string // "schema.name(identity args)" as introspected
    Args     string // pg_get_function_arguments: the arguments with their defaults
    Body     string
    SQLBody  bool // SQL-standard body; see LiveFunction.SQLBody
    Security string // definer|invoker
    Config   map[string]string // proconfig: lower-cased parameter name -> value
    Language  string   // pg_language.lanname; "" when unknown
    Transform []string // protrftypes as type names, in declaration order
}
type LiveTable struct{
    Columns     map[string]*LiveColumn
    Constraints map[string]bool // constraint name -> exists
    // ConstraintDefs: constraint name -> pg_get_constraintdef() output,
    // for detecting redefined constraints (drop+add, gated behind --unsafe)
    ConstraintDefs map[string]string
    Indexes     map[string]bool // index name -> exists
    // IndexOpclasses: index name -> per-key-column operator class, in column
    // order, for detecting opclass changes (drop+recreate, gated behind
    // --unsafe). Nil / missing entry means unknown: the index is adopted
    // by name and opclass changes are not detected.
    IndexOpclasses map[string][]LiveIndexOpclass
    // IndexDefs: index name -> structured definition, for detecting changed
    // indexes (drop+recreate, gated behind --unsafe). Missing entry means
    // unknown: only the opclass check applies.
    IndexDefs map[string]*LiveIndex
    Triggers    map[string]string // trigger name -> pg_get_triggerdef() output ("" if unknown)
    Policies    map[string]*LivePolicy // policy name -> live definition
    IndexComments      map[string]string // index name -> comment (absent key = no comment)
    TriggerComments    map[string]string // trigger name -> comment
    Rules              map[string]*LiveRule // rule name -> definition (excluding views' _RETURN)
    Statistics         map[string]*LiveStatistic // extended statistics name -> definition
    ReplicaIdentity    string               // default | full | nothing | index <name>
    ClusteredOn        string               // index marked for CLUSTER ("" = none)
    TriggerEnabled     map[string]string    // trigger name -> origin | disabled | replica | always
    RuleComments       map[string]string
    PolicyComments     map[string]string // policy name -> comment
    ConstraintComments map[string]string // constraint name -> comment (absent key = no comment)
    // ConstraintInherited / TriggerInherited: names cloned from a partitioned
    // parent or inherited from a parent table; never pruned on their own
    ConstraintInherited map[string]bool
    TriggerInherited    map[string]bool
    IsPartition bool            // relispartition: a partition of a partitioned table
    OfType      string          // "schema.type" of a typed table's composite type (reloftype), or ""
    Inherits    []string        // INHERITS parents (not partitioning), in inhseqno order
    Partitioned bool            // relkind = 'p': a partitioned table
    Unlogged    bool            // relpersistence = 'u'
    Tablespace  string          // the relation's own tablespace; "" = the database default
    Options     map[string]string // reloptions: lower-cased name -> value
    NoToast     bool            // reltoastrelid = 0: PostgreSQL discards toast.* storage parameters
    Foreign     *LiveForeignTable // set for foreign tables (relkind 'f')
    HasPK       bool            // whether a primary key constraint exists
    RLSEnabled  bool            // row level security enabled
    RLSForced   bool            // relforcerowsecurity: policies apply to the table owner too
}
// indexedRelation returns the live table or materialized view with the given
// key, which is where index introspection rows are recorded.
func (l *Live) indexedRelation(key string) *LiveTable {
    if t := l.Tables[key]; t != nil { return t }
    return l.MatViewTables[key]
}

func newLiveTable() *LiveTable {
    return &LiveTable{
        Columns:            map[string]*LiveColumn{},
        Constraints:        map[string]bool{},
        ConstraintDefs:     map[string]string{},
        Indexes:            map[string]bool{},
        IndexOpclasses:     map[string][]LiveIndexOpclass{},
        IndexDefs:          map[string]*LiveIndex{},
        Triggers:           map[string]string{},
        Policies:           map[string]*LivePolicy{},
        IndexComments:      map[string]string{},
        ConstraintComments: map[string]string{},
        TriggerComments:    map[string]string{},
        Rules:              map[string]*LiveRule{},
        Statistics:         map[string]*LiveStatistic{},
        TriggerEnabled:     map[string]string{},
        RuleComments:       map[string]string{},
        PolicyComments:     map[string]string{},
        ConstraintInherited: map[string]bool{},
        TriggerInherited:    map[string]bool{},
    }
}

// LiveAggregate is an aggregate's definition from pg_aggregate. Function
// fields are regproc text ("" when unset); Parallel is safe | restricted |
// unsafe. Options holds every schema.AggregateOptions entry by SQL name:
// functions, types and sortop as text ("" when unset), spaces as integers,
// *_extra as true/false and *_modify as read_only | shareable | read_write.
// HasInitCond and HasMInitCond tell an empty initial condition from none.
type LiveAggregate struct {
    SFunc, SType, FinalFunc, InitCond, CombineFunc, Parallel string
    HasInitCond, HasMInitCond bool
    Options map[string]string
}

// LivePartitionBound is a partition's parent and its bound clause as
// pg_get_expr prints it ("FOR VALUES FROM ('2024-01-01') TO (MAXVALUE)").
type LivePartitionBound struct {
    Parent, Bound string
}

// LiveStatistic is an extended statistics object: its kinds (empty = all)
// and items (columns and expressions) from pg_get_statisticsobjdef.
type LiveStatistic struct {
    Kinds   []string
    Items   []string
    Comment string
    Target  int // stxstattarget; -1 = default
}

var statisticsDefRe = regexp.MustCompile(`(?s)^CREATE STATISTICS ` + catalogName + `(?: \(([^)]*)\))? ON (.+) FROM ` + catalogName + `$`)

// LiveRule is a rewrite rule's event, INSTEAD flag and WHERE condition
// (as pg_get_ruledef prints it, parentheses included).
type LiveRule struct {
    Event   string
    Instead bool
    Where   string
    Def     string // pg_get_ruledef output
}

var ruleWhereRe = regexp.MustCompile(`(?s)\bON\s+\w+\s+TO\s+` + catalogName + `\s+WHERE\s+(.*?)\s+DO\s`)

// catalogName matches a possibly qualified name as catalog functions print
// it, each part bare or double-quoted.
const catalogName = `(?:"(?:[^"]|"")*"|[^\s".]+)(?:\.(?:"(?:[^"]|"")*"|[^\s".]+))?`

// LiveIndex is an index definition as introspected from pg_index. Keys holds
// each key column's name or expression (pg_get_indexdef per column, without
// opclass or ordering); Descending/NullsFirst decode indoption per key column.
type LiveIndex struct {
    Unique           bool
    Method           string
    Keys             []string
    Descending       []bool
    NullsFirst       []bool
    Include          []string
    Predicate        string
    NullsNotDistinct bool
    Options          map[string]string // reloptions: lower-cased name -> value
    Constraint       bool              // backs a primary key, unique or exclusion constraint
    Inherited        bool              // a partition's copy of an index on its parent
    Invalid          bool              // indisvalid is false (interrupted concurrent build, or a partitioned index missing partitions)
    Tablespace       string            // the index's own tablespace; "" = the database default
}

// LiveIndexOpclass is the operator class of one index key column. Default is
// true when the column uses the access method's default opclass for its type,
// so a desired index with no explicit opclass matches regardless of the
// default's actual name (text_ops, int4_ops, ...).
type LiveIndexOpclass struct {
    Name    string
    Default bool
    // Collation is the key column's COLLATE when it differs from the
    // column's (or, for an expression, its type's) default; "" otherwise.
    Collation string
    // StatisticsTarget is the index column's attstattarget; -1 = default.
    StatisticsTarget int
    // Params holds the operator class parameters of the key column as
    // name=value entries in catalog order: gist_trgm_ops(siglen=32) gives
    // "siglen=32".
    Params []string
}

// Declaration returns the operator class as an index's opclass entry spells
// it: its name, followed by its parameters in parentheses when it has any
// ("gist_trgm_ops(siglen=32)"). Parameters keep their catalog order.
func (o LiveIndexOpclass) Declaration() string {
    if len(o.Params) == 0 { return o.Name }
    parts := make([]string, 0, len(o.Params))
    for _, p := range o.Params {
        k, v, _ := strings.Cut(p, "=")
        if !opclassParamBareRe.MatchString(v) { v = quoteString(v) }
        parts = append(parts, k+"="+v)
    }
    return o.Name + "(" + strings.Join(parts, ", ") + ")"
}

var opclassParamBareRe = regexp.MustCompile(`^[A-Za-z0-9_.+-]+$`)

// LivePolicy is a policy as it exists in the live database. Cmd/Roles/Using/
// WithCheck mirror the CREATE POLICY clauses so a redefined policy can be
// detected and replaced. Roles is nil/empty when the policy applies to PUBLIC
// (i.e. no explicit TO clause).
type LivePolicy struct {
    Restrictive bool   // AS RESTRICTIVE (false = PERMISSIVE)
    Cmd         string // select|insert|update|delete|all
    Roles       []string
    Using       string
    WithCheck   string
}

type LiveColumn struct{
    Type      string
    Nullable  bool
    Default   string
    Comment   string
    Identity  string // "" (none), "always", or "by default"
    IdentitySequence *LiveSequence // the identity's sequence options; nil when unknown
    Generated string // generation expression of a generated column ("" = not generated)
    Virtual   bool   // generated column is VIRTUAL (PostgreSQL 18+), not STORED
    Collation   string // explicit collation differing from the type's default ("" = default)
    StatisticsTarget *int // attstattarget; nil = default
    Options     map[string]string // attoptions (n_distinct, n_distinct_inherited)
    FDWOptions  map[string]string // attfdwoptions of a foreign table column
    // NotNullNotValid names a NOT VALID not-null constraint on the column
    // (PostgreSQL 18+), which is enforced for new rows but not yet validated.
    NotNullNotValid string
    // NotNullName names the column's not-null constraint, valid or not;
    // "" before PostgreSQL 18, which records none for a table column.
    NotNullName string
    Inherited   bool   // the column comes from a parent table (attinhcount > 0); the parent owns its definition
    CollationSchema string // schema of Collation when it is not in pg_catalog
    TypeStorage string     // the column type's default storage
    Compression string // pglz | lz4 | "" (default)
    Storage     string // plain | external | extended | main
}

// Introspect reads the live database's schema. All catalog queries are sent
// in one pipelined batch; when DDL committed by another session invalidates
// an object mid-introspection (PostgreSQL then reports it as an internal
// error such as "could not open relation with OID"), the read is retried.
func Introspect(ctx context.Context, pool *pgxpool.Pool) (*Live, error) {
    var err error
    for attempt := 0; attempt < 3; attempt++ {
        var l *Live
        if l, err = introspectOnce(ctx, pool); err == nil || !isCatalogRace(err) {
            return l, err
        }
    }
    return nil, err
}

// errObjectVanished marks a NULL where an introspection query expects a
// value: catalog functions such as pg_get_function_identity_arguments
// return NULL for an object another session dropped after the query's
// snapshot was taken.
var errObjectVanished = errors.New("object dropped while being read")

// isCatalogRace reports whether err comes from a catalog object being
// dropped or altered by another session while it was being read.
func isCatalogRace(err error) bool {
    if errors.Is(err, errObjectVanished) { return true }
    var pgErr *pgconn.PgError
    if !errors.As(err, &pgErr) { return false }
    if pgErr.Code == "XX000" {
        return strings.Contains(pgErr.Message, "could not open relation") || strings.Contains(pgErr.Message, "cache lookup failed")
    }
    return pgErr.Code == "42P01" || pgErr.Code == "42704"
}

func introspectOnce(ctx context.Context, pool *pgxpool.Pool) (*Live, error) {
    l := &Live{
        Schemas: map[string]bool{},
        Tables: map[string]*LiveTable{},
        Types: map[string]bool{},
        Functions: map[string]bool{},
        Extensions: map[string]bool{},
        ExtensionDefs: map[string]*LiveExtension{},
        Views: map[string]bool{},
        MatViews: map[string]bool{},
        ViewOptions: map[string]map[string]string{},
        MatViewTables: map[string]*LiveTable{},
        ViewColumnComments: map[string]map[string]string{},
        ViewColumnDefaults: map[string]map[string]string{},
        ViewTables: map[string]*LiveTable{},
        Sequences: map[string]bool{},
        Domains: map[string]bool{},
        SequenceDefs: map[string]*LiveSequence{},
        Collations: map[string]bool{},
        CollationComments: map[string]string{},
        TSConfigs: map[string]map[string][]string{},
        TSDictionaries: map[string]bool{},
        TSDictionaryDefs: map[string]*schema.TSDictionary{},
        TSComments: map[string]string{},
        EventTriggers: map[string]*LiveEventTrigger{},
        Publications: map[string]*LivePublication{},
        Servers: map[string]*LiveServer{},
        Wrappers: map[string]*LiveWrapper{},
        Languages: map[string]*LiveLanguage{},
        DomainDefs: map[string]*LiveDomain{},
        Roles: map[string]bool{},
        RoleDefs: map[string]*LiveRole{},
        Owners: map[string]string{},
        DefaultACLs: map[string]map[string]map[string]bool{},
        DefaultACLRows: map[string]bool{},
        RoleMembers: map[string]map[string]bool{},
        RoleMemberOptions: map[string]map[string]*LiveMembership{},
        ColumnDependents: map[string][]ColumnDependent{},
        ViewDependents: map[string][]string{},
        RoleComments: map[string]string{},
        TableGrants: map[string]map[string]map[string]bool{},
        TypeGrants: map[string]map[string]map[string]bool{},
        TypePublicRevoked: map[string]bool{},
        FunctionGrants: map[string]map[string]map[string]bool{},
        SchemaGrants: map[string]map[string]map[string]bool{},
        ColumnGrants: map[string]map[string]map[string]map[string]bool{},
        FunctionPublicExec: map[string]bool{},
        RelComments: map[string]string{},
        FunctionComments: map[string]string{},
        TypeComments: map[string]string{},
        SchemaComments: map[string]string{},
        FunctionDefs: map[string]*LiveFunction{},
        Procedures: map[string]bool{},
        ProcedureDefs: map[string]*LiveProcedure{},
        EnumLabels: map[string][]string{},
        AggregateDefs: map[string]*LiveAggregate{},
        Partitions: map[string][]string{},
        PartitionBounds: map[string]LivePartitionBound{},
        CompositeAttributes: map[string]map[string]string{},
        CompositeCollations: map[string]map[string]string{},
        CompositeComments: map[string]map[string]string{},
        ExtensionMembers: map[string]bool{},
        MemberExtension: map[string]string{},
        InitPrivs: map[string]map[string]map[string]bool{},
        BaseTypes: map[string]bool{},
        OwnedSequences: map[string]bool{},
    }
    batch := &queryBatch{}
    
    // Query existing schemas
    schemaQ := `
        select n.nspname
        from pg_namespace n
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(schemaQ, func(schemaRows pgx.Rows) error {
        for schemaRows.Next() {
            var schemaName string
            if err := schemaRows.Scan(&schemaName); err != nil { 
                return err 
            }
            l.Schemas[schemaName] = true
        }
        return nil
    })
    
    // Query all tables (not just those with columns). pg_catalog rather than
    // information_schema, which hides objects the role has no privilege on.
    tableQ := `
        select n.nspname, c.relname, c.relispartition, c.relkind = 'p', c.relpersistence = 'u', c.reltoastrelid = 0,
               c.relkind = 'f', coalesce(fs.srvname, ''), coalesce(ft.ftoptions, '{}'::text[]),
               coalesce(c.reloptions, '{}'::text[]) ||
               coalesce((select array_agg('toast.' || o) from pg_class tc, unnest(tc.reloptions) o where tc.oid = c.reltoastrelid), '{}'::text[]),
               case c.relreplident when 'f' then 'full' when 'n' then 'nothing'
                    when 'i' then 'index ' || coalesce((select i.relname from pg_index x join pg_class i on i.oid = x.indexrelid
                                                        where x.indrelid = c.oid and x.indisreplident), '')
                    else 'default' end,
               coalesce((select tn.nspname || '.' || ty.typname from pg_type ty join pg_namespace tn on tn.oid = ty.typnamespace where ty.oid = c.reloftype), ''),
               coalesce(ts.spcname::text, '')
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
        left join pg_tablespace ts on ts.oid = c.reltablespace
        left join pg_foreign_table ft on ft.ftrelid = c.oid
        left join pg_foreign_server fs on fs.oid = ft.ftserver
        where c.relkind in ('r', 'p', 'f')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(tableQ, func(tableRows pgx.Rows) error {
        for tableRows.Next() {
            var schemaName, tableName, server string
            var isPartition, partitioned, unlogged, noToast, foreign bool
            var reloptions, ftoptions []string
            var replident, ofType, tablespace string
            if err := tableRows.Scan(&schemaName, &tableName, &isPartition, &partitioned, &unlogged, &noToast, &foreign, &server, &ftoptions, &reloptions, &replident, &ofType, &tablespace); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, tableName)
            lt := newLiveTable()
            lt.NoToast = noToast
            if foreign { lt.Foreign = &LiveForeignTable{Server: server, Options: parseFDWOptions(ftoptions)} }
            lt.IsPartition, lt.Partitioned, lt.Unlogged, lt.Options, lt.ReplicaIdentity, lt.OfType = isPartition, partitioned, unlogged, parseProconfig(reloptions), replident, ofType
            lt.Tablespace = tablespace
            l.Tables[key] = lt
        }
        return nil
    })
    
    // Query existing materialized views
    matViewQ := `
        select n.nspname, c.relname, c.reltoastrelid = 0,
               coalesce(c.reloptions, '{}'::text[]) ||
               coalesce((select array_agg('toast.' || o) from pg_class tc, unnest(tc.reloptions) o where tc.oid = c.reltoastrelid), '{}'::text[]),
               coalesce(ts.spcname::text, '')
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
        left join pg_tablespace ts on ts.oid = c.reltablespace
        where c.relkind = 'm'
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(matViewQ, func(matViewRows pgx.Rows) error {
        for matViewRows.Next() {
            var schemaName, viewName, tablespace string
            var noToast bool
            var reloptions []string
            if err := matViewRows.Scan(&schemaName, &viewName, &noToast, &reloptions, &tablespace); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, viewName)
            l.MatViews[key] = true
            lt := newLiveTable()
            lt.NoToast = noToast
            lt.Options = parseProconfig(reloptions)
            lt.Tablespace = tablespace
            l.MatViewTables[key] = lt
        }
        return nil
    })

    // Query columns to enrich table info. Uses pg_catalog rather than
    // information_schema so format_type keeps type modifiers (varchar(255))
    // and resolves arrays/user types (information_schema reports those as
    // ARRAY/USER-DEFINED, which cannot be diffed against YAML types).
    const q = `
        select n.nspname, c.relname, a.attname,
               format_type(a.atttypid, a.atttypmod),
               not a.attnotnull,
               coalesce(pg_get_expr(ad.adbin, ad.adrelid), ''),
               case a.attidentity when 'a' then 'always' when 'd' then 'by default' else '' end,
               a.attgenerated::text,
               coalesce(co.collname, ''),
               case a.attcompression when 'p' then 'pglz' when 'l' then 'lz4' else '' end,
               case a.attstorage when 'p' then 'plain' when 'e' then 'external' when 'm' then 'main' else 'extended' end,
               nullif(coalesce(a.attstattarget::int, -1), -1),
               case when cn.nspname <> 'pg_catalog' then cn.nspname else '' end,
               case ty.typstorage when 'p' then 'plain' when 'e' then 'external' when 'm' then 'main' else 'extended' end,
               a.attinhcount > 0,
               coalesce(a.attoptions, '{}'::text[]),
               coalesce(a.attfdwoptions, '{}'::text[]),
               coalesce(nn.conname, ''), coalesce(nn.convalidated, true)
        from pg_attribute a
        join pg_class c on c.oid = a.attrelid
        join pg_namespace n on n.oid = c.relnamespace
        join pg_type ty on ty.oid = a.atttypid
        left join pg_collation co on co.oid = a.attcollation and a.attcollation <> ty.typcollation
        left join pg_namespace cn on cn.oid = co.collnamespace
        left join pg_attrdef ad on ad.adrelid = a.attrelid and ad.adnum = a.attnum
        left join pg_constraint nn on nn.conrelid = a.attrelid and nn.contype = 'n' and nn.conkey[1] = a.attnum
        where a.attnum > 0 and not a.attisdropped
        and c.relkind in ('r', 'p', 'f')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        order by n.nspname, c.relname, a.attnum
    `
    batch.add(q, func(rows pgx.Rows) error {
        for rows.Next() {
            var schemaName, tableName, colName, dataType, def, identity string
            var nullable bool
            var generated, collation, compression, storage string
            var statTarget *int
            var collSchema, typeStorage string
            var inherited bool
            var attoptions, fdwoptions []string
            var notNullName string
            var notNullValid bool
            if err := rows.Scan(&schemaName, &tableName, &colName, &dataType, &nullable, &def, &identity, &generated, &collation, &compression, &storage, &statTarget, &collSchema, &typeStorage, &inherited, &attoptions, &fdwoptions, &notNullName, &notNullValid); err != nil { return err }
            key := fmt.Sprintf("%s.%s", schemaName, tableName)
            t := l.Tables[key]
            if t == nil { t = newLiveTable(); l.Tables[key] = t }
            lc := &LiveColumn{Type: dataType, Nullable: nullable, Default: def, Identity: identity, Collation: collation, Compression: compression, Storage: storage, TypeStorage: typeStorage, StatisticsTarget: statTarget, CollationSchema: collSchema, Inherited: inherited, NotNullName: notNullName}
            if !notNullValid { lc.NotNullNotValid = notNullName }
            if generated != "" { lc.Generated, lc.Default, lc.Virtual = def, "", generated == "v" }
            if len(attoptions) > 0 { lc.Options = parseProconfig(attoptions) }
            if len(fdwoptions) > 0 { lc.FDWOptions = parseFDWOptions(fdwoptions) }
            t.Columns[colName] = lc
        }
        return nil
    })

    // Query existing table constraints (pk, fk, check, unique, exclude) with
    // their full definitions so redefined constraints can be detected. Uses
    // pg_constraint rather than information_schema so pg_get_constraintdef is
    // available (and NOT NULL pseudo-constraints are excluded).
    conQ := `
        select n.nspname, c.relname, con.conname, con.contype::text,
               pg_get_constraintdef(con.oid), con.conparentid <> 0 or con.coninhcount > 0
        from pg_constraint con
        join pg_class c on c.oid = con.conrelid
        join pg_namespace n on n.oid = c.relnamespace
        where con.contype in ('p', 'f', 'c', 'u', 'x')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(conQ, func(conRows pgx.Rows) error {
        for conRows.Next() {
            var schemaName, tableName, conName, conType, conDef string
            var inherited bool
            if err := conRows.Scan(&schemaName, &tableName, &conName, &conType, &conDef, &inherited); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, tableName)
            if t := l.Tables[key]; t != nil {
                t.Constraints[conName] = true
                t.ConstraintDefs[conName] = conDef
                if inherited { t.ConstraintInherited[conName] = true }
                if conType == "p" {
                    t.HasPK = true
                }
            }
        }
        return nil
    })

    // Query existing indexes
    idxQ := `
        select schemaname, tablename, indexname
        from pg_indexes
        where schemaname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and schemaname !~ '^pg_(toast_)?temp_'
    `
    batch.add(idxQ, func(idxRows pgx.Rows) error {
        for idxRows.Next() {
            var schemaName, tableName, idxName string
            if err := idxRows.Scan(&schemaName, &tableName, &idxName); err != nil {
                return err
            }
            if t := l.indexedRelation(fmt.Sprintf("%s.%s", schemaName, tableName)); t != nil {
                t.Indexes[idxName] = true
            }
        }
        return nil
    })

    // Query per-key-column operator classes so redefined opclasses can be
    // detected (drop+recreate, gated behind --unsafe). Default opclasses are
    // flagged so a desired index without an explicit opclass matches. An
    // expression key's default collation is that of the opclass input type
    // when the opclass stores another type (gin_trgm_ops indexes text but
    // stores int4), else that of the stored type. A polymorphic input type
    // (array_ops takes anyarray) has no collation of its own, so the stored
    // type decides there too. Operator class parameters are the index
    // column's attoptions.
    opcQ := `
        select n.nspname, t.relname, i.relname,
               case when opn.nspname in ('pg_catalog', 'public') then op.opcname::text else quote_ident(opn.nspname) || '.' || quote_ident(op.opcname) end,
               op.opcdefault,
               case when x.indcollation[k.ord] = 0 or x.indcollation[k.ord] = coalesce(case when x.indkey[k.ord] > 0 then ta.attcollation end, case when op.opckeytype <> 0 and oty.typtype <> 'p' then oty.typcollation end, ity.typcollation)
                    then '' else case when cn.nspname = 'pg_catalog' then '' else cn.nspname || '.' end || co.collname end,
               coalesce(ia.attstattarget::int, -1),
               coalesce(ia.attoptions, '{}'::text[])
        from pg_index x
        join pg_class i on i.oid = x.indexrelid
        join pg_class t on t.oid = x.indrelid
        join pg_namespace n on n.oid = t.relnamespace
        cross join lateral generate_series(0, x.indnkeyatts - 1) k(ord)
        join pg_opclass op on op.oid = x.indclass[k.ord]
        join pg_namespace opn on opn.oid = op.opcnamespace
        left join pg_attribute ta on ta.attrelid = x.indrelid and ta.attnum = x.indkey[k.ord]
        left join pg_attribute ia on ia.attrelid = x.indexrelid and ia.attnum = k.ord + 1
        left join pg_type ity on ity.oid = ia.atttypid
        left join pg_type oty on oty.oid = op.opcintype
        left join pg_collation co on co.oid = x.indcollation[k.ord]
        left join pg_namespace cn on cn.oid = co.collnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        order by n.nspname, t.relname, i.relname, k.ord
    `
    batch.add(opcQ, func(opcRows pgx.Rows) error {
        for opcRows.Next() {
            var schemaName, tableName, idxName, opcName, collation string
            var opcDefault bool
            var target int
            var params []string
            if err := opcRows.Scan(&schemaName, &tableName, &idxName, &opcName, &opcDefault, &collation, &target, &params); err != nil {
                return err
            }
            if t := l.indexedRelation(fmt.Sprintf("%s.%s", schemaName, tableName)); t != nil {
                t.IndexOpclasses[idxName] = append(t.IndexOpclasses[idxName], LiveIndexOpclass{Name: opcName, Default: opcDefault, Collation: collation, StatisticsTarget: target, Params: params})
            }
        }
        return nil
    })

    // Query structured index definitions so changed indexes can be detected.
    idefQ := `
        select n.nspname, t.relname, i.relname, x.indisunique, am.amname, x.indnkeyatts,
               array(select case when x.indkey[k - 1] = 0 then pg_get_indexdef(x.indexrelid, k, true)
                                 else (select quote_ident(a.attname) from pg_attribute a where a.attrelid = x.indrelid and a.attnum = x.indkey[k - 1]) end
                     from generate_series(1, x.indnatts) k order by k),
               x.indoption::int2[],
               coalesce(pg_get_expr(x.indpred, x.indrelid, true), ''),
               x.indnullsnotdistinct,
               coalesce(i.reloptions, '{}'::text[]),
               exists (select 1 from pg_constraint con where con.conindid = x.indexrelid and con.contype in ('p', 'u', 'x')),
               i.relispartition,
               x.indisclustered,
               not x.indisvalid,
               coalesce(ts.spcname::text, '')
        from pg_index x
        join pg_class i on i.oid = x.indexrelid
        left join pg_tablespace ts on ts.oid = i.reltablespace
        join pg_class t on t.oid = x.indrelid
        join pg_am am on am.oid = i.relam
        join pg_namespace n on n.oid = t.relnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(idefQ, func(idefRows pgx.Rows) error {
        for idefRows.Next() {
            var schemaName, tableName, idxName, method, predicate, tablespace string
            var unique, nullsNotDistinct, constraint, inherited, clustered, invalid bool
            var nKey int16
            var cols, reloptions []string
            var indoption []int16
            if err := idefRows.Scan(&schemaName, &tableName, &idxName, &unique, &method, &nKey, &cols, &indoption, &predicate, &nullsNotDistinct, &reloptions, &constraint, &inherited, &clustered, &invalid, &tablespace); err != nil {
                return err
            }
            t := l.indexedRelation(fmt.Sprintf("%s.%s", schemaName, tableName))
            if t == nil || int(nKey) > len(cols) || int(nKey) > len(indoption) { continue }
            li := &LiveIndex{Unique: unique, Method: method, Keys: cols[:nKey], Include: cols[nKey:], Predicate: predicate, NullsNotDistinct: nullsNotDistinct, Options: parseProconfig(reloptions), Constraint: constraint, Inherited: inherited, Invalid: invalid, Tablespace: tablespace}
            for _, opt := range indoption[:nKey] {
                li.Descending = append(li.Descending, opt&1 != 0)
                li.NullsFirst = append(li.NullsFirst, opt&2 != 0)
            }
            t.IndexDefs[idxName] = li
            if clustered { t.ClusteredOn = idxName }
        }
        return nil
    })

    // Query existing triggers with their full definitions so redefined
    // triggers can be detected (skip internal triggers, e.g. FK enforcement)
    trgQ := `
        select n.nspname, c.relname, t.tgname, pg_get_triggerdef(t.oid), t.tgparentid <> 0, c.relkind = 'v',
               case t.tgenabled when 'D' then 'disabled' when 'R' then 'replica' when 'A' then 'always' else 'origin' end
        from pg_trigger t
        join pg_class c on c.oid = t.tgrelid
        join pg_namespace n on n.oid = c.relnamespace
        where not t.tgisinternal
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(trgQ, func(trgRows pgx.Rows) error {
        for trgRows.Next() {
            var schemaName, tableName, trgName, trgDef string
            var inherited, onView bool
            var enabled string
            if err := trgRows.Scan(&schemaName, &tableName, &trgName, &trgDef, &inherited, &onView, &enabled); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, tableName)
            t := l.Tables[key]
            if onView {
                if t = l.ViewTables[key]; t == nil { t = newLiveTable(); l.ViewTables[key] = t }
            }
            if t != nil {
                t.Triggers[trgName] = trgDef
                t.TriggerEnabled[trgName] = enabled
                if inherited { t.TriggerInherited[trgName] = true }
            }
        }
        return nil
    })

    inhQ := `
        select cn.nspname || '.' || c.relname, pn.nspname || '.' || p.relname
        from pg_inherits i
        join pg_class c on c.oid = i.inhrelid
        join pg_namespace cn on cn.oid = c.relnamespace
        join pg_class p on p.oid = i.inhparent
        join pg_namespace pn on pn.oid = p.relnamespace
        where not c.relispartition and c.relkind in ('r', 'p', 'f') and p.relkind in ('r', 'p', 'f')
        and cn.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        order by 1, i.inhseqno
    `
    batch.add(inhQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var child, parent string
            if err := rows.Scan(&child, &parent); err != nil { return err }
            if t := l.Tables[child]; t != nil { t.Inherits = append(t.Inherits, parent) }
        }
        return nil
    })

    partQ := `
        select pn.nspname || '.' || p.relname, cn.nspname || '.' || c.relname, coalesce(pg_get_expr(c.relpartbound, c.oid), '')
        from pg_inherits i
        join pg_class p on p.oid = i.inhparent
        join pg_namespace pn on pn.oid = p.relnamespace
        join pg_class c on c.oid = i.inhrelid
        join pg_namespace cn on cn.oid = c.relnamespace
        where p.relkind = 'p'
        and pn.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        order by 1, 2
    `
    batch.add(partQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var parent, child, bound string
            if err := rows.Scan(&parent, &child, &bound); err != nil { return err }
            l.Partitions[parent] = append(l.Partitions[parent], child)
            if bound != "" { l.PartitionBounds[child] = LivePartitionBound{Parent: parent, Bound: bound} }
        }
        return nil
    })

    aggQ := `
        select n.nspname, p.proname, pg_get_function_identity_arguments(p.oid),
               a.aggtransfn::text, format_type(a.aggtranstype, null), coalesce(nullif(a.aggfinalfn::text, '-'), ''),
               coalesce(a.agginitval, ''), coalesce(nullif(a.aggcombinefn::text, '-'), ''),
               case p.proparallel when 's' then 'safe' when 'r' then 'restricted' else 'unsafe' end,
               a.aggtransspace::text, a.aggfinalextra::text,
               case a.aggfinalmodify when 'r' then 'read_only' when 's' then 'shareable' else 'read_write' end,
               coalesce(nullif(a.aggserialfn::text, '-'), ''), coalesce(nullif(a.aggdeserialfn::text, '-'), ''),
               coalesce(nullif(a.aggmtransfn::text, '-'), ''), coalesce(nullif(a.aggminvtransfn::text, '-'), ''),
               case when a.aggmtranstype = 0 then '' else format_type(a.aggmtranstype, null) end, a.aggmtransspace::text,
               coalesce(nullif(a.aggmfinalfn::text, '-'), ''), a.aggmfinalextra::text,
               case a.aggmfinalmodify when 'r' then 'read_only' when 's' then 'shareable' else 'read_write' end,
               coalesce(a.aggminitval, ''), case when a.aggsortop = 0 then '' else a.aggsortop::regoper::text end,
               (a.aggkind = 'h')::text, a.agginitval is not null, a.aggminitval is not null
        from pg_aggregate a
        join pg_proc p on p.oid = a.aggfnoid
        join pg_namespace n on n.oid = p.pronamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(aggQ, func(aggRows pgx.Rows) error {
        for aggRows.Next() {
            var schemaName, name, args string
            la := &LiveAggregate{}
            opts := make([]string, len(schema.AggregateOptions))
            dest := []any{&schemaName, &name, &args, &la.SFunc, &la.SType, &la.FinalFunc, &la.InitCond, &la.CombineFunc, &la.Parallel}
            for i := range opts { dest = append(dest, &opts[i]) }
            dest = append(dest, &la.HasInitCond, &la.HasMInitCond)
            if err := aggRows.Scan(dest...); err != nil { return err }
            la.Options = map[string]string{}
            for i, o := range schema.AggregateOptions { la.Options[o[1]] = opts[i] }
            l.AggregateDefs[publicScope.functionSignature(fmt.Sprintf("%s.%s(%s)", schemaName, name, args))] = la
        }
        return nil
    })

    typeGrantQ := `
        select n.nspname || '.' || t.typname,
               case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end,
               lower(a.privilege_type) || case when a.is_grantable then '*' else '' end
        from pg_type t
        join pg_namespace n on n.oid = t.typnamespace
        cross join lateral aclexplode(t.typacl) a
        where (t.typtype in ('e', 'c', 'r', 'd', 'm') or t.typtype = 'b' and t.typcategory <> 'A')
        and (t.typrelid = 0 or (select c.relkind from pg_class c where c.oid = t.typrelid) = 'c')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        and a.grantee <> t.typowner
    `
    batch.add(typeGrantQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var key, role, priv string
            if err := rows.Scan(&key, &role, &priv); err != nil { return err }
            addLiveGrant(l.TypeGrants, key, role, priv)
        }
        return nil
    })
    typePublicQ := `
        select n.nspname || '.' || t.typname
        from pg_type t
        join pg_namespace n on n.oid = t.typnamespace
        where (t.typtype in ('e', 'c', 'r', 'd', 'm') or t.typtype = 'b' and t.typcategory <> 'A')
        and (t.typrelid = 0 or (select c.relkind from pg_class c where c.oid = t.typrelid) = 'c')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        and t.typacl is not null
        and not exists (select from aclexplode(t.typacl) a where a.grantee = 0 and a.privilege_type = 'USAGE')
    `
    batch.add(typePublicQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var key string
            if err := rows.Scan(&key); err != nil { return err }
            l.TypePublicRevoked[key] = true
        }
        return nil
    })

    statQ := `
        select n.nspname, c.relname, s.stxname, pg_get_statisticsobjdef(s.oid), coalesce(d.description, ''),
               coalesce(s.stxstattarget::int, -1)
        from pg_statistic_ext s
        join pg_class c on c.oid = s.stxrelid
        join pg_namespace n on n.oid = c.relnamespace
        left join pg_description d on d.objoid = s.oid and d.classoid = 'pg_statistic_ext'::regclass
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(statQ, func(statRows pgx.Rows) error {
        for statRows.Next() {
            var schemaName, tableName, name, def, comment string
            var target int
            if err := statRows.Scan(&schemaName, &tableName, &name, &def, &comment, &target); err != nil { return err }
            t := l.Tables[fmt.Sprintf("%s.%s", schemaName, tableName)]
            if t == nil { continue }
            ls := &LiveStatistic{Comment: comment, Target: target}
            if m := statisticsDefRe.FindStringSubmatch(def); m != nil {
                for _, k := range strings.Split(m[1], ",") {
                    if k = strings.TrimSpace(k); k != "" { ls.Kinds = append(ls.Kinds, k) }
                }
                ls.Items = splitTopLevel(m[2])
            }
            t.Statistics[name] = ls
        }
        return nil
    })

    ruleQ := `
        select n.nspname, c.relname, r.rulename, c.relkind = 'v',
               case r.ev_type when '1' then 'select' when '2' then 'update' when '3' then 'insert' else 'delete' end,
               r.is_instead, pg_get_ruledef(r.oid), coalesce(d.description, '')
        from pg_rewrite r
        join pg_class c on c.oid = r.ev_class
        join pg_namespace n on n.oid = c.relnamespace
        left join pg_description d on d.objoid = r.oid and d.classoid = 'pg_rewrite'::regclass
        where r.rulename <> '_RETURN' and c.relkind in ('r', 'p', 'v')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(ruleQ, func(ruleRows pgx.Rows) error {
        for ruleRows.Next() {
            var schemaName, relName, ruleName, def, comment string
            var onView bool
            lr := &LiveRule{}
            if err := ruleRows.Scan(&schemaName, &relName, &ruleName, &onView, &lr.Event, &lr.Instead, &def, &comment); err != nil {
                return err
            }
            if m := ruleWhereRe.FindStringSubmatch(def); m != nil { lr.Where = m[1] }
            lr.Def = def
            key := fmt.Sprintf("%s.%s", schemaName, relName)
            t := l.Tables[key]
            if onView {
                if t = l.ViewTables[key]; t == nil { t = newLiveTable(); l.ViewTables[key] = t }
            }
            if t == nil { continue }
            t.Rules[ruleName] = lr
            if comment != "" { t.RuleComments[ruleName] = comment }
        }
        return nil
    })

    // Query row level security state
    rlsQ := `
        select n.nspname, c.relname, c.relrowsecurity, c.relforcerowsecurity
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
        where c.relkind in ('r', 'p')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(rlsQ, func(rlsRows pgx.Rows) error {
        for rlsRows.Next() {
            var schemaName, tableName string
            var enabled, forced bool
            if err := rlsRows.Scan(&schemaName, &tableName, &enabled, &forced); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, tableName)
            if t := l.Tables[key]; t != nil {
                t.RLSEnabled, t.RLSForced = enabled, forced
            }
        }
        return nil
    })

    // Query existing policies, including their expressions and roles so a
    // redefined policy can be detected. polroles is '{0}' for PUBLIC (no TO
    // clause), which maps to an empty role list.
    polQ := `
        select n.nspname, c.relname, pol.polname, pol.polcmd::text, not pol.polpermissive,
               coalesce(
                   (select array_agg(r.rolname order by r.rolname)
                    from pg_roles r where r.oid = any(pol.polroles)),
                   '{}'
               ) as roles,
               coalesce(pg_get_expr(pol.polqual, pol.polrelid), ''),
               coalesce(pg_get_expr(pol.polwithcheck, pol.polrelid), '')
        from pg_policy pol
        join pg_class c on c.oid = pol.polrelid
        join pg_namespace n on n.oid = c.relnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(polQ, func(polRows pgx.Rows) error {
        for polRows.Next() {
            var schemaName, tableName, polName, polCmd string
            var roles []string
            var using, withCheck string
            var restrictive bool
            if err := polRows.Scan(&schemaName, &tableName, &polName, &polCmd, &restrictive, &roles, &using, &withCheck); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, tableName)
            if t := l.Tables[key]; t != nil {
                t.Policies[polName] = &LivePolicy{
                    Restrictive: restrictive,
                    Cmd:         expandPolicyCmd(polCmd),
                    Roles:       roles,
                    Using:       using,
                    WithCheck:   withCheck,
                }
            }
        }
        return nil
    })

    // Query existing types
    typeQ := `
        select n.nspname, t.typname, t.typtype in ('b', 'm')
        from pg_type t
        join pg_namespace n on n.oid = t.typnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        and (t.typtype in ('e', 'c', 'r', 'm') or t.typtype = 'b' and t.typcategory <> 'A' and t.typisdefined)
        and (t.typrelid = 0 or (select c.relkind from pg_class c where c.oid = t.typrelid) = 'c')
    `
    batch.add(typeQ, func(typeRows pgx.Rows) error {
        for typeRows.Next() {
            var schemaName, typeName string
            var base bool
            if err := typeRows.Scan(&schemaName, &typeName, &base); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, typeName)
            if base {
                l.BaseTypes[key] = true
                continue
            }
            l.Types[key] = true
        }
        return nil
    })
    
    // Query existing functions and procedures with definition details for
    // replace-on-change. prokind routes rows: 'p' = procedure, else function.
    funcQ := `
        select n.nspname, p.proname, pg_get_function_identity_arguments(p.oid) as args,
               case when p.prosqlbody is not null then pg_get_function_sqlbody(p.oid) else coalesce(p.prosrc, '') end,
               p.prosqlbody is not null, p.provolatile::text, p.prosecdef, p.proisstrict, p.proleakproof, p.prokind::text,
               coalesce(p.proconfig, '{}'::text[]),
               exists (select 1 from pg_depend d where d.classid = 'pg_proc'::regclass and d.objid = p.oid and d.deptype = 'i'),
               case p.proparallel when 's' then 'safe' when 'r' then 'restricted' else 'unsafe' end,
               p.procost::float8, p.prorows::float8,
               case when p.prokind = 'p' then '' else coalesce(pg_get_function_result(p.oid), '') end,
               coalesce(pg_get_function_arguments(p.oid), ''),
               lang.lanname, coalesce(p.protrftypes::regtype[]::text[], '{}'::text[])
        from pg_proc p
        join pg_namespace n on n.oid = p.pronamespace
        join pg_language lang on lang.oid = p.prolang
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(funcQ, func(funcRows pgx.Rows) error {
        for funcRows.Next() {
            var schemaName, funcName, args, src, vol, kind, parallel, returns, fullArgs, language string
            var secdef, strict, leakproof, internal, sqlBody bool
            var config, transform []string
            var cost, rows float64
            if err := funcRows.Scan(&schemaName, &funcName, &args, &src, &sqlBody, &vol, &secdef, &strict, &leakproof, &kind, &config, &internal, &parallel, &cost, &rows, &returns, &fullArgs, &language, &transform); err != nil {
                return err
            }
            src = routineSource(language, src)
            key := fmt.Sprintf("%s.%s(%s)", schemaName, funcName, args)
            if kind == "p" {
                norm := publicScope.functionSignature(key)
                l.Procedures[norm] = true
                lp := &LiveProcedure{Signature: key, Args: fullArgs, Body: src, SQLBody: sqlBody, Security: "invoker", Config: parseProconfig(config), Language: language, Transform: transform}
                if secdef { lp.Security = "definer" }
                l.ProcedureDefs[norm] = lp
                continue
            }
            l.Functions[key] = true
            lf := &LiveFunction{Signature: key, Kind: kind, Internal: internal, Body: src, Strict: strict, Leakproof: leakproof, Parallel: parallel, Cost: cost, Rows: rows, Security: "invoker", Volatility: "volatile", Config: parseProconfig(config), Returns: returns, SQLBody: sqlBody, Args: fullArgs, Language: language, Transform: transform}
            if secdef { lf.Security = "definer" }
            switch vol {
            case "i": lf.Volatility = "immutable"
            case "s": lf.Volatility = "stable"
            }
            l.FunctionDefs[publicScope.functionSignature(key)] = lf
        }
        return nil
    })

    // Query enum labels in sort order
    enumQ := `
        select n.nspname, t.typname, e.enumlabel
        from pg_enum e
        join pg_type t on t.oid = e.enumtypid
        join pg_namespace n on n.oid = t.typnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        order by t.oid, e.enumsortorder
    `
    batch.add(enumQ, func(enumRows pgx.Rows) error {
        for enumRows.Next() {
            var schemaName, typeName, label string
            if err := enumRows.Scan(&schemaName, &typeName, &label); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, typeName)
            l.EnumLabels[key] = append(l.EnumLabels[key], label)
        }
        return nil
    })
    
    serverQ := `
        select s.srvname, w.fdwname, coalesce(s.srvtype, ''), coalesce(s.srvversion, ''),
               coalesce(s.srvoptions, '{}'::text[]), pg_get_userbyid(s.srvowner),
               coalesce(obj_description(s.oid, 'pg_foreign_server'), ''),
               exists (select 1 from pg_user_mappings um where um.srvid = s.oid and (um.umuser = 0 or um.usename = current_user)),
               coalesce((select array_agg(case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end || '=' ||
                                          lower(a.privilege_type) || case when a.is_grantable then '*' else '' end)
                         from aclexplode(s.srvacl) a where a.grantee <> s.srvowner), '{}'::text[]),
               coalesce((select array_agg(um.usename::text order by um.usename) from pg_user_mappings um where um.srvid = s.oid), '{}'::text[])
        from pg_foreign_server s
        join pg_foreign_data_wrapper w on w.oid = s.srvfdw
        where not exists (select 1 from pg_depend d where d.classid = 'pg_foreign_server'::regclass and d.objid = s.oid and d.deptype = 'e')
    `
    batch.add(serverQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var name string
            var options, acl []string
            ls := &LiveServer{}
            if err := rows.Scan(&name, &ls.Wrapper, &ls.Type, &ls.Version, &options, &ls.Owner, &ls.Comment, &ls.UserMapping, &acl, &ls.UserMappings); err != nil { return err }
            ls.Options = parseFDWOptions(options)
            for _, e := range acl {
                if role, priv, ok := strings.Cut(e, "="); ok {
                    if ls.Grants == nil { ls.Grants = map[string]map[string]bool{} }
                    if ls.Grants[role] == nil { ls.Grants[role] = map[string]bool{} }
                    ls.Grants[role][priv] = true
                }
            }
            l.Servers[name] = ls
        }
        return nil
    })
    batch.add(wrapperQ, l.scanWrappers)
    batch.add(languageQ, l.scanLanguages)

    pubQ := `
        select p.pubname, p.puballtables, p.pubinsert, p.pubupdate, p.pubdelete, p.pubtruncate, p.pubviaroot,
               coalesce(d.description, ''),
               case to_jsonb(p) ->> 'pubgencols' when 's' then 'stored' when 'n' then 'none' else '' end,
               pg_get_userbyid(p.pubowner)
        from pg_publication p
        left join pg_description d on d.objoid = p.oid and d.classoid = 'pg_publication'::regclass
    `
    batch.add(pubQ, func(pubRows pgx.Rows) error {
        for pubRows.Next() {
            var name string
            var ins, upd, del, trunc bool
            lp := &LivePublication{Tables: map[string]LivePublicationTable{}}
            if err := pubRows.Scan(&name, &lp.AllTables, &ins, &upd, &del, &trunc, &lp.ViaRoot, &lp.Comment, &lp.GeneratedColumns, &lp.Owner); err != nil {
                return err
            }
            for _, a := range []struct{ on bool; name string }{{ins, "insert"}, {upd, "update"}, {del, "delete"}, {trunc, "truncate"}} {
                if a.on { lp.Publish = append(lp.Publish, a.name) }
            }
            l.Publications[name] = lp
        }
        return nil
    })

    // A table published without ONLY also publishes its inheritance
    // children, each with its own pg_publication_rel row; those are left
    // out, since declaring the parent again would publish them twice.
    pubRelQ := `
        select p.pubname, n.nspname || '.' || c.relname, coalesce(pg_get_expr(pr.prqual, pr.prrelid), ''),
               (select array_agg(a.attname::text order by a.attname) from pg_attribute a
                where a.attrelid = pr.prrelid and a.attnum = any(pr.prattrs)),
               c.relkind = 'r' and exists (select 1 from pg_inherits i where i.inhparent = pr.prrelid
                   and not exists (select 1 from pg_publication_rel x where x.prpubid = pr.prpubid and x.prrelid = i.inhrelid))
        from pg_publication_rel pr
        join pg_publication p on p.oid = pr.prpubid
        join pg_class c on c.oid = pr.prrelid
        join pg_namespace n on n.oid = c.relnamespace
        where not exists (
            select 1 from pg_inherits i
            join pg_publication_rel ppr on ppr.prrelid = i.inhparent and ppr.prpubid = pr.prpubid
            where i.inhrelid = pr.prrelid
            and coalesce(pg_get_expr(ppr.prqual, ppr.prrelid), '') = coalesce(pg_get_expr(pr.prqual, pr.prrelid), '')
            and coalesce((select array_agg(a.attname::text order by a.attname) from pg_attribute a where a.attrelid = ppr.prrelid and a.attnum = any(ppr.prattrs)), '{}')
              = coalesce((select array_agg(a.attname::text order by a.attname) from pg_attribute a where a.attrelid = pr.prrelid and a.attnum = any(pr.prattrs)), '{}'))
    `
    batch.add(pubRelQ, func(pubRelRows pgx.Rows) error {
        for pubRelRows.Next() {
            var pub, table, where string
            var cols []string
            var only bool
            if err := pubRelRows.Scan(&pub, &table, &where, &cols, &only); err != nil {
                return err
            }
            if lp := l.Publications[pub]; lp != nil { lp.Tables[table] = LivePublicationTable{Columns: cols, Where: where, Only: only} }
        }
        return nil
    })

    pubNspQ := `
        select p.pubname, n.nspname
        from pg_publication_namespace pn
        join pg_publication p on p.oid = pn.pnpubid
        join pg_namespace n on n.oid = pn.pnnspid
        order by 1, 2
    `
    batch.add(pubNspQ, func(pubNspRows pgx.Rows) error {
        for pubNspRows.Next() {
            var pub, nsp string
            if err := pubNspRows.Scan(&pub, &nsp); err != nil {
                return err
            }
            if lp := l.Publications[pub]; lp != nil { lp.Schemas = append(lp.Schemas, nsp) }
        }
        return nil
    })

    etQ := `
        select e.evtname, e.evtevent, n.nspname || '.' || p.proname,
               case e.evtenabled when 'D' then 'disabled' when 'R' then 'replica' when 'A' then 'always' else 'origin' end,
               coalesce(e.evttags, '{}'), coalesce(d.description, ''),
               exists (select 1 from pg_depend x where x.classid = 'pg_event_trigger'::regclass and x.objid = e.oid and x.deptype = 'e')
        from pg_event_trigger e
        join pg_proc p on p.oid = e.evtfoid
        join pg_namespace n on n.oid = p.pronamespace
        left join pg_description d on d.objoid = e.oid and d.classoid = 'pg_event_trigger'::regclass
    `
    batch.add(etQ, func(etRows pgx.Rows) error {
        for etRows.Next() {
            var name string
            et := &LiveEventTrigger{}
            if err := etRows.Scan(&name, &et.Event, &et.Function, &et.Enabled, &et.Tags, &et.Comment, &et.Extension); err != nil {
                return err
            }
            l.EventTriggers[name] = et
        }
        return nil
    })

    tsQ := `
        select 'config', n.nspname || '.' || c.cfgname, coalesce(d.description, '')
        from pg_ts_config c
        join pg_namespace n on n.oid = c.cfgnamespace
        left join pg_description d on d.objoid = c.oid and d.classoid = 'pg_ts_config'::regclass
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        union all
        select 'dict', n.nspname || '.' || t.dictname, coalesce(d.description, '')
        from pg_ts_dict t
        join pg_namespace n on n.oid = t.dictnamespace
        left join pg_description d on d.objoid = t.oid and d.classoid = 'pg_ts_dict'::regclass
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    batch.add(tsQ, func(tsRows pgx.Rows) error {
        for tsRows.Next() {
            var kind, key, comment string
            if err := tsRows.Scan(&kind, &key, &comment); err != nil {
                return err
            }
            if kind == "config" { l.TSConfigs[key] = map[string][]string{} } else { l.TSDictionaries[key] = true }
            if comment != "" { l.TSComments[kind+":"+key] = comment }
        }
        return nil
    })

    dictQ := `
        select n.nspname || '.' || t.dictname, tn.nspname || '.' || tp.tmplname, coalesce(t.dictinitoption, '')
        from pg_ts_dict t
        join pg_namespace n on n.oid = t.dictnamespace
        join pg_ts_template tp on tp.oid = t.dicttemplate
        join pg_namespace tn on tn.oid = tp.tmplnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    batch.add(dictQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var key, tmpl, opts string
            if err := rows.Scan(&key, &tmpl, &opts); err != nil { return err }
            l.TSDictionaryDefs[key] = &schema.TSDictionary{Template: strings.TrimPrefix(tmpl, "pg_catalog."), Options: parseDictOptions(opts)}
        }
        return nil
    })

    tsMapQ := `
        select n.nspname || '.' || c.cfgname, t.alias,
               array_agg(case when dn.nspname = 'pg_catalog' then dd.dictname::text else dn.nspname || '.' || dd.dictname end order by m.mapseqno)
        from pg_ts_config_map m
        join pg_ts_config c on c.oid = m.mapcfg
        join pg_namespace n on n.oid = c.cfgnamespace
        cross join lateral ts_token_type(c.cfgparser) t
        join pg_ts_dict dd on dd.oid = m.mapdict
        join pg_namespace dn on dn.oid = dd.dictnamespace
        where t.tokid = m.maptokentype
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        group by 1, 2
    `
    batch.add(tsMapQ, func(tsMapRows pgx.Rows) error {
        for tsMapRows.Next() {
            var cfg, alias string
            var dicts []string
            if err := tsMapRows.Scan(&cfg, &alias, &dicts); err != nil {
                return err
            }
            if l.TSConfigs[cfg] != nil { l.TSConfigs[cfg][alias] = dicts }
        }
        return nil
    })

    collQ := `
        select n.nspname, c.collname, coalesce(d.description, '')
        from pg_collation c
        join pg_namespace n on n.oid = c.collnamespace
        left join pg_description d on d.objoid = c.oid and d.classoid = 'pg_collation'::regclass
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(collQ, func(collRows pgx.Rows) error {
        for collRows.Next() {
            var schemaName, collName, comment string
            if err := collRows.Scan(&schemaName, &collName, &comment); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, collName)
            l.Collations[key] = true
            if comment != "" { l.CollationComments[key] = comment }
        }
        return nil
    })

    attrQ := `
        select n.nspname, t.typname, a.attname, format_type(a.atttypid, a.atttypmod),
               coalesce((select case when cn.nspname = 'pg_catalog' then '' else cn.nspname || '.' end || case when strpos(co.collname, '.') > 0 then quote_ident(co.collname) else co.collname::text end
                         from pg_collation co join pg_namespace cn on cn.oid = co.collnamespace join pg_type ty on ty.oid = a.atttypid
                         where co.oid = a.attcollation and a.attcollation <> ty.typcollation), ''),
               coalesce(col_description(c.oid, a.attnum), '')
        from pg_type t
        join pg_namespace n on n.oid = t.typnamespace
        join pg_class c on c.oid = t.typrelid and c.relkind = 'c'
        join pg_attribute a on a.attrelid = c.oid and a.attnum > 0 and not a.attisdropped
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(attrQ, func(attrRows pgx.Rows) error {
        for attrRows.Next() {
            var schemaName, typeName, attName, attType, collation, attComment string
            if err := attrRows.Scan(&schemaName, &typeName, &attName, &attType, &collation, &attComment); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, typeName)
            if l.CompositeAttributes[key] == nil { l.CompositeAttributes[key] = map[string]string{} }
            l.CompositeAttributes[key][attName] = attType
            if collation != "" {
                if l.CompositeCollations[key] == nil { l.CompositeCollations[key] = map[string]string{} }
                l.CompositeCollations[key][attName] = collation
            }
            if attComment != "" {
                if l.CompositeComments[key] == nil { l.CompositeComments[key] = map[string]string{} }
                l.CompositeComments[key][attName] = attComment
            }
        }
        return nil
    })

    // Query existing domains
    domQ := `
        select n.nspname, t.typname,
               coalesce(pg_get_expr(t.typdefaultbin, 0), t.typdefault, ''), t.typnotnull,
               coalesce((select array_agg(c.conname order by c.conname) from pg_constraint c where c.contypid = t.oid and c.contype = 'c'), '{}'),
               coalesce((select array_agg(pg_get_constraintdef(c.oid) order by c.conname) from pg_constraint c where c.contypid = t.oid and c.contype = 'c'), '{}'),
               coalesce((select array_agg(coalesce(obj_description(c.oid, 'pg_constraint'), '') order by c.conname) from pg_constraint c where c.contypid = t.oid and c.contype = 'c'), '{}'),
               format_type(t.typbasetype, t.typtypmod),
               coalesce((select c.conname::text from pg_constraint c where c.contypid = t.oid and c.contype = 'n' limit 1), '')
        from pg_type t
        join pg_namespace n on n.oid = t.typnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        and t.typtype = 'd'
    `
    batch.add(domQ, func(domRows pgx.Rows) error {
        for domRows.Next() {
            var schemaName, domName, def, baseType, notNullName string
            var notNull bool
            var conNames, conDefs, conComments []string
            if err := domRows.Scan(&schemaName, &domName, &def, &notNull, &conNames, &conDefs, &conComments, &baseType, &notNullName); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, domName)
            l.Domains[key] = true
            ld := &LiveDomain{Type: baseType, NotNullName: notNullName, Default: def, NotNull: notNull, Checks: map[string]string{}, CheckComments: map[string]string{}}
            for i := range conNames {
                if i < len(conDefs) { ld.Checks[conNames[i]] = conDefs[i] }
                if i < len(conComments) && conComments[i] != "" { ld.CheckComments[conNames[i]] = conComments[i] }
            }
            l.DomainDefs[key] = ld
        }
        return nil
    })

    // Query existing extensions
    extQ := `
        select e.extname, n.nspname, e.extversion, e.extrelocatable,
               coalesce(obj_description(e.oid, 'pg_extension'), ''),
               coalesce(a.comment, ''), coalesce(a.default_version, ''),
               array(select r.extname::text from pg_depend d join pg_extension r on r.oid = d.refobjid
                     where d.classid = 'pg_extension'::regclass and d.objid = e.oid
                     and d.refclassid = 'pg_extension'::regclass order by 1)
        from pg_extension e
        join pg_namespace n on n.oid = e.extnamespace
        left join pg_available_extensions a on a.name = e.extname
    `
    batch.add(extQ, func(extRows pgx.Rows) error {
        for extRows.Next() {
            var extName string
            le := &LiveExtension{}
            if err := extRows.Scan(&extName, &le.Schema, &le.Version, &le.Relocatable, &le.Comment, &le.DefaultComment, &le.DefaultVersion, &le.Requires); err != nil {
                return err
            }
            l.Extensions[extName] = true
            l.ExtensionDefs[extName] = le
        }
        return nil
    })

    // Query existing views
    viewQ := `
        select n.nspname, c.relname, coalesce(c.reloptions, '{}'::text[])
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
        where c.relkind = 'v'
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(viewQ, func(viewRows pgx.Rows) error {
        for viewRows.Next() {
            var schemaName, viewName string
            var reloptions []string
            if err := viewRows.Scan(&schemaName, &viewName, &reloptions); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, viewName)
            l.Views[key] = true
            l.ViewOptions[key] = parseProconfig(reloptions)
        }
        return nil
    })

    // Query existing sequences (includes serial/identity-owned sequences,
    // which is fine: presence only guards against duplicate CREATE SEQUENCE)
    seqQ := `
        select n.nspname, c.relname, format_type(s.seqtypid, null), s.seqincrement::text, s.seqmin::text,
               s.seqmax::text, s.seqstart::text, s.seqcache::text, s.seqcycle, c.relpersistence = 'u',
               coalesce((select tn.nspname || '.' || t.relname || '.' || a.attname
                         from pg_depend d
                         join pg_class t on t.oid = d.refobjid
                         join pg_namespace tn on tn.oid = t.relnamespace
                         join pg_attribute a on a.attrelid = t.oid and a.attnum = d.refobjsubid
                         where d.classid = 'pg_class'::regclass and d.objid = c.oid
                         and d.refclassid = 'pg_class'::regclass and d.refobjsubid > 0 and d.deptype = 'a'
                         limit 1), ''),
               coalesce(idn.nspname, ''), coalesce(idt.relname, ''), coalesce(ida.attname, '')
        from pg_class c
        left join pg_depend idd on idd.classid = 'pg_class'::regclass and idd.objid = c.oid
             and idd.refclassid = 'pg_class'::regclass and idd.refobjsubid > 0 and idd.deptype = 'i'
        left join pg_class idt on idt.oid = idd.refobjid
        left join pg_namespace idn on idn.oid = idt.relnamespace
        left join pg_attribute ida on ida.attrelid = idt.oid and ida.attnum = idd.refobjsubid
        join pg_namespace n on n.oid = c.relnamespace
        join pg_sequence s on s.seqrelid = c.oid
        where c.relkind = 'S'
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(seqQ, func(seqRows pgx.Rows) error {
        for seqRows.Next() {
            var schemaName, seqName, idSchema, idTable, idColumn string
            ls := &LiveSequence{}
            if err := seqRows.Scan(&schemaName, &seqName, &ls.Type, &ls.Increment, &ls.MinValue, &ls.MaxValue, &ls.Start, &ls.Cache, &ls.Cycle, &ls.Unlogged, &ls.Owner, &idSchema, &idTable, &idColumn); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, seqName)
            ls.Name = key
            l.Sequences[key] = true
            l.SequenceDefs[key] = ls
            if t := l.Tables[idSchema+"."+idTable]; t != nil && idColumn != "" {
                if lc := t.Columns[idColumn]; lc != nil { lc.IdentitySequence = ls }
            }
        }
        return nil
    })

    // Query relation grants (explicit ACL entries, owner excluded).
    // Views ('v') and materialized views ('m') share pg_class.relacl with
    // tables, so their grants land in the same TableGrants map.
    tgQ := `
        select n.nspname, c.relname,
               case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end,
               lower(a.privilege_type) || case when a.is_grantable then '*' else '' end
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
        cross join lateral aclexplode(c.relacl) a
        where c.relkind in ('r', 'p', 'v', 'm', 'S', 'f')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        and a.grantee <> c.relowner
    `
    batch.add(tgQ, func(tgRows pgx.Rows) error {
        for tgRows.Next() {
            var schemaName, tableName, role, priv string
            if err := tgRows.Scan(&schemaName, &tableName, &role, &priv); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, tableName)
            addLiveGrant(l.TableGrants, key, role, priv)
        }
        return nil
    })

    // Query column grants (explicit column ACL entries, owner excluded)
    cgQ := `
        select n.nspname, c.relname, att.attname,
               case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end,
               lower(a.privilege_type) || case when a.is_grantable then '*' else '' end
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
        join pg_attribute att on att.attrelid = c.oid and att.attnum > 0 and not att.attisdropped
        cross join lateral aclexplode(att.attacl) a
        where c.relkind in ('r', 'p', 'f', 'v', 'm')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        and a.grantee <> c.relowner
    `
    batch.add(cgQ, func(cgRows pgx.Rows) error {
        for cgRows.Next() {
            var schemaName, tableName, colName, role, priv string
            if err := cgRows.Scan(&schemaName, &tableName, &colName, &role, &priv); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, tableName)
            if l.ColumnGrants[key] == nil { l.ColumnGrants[key] = map[string]map[string]map[string]bool{} }
            addLiveGrant(l.ColumnGrants[key], colName, role, priv)
        }
        return nil
    })

    // Query function grants; left join keeps NULL-acl functions (default privileges,
    // meaning PUBLIC still has EXECUTE) visible for FunctionPublicExec.
    fgQ := `
        select n.nspname, p.proname, pg_get_function_identity_arguments(p.oid),
               coalesce(case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end, ''),
               coalesce(lower(a.privilege_type) || case when a.is_grantable then '*' else '' end, ''),
               p.proacl is null,
               coalesce(a.grantee = p.proowner, false)
        from pg_proc p
        join pg_namespace n on n.oid = p.pronamespace
        left join lateral aclexplode(p.proacl) a on true
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(fgQ, func(fgRows pgx.Rows) error {
        for fgRows.Next() {
            var schemaName, funcName, args, role, priv string
            var aclNull, isOwner bool
            if err := fgRows.Scan(&schemaName, &funcName, &args, &role, &priv, &aclNull, &isOwner); err != nil {
                return err
            }
            key := publicScope.functionSignature(fmt.Sprintf("%s.%s(%s)", schemaName, funcName, args))
            if aclNull || (role == "public" && priv == "execute") {
                l.FunctionPublicExec[key] = true
            }
            if role == "" || isOwner { continue }
            addLiveGrant(l.FunctionGrants, key, role, priv)
        }
        return nil
    })

    // Query schema grants (owner excluded)
    sgQ := `
        select n.nspname,
               case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end,
               lower(a.privilege_type) || case when a.is_grantable then '*' else '' end
        from pg_namespace n
        cross join lateral aclexplode(n.nspacl) a
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        and a.grantee <> n.nspowner
    `
    batch.add(sgQ, func(sgRows pgx.Rows) error {
        for sgRows.Next() {
            var schemaName, role, priv string
            if err := sgRows.Scan(&schemaName, &role, &priv); err != nil {
                return err
            }
            addLiveGrant(l.SchemaGrants, schemaName, role, priv)
        }
        return nil
    })

    // Query existing roles (skip pg_* system roles)
    roleQ := `
        select rolname, rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls, rolinherit, rolconnlimit,
               case when rolvaliduntil is null then '' when rolvaliduntil = 'infinity' then 'infinity'
                    else to_char(rolvaliduntil at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') end,
               coalesce((select s.setconfig from pg_db_role_setting s where s.setrole = r.oid and s.setdatabase = 0), '{}'::text[])
        from pg_roles r where rolname not like 'pg\_%'
    `
    batch.add(roleQ, func(roleRows pgx.Rows) error {
        for roleRows.Next() {
            var roleName string
            lr := &LiveRole{}
            var config []string
            if err := roleRows.Scan(&roleName, &lr.Login, &lr.Superuser, &lr.CreateDB, &lr.CreateRole, &lr.Replication, &lr.BypassRLS, &lr.Inherit, &lr.ConnectionLimit, &lr.ValidUntil, &config); err != nil {
                return err
            }
            lr.Config = parseProconfig(config)
            l.Roles[roleName] = true
            l.RoleDefs[roleName] = lr
        }
        return nil
    })

    // Query role memberships
    memQ := `
        select m.rolname, r.rolname, bool_or(am.admin_option),
               bool_or(coalesce((to_jsonb(am) ->> 'inherit_option')::boolean, m.rolinherit)),
               bool_or(coalesce((to_jsonb(am) ->> 'set_option')::boolean, true)),
               array_agg(distinct coalesce(pg_get_userbyid(am.grantor), '') order by coalesce(pg_get_userbyid(am.grantor), ''))
        from pg_auth_members am
        join pg_roles m on m.oid = am.member
        join pg_roles r on r.oid = am.roleid
        where m.rolname not like 'pg\_%'
        group by 1, 2
    `
    colDepQ := `
        select distinct n.nspname || '.' || c.relname || '.' || a.attname,
               case when d.classid = 'pg_trigger'::regclass then 'trigger'
                    when d.classid = 'pg_policy'::regclass then 'policy'
                    when d.classid = 'pg_attrdef'::regclass then 'generated'
                    when ev.relkind = 'v' then 'view' when ev.relkind = 'm' then 'matview' else 'rule' end,
               case when d.classid = 'pg_trigger'::regclass then tg.tgname::text
                    when d.classid = 'pg_policy'::regclass then po.polname::text
                    when d.classid = 'pg_attrdef'::regclass then ga.attname::text
                    when ev.relkind in ('v', 'm') then evn.nspname || '.' || ev.relname
                    else rw.rulename::text end,
               case when d.classid = 'pg_trigger'::regclass then tgn.nspname || '.' || tgc.relname
                    when d.classid = 'pg_policy'::regclass then pon.nspname || '.' || poc.relname
                    when d.classid = 'pg_attrdef'::regclass then n.nspname || '.' || c.relname
                    else evn.nspname || '.' || ev.relname end
        from pg_depend d
        join pg_class c on c.oid = d.refobjid
        join pg_namespace n on n.oid = c.relnamespace
        join pg_attribute a on a.attrelid = c.oid and a.attnum = d.refobjsubid
        left join pg_rewrite rw on d.classid = 'pg_rewrite'::regclass and rw.oid = d.objid
        left join pg_class ev on ev.oid = rw.ev_class
        left join pg_namespace evn on evn.oid = ev.relnamespace
        left join pg_trigger tg on d.classid = 'pg_trigger'::regclass and tg.oid = d.objid
        left join pg_class tgc on tgc.oid = tg.tgrelid
        left join pg_namespace tgn on tgn.oid = tgc.relnamespace
        left join pg_policy po on d.classid = 'pg_policy'::regclass and po.oid = d.objid
        left join pg_class poc on poc.oid = po.polrelid
        left join pg_namespace pon on pon.oid = poc.relnamespace
        left join pg_attrdef ad on d.classid = 'pg_attrdef'::regclass and ad.oid = d.objid
        left join pg_attribute ga on ga.attrelid = ad.adrelid and ga.attnum = ad.adnum
        where d.refclassid = 'pg_class'::regclass and d.refobjsubid > 0 and d.deptype = 'n'
        and d.classid in ('pg_rewrite'::regclass, 'pg_trigger'::regclass, 'pg_policy'::regclass, 'pg_attrdef'::regclass)
        and not (d.classid = 'pg_rewrite'::regclass and ev.oid = c.oid and ev.relkind in ('v', 'm'))
        and not (d.classid = 'pg_attrdef'::regclass and (ga.attgenerated = '' or ad.adrelid <> c.oid))
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(colDepQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var col string
            var dep ColumnDependent
            if err := rows.Scan(&col, &dep.Kind, &dep.Name, &dep.Table); err != nil { return err }
            l.ColumnDependents[col] = append(l.ColumnDependents[col], dep)
        }
        return nil
    })

    viewDepQ := `
        select distinct rn.nspname || '.' || r.relname, vn.nspname || '.' || v.relname
        from pg_depend d
        join pg_rewrite rw on d.classid = 'pg_rewrite'::regclass and rw.oid = d.objid
        join pg_class v on v.oid = rw.ev_class
        join pg_namespace vn on vn.oid = v.relnamespace
        join pg_class r on r.oid = d.refobjid
        join pg_namespace rn on rn.oid = r.relnamespace
        where d.refclassid = 'pg_class'::regclass and d.deptype = 'n'
        and v.oid <> r.oid and v.relkind in ('v', 'm') and r.relkind in ('v', 'm')
        and vn.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and vn.nspname !~ '^pg_(toast_)?temp_'
        order by 1, 2
    `
    batch.add(viewDepQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var ref, dep string
            if err := rows.Scan(&ref, &dep); err != nil { return err }
            l.ViewDependents[ref] = append(l.ViewDependents[ref], dep)
        }
        return nil
    })

    batch.add(memQ, func(memRows pgx.Rows) error {
        for memRows.Next() {
            var member, parent string
            lm := &LiveMembership{}
            if err := memRows.Scan(&member, &parent, &lm.Admin, &lm.Inherit, &lm.Set, &lm.Grantors); err != nil {
                return err
            }
            if l.RoleMembers[member] == nil { l.RoleMembers[member] = map[string]bool{} }
            l.RoleMembers[member][parent] = true
            if l.RoleMemberOptions[member] == nil { l.RoleMemberOptions[member] = map[string]*LiveMembership{} }
            l.RoleMemberOptions[member][parent] = lm
        }
        return nil
    })

    // Query role comments (shared catalog)
    rcmQ := `
        select r.rolname, d.description
        from pg_shdescription d
        join pg_roles r on r.oid = d.objoid
        where d.classoid = 'pg_authid'::regclass
    `
    batch.add(rcmQ, func(rcmRows pgx.Rows) error {
        for rcmRows.Next() {
            var roleName, comment string
            if err := rcmRows.Scan(&roleName, &comment); err != nil {
                return err
            }
            l.RoleComments[roleName] = comment
        }
        return nil
    })

    // Query comments on relations (tables, views, matviews)
    rcQ := `
        select n.nspname, c.relname, d.description
        from pg_description d
        join pg_class c on c.oid = d.objoid
        join pg_namespace n on n.oid = c.relnamespace
        where d.classoid = 'pg_class'::regclass and d.objsubid = 0
        and c.relkind in ('r', 'p', 'v', 'm', 'S', 'f')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(rcQ, func(rcRows pgx.Rows) error {
        for rcRows.Next() {
            var schemaName, relName, comment string
            if err := rcRows.Scan(&schemaName, &relName, &comment); err != nil {
                return err
            }
            l.RelComments[fmt.Sprintf("%s.%s", schemaName, relName)] = comment
        }
        return nil
    })

    viewDefaultQ := `
        select n.nspname || '.' || c.relname, a.attname, pg_get_expr(d.adbin, d.adrelid)
        from pg_attrdef d
        join pg_class c on c.oid = d.adrelid
        join pg_namespace n on n.oid = c.relnamespace
        join pg_attribute a on a.attrelid = d.adrelid and a.attnum = d.adnum
        where c.relkind = 'v'
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(viewDefaultQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var view, col, def string
            if err := rows.Scan(&view, &col, &def); err != nil { return err }
            if l.ViewColumnDefaults[view] == nil { l.ViewColumnDefaults[view] = map[string]string{} }
            l.ViewColumnDefaults[view][col] = def
        }
        return nil
    })

    // Query column comments
    ccQ := `
        select n.nspname, c.relname, a.attname, d.description, c.relkind in ('v', 'm')
        from pg_description d
        join pg_class c on c.oid = d.objoid
        join pg_namespace n on n.oid = c.relnamespace
        join pg_attribute a on a.attrelid = c.oid and a.attnum = d.objsubid
        where d.classoid = 'pg_class'::regclass and d.objsubid > 0
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(ccQ, func(ccRows pgx.Rows) error {
        for ccRows.Next() {
            var schemaName, relName, colName, comment string
            var onView bool
            if err := ccRows.Scan(&schemaName, &relName, &colName, &comment, &onView); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, relName)
            if t := l.Tables[key]; t != nil {
                if c := t.Columns[colName]; c != nil { c.Comment = comment }
            } else if onView {
                if l.ViewColumnComments[key] == nil { l.ViewColumnComments[key] = map[string]string{} }
                l.ViewColumnComments[key][colName] = comment
            }
        }
        return nil
    })

    // Query function comments
    fcQ := `
        select n.nspname, p.proname, pg_get_function_identity_arguments(p.oid), d.description
        from pg_description d
        join pg_proc p on p.oid = d.objoid
        join pg_namespace n on n.oid = p.pronamespace
        where d.classoid = 'pg_proc'::regclass
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(fcQ, func(fcRows pgx.Rows) error {
        for fcRows.Next() {
            var schemaName, funcName, args, comment string
            if err := fcRows.Scan(&schemaName, &funcName, &args, &comment); err != nil {
                return err
            }
            key := publicScope.functionSignature(fmt.Sprintf("%s.%s(%s)", schemaName, funcName, args))
            l.FunctionComments[key] = comment
        }
        return nil
    })

    // Query type comments
    tcQ := `
        select n.nspname, t.typname, d.description
        from pg_description d
        join pg_type t on t.oid = d.objoid
        join pg_namespace n on n.oid = t.typnamespace
        where d.classoid = 'pg_type'::regclass
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(tcQ, func(tcRows pgx.Rows) error {
        for tcRows.Next() {
            var schemaName, typeName, comment string
            if err := tcRows.Scan(&schemaName, &typeName, &comment); err != nil {
                return err
            }
            l.TypeComments[fmt.Sprintf("%s.%s", schemaName, typeName)] = comment
        }
        return nil
    })

    // Query schema comments
    scQ := `
        select n.nspname, d.description
        from pg_description d
        join pg_namespace n on n.oid = d.objoid
        where d.classoid = 'pg_namespace'::regclass
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(scQ, func(scRows pgx.Rows) error {
        for scRows.Next() {
            var schemaName, comment string
            if err := scRows.Scan(&schemaName, &comment); err != nil {
                return err
            }
            l.SchemaComments[schemaName] = comment
        }
        return nil
    })

    // Query index comments (pg_description keyed by the index's pg_class oid)
    icQ := `
        select n.nspname, t.relname, i.relname, d.description
        from pg_description d
        join pg_class i on i.oid = d.objoid and d.classoid = 'pg_class'::regclass
        join pg_index ix on ix.indexrelid = i.oid
        join pg_class t on t.oid = ix.indrelid
        join pg_namespace n on n.oid = t.relnamespace
        where d.objsubid = 0
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(icQ, func(icRows pgx.Rows) error {
        for icRows.Next() {
            var schemaName, tableName, idxName, comment string
            if err := icRows.Scan(&schemaName, &tableName, &idxName, &comment); err != nil {
                return err
            }
            if t := l.indexedRelation(fmt.Sprintf("%s.%s", schemaName, tableName)); t != nil {
                t.IndexComments[idxName] = comment
            }
        }
        return nil
    })

    // Query trigger and policy comments
    tpcQ := `
        select 'trigger', n.nspname, c.relname, t.tgname, d.description
        from pg_description d
        join pg_trigger t on t.oid = d.objoid and d.classoid = 'pg_trigger'::regclass
        join pg_class c on c.oid = t.tgrelid
        join pg_namespace n on n.oid = c.relnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        union all
        select 'policy', n.nspname, c.relname, p.polname, d.description
        from pg_description d
        join pg_policy p on p.oid = d.objoid and d.classoid = 'pg_policy'::regclass
        join pg_class c on c.oid = p.polrelid
        join pg_namespace n on n.oid = c.relnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
    `
    batch.add(tpcQ, func(tpcRows pgx.Rows) error {
        for tpcRows.Next() {
            var kind, schemaName, tableName, name, comment string
            if err := tpcRows.Scan(&kind, &schemaName, &tableName, &name, &comment); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, tableName)
            t := l.Tables[key]
            if t == nil && kind == "trigger" { t = l.ViewTables[key] }
            if t != nil {
                if kind == "trigger" { t.TriggerComments[name] = comment } else { t.PolicyComments[name] = comment }
            }
        }
        return nil
    })

    // Query constraint comments (pg_description keyed by pg_constraint oid)
    cncQ := `
        select n.nspname, c.relname, con.conname, d.description
        from pg_description d
        join pg_constraint con on con.oid = d.objoid and d.classoid = 'pg_constraint'::regclass
        join pg_class c on c.oid = con.conrelid
        join pg_namespace n on n.oid = c.relnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(cncQ, func(cncRows pgx.Rows) error {
        for cncRows.Next() {
            var schemaName, tableName, conName, comment string
            if err := cncRows.Scan(&schemaName, &tableName, &conName, &comment); err != nil {
                return err
            }
            if t := l.Tables[fmt.Sprintf("%s.%s", schemaName, tableName)]; t != nil {
                t.ConstraintComments[conName] = comment
            }
        }
        return nil
    })

    ownerQ := `
        select 'rel:' || n.nspname || '.' || c.relname, pg_get_userbyid(c.relowner)
        from pg_class c join pg_namespace n on n.oid = c.relnamespace
        where c.relkind in ('r', 'p', 'v', 'm', 'S', 'f')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast') and n.nspname !~ '^pg_(toast_)?temp_'
        union all
        select 'type:' || n.nspname || '.' || t.typname, pg_get_userbyid(t.typowner)
        from pg_type t join pg_namespace n on n.oid = t.typnamespace
        where (t.typtype in ('e', 'c', 'r', 'd', 'm') or t.typtype = 'b' and t.typcategory <> 'A')
        and (t.typrelid = 0 or (select c.relkind from pg_class c where c.oid = t.typrelid) = 'c')
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast') and n.nspname !~ '^pg_(toast_)?temp_'
        union all
        select 'schema:' || n.nspname, pg_get_userbyid(n.nspowner)
        from pg_namespace n
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast') and n.nspname !~ '^pg_(toast_)?temp_'
        union all
        select 'proc:' || n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', pg_get_userbyid(p.proowner)
        from pg_proc p join pg_namespace n on n.oid = p.pronamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast') and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(ownerQ, func(ownerRows pgx.Rows) error {
        for ownerRows.Next() {
            var key, owner string
            if err := ownerRows.Scan(&key, &owner); err != nil {
                return err
            }
            if sig, ok := strings.CutPrefix(key, "proc:"); ok { key = "proc:" + publicScope.functionSignature(sig) }
            l.Owners[key] = owner
        }
        return nil
    })

    dAclQ := `
        select pg_get_userbyid(d.defaclrole), coalesce(n.nspname, ''),
               case d.defaclobjtype when 'r' then 'tables' when 'S' then 'sequences' when 'f' then 'functions'
                    when 'T' then 'types' when 'n' then 'schemas' else d.defaclobjtype::text end,
               coalesce(case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end, ''),
               coalesce(lower(a.privilege_type) || case when a.is_grantable then '*' else '' end, ''), coalesce(a.grantee = d.defaclrole, false)
        from pg_default_acl d
        left join pg_namespace n on n.oid = d.defaclnamespace
        left join lateral aclexplode(d.defaclacl) a on true
    `
    batch.add(dAclQ, func(dAclRows pgx.Rows) error {
        for dAclRows.Next() {
            var role, nsp, kind, grantee, priv string
            var isOwner bool
            if err := dAclRows.Scan(&role, &nsp, &kind, &grantee, &priv, &isOwner); err != nil {
                return err
            }
            key := role + "|" + nsp + "|" + kind
            l.DefaultACLRows[key] = true
            if grantee == "" || isOwner { continue }
            addLiveGrant(l.DefaultACLs, key, grantee, priv)
        }
        return nil
    })
    dbRoleQ := `
        select pg_get_userbyid(s.setrole), s.setconfig
        from pg_db_role_setting s join pg_database d on d.oid = s.setdatabase
        where d.datname = current_database() and s.setrole <> 0
    `
    batch.add(dbRoleQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var role string
            var config []string
            if err := rows.Scan(&role, &config); err != nil { return err }
            if l.DatabaseRoleConfig == nil { l.DatabaseRoleConfig = map[string]map[string]string{} }
            l.DatabaseRoleConfig[role] = parseProconfig(config)
        }
        return nil
    })
    batch.add(`select current_user, current_setting('server_version_num')::int,
                      coalesce((select s.setconfig from pg_db_role_setting s join pg_database d on d.oid = s.setdatabase
                                where d.datname = current_database() and s.setrole = 0), '{}'::text[]),
                      coalesce((select t.spcname::text from pg_database d join pg_tablespace t on t.oid = d.dattablespace where d.datname = current_database()), '')`, func(rows pgx.Rows) error {
        for rows.Next() {
            var config []string
            if err := rows.Scan(&l.CurrentUser, &l.ServerVersion, &config, &l.DefaultTablespace); err != nil { return err }
            l.DatabaseConfig = parseProconfig(config)
        }
        return nil
    })

    extMemQ := `
        select m.key, x.extname::text
        from (
        select 'rel:' || n.nspname || '.' || c.relname, d.refobjid
        from pg_depend d join pg_class c on c.oid = d.objid join pg_namespace n on n.oid = c.relnamespace
        where d.deptype = 'e' and d.classid = 'pg_class'::regclass
        union all
        select 'type:' || n.nspname || '.' || t.typname, d.refobjid
        from pg_depend d join pg_type t on t.oid = d.objid join pg_namespace n on n.oid = t.typnamespace
        where d.deptype = 'e' and d.classid = 'pg_type'::regclass
        union all
        select 'tscfg:' || n.nspname || '.' || c.cfgname, d.refobjid
        from pg_depend d join pg_ts_config c on c.oid = d.objid join pg_namespace n on n.oid = c.cfgnamespace
        where d.deptype = 'e' and d.classid = 'pg_ts_config'::regclass
        union all
        select 'tsdict:' || n.nspname || '.' || c.dictname, d.refobjid
        from pg_depend d join pg_ts_dict c on c.oid = d.objid join pg_namespace n on n.oid = c.dictnamespace
        where d.deptype = 'e' and d.classid = 'pg_ts_dict'::regclass
        union all
        select 'coll:' || n.nspname || '.' || c.collname, d.refobjid
        from pg_depend d join pg_collation c on c.oid = d.objid join pg_namespace n on n.oid = c.collnamespace
        where d.deptype = 'e' and d.classid = 'pg_collation'::regclass
        union all
        select 'proc:' || n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', d.refobjid
        from pg_depend d join pg_proc p on p.oid = d.objid join pg_namespace n on n.oid = p.pronamespace
        where d.deptype = 'e' and d.classid = 'pg_proc'::regclass
        union all
        select 'language:' || l.lanname, d.refobjid
        from pg_depend d join pg_language l on l.oid = d.objid
        where d.deptype = 'e' and d.classid = 'pg_language'::regclass
        union all
        select 'fdw:' || w.fdwname, d.refobjid
        from pg_depend d join pg_foreign_data_wrapper w on w.oid = d.objid
        where d.deptype = 'e' and d.classid = 'pg_foreign_data_wrapper'::regclass
        ) m(key, extension)
        join pg_extension x on x.oid = m.extension
    `
    batch.add(extMemQ, func(extMemRows pgx.Rows) error {
        for extMemRows.Next() {
            var key, extension string
            if err := extMemRows.Scan(&key, &extension); err != nil {
                return err
            }
            if sig, ok := strings.CutPrefix(key, "proc:"); ok { key = "proc:" + publicScope.functionSignature(sig) }
            l.ExtensionMembers[key] = true
            l.MemberExtension[key] = extension
        }
        return nil
    })
    batch.add(initPrivsQ, l.scanInitPrivs)

    ownedQ := `
        select n.nspname || '.' || s.relname
        from pg_depend d
        join pg_class s on s.oid = d.objid and s.relkind = 'S'
        join pg_namespace n on n.oid = s.relnamespace
        where d.classid = 'pg_class'::regclass and d.refclassid = 'pg_class'::regclass
        and d.refobjsubid > 0 and d.deptype in ('a', 'i')
    `
    batch.add(ownedQ, func(ownedRows pgx.Rows) error {
        for ownedRows.Next() {
            var key string
            if err := ownedRows.Scan(&key); err != nil {
                return err
            }
            l.OwnedSequences[key] = true
        }
        return nil
    })

    if err := batch.run(ctx, pool); err != nil { return nil, err }
    l.SearchPath = batch.searchPath
    return l, nil
}

// procedureChanged reports whether a live procedure's definition differs from
// desired. Security is compared only when the YAML sets it; body comparison
// is whitespace-trimmed (prosrc stores the body verbatim).
func procedureChanged(p *schema.Procedure, lp *LiveProcedure) bool {
    if routineBodyChanged(p.Language, p.Body, p.HasBody(), p.Replace, lp.Body, lp.SQLBody) { return true }
    if p.HasBody() && (routineLanguageChanged(p.Language, lp.Language) || transformChanged(schema.RoutineTransform(p.Language, p.Transform), lp.Transform)) { return true }
    if changed, _ := argDefaultsDiff(p.ArgsSig, lp.Args); changed { return true }
    if p.Security != "" && strings.ToLower(p.Security) != lp.Security { return true }
    if len(configClauses(p.Set, lp.Config)) > 0 { return true }
    return false
}

// parseFDWOptions parses foreign-data wrapper options ("name=value", as
// srvoptions, ftoptions and attfdwoptions store them), keeping names as
// written since wrappers may treat them case-sensitively.
func parseFDWOptions(entries []string) map[string]string {
    out := map[string]string{}
    for _, e := range entries {
        if k, v, ok := strings.Cut(e, "="); ok { out[k] = v }
    }
    return out
}

// parseProconfig turns pg_proc.proconfig entries ("name=value") into a map
// keyed by lower-cased parameter name.
func parseProconfig(entries []string) map[string]string {
    out := map[string]string{}
    for _, e := range entries {
        k, v, ok := strings.Cut(e, "=")
        if !ok { continue }
        out[strings.ToLower(k)] = v
    }
    return out
}

var configCommaRe = regexp.MustCompile(`\s*,\s*`)

var configQuoteReplacer = strings.NewReplacer("'", "", `"`, "")

// normalizeConfigValue makes a YAML set value comparable with its proconfig
// form. Quotes are ignored, since postgres stores '64MB' as 64MB and may
// double-quote list elements ("$user", public), and so is whitespace around
// list commas.
func normalizeConfigValue(v string) string {
    v = configQuoteReplacer.Replace(strings.TrimSpace(v))
    return configCommaRe.ReplaceAllString(v, ",")
}

var configNumberRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// listQuoteParams are the parameters PostgreSQL stores as a list of quoted
// identifiers (GUC_LIST_QUOTE), the same names pg_dump singles out. Only
// their values are comma-separated lists.
var listQuoteParams = map[string]bool{
    "local_preload_libraries": true, "search_path": true, "session_preload_libraries": true,
    "shared_preload_libraries": true, "temp_tablespaces": true, "unix_socket_directories": true,
}

// configValueSQL renders the value of a SET clause for the parameter name.
// The value of a list parameter (listQuoteParams) is rendered element by
// element. Any other value is one value, commas included, and is quoted
// unless it is a number or a word PostgreSQL accepts bare, so 5s, 64MB,
// America/New_York, UTC (not folded to utc) and reserved words such as all
// form valid SQL. A value already written as one SQL string literal is kept
// as it is, and the multi-argument spelling ('a', 'b') becomes the single
// value PostgreSQL stores for it.
func configValueSQL(name, v string) string {
    v = strings.TrimSpace(v)
    if listQuoteParams[strings.ToLower(name)] {
        parts := splitTopLevelQuoted(v)
        for i, p := range parts { parts[i] = configListElementSQL(strings.TrimSpace(p)) }
        return strings.Join(parts, ", ")
    }
    switch {
    case v == "":
        return "''"
    case isQuotedWith(v, '\''), configNumberRe.MatchString(v), configBareWord(v):
        return v
    }
    if elems, ok := configQuotedElements(v); ok { return quoteString(strings.Join(elems, ", ")) }
    return quoteString(v)
}

// configListElementSQL renders one element of a list parameter's value. An
// element PostgreSQL could not read as a quoted identifier (empty, or
// longer than an identifier may be) becomes a string literal.
func configListElementSQL(p string) string {
    switch {
    case isQuotedWith(p, '\''):
        return p
    case isQuotedWith(p, '"'):
        if inner := unquote(p, '"'); inner != "" && len(inner) < 64 { return p }
        return quoteString(unquote(p, '"'))
    case configBareWord(p):
        return p
    }
    return quoteString(p)
}

// configBareWord reports whether v can follow SET ... = without quotes: a
// lower-case identifier that is not a reserved word, or one of the reserved
// words the SET grammar accepts.
func configBareWord(v string) bool {
    return plainIdentRe.MatchString(v) && (!reservedWords[v] || v == "on" || v == "true" || v == "false")
}

// configQuotedElements reads v as the multi-argument spelling of a SET
// value, a comma-separated list with at least one quoted element, and
// returns the elements without their quotes. ok is false for anything else,
// such as free text that merely contains a quote.
func configQuotedElements(v string) (elems []string, ok bool) {
    for _, p := range splitTopLevelQuoted(v) {
        p = strings.TrimSpace(p)
        switch {
        case isQuotedWith(p, '\''):
            p, ok = unquote(p, '\''), true
        case isQuotedWith(p, '"'):
            p, ok = unquote(p, '"'), true
        case strings.ContainsAny(p, `'"`):
            return nil, false
        }
        elems = append(elems, p)
    }
    return elems, ok
}

// isQuotedWith reports whether s is one string wrapped in the quote
// character q, with every q inside it doubled.
func isQuotedWith(s string, q byte) bool {
    if len(s) < 2 || s[0] != q || s[len(s)-1] != q { return false }
    return !strings.ContainsRune(strings.ReplaceAll(s[1:len(s)-1], string([]byte{q, q}), ""), rune(q))
}

// unquote strips the quote character q wrapping s and undoubles the ones
// inside it.
func unquote(s string, q byte) string {
    return strings.ReplaceAll(s[1:len(s)-1], string([]byte{q, q}), string(q))
}

// ConfigValueYAML returns the YAML set value under which pgy writes back
// value, a setting of the parameter name as PostgreSQL stores it. A value
// holding quotes or outer whitespace is written as an SQL string literal,
// since configValueSQL would otherwise read them as the value's own quoting.
func ConfigValueYAML(name, value string) string {
    if listQuoteParams[strings.ToLower(name)] { return value }
    if !strings.ContainsAny(value, `'"`) && value == strings.TrimSpace(value) { return value }
    return quoteString(value)
}

// splitTopLevelQuoted splits on commas outside single and double quotes.
func splitTopLevelQuoted(s string) []string {
    out, start := []string{}, 0
    var q byte
    for i := 0; i < len(s); i++ {
        switch c := s[i]; {
        case q != 0:
            if c == q { q = 0 }
        case c == '\'' || c == '"':
            q = c
        case c == ',':
            out = append(out, s[start:i])
            start = i + 1
        }
    }
    return append(out, s[start:])
}

// configClauses returns SET / RESET action clauses that bring a live
// function's or procedure's configuration in line with a YAML set block. A
// nil block leaves configuration unmanaged; an empty block resets every
// live setting. Clauses are sorted for deterministic output.
func configClauses(desired map[string]string, live map[string]string) []string {
    if desired == nil { return nil }
    out := []string{}
    keys := make([]string, 0, len(desired))
    managed := map[string]bool{}
    for k := range desired {
        keys = append(keys, k)
        managed[strings.ToLower(k)] = true
    }
    sort.Strings(keys)
    for _, k := range keys {
        lv, ok := live[strings.ToLower(k)]
        if !ok || normalizeConfigValue(lv) != normalizeConfigValue(desired[k]) {
            out = append(out, fmt.Sprintf("set %s = %s", k, configValueSQL(k, desired[k])))
        }
    }
    extra := []string{}
    for k := range live {
        if !managed[k] { extra = append(extra, k) }
    }
    sort.Strings(extra)
    for _, k := range extra {
        out = append(out, "reset "+k)
    }
    return out
}

// setsSearchPath reports whether clause, one of configClauses' results, sets
// search_path. ALTER DATABASE ... SET only reaches sessions that start
// later, so the plan repeats such a clause as a statement of its own: the
// rest of the migration, and the migrations that run after it on the same
// connection, then resolve unqualified names on the new search_path.
func setsSearchPath(clause string) bool {
    return strings.HasPrefix(strings.ToLower(clause), "set search_path = ")
}

// functionAttrClauses returns ALTER FUNCTION action clauses for attribute
// settings the YAML manages (non-empty volatility/security, non-nil
// strict/leakproof/set) that differ from live. Used when the body is unchanged
// (or unmanaged) so an attribute flip doesn't require CREATE OR REPLACE —
// essential for extension-owned functions (e.g. PostGIS LANGUAGE c) whose
// bodies pgy cannot express. A nil lf stands for a function whose
// attributes are unknown (an extension of the same plan creates it), so
// every attribute the YAML manages is set.
func functionAttrClauses(f *schema.Function, lf *LiveFunction) []string {
    out := []string{}
    unknown := lf == nil
    if unknown { lf = &LiveFunction{} }
    if f.Volatility != "" && strings.ToLower(f.Volatility) != lf.Volatility {
        out = append(out, strings.ToLower(f.Volatility))
    }
    if f.Security != "" && strings.ToLower(f.Security) != lf.Security {
        out = append(out, "security "+strings.ToLower(f.Security))
    }
    if f.Strict != nil && (unknown || *f.Strict != lf.Strict) {
        if *f.Strict {
            out = append(out, "strict")
        } else {
            out = append(out, "called on null input")
        }
    }
    if f.Leakproof != nil && (unknown || *f.Leakproof != lf.Leakproof) {
        if *f.Leakproof {
            out = append(out, "leakproof")
        } else {
            out = append(out, "not leakproof")
        }
    }
    if f.Parallel != "" && f.Parallel != lf.Parallel {
        out = append(out, "parallel "+f.Parallel)
    }
    if f.Cost != nil && (unknown || !float4Equal(*f.Cost, lf.Cost)) {
        out = append(out, "cost "+formatFloat(*f.Cost))
    }
    if f.Rows != nil && (unknown || !float4Equal(*f.Rows, lf.Rows)) {
        out = append(out, "rows "+formatFloat(*f.Rows))
    }
    out = append(out, configClauses(f.Set, lf.Config)...)
    return out
}

// procedureAttrClauses returns the ALTER PROCEDURE action clauses for the
// security and settings that p, a procedure declared without a body, sets
// and that differ from lp. A nil lp stands for a procedure whose attributes
// are unknown, as in functionAttrClauses.
func procedureAttrClauses(p *schema.Procedure, lp *LiveProcedure) []string {
    out := []string{}
    if lp == nil { lp = &LiveProcedure{} }
    if p.Security != "" && strings.ToLower(p.Security) != lp.Security {
        out = append(out, "security "+strings.ToLower(p.Security))
    }
    return append(out, configClauses(p.Set, lp.Config)...)
}

// float4Equal compares a YAML number with a value PostgreSQL stored as real,
// allowing for float4 rounding.
func float4Equal(want, have float64) bool {
    return math.Abs(want-have) <= 1e-6*math.Max(1, math.Abs(want))
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// compositeAlterStmts adds attributes a live composite type lacks. Under
// --unsafe it also changes attribute types and drops attributes the YAML no
// longer declares (both fail while a table column uses the type). Without
// live attribute data nothing is emitted.
func compositeAlterStmts(fq string, td *schema.TypeDef, live, liveColl map[string]string, unsafe, cascade bool) []string {
    if live == nil { return nil }
    tail := ";"
    if cascade { tail = " cascade;" }
    names := append([]string(nil), td.AttributeOrder...)
    seen := map[string]bool{}
    for _, n := range names { seen[n] = true }
    for _, n := range sortedKeys(td.Attributes) {
        if !seen[n] { names = append(names, n) }
    }
    out := []string{}
    for _, n := range names {
        t, ok := td.Attributes[n]
        if !ok { continue }
        lt, exists := live[n]
        switch {
        case !exists:
            out = append(out, fmt.Sprintf("alter type %s add attribute %s %s%s", pqIdent(fq), quoteIdent(n), t+attributeCollate(td, n), tail))
        case unsafe && (normalizeColumnType(t) != normalizeColumnType(lt) || collationName(td.AttributeCollations[n]) != collationName(liveColl[n])):
            out = append(out, fmt.Sprintf("alter type %s alter attribute %s type %s%s", pqIdent(fq), quoteIdent(n), t+attributeCollate(td, n), tail))
        }
    }
    if unsafe {
        for _, n := range sortedKeys(live) {
            if _, ok := td.Attributes[n]; !ok { out = append(out, fmt.Sprintf("alter type %s drop attribute %s%s", pqIdent(fq), quoteIdent(n), tail)) }
        }
    }
    return out
}

// attributeCollate renders " collate <c>" for a composite attribute with an
// explicit collation, or "".
func attributeCollate(td *schema.TypeDef, attr string) string {
    if c := td.AttributeCollations[attr]; c != "" { return " collate " + collationIdent(c) }
    return ""
}

// renderRangeType emits CREATE TYPE ... AS RANGE with the options set.
func renderRangeType(fq string, r *schema.RangeDef) string {
    opts := []string{"subtype = " + r.Subtype}
    collation := ""
    if r.Collation != "" { collation = collationIdent(r.Collation) }
    for _, o := range [][2]string{{"subtype_opclass", r.SubtypeOpclass}, {"collation", collation}, {"canonical", r.Canonical}, {"subtype_diff", r.SubtypeDiff}, {"multirange_type_name", r.MultirangeTypeName}} {
        if o[1] != "" { opts = append(opts, o[0]+" = "+o[1]) }
    }
    return fmt.Sprintf("create type %s as range (%s);", pqIdent(fq), strings.Join(opts, ", "))
}

// enumAddValueStmts emits ALTER TYPE ... ADD VALUE for desired labels missing
// from live, positioned BEFORE the next desired label that already exists so
// desired order is preserved. Removed/reordered labels are ignored (postgres
// cannot drop enum values).
func enumAddValueStmts(key string, desired, liveLabels []string) []string {
    liveSet := map[string]bool{}
    for _, l := range liveLabels { liveSet[l] = true }
    out := []string{}
    for i, lbl := range desired {
        if liveSet[lbl] { continue }
        pos := ""
        for j := i + 1; j < len(desired); j++ {
            if liveSet[desired[j]] {
                pos = " before " + quoteString(desired[j])
                break
            }
        }
        out = append(out, fmt.Sprintf("alter type %s add value %s%s;", pqIdent(key), quoteString(lbl), pos))
    }
    return out
}

// normalizedLiveFunctions returns the set of live function signatures in
// normalizeFunctionSignature form.
func normalizedLiveFunctions(live *Live) map[string]bool {
    out := make(map[string]bool, len(live.Functions))
    for sig, ok := range live.Functions {
        if ok { out[normalizeFunctionSignature(sig)] = true }
    }
    return out
}

func addLiveGrant(m map[string]map[string]map[string]bool, key, role, priv string) {
    if m[key] == nil { m[key] = map[string]map[string]bool{} }
    if m[key][role] == nil { m[key][role] = map[string]bool{} }
    m[key][role][priv] = true
}

type PlanDiff struct{
    // Prelude holds ALTER TYPE ... ADD VALUE statements. PostgreSQL rejects
    // a new enum label until the transaction that added it commits, so they
    // belong in a migration of their own that runs before the rest.
    Prelude []string
    Creates []string
    Alters  []string
    Drops   []string
    // Concurrent holds statements that cannot run inside a transaction
    // (CREATE / DROP INDEX CONCURRENTLY). They belong in a separate
    // no-transaction migration that runs after the rest of the plan.
    Concurrent []string
    // Warnings flags statements that lock or rewrite existing tables.
    Warnings []Warning
}

func (p *PlanDiff) Summary() map[string]int {
    return map[string]int{
        "creates": len(p.Creates),
        "alters": len(p.Alters),
        "drops": len(p.Drops),
        "concurrent": len(p.Concurrent),
        "prelude": len(p.Prelude),
    }
}

// Plan diffs desired against live. unsafe enables destructive changes; see
// Options.
func Plan(live *Live, desired *schema.Database, unsafe bool) *PlanDiff {
    return PlanWith(live, desired, Options{Unsafe: unsafe})
}

// PlanWith diffs desired against live with the given options.
func PlanWith(live *Live, desired *schema.Database, opts Options) *PlanDiff {
    return withoutMoveEchoes(planWith(live, desired, opts), live, desired, opts)
}

// planWith is PlanWith before withoutMoveEchoes drops the statements that
// only echo an extension's move to another schema.
func planWith(live *Live, desired *schema.Database, opts Options) *PlanDiff {
    unsafe := opts.Unsafe
    live = routinesAfterMoves(live.scoped(), desired)
    desired = expandAllPrivileges(desired, live.ServerVersion)
    extensions := &PlanDiff{}
    planExtensions(extensions, live, desired)
    desired, attributeOnlyWarnings := resolveAttributeOnly(live, desired, membersPending(live, extensions.Creates))
    live, renames := applyRenames(live, desired)
    live = applyExprProbes(live, desired)
    liveTransforms := routineTransforms(live)
    live, recreated, recreateWarnings := recreateChangedFunctions(live, desired, unsafe)
    var partitionDetaches []string
    live, replaceViews, viewDrops, viewPost, viewWarnings := planViewChanges(live, desired, opts)
    recreateWarnings = append(recreateWarnings, viewWarnings...)
    live, renamedArgs, blockedRoutines, argWarnings := recreateRenamedArguments(live, desired, unsafe)
    for k, v := range renamedArgs {
        if recreated == nil { recreated = map[string]string{} }
        recreated[k] = v
    }
    recreateWarnings = append(recreateWarnings, argWarnings...)
    live, updatePost, updateWarnings := recreateUpdateDependents(extensions, live, desired, opts)
    recreateWarnings = append(recreateWarnings, updateWarnings...)
    live, typeChangeEarly, typeChangePre, typeChangePost, readdedColumns, dependentWarnings := recreateColumnDependents(live, desired, opts)
    recreateWarnings = append(recreateWarnings, dependentWarnings...)
    recreateWarnings = append(recreateWarnings, attributeOnlyWarnings...)
    // Foreign key alters are deferred until all tables are processed so that
    // every PK/unique constraint exists before any FK references it
    // (required for circular FK pairs, which topological sort cannot order).
    deferredFKs := []string{}
    // Trigger creates are deferred until after all other creates so a trigger
    // function may declare dependsOn on the trigger's own table (needed for
    // SQL-language bodies, which are validated at CREATE time) without forming
    // a table->function->table cycle.
    deferredTriggers := []string{}
    // Column drops go after the policy and publication drops, which may use the columns.
    columnDrops := []string{}
    plan := &PlanDiff{Warnings: recreateWarnings}

    // ROLES HAVE HIGHEST PRIORITY - cluster-level, referenced by grants,
    // policies, and memberships later in the plan
    planRoles(plan, live, desired, opts.Unsafe && opts.Prune)

    // Collect all schemas needed from desired entities
    neededSchemas := map[string]bool{}
    for _, ext := range desired.Extensions {
        if ext != nil && ext.Schema != "" { neededSchemas[ext.Schema] = true }
    }
    for k := range desired.Types {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    for k := range desired.Functions {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    for k := range desired.Aggregates {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 { neededSchemas[parts[0]] = true }
    }
    for k := range desired.Procedures {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    for k := range desired.Tables {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    for k := range desired.Views {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    for k := range desired.Sequences {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    for k := range desired.Domains {
        if parts := strings.SplitN(k, ".", 2); len(parts) == 2 {
            neededSchemas[parts[0]] = true
        } else {
            neededSchemas["public"] = true
        }
    }
    for _, dp := range desired.DefaultPrivileges {
        if dp == nil { continue }
        for _, nsp := range dp.Schemas { neededSchemas[nsp] = true }
    }
    for _, keys := range [][]string{sortedKeys(desired.Collations), sortedKeys(desired.TSConfigs), sortedKeys(desired.TSDictionaries)} {
        for _, k := range keys {
            if parts := strings.SplitN(k, ".", 2); len(parts) == 2 { neededSchemas[parts[0]] = true }
        }
    }

    // Generate CREATE SCHEMA statements for missing schemas (public is always present)
    // SCHEMAS HAVE HIGHEST PRIORITY - must be created first
    schemaNames := make([]string, 0, len(neededSchemas))
    for s := range neededSchemas {
        if s == "public" || systemSchema(s) { continue }
        schemaNames = append(schemaNames, s)
    }
    sort.Strings(schemaNames)
    for _, schemaName := range schemaNames {
        if !live.Schemas[schemaName] {
            plan.Creates = append(plan.Creates, fmt.Sprintf("create schema if not exists %s;", quoteIdent(schemaName)))
        }
    }
    plan.Creates = append(plan.Creates, renames...)
    plan.Creates = append(plan.Creates, typeChangeEarly...)

    plan.Creates = append(plan.Creates, planDefaultPrivileges(live, desired)...)
    for _, role := range sortedKeys(desired.DatabaseRoleSettings) {
        prefix := "alter role " + strings.ReplaceAll(grantRole(role), "%", "%%") + " in database %I "
        for _, c := range configClauses(desired.DatabaseRoleSettings[role], live.DatabaseRoleConfig[resolveRole(role, live.CurrentUser)]) {
            plan.Alters = append(plan.Alters, inCurrentDatabase(prefix, c))
        }
    }

    // EXTENSIONS HAVE SECOND PRIORITY - created after schemas, before everything else
    plan.Creates = append(plan.Creates, extensions.Creates...)
    plan.Alters = append(plan.Alters, extensions.Alters...)
    plan.Warnings = append(plan.Warnings, extensions.Warnings...)
    for _, c := range configClauses(desired.DatabaseSettings, live.DatabaseConfig) {
        plan.Creates = append(plan.Creates, inCurrentDatabase("alter database %I ", c))
        if setsSearchPath(c) { plan.Creates = append(plan.Creates, c+";") }
    }
    planLanguages(plan, live, desired, opts.PublicGrants)
    planWrappers(plan, live, desired, opts.PublicGrants)
    planServers(plan, live, desired)
    plan.Warnings = append(plan.Warnings, unmappedMatViewWarnings(live, desired)...)

    // Topologically sort remaining entities (types, functions, tables) respecting dependsOn.
    // On a cycle the sort still returns every entity (cycle members appended in
    // name order) — use it as-is; the CLI surfaces the cycle error before
    // planning, so reaching here with err != nil means the caller opted in.
    sorted, _ := schema.TopologicalSort(desired)
    liveFuncs := normalizedLiveFunctions(live)
    // Attribute-only ALTER FUNCTION / PROCEDURE statements go into Creates
    // here, not Alters: an index or generated column created below may need
    // the function to be immutable already.
    routineAlterAt := len(plan.Creates)
    routineCreates := map[string]bool{}
    routineAlters := []string{}

    // Generate SQL in dependency order (excluding extensions, already handled above)
    for _, e := range sorted {
        switch e.Kind {
        case "extension":
            // Extensions already handled above, skip
            continue
        case "type":
            td, ok := desired.Types[e.Key]
            if !ok || td == nil { continue }
            // Existing enum: add missing labels (removal/reorder unsupported by postgres)
            if live.Types[e.Key] {
                switch td.Kind {
                case "enum":
                    plan.Prelude = append(plan.Prelude, enumAddValueStmts(e.Key, td.Labels, live.EnumLabels[e.Key])...)
                case "composite":
                    plan.Alters = append(plan.Alters, compositeAlterStmts(e.Key, td, live.CompositeAttributes[e.Key], live.CompositeCollations[e.Key], unsafe, typedTableUses(live, e.Key))...)
                }
                continue
            }
            switch td.Kind {
            case "enum":
                labels := make([]string, 0, len(td.Labels))
                for _, l := range td.Labels { labels = append(labels, quoteString(l)) }
                plan.Creates = append(plan.Creates, fmt.Sprintf("create type %s as enum (%s);", pqIdent(e.Key), strings.Join(labels, ", ")))
            case "composite":
                // preserve YAML declaration order (positional ROW(...)::type casts
                // depend on it); attributes missing from the order list follow sorted
                attrs := []string{}
                emitted := map[string]bool{}
                for _, an := range td.AttributeOrder {
                    if at, ok := td.Attributes[an]; ok && !emitted[an] {
                        attrs = append(attrs, fmt.Sprintf("%s %s%s", quoteIdent(an), at, attributeCollate(td, an)))
                        emitted[an] = true
                    }
                }
                keys := make([]string, 0, len(td.Attributes))
                for k := range td.Attributes { if !emitted[k] { keys = append(keys, k) } }
                sort.Strings(keys)
                for _, an := range keys { attrs = append(attrs, fmt.Sprintf("%s %s%s", quoteIdent(an), td.Attributes[an], attributeCollate(td, an))) }
                plan.Creates = append(plan.Creates, fmt.Sprintf("create type %s as (%s);", pqIdent(e.Key), strings.Join(attrs, ", ")))
            case "range":
                if td.Range == nil || td.Range.Subtype == "" { continue }
                plan.Creates = append(plan.Creates, renderRangeType(e.Key, td.Range))
            }
        case "function":
            f, ok := desired.Functions[e.Key]
            if !ok || f == nil { continue }
            fname := schema.RoutineName(e.Key)
            normalizedDesired := normalizeFunctionSignature(fname + f.ArgsSig)
            if blockedRoutines[normalizedDesired] { continue }
            found := liveFuncs[normalizedDesired]
            if found {
                lf := live.FunctionDefs[normalizedDesired]
                if lf == nil || returnsChanged(f.Returns, lf.Returns) { continue }
                defaultsChanged, defaultsRemoved := argDefaultsDiff(f.ArgsSig, lf.Args)
                if defaultsRemoved && f.HasBody() { continue }
                // A declaration without a body leaves the body unmanaged
                // (attribute-only, e.g. marking an extension function leakproof).
                bodyChanged := routineBodyChanged(f.Language, f.Body, f.HasBody(), f.Replace, lf.Body, lf.SQLBody) ||
                    f.HasBody() && (defaultsChanged || routineLanguageChanged(f.Language, lf.Language) || transformChanged(schema.RoutineTransform(f.Language, f.Transform), lf.Transform))
                if !bodyChanged {
                    if clauses := functionAttrClauses(f, lf); len(clauses) > 0 {
                        routineAlters = append(routineAlters, fmt.Sprintf("alter function %s %s;",
                            pqIdent(fname)+grantFuncArgs(f.ArgsSig), strings.Join(clauses, " ")))
                    }
                    continue
                }
                // fall through: body changed, emit CREATE OR REPLACE
            } else if !f.HasBody() {
                // Attribute-only declaration for a function that doesn't exist
                // yet: an extension of this plan creates it, or
                // resolveAttributeOnly emptied the declaration.
                if clauses := functionAttrClauses(f, nil); len(clauses) > 0 {
                    routineAlters = append(routineAlters, fmt.Sprintf("alter function %s %s;",
                        pqIdent(fname)+grantFuncArgs(f.ArgsSig), strings.Join(clauses, " ")))
                }
                continue
            }
            if found { f = keepLiveFunctionAttributes(f, live.FunctionDefs[normalizedDesired]) }
            setClauses := ""
            if len(f.Set) > 0 {
                keys := make([]string, 0, len(f.Set))
                for k := range f.Set { keys = append(keys, k) }
                sort.Strings(keys)
                for _, k := range keys {
                    setClauses += fmt.Sprintf(" set %s = %s", k, configValueSQL(k, f.Set[k]))
                }
            }
            attrs := []string{}
            if f.Security != "" { attrs = append(attrs, "security "+f.Security) }
            if f.Volatility != "" { attrs = append(attrs, f.Volatility) }
            if f.Strict != nil && *f.Strict { attrs = append(attrs, "strict") }
            if f.Leakproof != nil && *f.Leakproof { attrs = append(attrs, "leakproof") }
            if f.Parallel != "" { attrs = append(attrs, "parallel "+f.Parallel) }
            if f.Cost != nil { attrs = append(attrs, "cost "+formatFloat(*f.Cost)) }
            if f.Rows != nil { attrs = append(attrs, "rows "+formatFloat(*f.Rows)) }
            attrsStr := strings.Join(attrs, " ")
            if attrsStr != "" { attrsStr = " " + attrsStr }
            body := f.Body
            verb := "create function"
            if found { verb = "create or replace function" }
            if drop := recreated[normalizedDesired]; drop != "" { plan.Creates = append(plan.Creates, drop) }
            stmt := fmt.Sprintf("%s %s returns %s language %s%s %s;", verb, pqIdent(fname)+f.ArgsSig, f.Returns, routineLanguageSQL(f.Language, f.Transform, liveTransforms[normalizedDesired]), attrsStr+setClauses, routineBodySQL(f.Language, body))
            plan.Creates = append(plan.Creates, stmt)
            if !schema.IsSQLStandardBody(f.Language, body) { routineCreates[stmt] = true }
        case "procedure":
            pr, ok := desired.Procedures[e.Key]
            if !ok || pr == nil { continue }
            prname := schema.RoutineName(e.Key)
            norm := normalizeFunctionSignature(prname + pr.ArgsSig)
            if blockedRoutines[norm] { continue }
            found := live.Procedures[norm]
            if !pr.HasBody() {
                lp := live.ProcedureDefs[norm]
                if found && lp == nil { continue }
                if clauses := procedureAttrClauses(pr, lp); len(clauses) > 0 {
                    routineAlters = append(routineAlters, fmt.Sprintf("alter procedure %s %s;",
                        pqIdent(prname)+grantFuncArgs(pr.ArgsSig), strings.Join(clauses, " ")))
                }
                continue
            }
            if found {
                lp := live.ProcedureDefs[norm]
                if lp == nil || !procedureChanged(pr, lp) { continue }
                // fall through: definition changed, emit CREATE OR REPLACE
                pr = keepLiveProcedureAttributes(pr, lp)
            }
            setClauses := ""
            if len(pr.Set) > 0 {
                keys := make([]string, 0, len(pr.Set))
                for k := range pr.Set { keys = append(keys, k) }
                sort.Strings(keys)
                for _, k := range keys {
                    setClauses += fmt.Sprintf(" set %s = %s", k, configValueSQL(k, pr.Set[k]))
                }
            }
            attrsStr := ""
            if pr.Security != "" { attrsStr = " security " + pr.Security }
            verb := "create procedure"
            if found { verb = "create or replace procedure" }
            if drop := recreated[norm]; drop != "" { plan.Creates = append(plan.Creates, drop) }
            stmt := fmt.Sprintf("%s %s language %s%s %s;", verb, pqIdent(prname)+pr.ArgsSig, routineLanguageSQL(pr.Language, pr.Transform, liveTransforms[norm]), attrsStr+setClauses, routineBodySQL(pr.Language, pr.Body))
            plan.Creates = append(plan.Creates, stmt)
            if !schema.IsSQLStandardBody(pr.Language, pr.Body) { routineCreates[stmt] = true }
        case "aggregate":
            ag := desired.Aggregates[e.Key]
            if ag == nil || ag.SFunc == "" || ag.SType == "" { continue }
            name := schema.RoutineName(e.Key)
            la := live.AggregateDefs[normalizeFunctionSignature(name+aggregateArgs(ag.ArgsSig))]
            if la != nil && aggregateMatchesLive(ag, la) { continue }
            if h, ok := ag.Options["hypothetical"]; ok && la != nil && strings.EqualFold(h, "true") != strings.EqualFold(la.Options["hypothetical"], "true") {
                if !unsafe {
                    plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf("aggregate %s%s: changing hypothetical needs DROP AGGREGATE and CREATE AGGREGATE; rerun with --unsafe", name, ag.ArgsSig)})
                    continue
                }
                plan.Creates = append(plan.Creates, fmt.Sprintf("drop aggregate %s%s;", pqIdent(name), ag.ArgsSig))
            }
            plan.Creates = append(plan.Creates, renderAggregate(name, ag))
        case "tsdictionary":
            td := desired.TSDictionaries[e.Key]
            if td == nil || td.Template == "" { continue }
            if live.TSDictionaries[e.Key] {
                stmts, warn := tsDictionaryAlterStmts(e.Key, td, live.TSDictionaryDefs[e.Key])
                plan.Alters = append(plan.Alters, stmts...)
                if warn != "" { plan.Warnings = append(plan.Warnings, Warning{Message: warn}) }
                continue
            }
            opts := []string{"template = " + td.Template}
            for _, k := range sortedKeys(td.Options) { opts = append(opts, k+" = "+td.Options[k]) }
            plan.Creates = append(plan.Creates, fmt.Sprintf("create text search dictionary %s (%s);", pqIdent(e.Key), strings.Join(opts, ", ")))
        case "tsconfig":
            tc := desired.TSConfigs[e.Key]
            if tc == nil { continue }
            plan.Creates = append(plan.Creates, tsConfigStmts(e.Key, tc, live.TSConfigs[e.Key])...)
        case "collation":
            co := desired.Collations[e.Key]
            if co == nil || live.Collations[e.Key] { continue }
            plan.Creates = append(plan.Creates, renderCollation(e.Key, co))
        case "domain":
            dm, ok := desired.Domains[e.Key]
            if !ok || dm == nil || dm.Type == "" { continue }
            if live.Domains[e.Key] {
                if ld := live.DomainDefs[e.Key]; ld != nil && domainTypeChanged(dm.Type, ld.Type) {
                    plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf("domain %s: its base type changed from %s to %s, which ALTER DOMAIN can't do; create a new domain and move the columns to it in a hand-written migration", e.Key, ld.Type, dm.Type)})
                }
                plan.Alters = append(plan.Alters, domainAlterStmts(e.Key, dm, live.DomainDefs[e.Key], unsafe)...)
                continue
            }
            plan.Creates = append(plan.Creates, renderDomain(e.Key, dm))
        case "sequence":
            sq, ok := desired.Sequences[e.Key]
            if !ok || sq == nil { continue }
            if live.Sequences[e.Key] {
                plan.Alters = append(plan.Alters, sequenceAlterStmts(e.Key, sq, live.SequenceDefs[e.Key])...)
                continue
            }
            plan.Creates = append(plan.Creates, renderSequence(e.Key, sq))
            if sq.OwnedBy != "" {
                // OWNED BY goes into Alters (rendered after all Creates):
                // the owning table may itself be created by this plan, and
                // CREATE SEQUENCE ... OWNED BY would fail before it exists.
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter sequence %s owned by %s;", pqIdent(e.Key), ownedByIdent(e.Key, sq.OwnedBy)))
            }
        case "view":
            vw, ok := desired.Views[e.Key]
            if !ok || vw == nil || vw.Query == "" { continue }
            if vw.Materialized {
                if !live.MatViews[e.Key] {
                    stmt := fmt.Sprintf("create materialized view if not exists %s%s%s as %s", pqIdent(e.Key), renderStorageParams(vw.With), tablespaceClause(vw.Tablespace), vw.Query)
                    if vw.WithData != nil && !*vw.WithData { stmt += " with no data" }
                    plan.Creates = append(plan.Creates, stmt+";")
                } else {
                    lt := live.MatViewTables[e.Key]
                    if lt == nil { lt = newLiveTable() }
                    if vw.With != nil { plan.Alters = append(plan.Alters, storageParamStmts("materialized view", e.Key, vw.With, lt)...) }
                    if tablespaceDiffers(vw.Tablespace, lt.Tablespace, live) {
                        plan.Alters = append(plan.Alters, fmt.Sprintf("alter materialized view %s set tablespace %s;", pqIdent(e.Key), quoteIdent(vw.Tablespace)))
                        plan.Warnings = append(plan.Warnings, tablespaceMoveWarning("materialized view "+e.Key, e.Key))
                    }
                }
                if len(vw.Indexes) > 0 {
                    applyTableConstraints(plan, live, e.Key, &schema.Table{Indexes: vw.Indexes}, live.MatViewTables[e.Key], &deferredFKs, &deferredTriggers, unsafe)
                }
            } else {
                opts := viewOptions(vw)
                if live.Views[e.Key] && !vw.Replace && !replaceViews[e.Key] {
                    plan.Alters = append(plan.Alters, viewOptionStmts(e.Key, opts, live.ViewOptions[e.Key])...)
                } else {
                    plan.Creates = append(plan.Creates, fmt.Sprintf("create or replace view %s%s as %s;", pqIdent(e.Key), renderViewWith(opts, live.ViewOptions[e.Key]), vw.Query))
                }
                if len(vw.Triggers)+len(vw.Rules) > 0 {
                    applyTableConstraints(plan, live, e.Key, &schema.Table{Triggers: vw.Triggers, Rules: vw.Rules}, live.ViewTables[e.Key], &deferredFKs, &deferredTriggers, unsafe)
                }
                for _, col := range sortedKeys(vw.ColumnDefaults) {
                    want := vw.ColumnDefaults[col]
                    if have, ok := live.ViewColumnDefaults[e.Key][col]; live.Views[e.Key] && ok && defaultsEqual(want, have) { continue }
                    plan.Alters = append(plan.Alters, fmt.Sprintf("alter view %s alter column %s set default %s;", pqIdent(e.Key), quoteIdent(col), want))
                }
            }
        case "table":
            // Handle tables in dependency order
            dt, ok := desired.Tables[e.Key]
            if !ok || dt == nil { continue }
            fq := e.Key
            lt := live.Tables[fq]
            if lt == nil {
                if dt.PartitionOf != "" {
                    // partition children inherit columns from the parent
                    if dt.Foreign != nil {
                        plan.Creates = append(plan.Creates, fmt.Sprintf("create foreign table if not exists %s partition of %s %s server %s%s;", pqIdent(fq), pqIdent(dt.PartitionOf), renderPartitionBound(dt.Partition), quoteIdent(dt.Foreign.Server), fdwOptionsClause(dt.Foreign.Options)))
                    } else {
                        sub, unlogged := "", ""
                        if dt.PartitionBy != nil { sub = " partition by " + renderPartitionBy(dt.PartitionBy) }
                        if dt.Unlogged != nil && *dt.Unlogged { unlogged = "unlogged " }
                        plan.Creates = append(plan.Creates, fmt.Sprintf("create %stable if not exists %s partition of %s %s%s%s%s;", unlogged, pqIdent(fq), pqIdent(dt.PartitionOf), renderPartitionBound(dt.Partition), sub, renderStorageParams(dt.With), tablespaceClause(dt.Tablespace)))
                    }
                    for _, cn := range sortedKeys(dt.Columns) {
                        c := dt.Columns[cn]
                        if c == nil { continue }
                        plan.Alters = append(plan.Alters, inheritedColumnStmts(fq, cn, c, nil)...)
                        if dt.Foreign != nil { plan.Alters = append(plan.Alters, foreignColumnOptionStmts(fq, cn, c)...) }
                    }
                    applyTableConstraints(plan, live, fq, dt, nil, &deferredFKs, &deferredTriggers, unsafe)
                    continue
                }
                cols := make([]string, 0, len(dt.Columns))
                inherited := []string{}
                for _, cn := range sortedKeys(dt.Columns) {
                    if c := dt.Columns[cn]; c != nil && inheritedColumn(dt, c, nil) { inherited = append(inherited, cn) }
                }
                if len(dt.ColumnOrder) > 0 {
                    for _, cn := range dt.ColumnOrder {
                        if c, ok := dt.Columns[cn]; ok && !inheritedColumn(dt, c, nil) {
                            cols = append(cols, renderColumn(cn, c))
                        }
                    }
                    // include any remaining columns not listed (fallback)
                    rest := []string{}
                    for cn := range dt.Columns {
                        found := false
                        for _, on := range dt.ColumnOrder { if on == cn { found = true; break } }
                        if !found { rest = append(rest, cn) }
                    }
                    sort.Strings(rest)
                    for _, cn := range rest {
                        if c := dt.Columns[cn]; !inheritedColumn(dt, c, nil) { cols = append(cols, renderColumn(cn, c)) }
                    }
                } else {
                    for cn, c := range dt.Columns {
                        if !inheritedColumn(dt, c, nil) { cols = append(cols, renderColumn(cn, c)) }
                    }
                    sort.Strings(cols)
                }
                for _, cn := range inherited { plan.Alters = append(plan.Alters, inheritedColumnStmts(fq, cn, dt.Columns[cn], nil)...) }
                for _, cn := range sortedKeys(dt.Columns) {
                    if c := dt.Columns[cn]; c != nil && c.NotValid && !c.Nullable && !inheritedColumn(dt, c, nil) {
                        plan.Alters = append(plan.Alters, notValidNotNullStmt(fq, cn, c))
                    }
                }
                inherits := ""
                if len(dt.Inherits) > 0 {
                    parents := make([]string, len(dt.Inherits))
                    for i, p := range dt.Inherits { parents[i] = pqIdent(p) }
                    inherits = " inherits (" + strings.Join(parents, ", ") + ")"
                }
                if dt.Foreign != nil {
                    plan.Creates = append(plan.Creates, fmt.Sprintf("create foreign table if not exists %s (%s)%s server %s%s;", pqIdent(fq), strings.Join(cols, ", "), inherits, quoteIdent(dt.Foreign.Server), fdwOptionsClause(dt.Foreign.Options)))
                    plan.Alters = append(plan.Alters, columnStorageStmts(fq, dt)...)
                    applyTableConstraints(plan, live, fq, dt, nil, &deferredFKs, &deferredTriggers, unsafe)
                    continue
                }
                unlogged := ""
                if dt.Unlogged != nil && *dt.Unlogged { unlogged = "unlogged " }
                create := fmt.Sprintf("create %stable if not exists %s (%s)", unlogged, pqIdent(fq), strings.Join(cols, ", "))
                if dt.Of != "" { create = fmt.Sprintf("create %stable if not exists %s of %s", unlogged, pqIdent(fq), pqIdent(dt.Of)) }
                create += inherits
                if dt.PartitionBy != nil {
                    create += " partition by " + renderPartitionBy(dt.PartitionBy)
                }
                plan.Creates = append(plan.Creates, create+renderStorageParams(dt.With)+tablespaceClause(dt.Tablespace)+";")
                plan.Alters = append(plan.Alters, columnStorageStmts(fq, dt)...)
                applyTableConstraints(plan, live, fq, dt, nil, &deferredFKs, &deferredTriggers, unsafe)
                if stmt := replicaIdentityStmt(fq, dt, nil); stmt != "" { plan.Alters = append(plan.Alters, stmt) }
                if stmt := clusterOnStmt(fq, dt, nil); stmt != "" { plan.Alters = append(plan.Alters, stmt) }
            } else {
                if foreignKindMismatch(dt, lt) {
                    declared, actual := "a table", "a foreign table"
                    if dt.Foreign != nil { declared, actual = actual, declared }
                    plan.Warnings = append(plan.Warnings, Warning{Table: fq, Message: fmt.Sprintf("%s is declared as %s but is %s in the database, which no ALTER can change; it is left as it is, so drop and recreate it in a hand-written migration", fq, declared, actual)})
                    continue
                }
                // existing table: add missing columns, reconcile attributes
                // of columns present in both
                pkCols := map[string]bool{}
                for _, pc := range dt.PrimaryKey { pkCols[constraintColumnName(pc)] = true }
                plan.Creates = append(plan.Creates, typeChangePre[fq]...)
                if attachesPartition(dt, lt) {
                    plan.Creates = append(plan.Creates, fmt.Sprintf("alter table %s attach partition %s %s;", pqIdent(dt.PartitionOf), pqIdent(fq), renderPartitionBound(dt.Partition)))
                    plan.Warnings = append(plan.Warnings, Warning{Table: fq, Kind: "lock", Message: fmt.Sprintf("ATTACH PARTITION %s scans it under an ACCESS EXCLUSIVE lock to check its rows fit the bound, unless a validated CHECK constraint already implies the bound", fq)})
                } else if lb, ok := live.PartitionBounds[fq]; ok && unsafe && dt.PartitionOf != "" && lt.IsPartition && partitionMoved(dt, lb) {
                    partitionDetaches = append(partitionDetaches, fmt.Sprintf("alter table %s detach partition %s;", pqIdent(lb.Parent), pqIdent(fq)))
                    plan.Creates = append(plan.Creates, fmt.Sprintf("alter table %s attach partition %s %s;", pqIdent(dt.PartitionOf), pqIdent(fq), renderPartitionBound(dt.Partition)))
                    plan.Warnings = append(plan.Warnings, Warning{Table: fq, Kind: "lock", Message: fmt.Sprintf("re-attaching partition %s with its new bound takes an ACCESS EXCLUSIVE lock on %s and scans %s to check its rows fit, unless a validated CHECK constraint already implies the bound", fq, lb.Parent, fq)})
                }
                colNames := make([]string, 0, len(dt.Columns))
                if dt.PartitionOf == "" {
                    for cn := range dt.Columns { colNames = append(colNames, cn) }
                } else {
                    for _, cn := range sortedKeys(dt.Columns) {
                        if c, lc := dt.Columns[cn], lt.Columns[cn]; c != nil && lc != nil { plan.Alters = append(plan.Alters, inheritedColumnStmts(fq, cn, c, lc)...) }
                    }
                }
                sort.Strings(colNames)
                sort.SliceStable(colNames, func(i, j int) bool { return indexOf(readdedColumns[fq], colNames[i]) < 0 && indexOf(readdedColumns[fq], colNames[j]) >= 0 })
                for _, cn := range colNames {
                    c := dt.Columns[cn]
                    lc, ok := lt.Columns[cn]
                    if (ok && inheritedColumn(dt, c, lc)) || (!ok && inheritedColumn(dt, c, nil)) {
                        plan.Alters = append(plan.Alters, inheritedColumnStmts(fq, cn, c, lc)...)
                        continue
                    }
                    if !ok {
                        // ADD COLUMN is emitted into Creates at the table's
                        // topological position, not Alters: Render puts all
                        // Creates before Alters, so later creates in the same
                        // buffer that reference the new column (indexes on it,
                        // functions with dependsOn on this table) would
                        // otherwise run before the column exists and force a
                        // two-migration split.
                        plan.Creates = append(plan.Creates, fmt.Sprintf("alter table %s add column %s;", pqIdent(fq), renderColumn(cn, c)))
                        if c.NotValid && !c.Nullable { plan.Alters = append(plan.Alters, notValidNotNullStmt(fq, cn, c)) }
                        plan.Alters = append(plan.Alters, newColumnStmts(fq, cn, c)...)
                        continue
                    }
                    if name := notNullNotValidName(fq, cn, c, lc, lt, dt, pkCols, live.ServerVersion); name != "" {
                        plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s add constraint %s not null %s not valid;", pqIdent(fq), quoteIdent(name), quoteIdent(cn)))
                        if !c.NotValid { plan.Concurrent = append(plan.Concurrent, fmt.Sprintf("alter table %s validate constraint %s;", pqIdent(fq), quoteIdent(name))) }
                        enforced := *lc
                        enforced.Nullable = false
                        lc = &enforced
                    } else {
                        pending := lc.NotNullNotValid
                        if name := notNullRename(c, lc); name != "" {
                            plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s rename constraint %s to %s;", pqIdent(fq), quoteIdent(lc.NotNullName), quoteIdent(name)))
                            if pending != "" { pending = name }
                        }
                        if pending != "" && !c.Nullable && !c.NotValid {
                            plan.Concurrent = append(plan.Concurrent, fmt.Sprintf("alter table %s validate constraint %s;", pqIdent(fq), quoteIdent(pending)))
                        }
                    }
                    stmts := alterColumnStmts(fq, cn, c, lc, pkCols, unsafe, live.ServerVersion, func(msg string) {
                        plan.Warnings = append(plan.Warnings, Warning{Table: fq, Message: msg})
                    })
                    if len(typeChangePre[fq]) > 0 {
                        plan.Creates = append(plan.Creates, stmts...)
                    } else {
                        plan.Alters = append(plan.Alters, stmts...)
                    }
                }
                if dt.Foreign == nil { plan.Alters = append(plan.Alters, tableStorageStmts(fq, dt, lt, unsafe)...) }
                if dt.Foreign == nil && tablespaceDiffers(dt.Tablespace, lt.Tablespace, live) {
                    plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s set tablespace %s;", pqIdent(fq), quoteIdent(dt.Tablespace)))
                    if !lt.Partitioned { plan.Warnings = append(plan.Warnings, tablespaceMoveWarning("table "+fq, fq)) }
                }
                plan.Creates = append(plan.Creates, typedTableStmts(fq, dt, lt, unsafe, func(msg string) { plan.Warnings = append(plan.Warnings, Warning{Table: fq, Message: msg}) })...)
                if dt.Foreign != nil { plan.Alters = append(plan.Alters, foreignTableAlterStmts(fq, dt, lt, func(msg string) { plan.Warnings = append(plan.Warnings, Warning{Table: fq, Message: msg}) })...) }
                plan.Alters = append(plan.Alters, inheritStmts(fq, dt, lt, unsafe)...)
                // apply any missing constraints, indexes, triggers
                applyTableConstraints(plan, live, fq, dt, lt, &deferredFKs, &deferredTriggers, unsafe)
                if stmt := replicaIdentityStmt(fq, dt, lt); stmt != "" { plan.Alters = append(plan.Alters, stmt) }
                if stmt := clusterOnStmt(fq, dt, lt); stmt != "" { plan.Alters = append(plan.Alters, stmt) }
                // drops
                if unsafe && dt.PartitionOf == "" && dt.Of == "" && lt.OfType == "" {
                    dropCols := []string{}
                    for cn, lc := range lt.Columns {
                        if _, ok := dt.Columns[cn]; !ok && !lc.Inherited && !parentDeclaresColumn(desired, dt, cn, 0) { dropCols = append(dropCols, cn) }
                    }
                    sort.Strings(dropCols)
                    sort.SliceStable(dropCols, func(i, j int) bool { return lt.Columns[dropCols[i]].Generated != "" && lt.Columns[dropCols[j]].Generated == "" })
                    leftOut := map[string]bool{}
                    for _, cn := range dropCols {
                        if w, prunable := columnDropBlocked(live, desired, fq, cn, leftOut, opts.Prune); w != nil {
                            plan.Warnings = append(plan.Warnings, *w)
                            leftOut[cn] = prunable
                            continue
                        }
                        columnDrops = append(columnDrops, fmt.Sprintf("alter table %s drop column %s;", pqIdent(fq), quoteIdent(cn)))
                    }
                }
            }
        }
    }
    plan.Creates = append(plan.Creates[:routineAlterAt:routineAlterAt], append(routineAlters, plan.Creates[routineAlterAt:]...)...)
    plan.Creates = append(plan.Creates, deferredTriggers...)
    planEventTriggers(plan, live, desired, opts)
    dataWrites, dataDeletes := planData(live, desired, opts.Unsafe && opts.Prune)
    plan.Alters = append(plan.Alters, dataWrites...)
    plan.Alters = append(plan.Alters, deferredFKs...)
    planPolicies(plan, live, desired, unsafe)
    planPublications(plan, live, desired, opts)
    plan.Drops = append(plan.Drops, columnDrops...)
    plan.Alters = append(plan.Alters, planOwners(live, desired)...)
    grantStmts, grantWarnings := planGrantsWarned(live, desired, opts.PublicGrants)
    plan.Alters = append(plan.Alters, grantStmts...)
    plan.Warnings = append(plan.Warnings, grantWarnings...)
    plan.Alters = append(plan.Alters, planComments(live, desired, unsafe)...)
    plan.Alters = append(plan.Alters, viewPost...)
    plan.Alters = append(plan.Alters, updatePost...)
    plan.Alters = append(plan.Alters, typeChangePost...)
    plan.Drops = append(dataDeletes, plan.Drops...)
    if opts.Unsafe && opts.Prune {
        early, late := planPrune(live, desired, opts.MigrationsTable)
        plan.Drops = append(append(early, plan.Drops...), late...)
        serverDrops, serverWarnings := pruneServers(live, desired, opts.MigrationsTable)
        plan.Drops = append(plan.Drops, serverDrops...)
        plan.Warnings = append(plan.Warnings, serverWarnings...)
        wrapperDrops, wrapperWarnings := pruneWrappers(live, desired, serverDrops)
        plan.Drops = append(plan.Drops, wrapperDrops...)
        plan.Warnings = append(plan.Warnings, wrapperWarnings...)
        planExtensionDrops(plan, live, desired, opts)
    }
    plan.Creates = append(append(viewDrops, partitionDetaches...), plan.Creates...)
    if !keepsBodyChecks(plan, routineCreates) { plan.Creates = uncheckedRoutineCreates(plan.Creates, routineCreates) }
    plan.Warnings = append(plan.Warnings, versionWarnings(live, desired)...)
    plan.Warnings = append(plan.Warnings, lockWarnings(live, desired, plan)...)
    return plan
}

// planRoles emits CREATE ROLE for roles missing from live, ALTER ROLE for
// changed attributes and settings, and GRANT <parent> TO <role> for missing
// memberships. Roles are never dropped. With prune, a role whose inRoles is
// declared also loses the memberships it doesn't list.
func planRoles(plan *PlanDiff, live *Live, desired *schema.Database, prune bool) {
    names := make([]string, 0, len(desired.Roles))
    for k := range desired.Roles { names = append(names, k) }
    sort.Strings(names)
    for _, name := range names {
        r := desired.Roles[name]
        if r == nil { continue }
        if !live.Roles[name] {
            plan.Creates = append(plan.Creates, renderRole(r))
        } else if opts := roleAlterOptions(r, live.RoleDefs[name]); len(opts) > 0 {
            plan.Alters = append(plan.Alters, fmt.Sprintf("alter role %s with %s;", grantRole(name), strings.Join(opts, " ")))
        }
        var liveConfig map[string]string
        if lr := live.RoleDefs[name]; lr != nil && live.Roles[name] { liveConfig = lr.Config }
        for _, c := range configClauses(r.Set, liveConfig) {
            plan.Alters = append(plan.Alters, fmt.Sprintf("alter role %s %s;", grantRole(name), c))
        }
        for _, parent := range r.InRoles {
            if parent == "" { continue }
            plan.Alters = append(plan.Alters, membershipStmts(plan, live, name, parent, r.MemberOptions[parent])...)
        }
        if prune && r.InRoles != nil && live.Roles[name] {
            plan.Drops = append(plan.Drops, revokeMembershipStmts(live, name, r.InRoles)...)
        }
    }
}

// revokeMembershipStmts revokes member's live memberships in roles that
// declared doesn't list. PostgreSQL 16+ records a grant per grantor, and
// REVOKE only removes the current user's unless GRANTED BY names another,
// so it emits one REVOKE per grantor there.
func revokeMembershipStmts(live *Live, member string, declared []string) []string {
    keep := map[string]bool{}
    for _, p := range declared { keep[p] = true }
    out := []string{}
    for _, parent := range sortedKeys(live.RoleMembers[member]) {
        if keep[parent] { continue }
        target := fmt.Sprintf("revoke %s from %s", grantRole(parent), grantRole(member))
        lm := live.RoleMemberOptions[member][parent]
        if live.ServerVersion < 160000 || lm == nil || len(lm.Grantors) == 0 {
            out = append(out, target+";")
            continue
        }
        for _, g := range lm.Grantors {
            if g == "" { out = append(out, target+";"); continue }
            out = append(out, fmt.Sprintf("%s granted by %s;", target, grantRole(g)))
        }
    }
    return out
}

// membershipStmts grants parent to member with the options o sets, or, for
// an existing membership, changes the options that differ. WITH INHERIT and
// WITH SET need PostgreSQL 16; on an older server they are skipped with a
// warning.
func membershipStmts(plan *PlanDiff, live *Live, member, parent string, o *schema.MembershipOptions) []string {
    target := grantRole(parent) + " to " + grantRole(member)
    if o == nil { o = &schema.MembershipOptions{} }
    if (o.Inherit != nil || o.Set != nil) && live.ServerVersion > 0 && live.ServerVersion < 160000 {
        plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf("role %s: the inherit and set options of its membership in %s need PostgreSQL 16; they were left out", member, parent)})
        c := *o
        c.Inherit, c.Set = nil, nil
        o = &c
    }
    if !live.RoleMembers[member][parent] {
        opts := []string{}
        for _, f := range []struct {
            name string
            v    *bool
        }{{"admin", o.Admin}, {"inherit", o.Inherit}, {"set", o.Set}} {
            if f.v != nil { opts = append(opts, fmt.Sprintf("%s %t", f.name, *f.v)) }
        }
        switch {
        case len(opts) == 0:
            return []string{"grant " + target + ";"}
        case len(opts) == 1 && o.Admin != nil:
            if *o.Admin { return []string{"grant " + target + " with admin option;"} }
            return []string{"grant " + target + ";"}
        }
        return []string{"grant " + target + " with " + strings.Join(opts, ", ") + ";"}
    }
    lm := live.RoleMemberOptions[member][parent]
    if lm == nil { return nil }
    out := []string{}
    if o.Admin != nil && *o.Admin != lm.Admin {
        if *o.Admin {
            out = append(out, "grant "+target+" with admin option;")
        } else {
            out = append(out, fmt.Sprintf("revoke admin option for %s from %s;", grantRole(parent), grantRole(member)))
        }
    }
    for _, f := range []struct {
        name       string
        want       *bool
        have       bool
    }{{"inherit", o.Inherit, lm.Inherit}, {"set", o.Set, lm.Set}} {
        if f.want != nil && *f.want != f.have {
            out = append(out, fmt.Sprintf("grant %s with %s %t;", target, f.name, *f.want))
        }
    }
    return out
}

// normalizeValidUntil puts a VALID UNTIL value in the form introspection
// reads: UTC RFC 3339, or infinity. A value without a zone is taken as UTC.
func normalizeValidUntil(s string) string {
    s = strings.TrimSpace(s)
    if strings.EqualFold(s, "infinity") { return "infinity" }
    for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05Z07", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
        if t, err := time.Parse(layout, s); err == nil { return t.UTC().Format(time.RFC3339) }
    }
    return s
}

// roleAlterOptions returns ALTER ROLE options for attributes the YAML sets
// explicitly that differ from the live role.
func roleAlterOptions(r *schema.Role, lr *LiveRole) []string {
    if lr == nil { return nil }
    out := []string{}
    flag := func(key string, want, have bool, on, off string) {
        if !r.Managed[key] || want == have { return }
        if want { out = append(out, on) } else { out = append(out, off) }
    }
    flag("login", r.Login, lr.Login, "login", "nologin")
    flag("superuser", r.Superuser, lr.Superuser, "superuser", "nosuperuser")
    flag("createdb", r.CreateDB, lr.CreateDB, "createdb", "nocreatedb")
    flag("createrole", r.CreateRole, lr.CreateRole, "createrole", "nocreaterole")
    flag("replication", r.Replication, lr.Replication, "replication", "noreplication")
    flag("bypassRLS", r.BypassRLS, lr.BypassRLS, "bypassrls", "nobypassrls")
    flag("inherit", !r.NoInherit, lr.Inherit, "inherit", "noinherit")
    if r.Managed["connectionLimit"] && r.ConnectionLimit != lr.ConnectionLimit {
        out = append(out, fmt.Sprintf("connection limit %d", r.ConnectionLimit))
    }
    if r.Managed["validUntil"] && r.ValidUntil != "" && normalizeValidUntil(r.ValidUntil) != lr.ValidUntil {
        out = append(out, "valid until "+quoteString(r.ValidUntil))
    }
    return out
}

func renderRole(r *schema.Role) string {
    opts := []string{}
    if r.Login { opts = append(opts, "login") }
    if r.Superuser { opts = append(opts, "superuser") }
    if r.CreateDB { opts = append(opts, "createdb") }
    if r.CreateRole { opts = append(opts, "createrole") }
    if r.Replication { opts = append(opts, "replication") }
    if r.BypassRLS { opts = append(opts, "bypassrls") }
    if r.NoInherit { opts = append(opts, "noinherit") }
    if r.ConnectionLimit >= 0 { opts = append(opts, fmt.Sprintf("connection limit %d", r.ConnectionLimit)) }
    if r.ValidUntil != "" { opts = append(opts, "valid until "+quoteString(r.ValidUntil)) }
    stmt := "create role " + grantRole(r.Name)
    if len(opts) > 0 { stmt += " with " + strings.Join(opts, " ") }
    // Roles are cluster-level and may pre-exist even when the target database
    // is fresh (e.g. diff --from-empty bootstraps against an empty Live
    // snapshot). Postgres has no CREATE ROLE IF NOT EXISTS, so guard with a
    // DO block instead.
    return "do " + dollarQuoteInline(fmt.Sprintf("begin if not exists (select from pg_catalog.pg_roles where rolname = %s) then %s; end if; end", quoteString(r.Name), stmt)) + ";"
}

// viewOptions returns the view reloptions the YAML manages, in a fixed
// order, as name/value pairs. An empty value means the option should be
// unset (false booleans, checkOption: none).
func viewOptions(vw *schema.View) [][2]string {
    out := [][2]string{}
    for _, b := range []struct {
        name string
        val  *bool
    }{{"check_option", nil}, {"security_barrier", vw.SecurityBarrier}, {"security_invoker", vw.SecurityInvoker}} {
        if b.name == "check_option" {
            switch vw.CheckOption {
            case "":
            case "none":
                out = append(out, [2]string{b.name, ""})
            default:
                out = append(out, [2]string{b.name, vw.CheckOption})
            }
            continue
        }
        if b.val == nil { continue }
        v := ""
        if *b.val { v = "true" }
        out = append(out, [2]string{b.name, v})
    }
    return out
}

// renderViewWith renders the WITH (...) clause of CREATE OR REPLACE VIEW, or
// "" when no option is set. CREATE OR REPLACE replaces the view's options
// with exactly this list, so live options the YAML does not manage are
// carried over.
func renderViewWith(opts [][2]string, live map[string]string) string {
    merged := map[string]string{}
    for k, v := range live { merged[k] = v }
    for _, o := range opts {
        if o[1] == "" { delete(merged, o[0]) } else { merged[o[0]] = o[1] }
    }
    keys := make([]string, 0, len(merged))
    for k := range merged { keys = append(keys, k) }
    sort.Strings(keys)
    parts := make([]string, 0, len(keys))
    for _, k := range keys { parts = append(parts, k+" = "+merged[k]) }
    if len(parts) == 0 { return "" }
    return " with (" + strings.Join(parts, ", ") + ")"
}

// viewOptionStmts emits ALTER VIEW ... SET / RESET for managed options that
// differ from the live reloptions. A live "false" counts as unset.
func viewOptionStmts(key string, opts [][2]string, live map[string]string) []string {
    set, reset := []string{}, []string{}
    for _, o := range opts {
        have := strings.ToLower(live[o[0]])
        if have == "false" { have = "" }
        if have == o[1] { continue }
        if o[1] == "" {
            reset = append(reset, o[0])
        } else {
            set = append(set, o[0]+" = "+o[1])
        }
    }
    out := []string{}
    if len(set) > 0 { out = append(out, fmt.Sprintf("alter view %s set (%s);", pqIdent(key), strings.Join(set, ", "))) }
    if len(reset) > 0 { out = append(out, fmt.Sprintf("alter view %s reset (%s);", pqIdent(key), strings.Join(reset, ", "))) }
    return out
}

// renderSequence emits CREATE SEQUENCE IF NOT EXISTS with only the options
// set in YAML (postgres defaults apply otherwise). OWNED BY is not emitted
// here: sequences are created before tables, so the owning column may not
// exist yet — the caller emits a separate ALTER SEQUENCE ... OWNED BY.
func renderSequence(fq string, sq *schema.Sequence) string {
    parts := []string{"create sequence if not exists", pqIdent(fq)}
    if sq.Unlogged != nil && *sq.Unlogged { parts[0] = "create unlogged sequence if not exists" }
    if sq.As != "" { parts = append(parts, "as "+sq.As) }
    if sq.Increment != "" { parts = append(parts, "increment by "+sq.Increment) }
    if sq.MinValue != "" { parts = append(parts, "minvalue "+sq.MinValue) }
    if sq.MaxValue != "" { parts = append(parts, "maxvalue "+sq.MaxValue) }
    if sq.Start != "" { parts = append(parts, "start with "+sq.Start) }
    if sq.Cache != "" { parts = append(parts, "cache "+sq.Cache) }
    if sq.Cycle != nil && *sq.Cycle { parts = append(parts, "cycle") }
    return strings.Join(parts, " ") + ";"
}

// sequenceAlterStmts emits ALTER SEQUENCE for options the YAML sets that
// differ from the live sequence, and OWNED BY when the owner differs.
func sequenceAlterStmts(fq string, sq *schema.Sequence, ls *LiveSequence) []string {
    if ls == nil { return nil }
    opts := []string{}
    if sq.As != "" && normalizeColumnType(sq.As) != normalizeColumnType(ls.Type) { opts = append(opts, "as "+sq.As) }
    for _, o := range []struct{ clause, want, have string }{
        {"increment by", sq.Increment, ls.Increment},
        {"minvalue", sq.MinValue, ls.MinValue},
        {"maxvalue", sq.MaxValue, ls.MaxValue},
        {"start with", sq.Start, ls.Start},
        {"cache", sq.Cache, ls.Cache},
    } {
        if o.want != "" && strings.TrimSpace(o.want) != o.have { opts = append(opts, o.clause+" "+o.want) }
    }
    if sq.Cycle != nil && *sq.Cycle != ls.Cycle {
        if *sq.Cycle { opts = append(opts, "cycle") } else { opts = append(opts, "no cycle") }
    }
    out := []string{}
    if len(opts) > 0 { out = append(out, fmt.Sprintf("alter sequence %s %s;", pqIdent(fq), strings.Join(opts, " "))) }
    if sq.Unlogged != nil && *sq.Unlogged != ls.Unlogged {
        mode := "logged"
        if *sq.Unlogged { mode = "unlogged" }
        out = append(out, fmt.Sprintf("alter sequence %s set %s;", pqIdent(fq), mode))
    }
    if sq.OwnedBy != "" && qualifyOwnedBy(fq, sq.OwnedBy) != ls.Owner {
        out = append(out, fmt.Sprintf("alter sequence %s owned by %s;", pqIdent(fq), ownedByIdent(fq, sq.OwnedBy)))
    }
    return out
}

// qualifyOwnedBy returns sequence seq's OWNED BY target as
// "schema.table.column" (in seq's schema when unqualified, since PostgreSQL
// requires the two to share a schema), or "" for none.
func qualifyOwnedBy(seq, s string) string {
    schemaName, table, column, ok := schema.OwnedByParts(seq, s)
    if !ok { return "" }
    return schemaName + "." + table + "." + column
}

// domainCheckName is the name of a domain's single check: constraintName, or
// PostgreSQL's default <domain>_check.
func domainCheckName(fq string, dm *schema.Domain) string {
    if dm.ConstraintName != "" { return truncateIdent(dm.ConstraintName) }
    return truncateIdent(fq[strings.IndexByte(fq, '.')+1:] + "_check")
}

// desiredDomainChecks returns a domain's CHECK constraints by name: the
// single check plus the checks map.
func desiredDomainChecks(fq string, dm *schema.Domain) map[string]string {
    out := map[string]string{}
    if dm.Check != "" { out[domainCheckName(fq, dm)] = dm.Check }
    for n, e := range dm.Checks {
        if e != "" { out[truncateIdent(n)] = e }
    }
    return out
}

// domainAlterStmts reconciles an existing domain's default, NOT NULL and
// CHECK constraints. Checks are matched by name; a missing one is added,
// while replacing a changed one or dropping an undeclared one requires
// --unsafe (adding a check validates every column using the domain). A
// checks map makes the declared set authoritative; without one only the
// single check's name is managed. The base type and collation cannot be
// altered.
// domainTypeChanged reports whether a domain's declared base type differs
// from the live one (empty when unknown), ignoring case, quotes, alias
// spellings and an optional (see nameScope) or pg_catalog qualifier.
func domainTypeChanged(desired, live string) bool {
    if live == "" || desired == "" { return false }
    key := func(s string) string {
        s = strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(s), " ")), `"`, "")
        return normalizeColumnType(strings.TrimPrefix(strings.TrimPrefix(s, "public."), "pg_catalog."))
    }
    return key(desired) != key(live)
}

func domainAlterStmts(fq string, dm *schema.Domain, ld *LiveDomain, unsafe bool) []string {
    if ld == nil { return nil }
    target := "alter domain " + pqIdent(fq)
    out := []string{}
    switch {
    case dm.Default != "" && !defaultsEqual(dm.Default, ld.Default):
        out = append(out, fmt.Sprintf("%s set default %s;", target, dm.Default))
    case dm.Default == "" && ld.Default != "":
        out = append(out, target+" drop default;")
    }
    if dm.NotNull != ld.NotNull {
        if dm.NotNull { out = append(out, target+" set not null;") } else { out = append(out, target+" drop not null;") }
    } else if dm.NotNull && dm.NotNullName != "" && ld.NotNullName != "" && dm.NotNullName != ld.NotNullName {
        out = append(out, fmt.Sprintf("%s rename constraint %s to %s;", target, quoteIdent(ld.NotNullName), quoteIdent(dm.NotNullName)))
    }
    want := desiredDomainChecks(fq, dm)
    drop := func(name string) string { return fmt.Sprintf("%s drop constraint %s;", target, quoteIdent(name)) }
    if unsafe {
        for _, name := range sortedKeys(ld.Checks) {
            if _, ok := want[name]; ok { continue }
            if dm.Checks != nil || name == domainCheckName(fq, dm) { out = append(out, drop(name)) }
        }
    }
    for _, name := range sortedKeys(want) {
        add := fmt.Sprintf("%s add constraint %s check (%s);", target, quoteIdent(name), want[name])
        liveDef, exists := ld.Checks[name]
        switch {
        case !exists:
            out = append(out, add)
        case unsafe && !constraintDefsEqual("check ("+want[name]+")", liveDef):
            out = append(out, drop(name), add)
        }
    }
    return out
}

// renderPartitionBy renders "range ("col1", "col2")" etc. for PARTITION BY.
// Plain identifiers are quoted; anything else (an expression) passes through.
var quotedIdentRe = regexp.MustCompile(`^"(?:[^"]|"")+"$`)

func renderPartitionBy(pb *schema.PartitionBy) string {
    cols := make([]string, 0, len(pb.Columns))
    for _, c := range pb.Columns { cols = append(cols, partitionKeyExpr(c)) }
    return strings.ToLower(pb.Type) + " (" + strings.Join(cols, ", ") + ")"
}

// partitionKeyExpr quotes a partition key column unless it is quoted
// already or looks like an expression (contains parens, spaces, or an
// operator).
func partitionKeyExpr(s string) string {
    if quotedIdentRe.MatchString(s) { return s }
    if strings.ContainsAny(s, "() ,+-/*") {
        return "(" + s + ")"
    }
    return quoteIdent(s)
}

// inheritedColumn reports whether c, declared on dt, sets what dt keeps of
// a column it inherits rather than declaring the column: every column of a
// partition or typed table, a column the live table inherits, or a column
// of an inheritance child declared without a type.
func inheritedColumn(dt *schema.Table, c *schema.Column, lc *LiveColumn) bool {
    return dt.PartitionOf != "" || dt.Of != "" || (lc != nil && lc.Inherited) || (c.Type == "" && len(dt.Inherits) > 0)
}

// typedTableStmts links existing table lt to the composite type dt names
// with ALTER TABLE ... OF, which PostgreSQL allows only when the columns
// match the type exactly. A live typed table that dt doesn't type, or types
// differently, is unlinked with NOT OF under --unsafe; without it that is
// reported.
func typedTableStmts(fq string, dt *schema.Table, lt *LiveTable, unsafe bool, warn func(string)) []string {
    if lt.OfType != "" && (dt.Of == "" || !sameType(dt.Of, lt.OfType)) {
        if !unsafe {
            warn(fmt.Sprintf("%s is a typed table (OF %s), which the YAML doesn't declare; ALTER TABLE ... NOT OF needs --unsafe", fq, lt.OfType))
            return nil
        }
        out := []string{fmt.Sprintf("alter table %s not of;", pqIdent(fq))}
        if dt.Of != "" { out = append(out, fmt.Sprintf("alter table %s of %s;", pqIdent(fq), pqIdent(dt.Of))) }
        return out
    }
    if dt.Of != "" && lt.OfType == "" {
        return []string{fmt.Sprintf("alter table %s of %s;", pqIdent(fq), pqIdent(dt.Of))}
    }
    return nil
}

// sameType compares two "schema.name" type names, ignoring quotes and an
// optional qualifier (see nameScope).
func sameType(a, b string) bool {
    sc := activeScope()
    norm := func(s string) string {
        s = strings.ReplaceAll(strings.TrimSpace(s), `"`, "")
        if sch, name, ok := strings.Cut(s, "."); ok && sc.schemas[sch] { return name }
        return s
    }
    return norm(a) == norm(b)
}

// typedTableUses reports whether a live typed table is OF composite type fq,
// which makes ALTER TYPE ... ATTRIBUTE need CASCADE.
func typedTableUses(live *Live, fq string) bool {
    for _, lt := range live.Tables {
        if lt != nil && lt.OfType != "" && sameType(lt.OfType, fq) { return true }
    }
    return false
}

// inheritedColumnStmts sets what a partition or inheritance child declares
// for column cn it inherits: a default, NOT NULL (only when nullable is
// written, and only adding it), storage, compression, the statistics
// target and n_distinct. Settings left out are unmanaged, so the parent's
// copies stay. lc is the live column, nil for a table this plan creates.
func inheritedColumnStmts(fq, cn string, c *schema.Column, lc *LiveColumn) []string {
    target := fmt.Sprintf("alter table %s alter column %s", pqIdent(fq), quoteIdent(cn))
    if lc == nil { lc = &LiveColumn{Nullable: true} }
    out := []string{}
    if c.Default != "" && !defaultsEqual(c.Default, lc.Default) {
        out = append(out, fmt.Sprintf("%s set default %s;", target, c.Default))
    }
    if c.NullableSet && !c.Nullable && lc.Nullable {
        out = append(out, target+" set not null;")
    }
    if c.Storage != "" && c.Storage != lc.Storage {
        out = append(out, fmt.Sprintf("%s set storage %s;", target, c.Storage))
    }
    if c.Compression != "" {
        want := c.Compression
        if want == "default" { want = "" }
        if want != lc.Compression { out = append(out, fmt.Sprintf("%s set compression %s;", target, c.Compression)) }
    }
    if c.StatisticsTarget != nil && *c.StatisticsTarget != statisticsTargetOf(lc) {
        out = append(out, fmt.Sprintf("%s set statistics %d;", target, *c.StatisticsTarget))
    }
    return append(out, attributeOptionStmts(target, c, lc)...)
}

// attachesPartition reports whether the plan attaches existing table lt to
// the parent dt declares: dt is a partition, lt a table that isn't one yet.
// Its indexes, keys and foreign keys become the partition's copies of the
// parent's when it is attached, so they are not pruned.
func attachesPartition(dt *schema.Table, lt *LiveTable) bool {
    return dt.PartitionOf != "" && lt != nil && !lt.IsPartition && !lt.Partitioned
}

// partitionMoved reports whether a declared partition has a different parent
// or bound than the live one.
func partitionMoved(dt *schema.Table, lb LivePartitionBound) bool {
    if qualifyName(dt.PartitionOf) != qualifyName(lb.Parent) && !strings.EqualFold(dt.PartitionOf, lb.Parent) { return true }
    return !partitionBoundsEqual(renderPartitionBound(dt.Partition), lb.Bound)
}

// partitionBoundsEqual compares two bound clauses value by value: string
// literals by content, numbers numerically, dates and timestamps as times,
// and casts ignored, so '2024-01-01' matches ('2024-01-01 00:00:00').
func partitionBoundsEqual(a, b string) bool {
    ta, tb := boundTokens(a), boundTokens(b)
    if len(ta) != len(tb) { return false }
    for i := range ta {
        if ta[i] == tb[i] { continue }
        if x, errX := strconv.ParseFloat(ta[i], 64); errX == nil {
            if y, errY := strconv.ParseFloat(tb[i], 64); errY == nil && x == y { continue }
        }
        if x, zx, okX := boundTime(ta[i]); okX {
            if y, zy, okY := boundTime(tb[i]); okY && (zx && zy && x.Equal(y) || (!zx || !zy) && x.Format(time.DateTime+".999999") == y.Format(time.DateTime+".999999")) { continue }
        }
        return false
    }
    return true
}

// boundTokens splits a bound clause into lower-cased keywords and
// punctuation, and literal values with their quotes and casts removed.
func boundTokens(s string) []string {
    out := []string{}
    for i := 0; i < len(s); {
        c := s[i]
        switch {
        case c == ' ' || c == '\t' || c == '\n':
            i++
        case c == '\'':
            j, v := i+1, strings.Builder{}
            for j < len(s) {
                if s[j] == '\'' {
                    if j+1 < len(s) && s[j+1] == '\'' { v.WriteByte('\''); j += 2; continue }
                    break
                }
                v.WriteByte(s[j])
                j++
            }
            out = append(out, "'"+v.String())
            i = j + 1
        case c == ':' && i+1 < len(s) && s[i+1] == ':':
            j := i + 2
            for j < len(s) && s[j] != ',' && s[j] != ')' { j++ }
            i = j
        case c == '(' || c == ')' || c == ',':
            out = append(out, string(c))
            i++
        default:
            j := i
            for j < len(s) && !strings.ContainsRune(" \t\n'(),", rune(s[j])) && (s[j] != ':' || j+1 >= len(s) || s[j+1] != ':') { j++ }
            out = append(out, strings.ToLower(s[i:j]))
            i = j
        }
    }
    for i, t := range out {
        if strings.HasPrefix(t, "'") {
            if _, err := strconv.ParseFloat(t[1:], 64); err == nil { out[i] = t[1:] }
        }
    }
    return out
}

var boundTimeLayouts = []string{"2006-01-02", "2006-01-02 15:04:05", "2006-01-02 15:04:05.999999", "2006-01-02T15:04:05", "2006-01-02T15:04:05.999999"}

var boundZonedLayouts = []string{"2006-01-02 15:04:05-07", "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05.999999-07:00", "2006-01-02T15:04:05Z07:00"}

// boundTime parses a date or timestamp bound value, reporting whether it
// carries a zone offset.
func boundTime(t string) (v time.Time, zoned, ok bool) {
    t = strings.TrimPrefix(t, "'")
    for _, layout := range boundZonedLayouts {
        if v, err := time.Parse(layout, t); err == nil { return v, true, true }
    }
    for _, layout := range boundTimeLayouts {
        if v, err := time.Parse(layout, t); err == nil { return v, false, true }
    }
    return time.Time{}, false, false
}

// renderPartitionBound renders a partition child's bound clause:
// DEFAULT, FOR VALUES FROM/TO, FOR VALUES IN, or FOR VALUES WITH (hash).
func renderPartitionBound(ps *schema.PartitionSpec) string {
    if ps == nil || ps.Default { return "default" }
    if ps.Modulus >= 0 && ps.Remainder >= 0 {
        return fmt.Sprintf("for values with (modulus %d, remainder %d)", ps.Modulus, ps.Remainder)
    }
    if len(ps.In) > 0 {
        return "for values in (" + joinBoundList(ps.In) + ")"
    }
    return fmt.Sprintf("for values from (%s) to (%s)", joinBoundList(ps.From), joinBoundList(ps.To))
}

func joinBoundList(vals []string) string {
    out := make([]string, 0, len(vals))
    for _, v := range vals { out = append(out, partitionBoundLiteral(v)) }
    return strings.Join(out, ", ")
}

var sqlLiteralRe = regexp.MustCompile(`^'(?:[^']|'')*'(?:::[\w ."\[\]()]+)?$`)

// partitionBoundLiteral renders one bound value: MINVALUE/MAXVALUE keywords,
// numbers, booleans, NULL and values already written as SQL string literals
// ('2024-01-01', optionally with a cast) stay as they are; everything else
// becomes a string literal.
func partitionBoundLiteral(v string) string {
    t := strings.TrimSpace(v)
    if sqlLiteralRe.MatchString(t) { return t }
    switch strings.ToLower(t) {
    case "minvalue", "maxvalue", "true", "false", "null":
        return strings.ToLower(t)
    }
    if isNumericLiteral(t) { return t }
    return quoteString(t)
}

func isNumericLiteral(s string) bool {
    if s == "" { return false }
    dot := false
    for i, r := range s {
        switch {
        case r >= '0' && r <= '9':
        case r == '-' && i == 0:
        case r == '.' && !dot:
            dot = true
        default:
            return false
        }
    }
    return s != "-" && s != "." && s != "-."
}

// planPublications reconciles publications after every table create and
// alter. Membership changes emit ALTER PUBLICATION ... SET (or DROP when the
// desired publication has no members); option changes SET (...). Switching
// to or from FOR ALL TABLES needs DROP + CREATE, which briefly unpublishes
// every table, so it requires --unsafe, as does --prune dropping
// publications missing from a present publications block.
func planPublications(plan *PlanDiff, live *Live, desired *schema.Database, opts Options) {
    if desired.Publications == nil { return }
    for _, name := range sortedKeys(desired.Publications) {
        p := desired.Publications[name]
        if p == nil { continue }
        if p.GeneratedColumns == "stored" && live.ServerVersion > 0 && live.ServerVersion < 180000 {
            plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf("publication %s: generatedColumns: stored needs PostgreSQL 18, and the server is PostgreSQL %d; it is left unset", name, live.ServerVersion/10000)})
            cp := *p
            cp.GeneratedColumns = ""
            p = &cp
        }
        ident := quoteIdent(truncateIdent(name))
        lp := live.Publications[truncateIdent(name)]
        if lp != nil && lp.AllTables != p.AllTables {
            if !opts.Unsafe { continue }
            plan.Alters = append(plan.Alters, fmt.Sprintf("drop publication %s;", ident))
            lp = nil
        }
        if lp == nil {
            stmt := "create publication " + ident
            if p.AllTables {
                stmt += " for all tables"
            } else if members := publicationMembers(p); members != "" {
                stmt += " for " + members
            }
            if with := publicationWith(p, nil); with != "" { stmt += " with (" + with + ")" }
            plan.Alters = append(plan.Alters, stmt+";")
            if p.Comment != "" { plan.Alters = append(plan.Alters, fmt.Sprintf("comment on publication %s is %s;", ident, quoteString(p.Comment))) }
            if p.Owner != "" && resolveRole(p.Owner, live.CurrentUser) != live.CurrentUser {
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter publication %s owner to %s;", ident, grantRole(p.Owner)))
            }
            continue
        }
        if !p.AllTables && !publicationMembersMatch(p, lp) {
            if members := publicationMembers(p); members != "" {
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter publication %s set %s;", ident, members))
            } else {
                if len(lp.Tables) > 0 {
                    tables := sortedKeys(lp.Tables)
                    for i, t := range tables { tables[i] = pqIdent(t) }
                    plan.Alters = append(plan.Alters, fmt.Sprintf("alter publication %s drop table %s;", ident, strings.Join(tables, ", ")))
                }
                if len(lp.Schemas) > 0 {
                    schemas := make([]string, len(lp.Schemas))
                    for i, s := range lp.Schemas { schemas[i] = quoteIdent(s) }
                    plan.Alters = append(plan.Alters, fmt.Sprintf("alter publication %s drop tables in schema %s;", ident, strings.Join(schemas, ", ")))
                }
            }
        }
        if with := publicationWith(p, lp); with != "" {
            plan.Alters = append(plan.Alters, fmt.Sprintf("alter publication %s set (%s);", ident, with))
        }
        if p.Comment != "" && p.Comment != lp.Comment {
            plan.Alters = append(plan.Alters, fmt.Sprintf("comment on publication %s is %s;", ident, quoteString(p.Comment)))
        }
        if p.Owner != "" && resolveRole(p.Owner, live.CurrentUser) != lp.Owner {
            plan.Alters = append(plan.Alters, fmt.Sprintf("alter publication %s owner to %s;", ident, grantRole(p.Owner)))
        }
    }
    if !opts.Unsafe || !opts.Prune { return }
    for _, name := range sortedKeys(live.Publications) {
        if desired.Publications[name] == nil { plan.Drops = append(plan.Drops, fmt.Sprintf("drop publication %s;", quoteIdent(name))) }
    }
}

// publicationMembers renders the TABLE ... / TABLES IN SCHEMA ... list.
func publicationMembers(p *schema.Publication) string {
    parts := []string{}
    for _, t := range p.Tables {
        entry := "table " + pqIdent(t.Table)
        if t.Only { entry = "table only " + pqIdent(t.Table) }
        if len(t.Columns) > 0 { entry += " (" + joinIdentList(t.Columns) + ")" }
        if t.Where != "" { entry += " where (" + t.Where + ")" }
        parts = append(parts, entry)
    }
    for _, s := range p.Schemas { parts = append(parts, "tables in schema "+quoteIdent(s)) }
    return strings.Join(parts, ", ")
}

func publicationMembersMatch(p *schema.Publication, lp *LivePublication) bool {
    if len(p.Tables) != len(lp.Tables) { return false }
    for _, t := range p.Tables {
        lt, ok := lp.Tables[t.Table]
        if !ok || t.Only != lt.Only || !constraintDefsEqual(t.Where, lt.Where) { return false }
        want := append([]string(nil), t.Columns...)
        sort.Strings(want)
        if strings.Join(want, ",") != strings.Join(lt.Columns, ",") { return false }
    }
    want := append([]string(nil), p.Schemas...)
    sort.Strings(want)
    return strings.Join(want, ",") == strings.Join(lp.Schemas, ",")
}

// publicationGeneratedColumns returns lp's publish_generated_columns, none
// when the server predates the option.
func publicationGeneratedColumns(lp *LivePublication) string {
    if lp.GeneratedColumns == "" { return "none" }
    return lp.GeneratedColumns
}

// publicationWith renders the WITH options that differ from live (all set
// options when live is nil). A new publication publishes no generated
// columns by default, so generatedColumns: none needs no option.
func publicationWith(p *schema.Publication, lp *LivePublication) string {
    out := []string{}
    if p.Publish != nil {
        order := map[string]int{"insert": 0, "update": 1, "delete": 2, "truncate": 3}
        want := append([]string(nil), p.Publish...)
        sort.Slice(want, func(i, j int) bool { return order[want[i]] < order[want[j]] })
        if lp == nil || strings.Join(want, ", ") != strings.Join(lp.Publish, ", ") {
            out = append(out, "publish = "+quoteString(strings.Join(want, ", ")))
        }
    }
    if p.ViaPartitionRoot != nil && (lp == nil || *p.ViaPartitionRoot != lp.ViaRoot) {
        out = append(out, fmt.Sprintf("publish_via_partition_root = %t", *p.ViaPartitionRoot))
    }
    if g := p.GeneratedColumns; g != "" {
        if (lp == nil && g != "none") || (lp != nil && publicationGeneratedColumns(lp) != g) {
            out = append(out, "publish_generated_columns = "+g)
        }
    }
    return strings.Join(out, ", ")
}

// planEventTriggers creates missing event triggers and drops and recreates
// ones whose event, tags or function changed (metadata only, so not gated
// behind --unsafe), after every function create. Enabled state and comments
// are reconciled with ALTER / COMMENT. With a present eventTriggers block,
// --unsafe --prune drops undeclared ones that no extension owns.
func planEventTriggers(plan *PlanDiff, live *Live, desired *schema.Database, opts Options) {
    if desired.EventTriggers == nil { return }
    for _, name := range sortedKeys(desired.EventTriggers) {
        et := desired.EventTriggers[name]
        if et == nil || et.Event == "" || et.Function == "" { continue }
        ident := quoteIdent(truncateIdent(name))
        create := fmt.Sprintf("create event trigger %s on %s", ident, strings.ToLower(et.Event))
        if len(et.Tags) > 0 {
            tags := make([]string, len(et.Tags))
            for i, t := range et.Tags { tags[i] = quoteString(strings.ToUpper(strings.TrimSpace(t))) }
            create += " when tag in (" + strings.Join(tags, ", ") + ")"
        }
        fn := strings.TrimSuffix(strings.TrimSpace(et.Function), "()")
        create += " execute function " + pqIdent(qualifyName(fn)) + "();"
        le := live.EventTriggers[truncateIdent(name)]
        switch {
        case le == nil:
            plan.Creates = append(plan.Creates, create)
            le = &LiveEventTrigger{Enabled: "origin"}
        case !eventTriggerMatches(et, le):
            plan.Creates = append(plan.Creates, fmt.Sprintf("drop event trigger %s;", ident), create)
            le = &LiveEventTrigger{Enabled: "origin"}
        }
        if et.Enabled != "" && et.Enabled != le.Enabled {
            action := map[string]string{"origin": "enable", "disabled": "disable", "replica": "enable replica", "always": "enable always"}[et.Enabled]
            if action != "" { plan.Alters = append(plan.Alters, fmt.Sprintf("alter event trigger %s %s;", ident, action)) }
        }
        if et.Comment != "" && et.Comment != le.Comment {
            plan.Alters = append(plan.Alters, fmt.Sprintf("comment on event trigger %s is %s;", ident, quoteString(et.Comment)))
        }
    }
    if !opts.Unsafe || !opts.Prune { return }
    for _, name := range sortedKeys(live.EventTriggers) {
        if le := live.EventTriggers[name]; le.Extension || desired.EventTriggers[name] != nil { continue }
        plan.Drops = append(plan.Drops, fmt.Sprintf("drop event trigger %s;", quoteIdent(name)))
    }
}

func eventTriggerMatches(et *schema.EventTrigger, le *LiveEventTrigger) bool {
    if strings.ToLower(et.Event) != le.Event { return false }
    if qualifyName(strings.TrimSuffix(strings.TrimSpace(et.Function), "()")) != le.Function { return false }
    want := make([]string, len(et.Tags))
    for i, t := range et.Tags { want[i] = strings.ToUpper(strings.TrimSpace(t)) }
    have := append([]string(nil), le.Tags...)
    sort.Strings(want)
    sort.Strings(have)
    return strings.Join(want, ",") == strings.Join(have, ",")
}

// inCurrentDatabase wraps prefix (a format string whose %I names the
// database) plus clause in a DO block that runs it for current_database(),
// so committed migrations do not hardcode the database name.
func inCurrentDatabase(prefix, clause string) string {
    return "do " + dollarQuoteInline(fmt.Sprintf("begin execute format(%s, current_database()); end", quoteString(prefix+strings.ReplaceAll(clause, "%", "%%")))) + ";"
}

// planServers creates and reconciles the declared foreign servers. A
// changed wrapper or type cannot be altered: it is reported as a warning
// and the server gets no other statement until it has been recreated.
// Dropping undeclared servers is part of pruning.
func planServers(plan *PlanDiff, live *Live, desired *schema.Database) {
    for _, name := range sortedKeys(desired.Servers) {
        s := desired.Servers[name]
        if s == nil { continue }
        ident := quoteIdent(name)
        ls := live.Servers[name]
        if ls == nil {
            stmt := "create server " + ident
            if s.Type != "" { stmt += " type " + quoteString(s.Type) }
            if s.Version != "" { stmt += " version " + quoteString(s.Version) }
            plan.Creates = append(plan.Creates, stmt+" foreign data wrapper "+quoteIdent(s.Wrapper)+fdwOptionsClause(s.Options)+";")
        } else {
            if s.Wrapper != ls.Wrapper || s.Type != ls.Type {
                plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf("server %s: its foreign-data wrapper or type changed, which ALTER SERVER cannot do; drop and recreate it, with its foreign tables and user mappings, in a hand-written migration", name)})
                continue
            }
            if s.Version != "" && s.Version != ls.Version {
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter server %s version %s;", ident, quoteString(s.Version)))
            }
            if clause := fdwOptionsAlter(s.Options, ls.Options); clause != "" {
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter server %s %s;", ident, clause))
            }
        }
        if s.Owner != "" && (ls == nil || resolveRole(s.Owner, live.CurrentUser) != ls.Owner) {
            plan.Alters = append(plan.Alters, fmt.Sprintf("alter server %s owner to %s;", ident, grantRole(s.Owner)))
        }
        if s.Comment != "" && (ls == nil || s.Comment != ls.Comment) {
            plan.Alters = append(plan.Alters, fmt.Sprintf("comment on server %s is %s;", ident, quoteString(s.Comment)))
        }
        if !live.Offline && needsUserMapping(s.Wrapper) && (ls == nil || !ls.UserMapping) {
            plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf("server %s has no user mapping for the connected role or PUBLIC; create one outside pgy (its options hold credentials) before querying its foreign tables", name)})
        }
    }
}

// needsUserMapping reports whether foreign tables of a server on wrapper
// can only be read through a user mapping.
func needsUserMapping(wrapper string) bool { return wrapper == "postgres_fdw" }

// unmappedMatViewWarnings reports the materialized views this plan creates
// with data whose query reads a foreign table on a server without a user
// mapping. Populating such a view fails, and pgy cannot create the mapping:
// its options hold credentials.
func unmappedMatViewWarnings(live *Live, desired *schema.Database) []Warning {
    type remote struct {
        re     *regexp.Regexp
        table  string
        server string
        reason string
    }
    remotes := []remote{}
    for _, fq := range sortedKeys(desired.Tables) {
        dt := desired.Tables[fq]
        if dt == nil || dt.Foreign == nil || !strings.Contains(fq, ".") { continue }
        name := dt.Foreign.Server
        ls := live.Servers[name]
        wrapper, reason := "", "this plan creates without a user mapping"
        switch {
        case ls != nil && ls.UserMapping:
            continue
        case ls != nil:
            wrapper, reason = ls.Wrapper, "has no user mapping for the connected role or PUBLIC"
        case desired.Servers[name] != nil:
            wrapper = desired.Servers[name].Wrapper
        }
        if !needsUserMapping(wrapper) { continue }
        remotes = append(remotes, remote{regexp.MustCompile(`(?i)(?:^|[^\w."$])` + relationPattern(fq) + `(?:$|[^\w."$(])`), fq, name, reason})
    }
    out := []Warning{}
    if len(remotes) == 0 { return out }
    for _, k := range sortedKeys(desired.Views) {
        vw := desired.Views[k]
        if vw == nil || !vw.Materialized || vw.Query == "" || live.MatViews[k] || (vw.WithData != nil && !*vw.WithData) { continue }
        for _, r := range remotes {
            if !r.re.MatchString(vw.Query) { continue }
            out = append(out, Warning{Message: fmt.Sprintf("materialized view %s reads foreign table %s, whose server %s %s, so creating it with data fails; set withData: false and refresh it once the user mapping exists, or create the server and its user mapping before this migration runs", k, r.table, r.server, r.reason)})
        }
    }
    return out
}

// foreignKindMismatch reports whether dt declares a foreign table while lt
// is an ordinary one, or the reverse. PostgreSQL cannot turn one into the
// other, so the plan reports such a table and leaves it alone.
func foreignKindMismatch(dt *schema.Table, lt *LiveTable) bool {
    return dt != nil && lt != nil && (dt.Foreign != nil) != (lt.Foreign != nil)
}

// foreignTableAlterStmts reconciles a live foreign table's options and its
// columns' options. A changed server cannot be altered and is reported
// through warn.
func foreignTableAlterStmts(fq string, dt *schema.Table, lt *LiveTable, warn func(string)) []string {
    if lt.Foreign == nil { return nil }
    out := []string{}
    if dt.Foreign.Server != lt.Foreign.Server {
        warn(fmt.Sprintf("foreign table %s: server changed from %s to %s, which ALTER FOREIGN TABLE cannot do; drop and recreate it in a hand-written migration", fq, lt.Foreign.Server, dt.Foreign.Server))
    }
    if clause := fdwOptionsAlter(dt.Foreign.Options, lt.Foreign.Options); clause != "" {
        out = append(out, fmt.Sprintf("alter foreign table %s %s;", pqIdent(fq), clause))
    }
    for _, cn := range sortedKeys(dt.Columns) {
        c, lc := dt.Columns[cn], lt.Columns[cn]
        if c == nil || lc == nil { continue }
        if clause := fdwOptionsAlter(c.FDWOptions, lc.FDWOptions); clause != "" {
            out = append(out, fmt.Sprintf("alter foreign table %s alter column %s %s;", pqIdent(fq), quoteIdent(cn), clause))
        }
    }
    return out
}

// fdwOptionsClause renders " options (name 'value', ...)" for a CREATE, or
// "" when there are none.
func fdwOptionsClause(opts map[string]string) string {
    if len(opts) == 0 { return "" }
    parts := make([]string, 0, len(opts))
    for _, k := range sortedKeys(opts) { parts = append(parts, quoteIdent(k)+" "+quoteString(opts[k])) }
    return " options (" + strings.Join(parts, ", ") + ")"
}

// fdwOptionsAlter renders "options (add ..., set ..., drop ...)" turning
// have into want, or "" when they match. A nil want is unmanaged.
func fdwOptionsAlter(want, have map[string]string) string {
    if want == nil { return "" }
    parts := []string{}
    for _, k := range sortedKeys(want) {
        if v, ok := have[k]; !ok {
            parts = append(parts, "add "+quoteIdent(k)+" "+quoteString(want[k]))
        } else if v != want[k] {
            parts = append(parts, "set "+quoteIdent(k)+" "+quoteString(want[k]))
        }
    }
    for _, k := range sortedKeys(have) {
        if _, ok := want[k]; !ok { parts = append(parts, "drop "+quoteIdent(k)) }
    }
    if len(parts) == 0 { return "" }
    return "options (" + strings.Join(parts, ", ") + ")"
}

// hasForeignPartition reports whether partitioned table fq has a foreign
// table among its partitions, at any level.
func hasForeignPartition(live *Live, fq string) bool {
    for _, child := range live.Partitions[fq] {
        if lt := live.Tables[child]; lt != nil && lt.Foreign != nil { return true }
        if hasForeignPartition(live, child) { return true }
    }
    return false
}

// notNullNotValidName returns the constraint name to make existing column
// cn NOT NULL with, on PostgreSQL 18+, instead of SET NOT NULL, which scans
// the table under an ACCESS EXCLUSIVE lock: the plan adds the constraint
// NOT VALID (enforced for new rows, no scan) and validates it in the
// no-transaction migration, which scans under SHARE UPDATE EXCLUSIVE. It
// returns "" when SET NOT NULL is used: before PostgreSQL 18, on primary
// key, identity and generated columns, on foreign tables and partitions,
// and when a validated CHECK (cn IS NOT NULL) already lets PostgreSQL skip
// the scan.
// notValidNotNullStmt adds column cn's NOT NULL constraint NOT VALID, under
// its declared name or PostgreSQL's default one, for a column declared
// notValid.
func notValidNotNullStmt(fq, cn string, c *schema.Column) string {
    return fmt.Sprintf("alter table %s add constraint %s not null %s not valid;", pqIdent(fq), quoteIdent(notNullConstraintName(fq, cn, c)), quoteIdent(cn))
}

func notNullNotValidName(fq, cn string, c *schema.Column, lc *LiveColumn, lt *LiveTable, dt *schema.Table, pkCols map[string]bool, serverVersion int) string {
    switch {
    case serverVersion < 180000, c.Nullable, !lc.Nullable:
        return ""
    case c.PrimaryKey, pkCols[cn], c.Identity != "", c.Generated != "", isSerialType(c.Type):
        return ""
    case dt.Foreign != nil, dt.PartitionOf != "", lt.IsPartition, hasNotNullCheck(lt, cn):
        return ""
    }
    return notNullConstraintName(fq, cn, c)
}

// DefaultNotNullName returns the name PostgreSQL 18 gives the NOT NULL
// constraint of column cn of table fq when the column definition names
// none: <table>_<column>_not_null, shortening the longer of the two names
// first when that exceeds maxIdentifierLength.
func DefaultNotNullName(fq, cn string) string {
    _, table := splitQualified(fq)
    t, c := len(table), len(cn)
    for t+c > maxIdentifierLength-len("__not_null") {
        if t > c { t-- } else { c-- }
    }
    return clipBytes(table, t) + "_" + clipBytes(cn, c) + "_not_null"
}

// notNullConstraintName returns the name the plan gives column cn's NOT
// NULL constraint when it adds one with ADD CONSTRAINT: the declared
// notNullName, or PostgreSQL's default.
func notNullConstraintName(fq, cn string, c *schema.Column) string {
    if c.NotNullName != "" { return truncateIdent(c.NotNullName) }
    return DefaultNotNullName(fq, cn)
}

// notNullRename returns the name to rename the live NOT NULL constraint of
// a column to: its declared notNullName, when the constraint (PostgreSQL
// 18+) has another name. It returns "" when there is nothing to rename.
func notNullRename(c *schema.Column, lc *LiveColumn) string {
    name := truncateIdent(c.NotNullName)
    if c.Nullable || lc.Nullable || name == "" || lc.NotNullName == "" || lc.NotNullName == name { return "" }
    return name
}

// routineBodySQL renders a routine body for CREATE: a SQL-standard body
// (BEGIN ATOMIC / RETURN) as written, anything else as a dollar-quoted
// string after AS.
func routineBodySQL(language, body string) string {
    if schema.IsSQLStandardBody(language, body) {
        return strings.TrimRight(strings.TrimSpace(body), "; \n\t")
    }
    return "as " + dollarQuote(StoredRoutineBody(body))
}

// routineBodyChanged reports whether a routine's declared body differs from
// the live one. Without a declared body (hasBody false) it is unmanaged. A
// change between a string body and a SQL-standard one always counts; two
// SQL-standard bodies are compared only with replace, since PostgreSQL
// stores a rewritten form. String bodies are compared without their
// surrounding whitespace, so the newlines pgy puts around a body, now or in
// earlier versions, never count as a change.
func routineBodyChanged(language, body string, hasBody, replace bool, liveBody string, liveSQLBody bool) bool {
    if !hasBody { return false }
    standard := schema.IsSQLStandardBody(language, body)
    switch {
    case standard != liveSQLBody:
        return true
    case standard:
        return replace
    }
    return strings.TrimSpace(body) != strings.TrimSpace(liveBody)
}

// LivePrimaryKeyName returns the name of lt's primary key constraint, or ""
// when it has none (or its constraints are unknown).
func LivePrimaryKeyName(lt *LiveTable) string {
    if lt == nil { return "" }
    for _, name := range sortedKeys(lt.ConstraintDefs) {
        if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(lt.ConstraintDefs[name])), "PRIMARY KEY") { return name }
    }
    return ""
}

// DefaultPrimaryKeyName is the name PostgreSQL gives table fq's primary key
// when none is declared: <table>_pkey, truncated to fit.
func DefaultPrimaryKeyName(fq string) string {
    _, table := splitQualified(fq)
    return makeObjectName(table, "pkey")
}

// serialConversionStmts turns an existing plain column into the serial
// column c declares: it creates the column's <table>_<column>_seq owned by
// it, starts it past the column's largest value, and makes it the default.
func serialConversionStmts(fq, cn string, c *schema.Column, lc *LiveColumn) []string {
    if !isSerialType(c.Type) || lc.Identity != "" || lc.Generated != "" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(lc.Default)), "nextval(") {
        return nil
    }
    seqType := map[string]string{"smallserial": "smallint", "serial2": "smallint", "bigserial": "bigint", "serial8": "bigint"}[strings.ToLower(strings.TrimSpace(c.Type))]
    if seqType == "" { seqType = "integer" }
    nsp, table := splitQualified(fq)
    seq := nsp + "." + makeObjectName(table+"_"+cn, "seq")
    return []string{
        fmt.Sprintf("create sequence if not exists %s as %s owned by %s.%s;", pqIdent(seq), seqType, pqIdent(fq), quoteIdent(cn)),
        fmt.Sprintf("select setval(%s, (select coalesce(max(%s), 0) + 1 from %s), false);", quoteString(pqIdent(seq)), quoteIdent(cn), pqIdent(fq)),
        fmt.Sprintf("alter table %s alter column %s set default nextval(%s::regclass);", pqIdent(fq), quoteIdent(cn), quoteString(pqIdent(seq))),
    }
}

// keepsBodyChecks reports whether plan can create the routines in
// creates, the CREATE statements of its routines with a string body, with
// check_function_bodies on, so that the validator of their language reports
// a mistake in the body when the migration runs. That holds when the plan
// creates or replaces at most one routine and otherwise only changes
// privileges, comments, owners and routine attributes, or drops objects:
// nothing else in the plan can be what the body refers to.
func keepsBodyChecks(plan *PlanDiff, creates map[string]bool) bool {
    if len(creates) > 1 { return false }
    for _, sec := range [][]string{plan.Prelude, plan.Creates, plan.Alters} {
        for _, s := range sec {
            if !creates[s] && !bodyNeutral(s) { return false }
        }
    }
    return true
}

// bodyNeutral reports whether stmt, a planned statement, changes nothing a
// routine body can refer to.
func bodyNeutral(stmt string) bool {
    s := strings.ToLower(strings.TrimSpace(stmt))
    for _, prefix := range []string{"comment on ", "grant ", "revoke ", "alter function ", "alter procedure ", "alter default privileges ", "drop "} {
        if strings.HasPrefix(s, prefix) { return true }
    }
    return strings.HasPrefix(s, "alter ") && strings.Contains(s, " owner to ")
}

// uncheckedRoutineCreates puts the statements of creates that are in
// routines, CREATE statements of routines with a string body, between SET
// check_function_bodies = off and RESET, as pg_dump does: the validator of
// their language then leaves alone what a body refers to, such as a
// relation or routine created later in the same migration, or a class a
// PL/Java jar provides, and the body is checked when the routine first
// runs. Routines created one after another share one SET and RESET.
// SQL-standard bodies are parsed at CREATE whatever the setting, so they
// are not in routines: their dependencies must be created first.
func uncheckedRoutineCreates(creates []string, routines map[string]bool) []string {
    if len(routines) == 0 { return creates }
    out := make([]string, 0, len(creates)+2)
    unchecked := false
    for _, s := range creates {
        if routines[s] != unchecked {
            if unchecked { out = append(out, "reset check_function_bodies;") } else { out = append(out, "set check_function_bodies = off;") }
            unchecked = !unchecked
        }
        out = append(out, s)
    }
    if unchecked { out = append(out, "reset check_function_bodies;") }
    return out
}

// recreateChangedFunctions finds desired functions whose return type differs
// from live, which CREATE OR REPLACE cannot change. Under --unsafe each is
// planned as new: the returned live is a copy without it (or its comment,
// grants, owner and PUBLIC EXECUTE, which DROP FUNCTION discards), and
// recreated maps its normalized signature to the DROP FUNCTION to run first.
// Without --unsafe, live is returned unchanged with a warning per function.
func recreateChangedFunctions(live *Live, desired *schema.Database, unsafe bool) (out *Live, recreated map[string]string, warns []Warning) {
    out = live
    if live == nil || desired == nil { return out, nil, nil }
    for _, k := range sortedKeys(desired.Functions) {
        f := desired.Functions[k]
        if f == nil || !f.HasBody() { continue }
        fname := schema.RoutineName(k)
        norm := normalizeFunctionSignature(fname + f.ArgsSig)
        lf := live.FunctionDefs[norm]
        if lf == nil { continue }
        _, defaultsRemoved := argDefaultsDiff(f.ArgsSig, lf.Args)
        returnChanged := returnsChanged(f.Returns, lf.Returns)
        if !returnChanged && !defaultsRemoved { continue }
        if !unsafe {
            why := fmt.Sprintf("return type changed from %s to %s", lf.Returns, f.Returns)
            if !returnChanged { why = "an argument default was removed, which CREATE OR REPLACE can't do" }
            warns = append(warns, Warning{Message: fmt.Sprintf("function %s%s: %s; that drops and recreates the function, which needs --unsafe", fname, f.ArgsSig, why)})
            continue
        }
        if recreated == nil { recreated, out = map[string]string{}, cloneLive(live) }
        recreated[norm] = fmt.Sprintf("drop function %s;", pqIdent(fname)+grantFuncArgs(f.ArgsSig))
        delete(out.Functions, lf.Signature)
        delete(out.FunctionDefs, norm)
        delete(out.FunctionComments, norm)
        delete(out.FunctionGrants, norm)
        delete(out.FunctionPublicExec, norm)
        delete(out.Owners, "proc:"+norm)
    }
    for _, k := range sortedKeys(desired.Procedures) {
        p := desired.Procedures[k]
        if p == nil || !p.HasBody() { continue }
        pname := schema.RoutineName(k)
        norm := normalizeFunctionSignature(pname + p.ArgsSig)
        lp := live.ProcedureDefs[norm]
        if lp == nil { continue }
        if _, removed := argDefaultsDiff(p.ArgsSig, lp.Args); !removed { continue }
        if !unsafe {
            warns = append(warns, Warning{Message: fmt.Sprintf("procedure %s%s: an argument default was removed, which CREATE OR REPLACE can't do; that drops and recreates the procedure, which needs --unsafe", pname, p.ArgsSig)})
            if out == live { out = cloneLive(live) }
            out.ProcedureDefs[norm] = &LiveProcedure{Signature: lp.Signature, Body: p.Body, SQLBody: lp.SQLBody, Security: lp.Security, Config: lp.Config, Args: p.ArgsSig, Transform: schema.RoutineTransform(p.Language, p.Transform)}
            continue
        }
        if recreated == nil { recreated = map[string]string{} }
        if out == live { out = cloneLive(live) }
        recreated[norm] = fmt.Sprintf("drop procedure %s;", pqIdent(pname)+grantFuncArgs(p.ArgsSig))
        delete(out.Procedures, norm)
        delete(out.ProcedureDefs, norm)
        delete(out.FunctionComments, norm)
        delete(out.FunctionGrants, norm)
        delete(out.FunctionPublicExec, norm)
        delete(out.Owners, "proc:"+norm)
    }
    return out, recreated, warns
}

// argDefaultsDiff compares the argument defaults in a YAML signature with
// the live pg_get_function_arguments form. changed reports a default that
// was added or altered, which CREATE OR REPLACE can do; removed reports one
// the YAML no longer declares, which needs DROP. Both are false when the
// argument lists don't line up or live is unknown.
func argDefaultsDiff(desiredArgs, liveArgs string) (changed, removed bool) {
    d, l := argDefaults(desiredArgs), argDefaults(liveArgs)
    if strings.TrimSpace(liveArgs) == "" && len(d) > 0 || len(d) != len(l) { return false, false }
    for i := range d {
        switch {
        case l[i] != "" && d[i] == "":
            removed = true
        case d[i] != l[i] && (l[i] == "" || !defaultsEqual(d[i], l[i])):
            changed = true
        }
    }
    return changed, removed
}

// argDefaults returns the default expression of each argument in args (with
// or without its enclosing parentheses), "" for those without one, leaving
// out OUT arguments, which can't have defaults.
func argDefaults(args string) []string {
    args = strings.TrimSpace(args)
    if strings.HasPrefix(args, "(") && strings.HasSuffix(args, ")") { args = args[1 : len(args)-1] }
    out := []string{}
    for _, a := range splitTopLevelQuotedParens(args) {
        a = strings.TrimSpace(a)
        if a == "" { continue }
        if w := strings.Fields(a); len(w) > 1 && strings.EqualFold(w[0], "out") { continue }
        out = append(out, argDefault(a))
    }
    return out
}

// argDefault returns the expression after an argument's top-level DEFAULT
// or =, or "".
func argDefault(a string) string {
    _, def := splitArgDefault(a)
    return def
}

// splitArgDefault splits an argument at its top-level DEFAULT or = into the
// declaration before it and the default expression after it ("" if none).
func splitArgDefault(a string) (decl, def string) {
    depth, quote := 0, rune(0)
    for i, r := range a {
        switch {
        case quote != 0:
            if r == quote { quote = 0 }
        case r == '\'' || r == '"':
            quote = r
        case r == '(':
            depth++
        case r == ')':
            depth--
        case depth == 0 && r == '=':
            return strings.TrimSpace(a[:i]), strings.TrimSpace(a[i+1:])
        case depth == 0 && (r == 'd' || r == 'D') && i > 0 && (a[i-1] == ' ' || a[i-1] == '\t') && len(a) > i+8 && strings.EqualFold(a[i:i+7], "default") && (a[i+7] == ' ' || a[i+7] == '\t'):
            return strings.TrimSpace(a[:i]), strings.TrimSpace(a[i+8:])
        }
    }
    return strings.TrimSpace(a), ""
}


// recreateRenamedArguments finds desired functions and procedures that
// aren't live under their own signature but match a live routine's argument
// types: their argument names or modes changed, which CREATE OR REPLACE
// rejects. Under --unsafe each is planned as new, like
// recreateChangedFunctions does, with recreated holding the DROP of the live
// routine. Without --unsafe each is blocked (left out of the plan) with a
// warning, and its comment, grants and owner are compared with the live
// routine's.
func recreateRenamedArguments(live *Live, desired *schema.Database, unsafe bool) (out *Live, recreated map[string]string, blocked map[string]bool, warns []Warning) {
    out = live
    if live == nil || desired == nil { return out, nil, nil, nil }
    declared := map[string]bool{}
    for k, f := range desired.Functions {
        if f != nil { declared[normalizeFunctionSignature(schema.RoutineName(k)+f.ArgsSig)] = true }
    }
    for k, p := range desired.Procedures {
        if p != nil { declared[normalizeFunctionSignature(schema.RoutineName(k)+p.ArgsSig)] = true }
    }
    byIdentity := map[string]string{}
    for norm, lf := range live.FunctionDefs {
        if lf != nil && lf.Kind != "a" && !lf.Internal && !declared[norm] { byIdentity[routineIdentity(lf.Signature, false)] = norm }
    }
    for norm := range live.Procedures {
        if !declared[norm] { byIdentity["procedure "+routineIdentity(norm, true)] = norm }
    }
    check := func(kind, name, args string) {
        norm := normalizeFunctionSignature(name + args)
        if kind == "function" && live.FunctionDefs[norm] != nil || kind == "procedure" && live.Procedures[norm] { return }
        id := routineIdentity(name+args, kind == "procedure")
        if kind == "procedure" { id = "procedure " + id }
        lnorm, ok := byIdentity[id]
        if !ok { return }
        liveArgs := lnorm[strings.IndexByte(lnorm, '('):]
        if lf := live.FunctionDefs[lnorm]; lf != nil { liveArgs = lf.Signature[strings.IndexByte(lf.Signature, '('):] }
        if !unsafe {
            if blocked == nil { blocked = map[string]bool{} }
            if out == live { out = cloneLive(live) }
            blocked[norm] = true
            if c, ok := live.FunctionComments[lnorm]; ok { out.FunctionComments[norm] = c }
            if g, ok := live.FunctionGrants[lnorm]; ok { out.FunctionGrants[norm] = g }
            if e, ok := live.FunctionPublicExec[lnorm]; ok { out.FunctionPublicExec[norm] = e }
            if o, ok := live.Owners["proc:"+lnorm]; ok { out.Owners["proc:"+norm] = o }
            warns = append(warns, Warning{Message: fmt.Sprintf("%s %s%s: the live %s%s has the same argument types under other names or modes, which CREATE OR REPLACE can't change; that drops and recreates it, which needs --unsafe", kind, name, args, name, liveArgs)})
            return
        }
        if recreated == nil { recreated, out = map[string]string{}, cloneLive(live) }
        recreated[norm] = fmt.Sprintf("drop %s %s;", kind, pqIdent(name)+grantFuncArgs(liveArgs))
        if lf := out.FunctionDefs[lnorm]; lf != nil { delete(out.Functions, lf.Signature) }
        delete(out.FunctionDefs, lnorm)
        delete(out.Procedures, lnorm)
        delete(out.ProcedureDefs, lnorm)
        delete(out.FunctionComments, lnorm)
        delete(out.FunctionGrants, lnorm)
        delete(out.FunctionPublicExec, lnorm)
        delete(out.Owners, "proc:"+lnorm)
        delete(byIdentity, id)
    }
    for _, k := range sortedKeys(desired.Functions) {
        if f := desired.Functions[k]; f != nil && f.HasBody() { check("function", schema.RoutineName(k), f.ArgsSig) }
    }
    for _, k := range sortedKeys(desired.Procedures) {
        if p := desired.Procedures[k]; p != nil && p.HasBody() { check("procedure", schema.RoutineName(k), p.ArgsSig) }
    }
    return out, recreated, blocked, warns
}

// routineIdentity returns "name(types)" for a routine signature: the
// argument types PostgreSQL identifies the routine by, without argument
// names, modes or defaults, and without OUT arguments unless procedure is set
// (a procedure's OUT arguments are part of its identity).
func routineIdentity(sig string, procedure bool) string {
    open := strings.IndexByte(sig, '(')
    if open < 0 { return strings.ToLower(strings.TrimSpace(sig)) }
    name := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(sig[:open]), " ", ""))
    inner := strings.TrimSuffix(strings.TrimSpace(sig[open+1:]), ")")
    types := []string{}
    for _, a := range splitTopLevelQuotedParens(inner) {
        a, _ = splitArgDefault(a)
        words := strings.Fields(typmodRe.ReplaceAllString(a, ""))
        if len(words) == 0 { continue }
        mode := ""
        if len(words) > 1 {
            switch strings.ToLower(words[0]) {
            case "in", "out", "inout", "variadic":
                mode, words = strings.ToLower(words[0]), words[1:]
            }
        }
        if mode == "out" && !procedure { continue }
        if len(words) > 1 && !isMultiWordTypeStart(strings.ToLower(words[0]), strings.ToLower(words[1])) { words = words[1:] }
        types = append(types, normalizeColumnType(strings.TrimPrefix(strings.ToLower(strings.Join(words, " ")), "public.")))
    }
    return name + "(" + strings.Join(types, ", ") + ")"
}

// returnsChanged reports whether a function's declared return type differs
// from the live one. Only "[setof] type" forms are compared, ignoring schema
// qualifiers and type modifiers (PostgreSQL keeps neither in a function's
// result type); RETURNS TABLE and unknown live types never count as changed.
func returnsChanged(desired, live string) bool {
    a, ok := returnTypeKey(desired)
    if !ok { return false }
    b, ok := returnTypeKey(live)
    return ok && a != b
}

var typmodRe = regexp.MustCompile(`\([^()]*\)`)

func returnTypeKey(s string) (string, bool) {
    s = strings.ToLower(strings.Join(strings.Fields(s), " "))
    if s == "" || strings.HasPrefix(s, "table(") || strings.HasPrefix(s, "table ") { return "", false }
    setof := ""
    if rest, ok := strings.CutPrefix(s, "setof "); ok { setof, s = "setof ", rest }
    s = typmodRe.ReplaceAllString(s, "")
    s = strings.ReplaceAll(s, `"`, "")
    if i := strings.LastIndexByte(s, '.'); i >= 0 { s = s[i+1:] }
    return setof + normalizeColumnType(strings.TrimSpace(s)), true
}

// dollarTag returns a dollar-quote delimiter that does not occur in body:
// $$, else $body$, $body1$, ...
func dollarTag(body string) string {
    closes := func(tag string) bool { return strings.Index(body+tag, tag) == len(body) }
    if closes("$$") { return "$$" }
    for i := 0; ; i++ {
        tag := "$body$"
        if i > 0 { tag = fmt.Sprintf("$body%d$", i) }
        if closes(tag) { return tag }
    }
}

// dollarQuote quotes body exactly as given.
func dollarQuote(body string) string {
    tag := dollarTag(body)
    return tag + body + tag
}

// dollarQuoteInline quotes body on one line.
func dollarQuoteInline(body string) string {
    tag := dollarTag(body)
    return tag + " " + body + " " + tag
}

// qualifyName qualifies an unqualified object name with public. A name in
// double quotes is read with schema.SplitName.
func qualifyName(s string) string {
    if parts, ok := schema.SplitName(s); ok && len(parts) <= 2 {
        if len(parts) == 1 { return "public." + parts[0] }
        return parts[0] + "." + parts[1]
    }
    if strings.Contains(s, ".") { return s }
    return "public." + s
}

// aggregateArgs maps the (*) argument list of a count(*)-style aggregate to
// the empty list PostgreSQL reports for it.
func aggregateArgs(args string) string {
    if strings.ReplaceAll(args, " ", "") == "(*)" { return "()" }
    return args
}

// renderAggregate emits CREATE OR REPLACE AGGREGATE with the options set.
func renderAggregate(name string, ag *schema.Aggregate) string {
    opts := []string{"sfunc = " + ag.SFunc, "stype = " + ag.SType}
    if ag.FinalFunc != "" { opts = append(opts, "finalfunc = "+ag.FinalFunc) }
    if ag.CombineFunc != "" { opts = append(opts, "combinefunc = "+ag.CombineFunc) }
    if ag.HasInitCond() { opts = append(opts, "initcond = "+quoteString(ag.InitCond)) }
    if ag.Parallel != "" { opts = append(opts, "parallel = "+ag.Parallel) }
    for _, o := range schema.AggregateOptions {
        v, ok := ag.Options[o[1]]
        switch {
        case !ok:
        case o[1] == "minitcond":
            opts = append(opts, "minitcond = "+quoteString(v))
        case v == "":
        case strings.HasSuffix(o[1], "_extra") || o[1] == "hypothetical":
            if strings.EqualFold(v, "true") { opts = append(opts, o[1]) }
        case o[1] == "sortop" && strings.Contains(v, ".") && !strings.HasPrefix(strings.ToLower(v), "operator("):
            opts = append(opts, "sortop = operator("+v+")")
        default:
            opts = append(opts, o[1]+" = "+v)
        }
    }
    return fmt.Sprintf("create or replace aggregate %s%s (%s);", pqIdent(name), ag.ArgsSig, strings.Join(opts, ", "))
}

// aggregateOptionsMatch compares the optional aggregate options the YAML
// sets with live; options it leaves out are unmanaged.
func aggregateOptionsMatch(ag *schema.Aggregate, la *LiveAggregate, fn func(string) string) bool {
    for k, want := range ag.Options {
        have := la.Options[k]
        switch {
        case strings.HasSuffix(k, "_extra") || k == "hypothetical":
            if strings.EqualFold(want, "true") != strings.EqualFold(have, "true") { return false }
        case strings.HasSuffix(k, "_modify"):
            if want == "" { want = "read_only" }
            if want != have { return false }
        case strings.HasSuffix(k, "space"):
            if want == "" { want = "0" }
            if want != have { return false }
        case k == "mstype":
            if normalizeColumnType(want) != normalizeColumnType(have) { return false }
        case k == "minitcond":
            if !initCondMatches(want, true, have, la.HasMInitCond) { return false }
        case k == "sortop":
            op := func(s string) string {
                s = strings.TrimSpace(s)
                if l := strings.ToLower(s); strings.HasPrefix(l, "operator(") && strings.HasSuffix(s, ")") { s = s[len("operator(") : len(s)-1] }
                return strings.TrimPrefix(activeScope().unqualifyFolded(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), `"`, ""))), "pg_catalog.")
            }
            if op(want) != op(have) { return false }
        default:
            if fn(want) != fn(have) { return false }
        }
    }
    return true
}

// initCondMatches compares a declared initial condition with the live one.
// The empty string is a condition of its own, so a declared one (wantSet)
// does not match an aggregate that has none (haveSet false). An undeclared
// condition still matches a live empty one, which pgy versions that could
// not express it left out of pulled YAML.
func initCondMatches(want string, wantSet bool, have string, haveSet bool) bool {
    return want == have && (want != "" || !wantSet || haveSet)
}

// aggregateMatchesLive compares an aggregate's functions (ignoring optional
// and pg_catalog qualifiers), state type, initial condition and, when set,
// parallel safety.
func aggregateMatchesLive(ag *schema.Aggregate, la *LiveAggregate) bool {
    fn := func(s string) string {
        s = strings.ToLower(strings.ReplaceAll(strings.TrimSuffix(strings.TrimSpace(s), "()"), `"`, ""))
        return strings.TrimPrefix(activeScope().unqualifyFolded(s), "pg_catalog.")
    }
    return fn(ag.SFunc) == fn(la.SFunc) && normalizeColumnType(ag.SType) == normalizeColumnType(la.SType) &&
        fn(ag.FinalFunc) == fn(la.FinalFunc) && fn(ag.CombineFunc) == fn(la.CombineFunc) &&
        initCondMatches(ag.InitCond, ag.InitCondSet, la.InitCond, la.HasInitCond) && (ag.Parallel == "" || ag.Parallel == la.Parallel) && aggregateOptionsMatch(ag, la, fn)
}

// tsDictionaryAlterStmts brings live dictionary ld's options in line with
// td's: ALTER TEXT SEARCH DICTIONARY sets changed options and drops those td
// doesn't list. Options are unmanaged when td has none. A changed template
// can't be altered, so it is reported instead.
func tsDictionaryAlterStmts(fq string, td *schema.TSDictionary, ld *schema.TSDictionary) ([]string, string) {
    if ld == nil { return nil, "" }
    warn := ""
    if t := strings.TrimPrefix(td.Template, "pg_catalog."); !tsTemplatesEqual(t, ld.Template) {
        warn = fmt.Sprintf("text search dictionary %s: its template changed from %s to %s, which ALTER cannot do; drop and recreate it, with the configurations that use it, in a hand-written migration", fq, ld.Template, t)
    }
    if td.Options == nil { return nil, warn }
    opts := []string{}
    for _, k := range sortedKeys(td.Options) {
        if v, ok := ld.Options[strings.ToLower(k)]; !ok || !strings.EqualFold(normalizeConfigValue(v), normalizeConfigValue(td.Options[k])) {
            opts = append(opts, k+" = "+td.Options[k])
        }
    }
    for _, k := range sortedKeys(ld.Options) {
        found := false
        for w := range td.Options { found = found || strings.EqualFold(w, k) }
        if !found { opts = append(opts, k) }
    }
    if len(opts) == 0 { return nil, warn }
    return []string{fmt.Sprintf("alter text search dictionary %s (%s);", pqIdent(fq), strings.Join(opts, ", "))}, warn
}

// tsConfigStmts creates a missing text search configuration (as a copy or
// from a parser) and reconciles the token type mappings the YAML manages.
// For a new configuration the copied mappings are unknown, so each managed
// mapping is dropped (if present) and added.
func tsConfigStmts(fq string, tc *schema.TSConfig, live map[string][]string) []string {
    out := []string{}
    target := "alter text search configuration " + pqIdent(fq)
    if live == nil {
        switch {
        case tc.Copy != "":
            out = append(out, fmt.Sprintf("create text search configuration %s (copy = %s);", pqIdent(fq), pqIdent(tc.Copy)))
        case tc.Parser != "":
            out = append(out, fmt.Sprintf("create text search configuration %s (parser = %s);", pqIdent(fq), pqIdent(tc.Parser)))
        default:
            return nil
        }
        for _, tok := range sortedKeys(tc.Mappings) {
            out = append(out, fmt.Sprintf("%s drop mapping if exists for %s;", target, tok))
            if dicts := tc.Mappings[tok]; len(dicts) > 0 {
                out = append(out, fmt.Sprintf("%s add mapping for %s with %s;", target, tok, joinTSDicts(dicts)))
            }
        }
        return out
    }
    for _, tok := range sortedKeys(tc.Mappings) {
        want, have := tc.Mappings[tok], live[tok]
        switch {
        case len(want) == 0 && len(have) > 0:
            out = append(out, fmt.Sprintf("%s drop mapping for %s;", target, tok))
        case len(want) > 0 && len(have) == 0:
            out = append(out, fmt.Sprintf("%s add mapping for %s with %s;", target, tok, joinTSDicts(want)))
        case len(want) > 0 && !sameTSDicts(want, have):
            out = append(out, fmt.Sprintf("%s alter mapping for %s with %s;", target, tok, joinTSDicts(want)))
        }
    }
    return out
}

func joinTSDicts(dicts []string) string {
    parts := make([]string, len(dicts))
    for i, d := range dicts {
        if names, ok := schema.SplitName(d); ok {
            for j, n := range names { names[j] = quoteIdent(n) }
            parts[i] = strings.Join(names, ".")
            continue
        }
        parts[i] = pqIdent(strings.TrimSpace(d))
    }
    return strings.Join(parts, ", ")
}

// sameTSDicts compares dictionary lists by unqualified name, in order.
func sameTSDicts(want, have []string) bool {
    if len(want) != len(have) { return false }
    plain := func(d string) string {
        if names, ok := schema.SplitName(d); ok { return strings.Join(names, ".") }
        return d
    }
    for i := range want {
        if collationName(plain(want[i])) != collationName(plain(have[i])) { return false }
    }
    return true
}

// renderCollation emits CREATE COLLATION IF NOT EXISTS, copying From when
// set, else with the provider options set in YAML.
func renderCollation(fq string, co *schema.Collation) string {
    if co.From != "" { return fmt.Sprintf("create collation if not exists %s from %s;", pqIdent(fq), pqIdent(co.From)) }
    opts := []string{}
    for _, o := range [][2]string{{"provider", co.Provider}, {"locale", co.Locale}, {"lc_collate", co.LcCollate}, {"lc_ctype", co.LcCtype}, {"rules", co.Rules}} {
        if o[1] == "" { continue }
        if o[0] == "provider" { opts = append(opts, "provider = "+o[1]); continue }
        opts = append(opts, o[0]+" = "+quoteString(o[1]))
    }
    if co.Deterministic != nil { opts = append(opts, fmt.Sprintf("deterministic = %t", *co.Deterministic)) }
    return fmt.Sprintf("create collation if not exists %s (%s);", pqIdent(fq), strings.Join(opts, ", "))
}

// renderDomain emits CREATE DOMAIN with only the options set in YAML.
// CREATE DOMAIN has no IF NOT EXISTS; existence is guarded by live.Domains.
func renderDomain(fq string, dm *schema.Domain) string {
    parts := []string{"create domain", pqIdent(fq), "as", dm.Type}
    if dm.Collate != "" { parts = append(parts, "collate "+collationIdent(dm.Collate)) }
    if dm.Default != "" { parts = append(parts, "default "+dm.Default) }
    if dm.NotNull && dm.NotNullName != "" { parts = append(parts, "constraint "+quoteIdent(dm.NotNullName)) }
    if dm.NotNull { parts = append(parts, "not null") }
    if dm.Check != "" {
        if dm.ConstraintName != "" { parts = append(parts, "constraint "+quoteIdent(dm.ConstraintName)) }
        parts = append(parts, fmt.Sprintf("check (%s)", dm.Check))
    }
    for _, n := range sortedKeys(dm.Checks) {
        if dm.Checks[n] != "" { parts = append(parts, fmt.Sprintf("constraint %s check (%s)", quoteIdent(n), dm.Checks[n])) }
    }
    return strings.Join(parts, " ") + ";"
}

// ownedByIdent quotes sequence seq's table.column (or schema.table.column)
// OWNED BY target, schema-qualified, read as schema.OwnedByParts reads it.
func ownedByIdent(seq, s string) string {
    schemaName, table, column, ok := schema.OwnedByParts(seq, s)
    if !ok { return "none" }
    return quoteIdent(schemaName) + "." + quoteIdent(table) + "." + quoteIdent(column)
}

// planComments emits COMMENT ON for objects whose desired comment is set and
// differs from live. Empty desired comment = unmanaged (never cleared).
func planComments(live *Live, desired *schema.Database, unsafe bool) []string {
    stmts := []string{}
    comment := func(target, want, have string) {
        if want != "" && want != have {
            stmts = append(stmts, fmt.Sprintf("comment on %s is %s;", target, quoteString(want)))
        }
    }

    schemaNames := make([]string, 0, len(desired.SchemaComments))
    for s := range desired.SchemaComments { schemaNames = append(schemaNames, s) }
    sort.Strings(schemaNames)
    for _, s := range schemaNames {
        comment("schema "+quoteIdent(s), desired.SchemaComments[s], live.SchemaComments[s])
    }

    roleNames := make([]string, 0, len(desired.Roles))
    for k := range desired.Roles { roleNames = append(roleNames, k) }
    sort.Strings(roleNames)
    for _, k := range roleNames {
        r := desired.Roles[k]
        if r == nil { continue }
        comment("role "+grantRole(k), r.Comment, live.RoleComments[k])
    }

    typeNames := make([]string, 0, len(desired.Types))
    for k := range desired.Types { typeNames = append(typeNames, k) }
    sort.Strings(typeNames)
    for _, k := range typeNames {
        td := desired.Types[k]
        if td == nil { continue }
        comment("type "+pqIdent(k), td.Comment, live.TypeComments[k])
        for _, a := range sortedKeys(td.AttributeComments) {
            comment("column "+pqIdent(k)+"."+quoteIdent(a), td.AttributeComments[a], live.CompositeComments[k][a])
        }
    }

    for _, k := range sortedKeys(desired.Collations) {
        if co := desired.Collations[k]; co != nil {
            comment("collation "+pqIdent(k), co.Comment, live.CollationComments[k])
        }
    }
    for _, k := range sortedKeys(desired.TSDictionaries) {
        if td := desired.TSDictionaries[k]; td != nil {
            comment("text search dictionary "+pqIdent(k), td.Comment, live.TSComments["dict:"+k])
        }
    }
    for _, k := range sortedKeys(desired.TSConfigs) {
        if tc := desired.TSConfigs[k]; tc != nil {
            comment("text search configuration "+pqIdent(k), tc.Comment, live.TSComments["config:"+k])
        }
    }

    domainNames := make([]string, 0, len(desired.Domains))
    for k := range desired.Domains { domainNames = append(domainNames, k) }
    sort.Strings(domainNames)
    for _, k := range domainNames {
        dm := desired.Domains[k]
        if dm == nil { continue }
        // domain comments live in pg_type's pg_description entries
        comment("domain "+pqIdent(k), dm.Comment, live.TypeComments[k])
        var liveCC map[string]string
        if ld := live.DomainDefs[k]; ld != nil { liveCC = ld.CheckComments }
        for _, cn := range sortedKeys(dm.CheckComments) {
            comment(fmt.Sprintf("constraint %s on domain %s", quoteIdent(truncateIdent(cn)), pqIdent(k)), dm.CheckComments[cn], liveCC[truncateIdent(cn)])
        }
    }

    tableNames := make([]string, 0, len(desired.Tables))
    for k := range desired.Tables { tableNames = append(tableNames, k) }
    sort.Strings(tableNames)
    for _, k := range tableNames {
        dt := desired.Tables[k]
        if dt == nil || foreignKindMismatch(dt, live.Tables[k]) { continue }
        kind := "table "
        if dt.Foreign != nil { kind = "foreign table " }
        comment(kind+pqIdent(k), dt.Comment, live.RelComments[k])
        colNames := make([]string, 0, len(dt.Columns))
        for cn := range dt.Columns { colNames = append(colNames, cn) }
        sort.Strings(colNames)
        for _, cn := range colNames {
            c := dt.Columns[cn]
            if c == nil || c.Comment == "" { continue }
            have := ""
            if lt := live.Tables[k]; lt != nil {
                if lc := lt.Columns[cn]; lc != nil { have = lc.Comment }
            }
            comment(fmt.Sprintf("column %s.%s", pqIdent(k), quoteIdent(cn)), c.Comment, have)
        }
        lt := live.Tables[k]
        if dt.PrimaryKeyComment != "" {
            name, have := truncateIdent(dt.PrimaryKeyName), ""
            if ln := LivePrimaryKeyName(lt); ln != "" {
                have = lt.ConstraintComments[ln]
                if name == "" { name = ln }
            }
            if name == "" { name = DefaultPrimaryKeyName(k) }
            comment(fmt.Sprintf("constraint %s on %s", quoteIdent(name), pqIdent(k)), dt.PrimaryKeyComment, have)
        }
        for _, fk := range dt.ForeignKeys {
            if fk == nil || fk.Comment == "" || len(fk.Columns) == 0 || fk.RefTable == "" { continue }
            fkName := truncateIdent(fk.Name)
            have := ""
            if lt != nil && !constraintRecreated(lt, fkName, renderFKDef(fk), unsafe) { have = lt.ConstraintComments[fkName] }
            comment(fmt.Sprintf("constraint %s on %s", quoteIdent(fkName), pqIdent(k)), fk.Comment, have)
        }
        for _, ct := range dt.Constraints {
            if ct == nil || ct.Comment == "" || renderConstraintDef(ct) == "" { continue }
            name := truncateIdent(ct.Name)
            have := ""
            if lt != nil && (!unsafe || !lt.Constraints[name] || lt.ConstraintDefs[name] == "" || constraintMatchesLive(lt, name, ct)) { have = lt.ConstraintComments[name] }
            comment(fmt.Sprintf("constraint %s on %s", quoteIdent(name), pqIdent(k)), ct.Comment, have)
        }
        for _, tr := range dt.Triggers {
            if tr == nil || tr.Comment == "" || tr.Procedure == "" { continue }
            name := truncateIdent(tr.Name)
            have := ""
            if lt != nil {
                if def, ok := lt.Triggers[name]; ok && (def == "" || triggerDefsEqual(renderTrigger(k, tr), def)) { have = lt.TriggerComments[name] }
            }
            comment(fmt.Sprintf("trigger %s on %s", quoteIdent(name), pqIdent(k)), tr.Comment, have)
        }
        for _, r := range dt.Rules {
            if r == nil || r.Comment == "" || r.Do == "" { continue }
            name := truncateIdent(r.Name)
            have := ""
            if lt != nil {
                if lr := lt.Rules[name]; lr != nil && !r.Replace && ruleMatchesLive(r, lr) { have = lt.RuleComments[name] }
            }
            comment(fmt.Sprintf("rule %s on %s", quoteIdent(name), pqIdent(k)), r.Comment, have)
        }
        for _, st := range dt.Statistics {
            if st == nil || st.Comment == "" || len(st.Columns) == 0 { continue }
            name := truncateIdent(st.Name)
            have := ""
            if lt != nil {
                if ls := lt.Statistics[name]; ls != nil && statisticMatchesLive(st, ls) { have = ls.Comment }
            }
            comment("statistics "+pqIdent(schemaPrefixOf(k)+name), st.Comment, have)
        }
        for _, pol := range dt.Policies {
            if pol == nil || pol.Comment == "" || pol.Name == "" { continue }
            name := truncateIdent(pol.Name)
            have := ""
            if lt != nil {
                if lp := lt.Policies[name]; lp != nil && policyMatchesLive(pol, lp, live.CurrentUser) { have = lt.PolicyComments[name] }
            }
            comment(fmt.Sprintf("policy %s on %s", quoteIdent(name), pqIdent(k)), pol.Comment, have)
        }
    }

    viewNames := make([]string, 0, len(desired.Views))
    for k := range desired.Views { viewNames = append(viewNames, k) }
    sort.Strings(viewNames)
    for _, k := range viewNames {
        vw := desired.Views[k]
        if vw == nil { continue }
        kind := "view "
        if vw.Materialized { kind = "materialized view " }
        comment(kind+pqIdent(k), vw.Comment, live.RelComments[k])
        for _, col := range sortedKeys(vw.ColumnComments) {
            comment(fmt.Sprintf("column %s.%s", pqIdent(k), quoteIdent(col)), vw.ColumnComments[col], live.ViewColumnComments[k][col])
        }
        if vt := live.ViewTables[k]; !vw.Materialized {
            for _, tr := range vw.Triggers {
                if tr == nil || tr.Comment == "" || tr.Procedure == "" { continue }
                name := truncateIdent(tr.Name)
                have := ""
                if vt != nil {
                    if def, ok := vt.Triggers[name]; ok && (def == "" || triggerDefsEqual(renderTrigger(k, tr), def)) { have = vt.TriggerComments[name] }
                }
                comment(fmt.Sprintf("trigger %s on %s", quoteIdent(name), pqIdent(k)), tr.Comment, have)
            }
        }
        if vt := live.ViewTables[k]; !vw.Materialized {
            for _, r := range vw.Rules {
                if r == nil || r.Comment == "" || r.Do == "" { continue }
                name := truncateIdent(r.Name)
                have := ""
                if vt != nil {
                    if lr := vt.Rules[name]; lr != nil && !r.Replace && ruleMatchesLive(r, lr) { have = vt.RuleComments[name] }
                }
                comment(fmt.Sprintf("rule %s on %s", quoteIdent(name), pqIdent(k)), r.Comment, have)
            }
        }
    }

    seqNames := make([]string, 0, len(desired.Sequences))
    for k := range desired.Sequences { seqNames = append(seqNames, k) }
    sort.Strings(seqNames)
    for _, k := range seqNames {
        sq := desired.Sequences[k]
        if sq == nil { continue }
        comment("sequence "+pqIdent(k), sq.Comment, live.RelComments[k])
    }

    funcNames := make([]string, 0, len(desired.Functions))
    for k := range desired.Functions { funcNames = append(funcNames, k) }
    sort.Strings(funcNames)
    for _, k := range funcNames {
        f := desired.Functions[k]
        if f == nil { continue }
        name := schema.RoutineName(k)
        norm := normalizeFunctionSignature(name + f.ArgsSig)
        comment("function "+pqIdent(name)+grantFuncArgs(f.ArgsSig), f.Comment, live.FunctionComments[norm])
    }

    procNames := make([]string, 0, len(desired.Procedures))
    for k := range desired.Procedures { procNames = append(procNames, k) }
    sort.Strings(procNames)
    for _, k := range procNames {
        pr := desired.Procedures[k]
        if pr == nil { continue }
        // procedure comments share pg_proc's pg_description entries
        name := schema.RoutineName(k)
        norm := normalizeFunctionSignature(name + pr.ArgsSig)
        comment("procedure "+pqIdent(name)+grantFuncArgs(pr.ArgsSig), pr.Comment, live.FunctionComments[norm])
    }
    for _, k := range sortedKeys(desired.Aggregates) {
        ag := desired.Aggregates[k]
        if ag == nil { continue }
        name := schema.RoutineName(k)
        comment("aggregate "+pqIdent(name)+ag.ArgsSig, ag.Comment, live.FunctionComments[normalizeFunctionSignature(name+aggregateArgs(ag.ArgsSig))])
    }
    return stmts
}

// constraintRecreated reports whether applyTableConstraints drops and
// re-adds a live constraint (losing its comment) because its definition
// changed.
func constraintRecreated(lt *LiveTable, name, def string, unsafe bool) bool {
    liveDef, ok := lt.ConstraintDefs[name]
    return unsafe && ok && lt.Constraints[name] && !constraintDefsEqual(def, liveDef)
}

// planOwners emits ALTER ... OWNER TO for objects whose YAML owner differs
// from the live owner (or that this plan creates). It runs before grants,
// since a new owner changes who holds the implicit owner privileges.
func planOwners(live *Live, desired *schema.Database) []string {
    out := []string{}
    owner := func(kind, target, want, liveKey string) {
        if want == "" || live.Owners[liveKey] == resolveRole(want, live.CurrentUser) { return }
        out = append(out, fmt.Sprintf("alter %s %s owner to %s;", kind, target, grantRole(want)))
    }
    for _, s := range sortedKeys(desired.SchemaOwners) {
        owner("schema", quoteIdent(s), desired.SchemaOwners[s], "schema:"+s)
    }
    for _, k := range sortedKeys(desired.Types) {
        if td := desired.Types[k]; td != nil { owner("type", pqIdent(k), td.Owner, "type:"+k) }
    }
    for _, k := range sortedKeys(desired.Domains) {
        if dm := desired.Domains[k]; dm != nil { owner("domain", pqIdent(k), dm.Owner, "type:"+k) }
    }
    for _, k := range sortedKeys(desired.Sequences) {
        if sq := desired.Sequences[k]; sq != nil { owner("sequence", pqIdent(k), sq.Owner, "rel:"+k) }
    }
    for _, k := range sortedKeys(desired.Tables) {
        if dt := desired.Tables[k]; dt != nil { owner("table", pqIdent(k), dt.Owner, "rel:"+k) }
    }
    for _, k := range sortedKeys(desired.Views) {
        if vw := desired.Views[k]; vw != nil {
            kind := "view"
            if vw.Materialized { kind = "materialized view" }
            owner(kind, pqIdent(k), vw.Owner, "rel:"+k)
        }
    }
    for _, k := range sortedKeys(desired.Functions) {
        if f := desired.Functions[k]; f != nil {
            name := schema.RoutineName(k)
            owner("function", pqIdent(name)+grantFuncArgs(f.ArgsSig), f.Owner, "proc:"+normalizeFunctionSignature(name+f.ArgsSig))
        }
    }
    for _, k := range sortedKeys(desired.Aggregates) {
        if ag := desired.Aggregates[k]; ag != nil {
            name := schema.RoutineName(k)
            owner("aggregate", pqIdent(name)+ag.ArgsSig, ag.Owner, "proc:"+normalizeFunctionSignature(name+aggregateArgs(ag.ArgsSig)))
        }
    }
    for _, k := range sortedKeys(desired.Procedures) {
        if p := desired.Procedures[k]; p != nil {
            name := schema.RoutineName(k)
            owner("procedure", pqIdent(name)+grantFuncArgs(p.ArgsSig), p.Owner, "proc:"+normalizeFunctionSignature(name+p.ArgsSig))
        }
    }
    return out
}

// defaultACLImplicit is what a role's objects of each kind grant PUBLIC when
// no global pg_default_acl row overrides it.
var defaultACLImplicit = map[string]string{"functions": "execute", "types": "usage"}

// planDefaultPrivileges reconciles ALTER DEFAULT PRIVILEGES for the roles
// each defaultPrivileges entry lists; unlisted grantees are unmanaged. It is
// emitted right after schemas are created so objects this plan creates
// already receive the defaults.
func planDefaultPrivileges(live *Live, desired *schema.Database) []string {
    revokes, grants := []string{}, []string{}
    for _, dp := range desired.DefaultPrivileges {
        if dp == nil { continue }
        role := resolveRole(dp.ForRole, live.CurrentUser)
        if role == "" { role = live.CurrentUser }
        schemas := dp.Schemas
        if len(schemas) == 0 { schemas = []string{""} }
        for _, kind := range sortedKeys(dp.Grants) {
            for _, nsp := range schemas {
                key := role + "|" + nsp + "|" + kind
                liveG := map[string]map[string]bool{}
                for r, privs := range live.DefaultACLs[key] { liveG[r] = privs }
                if priv, ok := defaultACLImplicit[kind]; ok && nsp == "" && !live.DefaultACLRows[key] {
                    liveG["public"] = map[string]bool{priv: true}
                }
                prefix := "alter default privileges"
                if dp.ForRole != "" { prefix += " for role " + grantRole(dp.ForRole) }
                if nsp != "" { prefix += " in schema " + quoteIdent(nsp) }
                want, have := map[string][]string{}, map[string]map[string]bool{}
                for _, grantee := range sortedKeys(dp.Grants[kind]) {
                    r := grantKey(grantee, live.CurrentUser)
                    want[r] = dp.Grants[kind][grantee]
                    if h := liveG[r]; h != nil { have[r] = h }
                }
                if w, ok := want["public"]; ok {
                    keep, extra := map[string]bool{}, []string{}
                    for _, p := range w { keep[strings.TrimSuffix(strings.ToLower(p), "*")] = true }
                    for _, p := range sortedKeys(have["public"]) {
                        if b := strings.TrimSuffix(p, "*"); !keep[b] { extra = append(extra, b) }
                    }
                    if len(extra) > 0 { revokes = append(revokes, fmt.Sprintf("%s revoke %s on %s from public;", prefix, strings.Join(extra, ", "), kind)) }
                }
                rv, gr := roleGrantStmts(want, have, func(privs []string) string { return strings.Join(privs, ", ") + " on " + kind })
                for _, s := range rv { revokes = append(revokes, prefix+" "+s) }
                for _, s := range gr { grants = append(grants, prefix+" "+s) }
            }
        }
    }
    return append(revokes, grants...)
}

// planGrants is planGrantsWarned without the warnings.
func planGrants(live *Live, desired *schema.Database) []string {
    stmts, _ := planGrantsWarned(live, desired, PublicByKind)
    return stmts
}

// planGrantsWarned reconciles desired grants against live ACLs. A present
// grants block is authoritative: missing privileges are granted, live
// privileges absent from the block are revoked. On objects PostgreSQL gives
// PUBLIC nothing by default (tables, views, sequences, columns, servers and
// schemas other than public) that includes PUBLIC, with a warning for each
// PUBLIC revoke. On functions, procedures, types, domains and the public
// schema, whose PUBLIC defaults count as live privileges, PUBLIC is managed
// only when the block lists it, or by revokePublic. mode can instead make
// every block cover PUBLIC (PublicAuthoritative) or only blocks that list it
// (PublicExplicit).
//
// On an object an extension owns, the privileges its script granted (see
// Live.InitialGrants) are left alone unless the block names their grantee;
// PublicAuthoritative still covers PUBLIC there.
//
// Every REVOKE is emitted ahead of every GRANT. PostgreSQL's REVOKE ... ON
// TABLE also drops that role's column-level privileges, so narrowing a
// table-wide grant to per-column grants in one migration must revoke first or
// the table revoke wipes the column grants issued above it.
func planGrantsWarned(live *Live, desired *schema.Database, mode PublicGrants) ([]string, []Warning) {
    liveFuncs := normalizedLiveFunctions(live)
    revokes := []string{}
    grants := []string{}
    var warns []Warning
    add := func(rv, gr []string) {
        revokes = append(revokes, rv...)
        grants = append(grants, gr...)
    }
    addCovering := func(rv, gr []string) {
        warns = append(warns, publicRevokeWarnings(rv, mode, "grants blocks cover PUBLIC on tables, views, sequences, columns, servers and schemas since v0.43.0")...)
        add(rv, gr)
    }
    listsPublic := func(g map[string][]string) bool { _, ok := g["public"]; return ok }
    covers := func(byKind bool, g map[string][]string) bool { return coversPublic(mode, byKind, g) }
    addFor := func(g map[string][]string) func(rv, gr []string) {
        if listsPublic(g) { return add }
        return addCovering
    }
    acls := cloneGrantMap(live.DefaultACLs)
    if acls == nil { acls = map[string]map[string]map[string]bool{} }
    rows := cloneMap(live.DefaultACLRows, nil)
    if rows == nil { rows = map[string]bool{} }
    applyDefaultPrivileges(acls, rows, live.CurrentUser, desired.DefaultPrivileges)
    created := func(k, kind string) map[string]map[string]bool { return CreatedGrants(acls, live.CurrentUser, k, kind) }
    member := func(key, publicDefault string, liveG map[string]map[string]bool, g map[string][]string) map[string]map[string]bool {
        return memberLiveGrants(live, mode, key, publicDefault, liveG, g)
    }

    for _, name := range sortedKeys(desired.Servers) {
        s := desired.Servers[name]
        if s == nil || s.Grants == nil { continue }
        var liveG map[string]map[string]bool
        owner := live.CurrentUser
        if ls := live.Servers[name]; ls != nil { liveG, owner = ls.Grants, ls.Owner }
        if s.Owner != "" { owner = resolveRole(s.Owner, live.CurrentUser) }
        desiredG := resolveGrantRoles(s.Grants, live.CurrentUser)
        addFor(desiredG)(ownedGrantDiff("foreign server "+quoteIdent(name), desiredG, liveG, owner, covers(true, desiredG)))
    }

    schemaNames := make([]string, 0, len(desired.SchemaGrants))
    for s := range desired.SchemaGrants { schemaNames = append(schemaNames, s) }
    sort.Strings(schemaNames)
    for _, s := range schemaNames {
        desiredG := resolveGrantRoles(desired.SchemaGrants[s], live.CurrentUser)
        addFor(desiredG)(ownedGrantDiff("schema "+quoteIdent(s), desiredG, live.SchemaGrants[s], objectOwner(live, "schema:"+s, desired.SchemaOwners[s]), covers(s != "public", desiredG)))
    }

    tableNames := make([]string, 0, len(desired.Tables))
    for k := range desired.Tables { tableNames = append(tableNames, k) }
    sort.Strings(tableNames)
    for _, k := range tableNames {
        dt := desired.Tables[k]
        if dt == nil { continue }
        var revoked map[string]map[string]bool
        if dt.Grants != nil {
            liveG := live.TableGrants[k]
            if live.Tables[k] == nil { liveG = created(k, "tables") }
            desiredG, owner := resolveGrantRoles(dt.Grants, live.CurrentUser), objectOwner(live, "rel:"+k, dt.Owner)
            liveG = member("rel:"+k, "", liveG, desiredG)
            addFor(desiredG)(ownedGrantDiff("table "+pqIdent(k), desiredG, liveG, owner, covers(true, desiredG)))
            revoked = revokedPrivileges(desiredG, liveG, owner, covers(true, desiredG))
        }
        colNames := make([]string, 0, len(dt.Columns))
        for cn := range dt.Columns { colNames = append(colNames, cn) }
        sort.Strings(colNames)
        for _, cn := range colNames {
            c := dt.Columns[cn]
            if c == nil || c.Grants == nil { continue }
            colG := resolveGrantRoles(c.Grants, live.CurrentUser)
            addFor(colG)(columnGrantDiffStmts(k, cn, colG, withoutPrivileges(live.ColumnGrants[k][cn], revoked), covers(true, colG)))
        }
    }

    for _, k := range sortedKeys(desired.Sequences) {
        if sq := desired.Sequences[k]; sq != nil && sq.Grants != nil {
            liveG := live.TableGrants[k]
            if !live.Sequences[k] { liveG = created(k, "sequences") }
            desiredG := resolveGrantRoles(sq.Grants, live.CurrentUser)
            liveG = member("rel:"+k, "", liveG, desiredG)
            addFor(desiredG)(ownedGrantDiff("sequence "+pqIdent(k), desiredG, liveG, objectOwner(live, "rel:"+k, sq.Owner), covers(true, desiredG)))
        }
    }
    typeGrants := func(kind, k string, grantsBlock map[string][]string, revokePublic, isLive, attributeOnly bool, owner string) {
        desiredG := resolveGrantRoles(grantsBlock, live.CurrentUser)
        defaultUsage := !isLive || !live.TypePublicRevoked[k]
        if revokePublic && (grantsBlock == nil || !covers(false, desiredG)) && defaultUsage {
            revokes = append(revokes, fmt.Sprintf("revoke usage on %s %s from public;", kind, pqIdent(k)))
        }
        if grantsBlock == nil { return }
        liveG := withPublicDefault(live.TypeGrants[k], "usage", defaultUsage)
        if !isLive {
            liveG = withPublicDefault(created(k, "types"), "usage", true)
            if attributeOnly { liveG = pendingGrants(created(k, "types"), desiredG) }
        }
        liveG = member("type:"+k, "usage", liveG, desiredG)
        addFor(desiredG)(ownedGrantDiff(kind+" "+pqIdent(k), desiredG, liveG, owner, covers(false, desiredG)))
    }
    for _, k := range sortedKeys(desired.Types) {
        if td := desired.Types[k]; td != nil {
            typeGrants("type", k, td.Grants, td.RevokePublic, live.Types[k] || td.Kind == "" && liveType(live, k), td.Kind == "", objectOwner(live, "type:"+k, td.Owner))
        }
    }
    for _, k := range sortedKeys(desired.Domains) {
        if dm := desired.Domains[k]; dm != nil {
            typeGrants("domain", k, dm.Grants, dm.RevokePublic, live.Domains[k], false, objectOwner(live, "type:"+k, dm.Owner))
        }
    }

    // Views and materialized views: ACLs live in pg_class.relacl like tables,
    // so live state comes from the same TableGrants map. pg_dump likewise
    // emits GRANT ... ON TABLE for views.
    viewNames := make([]string, 0, len(desired.Views))
    for k := range desired.Views { viewNames = append(viewNames, k) }
    sort.Strings(viewNames)
    for _, k := range viewNames {
        vw := desired.Views[k]
        if vw == nil || vw.Grants == nil { continue }
        liveG := live.TableGrants[k]
        desiredG := resolveGrantRoles(vw.Grants, live.CurrentUser)
        if !live.Views[k] && !live.MatViews[k] {
            liveG = created(k, "tables")
            if strings.TrimSpace(vw.Query) == "" { liveG = pendingGrants(liveG, desiredG) }
        }
        liveG = member("rel:"+k, "", liveG, desiredG)
        addFor(desiredG)(ownedGrantDiff("table "+pqIdent(k), desiredG, liveG, objectOwner(live, "rel:"+k, vw.Owner), covers(true, desiredG)))
    }

    funcNames := make([]string, 0, len(desired.Functions))
    for k := range desired.Functions { funcNames = append(funcNames, k) }
    sort.Strings(funcNames)
    for _, k := range funcNames {
        f := desired.Functions[k]
        if f == nil { continue }
        name := schema.RoutineName(k)
        norm := normalizeFunctionSignature(name + f.ArgsSig)
        target := "function " + pqIdent(name) + grantFuncArgs(f.ArgsSig)
        desiredG := resolveGrantRoles(f.Grants, live.CurrentUser)
        defaultExec := !liveFuncs[norm] || live.FunctionPublicExec[norm]
        if f.RevokePublic && (f.Grants == nil || !covers(false, desiredG)) && defaultExec {
            revokes = append(revokes, fmt.Sprintf("revoke all on %s from public;", target))
        }
        if f.Grants != nil {
            liveG := withPublicDefault(live.FunctionGrants[norm], "execute", defaultExec)
            if !liveFuncs[norm] {
                liveG = withPublicDefault(created(name, "functions"), "execute", true)
                if !f.HasBody() { liveG = pendingGrants(created(name, "functions"), desiredG) }
            }
            liveG = member("proc:"+norm, "execute", liveG, desiredG)
            addFor(desiredG)(ownedGrantDiff(target, desiredG, liveG, objectOwner(live, "proc:"+norm, f.Owner), covers(false, desiredG)))
        }
    }

    procNames := make([]string, 0, len(desired.Procedures))
    for k := range desired.Procedures { procNames = append(procNames, k) }
    sort.Strings(procNames)
    for _, k := range procNames {
        pr := desired.Procedures[k]
        if pr == nil { continue }
        // procedure ACLs share pg_proc's live maps (FunctionGrants/PublicExec)
        name := schema.RoutineName(k)
        norm := normalizeFunctionSignature(name + pr.ArgsSig)
        target := "procedure " + pqIdent(name) + grantFuncArgs(pr.ArgsSig)
        desiredG := resolveGrantRoles(pr.Grants, live.CurrentUser)
        defaultExec := !live.Procedures[norm] || live.FunctionPublicExec[norm]
        if pr.RevokePublic && (pr.Grants == nil || !covers(false, desiredG)) && defaultExec {
            revokes = append(revokes, fmt.Sprintf("revoke all on %s from public;", target))
        }
        if pr.Grants != nil {
            liveG := withPublicDefault(live.FunctionGrants[norm], "execute", defaultExec)
            if !live.Procedures[norm] {
                liveG = withPublicDefault(created(name, "functions"), "execute", true)
                if !pr.HasBody() { liveG = pendingGrants(created(name, "functions"), desiredG) }
            }
            liveG = member("proc:"+norm, "execute", liveG, desiredG)
            addFor(desiredG)(ownedGrantDiff(target, desiredG, liveG, objectOwner(live, "proc:"+norm, pr.Owner), covers(false, desiredG)))
        }
    }
    return append(revokes, grants...), warns
}

// withPublicDefault returns liveG with PUBLIC holding priv when present
// reports that it still has PostgreSQL's default (EXECUTE on routines, USAGE
// on types) and the ACL doesn't spell PUBLIC out.
func withPublicDefault(liveG map[string]map[string]bool, priv string, present bool) map[string]map[string]bool {
    if !present || liveG["public"] != nil { return liveG }
    out := make(map[string]map[string]bool, len(liveG)+1)
    for r, p := range liveG { out[r] = p }
    out["public"] = map[string]bool{priv: true}
    return out
}

// CreatedGrants returns the grants an object of kind (tables, sequences,
// functions or types) named fq receives from default privileges when role
// creates it: the role's global and per-schema defaults from acls (as
// Live.DefaultACLs).
func CreatedGrants(acls map[string]map[string]map[string]bool, role, fq, kind string) map[string]map[string]bool {
    nsp, _, _ := strings.Cut(fq, ".")
    var out map[string]map[string]bool
    for _, key := range []string{role + "||" + kind, role + "|" + nsp + "|" + kind} {
        for grantee, privs := range acls[key] {
            if len(privs) == 0 { continue }
            if out == nil { out = map[string]map[string]bool{} }
            if out[grantee] == nil { out[grantee] = map[string]bool{} }
            for p := range privs { out[grantee][p] = true }
        }
    }
    return out
}

// grantDiffStmts returns revoke and grant statements reconciling desired
// role->privs against live role->priv->exists for one target ("table X",
// "schema Y", ...). Revokes and grants are returned separately so the caller
// can emit every revoke ahead of every grant: REVOKE ... ON TABLE also strips
// that role's column-level privileges, so a table revoke running after a
// column grant would silently destroy it.
func grantDiffStmts(target string, desired map[string][]string, liveG map[string]map[string]bool) (revokes, grants []string) {
    return roleGrantStmts(desired, liveG, func(privs []string) string { return strings.Join(privs, ", ") + " on " + target })
}

// roleGrantStmts reconciles desired role->privileges with live
// role->privilege sets; on renders the "<privileges> on <target>" part of
// each statement. A privilege ending in * carries WITH GRANT OPTION: one
// missing from live is granted with it, a live grant option the YAML no
// longer declares is revoked with REVOKE GRANT OPTION FOR, and the privilege
// itself is kept. PUBLIC is never revoked; roleGrantStmtsCovering can.
func roleGrantStmts(desired map[string][]string, liveG map[string]map[string]bool, on func([]string) string) (revokes, grants []string) {
    return roleGrantStmtsCovering(desired, liveG, on, false)
}

// roleGrantStmtsCovering is roleGrantStmts that also revokes PUBLIC's live
// privileges desired doesn't list when coversPublic is set.
func roleGrantStmtsCovering(desired map[string][]string, liveG map[string]map[string]bool, on func([]string) string, coversPublic bool) (revokes, grants []string) {
    parse := func(privs map[string]bool) map[string]bool {
        out := map[string]bool{}
        for p := range privs {
            base := strings.TrimSuffix(p, "*")
            out[base] = out[base] || base != p
        }
        return out
    }
    roles := map[string]bool{}
    for r := range desired { roles[r] = true }
    for r := range liveG { roles[r] = true }
    for _, role := range sortedKeys(roles) {
        wantSet := map[string]bool{}
        for _, p := range desired[role] { wantSet[strings.ToLower(p)] = true }
        want, have := parse(wantSet), parse(liveG[role])
        var grant, grantOpt, revoke, revokeOpt []string
        for _, p := range sortedKeys(want) {
            opt, ok := have[p]
            switch {
            case !ok && want[p]:
                grantOpt = append(grantOpt, p)
            case !ok:
                grant = append(grant, p)
            case want[p] && !opt:
                grantOpt = append(grantOpt, p)
            case !want[p] && opt:
                revokeOpt = append(revokeOpt, p)
            }
        }
        for _, p := range sortedKeys(have) {
            if _, ok := want[p]; !ok && (coversPublic || role != "public") { revoke = append(revoke, p) }
        }
        if len(revoke) > 0 { revokes = append(revokes, fmt.Sprintf("revoke %s from %s;", on(revoke), grantRole(role))) }
        if len(revokeOpt) > 0 { revokes = append(revokes, fmt.Sprintf("revoke grant option for %s from %s;", on(revokeOpt), grantRole(role))) }
        if len(grant) > 0 { grants = append(grants, fmt.Sprintf("grant %s to %s;", on(grant), grantRole(role))) }
        if len(grantOpt) > 0 { grants = append(grants, fmt.Sprintf("grant %s to %s with grant option;", on(grantOpt), grantRole(role))) }
    }
    return revokes, grants
}

// revokedPrivileges returns the role -> privileges a grants block revokes
// from live: privileges a role holds that the block doesn't list for it.
// The owner is left out, and PUBLIC unless the block covers it.
func revokedPrivileges(desired map[string][]string, liveG map[string]map[string]bool, owner string, coversPublic bool) map[string]map[string]bool {
    out := map[string]map[string]bool{}
    for role, privs := range liveG {
        if role == owner || (role == "public" && !coversPublic) { continue }
        want := map[string]bool{}
        for _, p := range desired[role] { want[strings.TrimSuffix(strings.ToLower(p), "*")] = true }
        for p := range privs {
            if base := strings.TrimSuffix(p, "*"); !want[base] {
                if out[role] == nil { out[role] = map[string]bool{} }
                out[role][base] = true
            }
        }
    }
    return out
}

// withoutPrivileges returns liveG minus the role privileges in revoked: a
// table-wide REVOKE also takes the role's column privileges of that kind,
// so they have to be granted again.
func withoutPrivileges(liveG map[string]map[string]bool, revoked map[string]map[string]bool) map[string]map[string]bool {
    if len(revoked) == 0 { return liveG }
    out := map[string]map[string]bool{}
    for role, privs := range liveG {
        for p := range privs {
            if revoked[role][strings.TrimSuffix(p, "*")] { continue }
            if out[role] == nil { out[role] = map[string]bool{} }
            out[role][p] = true
        }
    }
    return out
}

// columnGrantDiffStmts reconciles desired role->privs for one column against
// live column ACLs (pg_attribute.attacl). Same authoritative semantics as
// grantDiffStmts: missing privileges are granted, extra live privileges are
// revoked, PUBLIC is never auto-revoked. Emits column-list grant syntax:
// grant select ("email") on table "s"."t" to "role";
func columnGrantDiffStmts(tableKey, col string, desired map[string][]string, liveG map[string]map[string]bool, coversPublic bool) (revokes, grants []string) {
    target := "table " + pqIdent(tableKey)
    colIdent := quoteIdent(col)
    return roleGrantStmtsCovering(desired, liveG, func(privs []string) string {
        parts := make([]string, len(privs))
        for i, p := range privs { parts[i] = p + " (" + colIdent + ")" }
        return strings.Join(parts, ", ") + " on " + target
    }, coversPublic)
}

// grantRole quotes a role name, leaving the PUBLIC pseudo-role bare.
func grantRole(role string) string {
    if strings.EqualFold(role, "public") { return "public" }
    if isRoleKeyword(role) { return strings.ToLower(strings.TrimSpace(role)) }
    return quoteIdent(role)
}

// objectOwner returns the role that owns (or will own) an object: the
// declared owner, else the live owner, else the connecting role, which
// creates new objects.
func objectOwner(live *Live, key, declared string) string {
    if declared != "" { return resolveRole(declared, live.CurrentUser) }
    if o := live.Owners[key]; o != "" { return o }
    return live.CurrentUser
}

// ownedGrantDiff is grantDiffStmts with the object owner left out on both
// sides: an owner holds every privilege implicitly, and live ACLs never
// list it, so a grant to the owner would be re-emitted on every diff. With
// coversPublic, PUBLIC's privileges desired doesn't list are revoked too.
func ownedGrantDiff(target string, desired map[string][]string, liveG map[string]map[string]bool, owner string, coversPublic bool) (revokes, grants []string) {
    if owner != "" {
        if _, ok := desired[owner]; ok {
            d := make(map[string][]string, len(desired))
            for r, p := range desired {
                if r != owner { d[r] = p }
            }
            desired = d
        }
        if _, ok := liveG[owner]; ok {
            l := make(map[string]map[string]bool, len(liveG))
            for r, p := range liveG {
                if r != owner { l[r] = p }
            }
            liveG = l
        }
    }
    return roleGrantStmtsCovering(desired, liveG, func(privs []string) string { return strings.Join(privs, ", ") + " on " + target }, coversPublic)
}

// isRoleKeyword reports whether role is CURRENT_USER, CURRENT_ROLE or
// SESSION_USER, which role specifications accept unquoted.
func isRoleKeyword(role string) bool {
    switch strings.ToLower(strings.TrimSpace(role)) {
    case "current_user", "current_role", "session_user":
        return true
    }
    return false
}

// resolveRole returns the role a keyword stands for in the connected session
// (PostgreSQL stores the resolved role, not the keyword), or role itself.
func resolveRole(role, currentUser string) string {
    if isRoleKeyword(role) && currentUser != "" { return currentUser }
    return role
}

// grantKey spells a grantee as live ACLs do: PUBLIC as public, role
// keywords as the connected role, and other role names exactly.
func grantKey(role, currentUser string) string {
    if strings.EqualFold(strings.TrimSpace(role), "public") { return "public" }
    return resolveRole(role, currentUser)
}

// resolveGrantRoles returns grants with role keywords replaced by the
// connected role and PUBLIC spelled as live ACLs spell it, so they compare
// with live ACLs.
func resolveGrantRoles(grants map[string][]string, currentUser string) map[string][]string {
    if grants == nil { return grants }
    key := func(r string) string { return grantKey(r, currentUser) }
    changed := false
    for r := range grants {
        if key(r) != r { changed = true }
    }
    if !changed { return grants }
    out := make(map[string][]string, len(grants))
    for r, privs := range grants {
        k := key(r)
        out[k] = append(out[k], privs...)
    }
    return out
}

// grantFuncArgs strips default clauses from an args signature — GRANT/REVOKE
// ON FUNCTION accepts identity arguments only.
func grantFuncArgs(argsSig string) string {
    inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(argsSig), "("), ")")
    if strings.TrimSpace(inner) == "" { return "()" }
    args := []string{}
    for _, a := range splitTopLevelQuotedParens(inner) {
        if decl, _ := splitArgDefault(a); decl != "" { args = append(args, decl) }
    }
    return "(" + strings.Join(args, ", ") + ")"
}

// applyTableConstraints emits SQL for primary keys, foreign keys, indexes, constraints, and triggers
// on a table. lt is the live table state (nil for new tables — skip existence checks).
// FK statements go to deferredFKs so the caller can emit them after all PK/unique alters.
// Trigger statements go to deferredTriggers so they run after every table AND
// function create (trigger functions may be topologically sorted after this table).
// A live constraint whose definition no longer matches the desired one is
// dropped and re-added; since DROP CONSTRAINT can break dependents and FK/unique
// re-validation can fail on existing data, that path is gated behind --unsafe.
func applyTableConstraints(plan *PlanDiff, live *Live, fq string, dt *schema.Table, lt *LiveTable, deferredFKs, deferredTriggers *[]string, unsafe bool) {
    liveConstraints := map[string]bool{}
    liveConstraintDefs := map[string]string{}
    liveIndexes := map[string]bool{}
    liveTriggers := map[string]string{}
    livePK := false
    if lt != nil {
        liveConstraints = lt.Constraints
        liveConstraintDefs = lt.ConstraintDefs
        liveIndexes = lt.Indexes
        liveTriggers = lt.Triggers
        livePK = lt.HasPK
    }

    // Primary key — check by type, not name (Postgres auto-names it tablename_pkey)
    pkAdd := "add primary key"
    if dt.PrimaryKeyName != "" { pkAdd = "add constraint " + quoteIdent(truncateIdent(dt.PrimaryKeyName)) + " primary key" }
    if !livePK {
        if len(dt.PrimaryKey) > 0 {
            plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s %s (%s)%s;", pqIdent(fq), pkAdd, constraintColumns(dt.PrimaryKey), indexParameters(dt.PrimaryKeyInclude, dt.PrimaryKeyWith, dt.PrimaryKeyTablespace)))
        } else {
            // derive from column PrimaryKey flags
            pkCols := []string{}
            for cn, c := range dt.Columns { if c.PrimaryKey { pkCols = append(pkCols, cn) } }
            sort.Strings(pkCols)
            if len(pkCols) > 0 {
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s %s (%s);", pqIdent(fq), pkAdd, joinIdentList(pkCols)))
            }
        }
    } else {
        have := LivePrimaryKeyName(lt)
        name := have
        if want := truncateIdent(dt.PrimaryKeyName); want != "" && have != "" && have != want {
            plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s rename constraint %s to %s;", pqIdent(fq), quoteIdent(have), quoteIdent(want)))
            name = want
        }
        if have != "" { plan.Alters = append(plan.Alters, keyIndexStorageStmts(schemaPrefixOf(fq)+name, dt.PrimaryKeyWith, lt.IndexDefs[have])...) }
        if li := lt.IndexDefs[have]; have != "" && li != nil && tablespaceDiffers(dt.PrimaryKeyTablespace, li.Tablespace, live) {
            plan.Alters = append(plan.Alters, keyIndexTablespaceStmt(fq, name, dt.PrimaryKeyTablespace))
            if !lt.Partitioned { plan.Warnings = append(plan.Warnings, tablespaceMoveWarning("primary key index "+name, fq)) }
        }
    }

    // Foreign keys
    for _, fk := range dt.ForeignKeys {
        if fk == nil || len(fk.Columns) == 0 || fk.RefTable == "" { continue }
        if fk.NotValid && dt.PartitionBy != nil && live.ServerVersion > 0 && live.ServerVersion < 180000 && !liveConstraints[truncateIdent(fk.Name)] {
            if lt != nil {
                plan.Warnings = append(plan.Warnings, Warning{Table: fq, Message: fmt.Sprintf("foreign key %s not added: PostgreSQL %d cannot add a NOT VALID foreign key to a partitioned table (supported from 18); drop notValid to add it validated, which locks the partitions while it checks them", fk.Name, live.ServerVersion/10000)})
                continue
            }
            c := *fk
            c.NotValid = false
            fk = &c
        }
        def := renderFKDef(fk)
        fkName := truncateIdent(fk.Name)
        validate := fmt.Sprintf("alter table %s validate constraint %s;", pqIdent(fq), quoteIdent(fkName))
        stmt := fmt.Sprintf("alter table %s add constraint %s %s;", pqIdent(fq), quoteIdent(fkName), def)
        partitionedBefore18 := dt.PartitionBy != nil && live.ServerVersion > 0 && live.ServerVersion < 180000
        split := lt != nil && !fk.NotValid && !fk.NotEnforced && !partitionedBefore18
        if split {
            c := *fk
            c.NotValid = true
            stmt = fmt.Sprintf("alter table %s add constraint %s %s;", pqIdent(fq), quoteIdent(fkName), renderFKDef(&c))
        }
        if liveConstraints[fkName] {
            liveDef, ok := liveConstraintDefs[fkName]
            switch {
            case unsafe && ok && !constraintDefsEqual(def, liveDef):
                *deferredFKs = append(*deferredFKs, fmt.Sprintf("alter table %s drop constraint %s;", pqIdent(fq), quoteIdent(fkName)), stmt)
                if split { plan.Concurrent = append(plan.Concurrent, validate) }
            case !fk.NotValid && !fk.NotEnforced && liveNotValid(liveDef):
                plan.Concurrent = append(plan.Concurrent, validate)
            }
            continue
        }
        *deferredFKs = append(*deferredFKs, stmt)
        if split { plan.Concurrent = append(plan.Concurrent, validate) }
    }

    // Indexes (always use IF NOT EXISTS)
    for _, ix := range dt.Indexes {
        if ix == nil || len(ix.Columns) == 0 { continue }
        name := LiveIndexName(fq, ix, liveIndexes)
        uniq := ""
        if ix.Unique { uniq = " unique" }
        using := ""
        if ix.Using != "" && !strings.EqualFold(ix.Using, "btree") { using = " using " + strings.ToLower(ix.Using) }
        concurrent := ix.Concurrently && lt != nil && dt.PartitionBy == nil
        partitioned := ix.Concurrently && lt != nil && dt.PartitionBy != nil
        foreignParts := partitioned && hasForeignPartition(live, fq)
        if foreignParts {
            partitioned = false
            if !liveIndexes[name] || (lt.IndexDefs[name] != nil && lt.IndexDefs[name].Invalid) {
                plan.Warnings = append(plan.Warnings, Warning{Table: fq, Message: fmt.Sprintf("index %s: %s has foreign partitions, which cannot be indexed, so an index built partition by partition never becomes valid; it is built without CONCURRENTLY, which skips them", name, fq)})
            }
        }
        how := " index"
        if concurrent { how = " index concurrently" }
        elems, tail := renderIndexElems(ix, dt.Columns), renderIndexTail(ix)
        stmt := fmt.Sprintf("create%s%s if not exists %s on %s%s(%s)%s;", uniq, how, quoteIdent(name), pqIdent(fq), using, elems, tail)
        build := func(idx, table string, only, conc bool) string {
            h, o := " index", ""
            if conc { h = " index concurrently" }
            if only { o = "only " }
            _, bare := splitQualified(idx)
            return fmt.Sprintf("create%s%s if not exists %s on %s%s%s(%s)%s;", uniq, h, quoteIdent(bare), o, pqIdent(table), using, elems, tail)
        }
        idxFq := schemaPrefixOf(fq) + name
        indexComment := func(built bool) []string {
            out := []string{}
            if ix.Comment != "" && (built || lt == nil || lt.IndexComments[name] != ix.Comment) {
                out = append(out, fmt.Sprintf("comment on index %s is %s;", pqIdent(idxFq), quoteString(ix.Comment)))
            }
            var liveOCs []LiveIndexOpclass
            if !built { liveOCs = liveIndexOpclasses(lt)[name] }
            for i, c := range ix.Columns {
                want, ok := ix.StatisticsTargets[c]
                switch {
                case !ok:
                    continue
                case built && want == -1:
                    continue
                case !built && (i >= len(liveOCs) || liveOCs[i].StatisticsTarget == want):
                    continue
                }
                out = append(out, fmt.Sprintf("alter index %s alter column %d set statistics %d;", pqIdent(idxFq), i+1, want))
            }
            return out
        }
        if foreignParts && liveIndexes[name] && lt.IndexDefs[name] != nil && lt.IndexDefs[name].Invalid {
            plan.Alters = append(append(plan.Alters, fmt.Sprintf("drop index if exists %s;", pqIdent(idxFq)), stmt), indexComment(true)...)
            continue
        }
        if liveIndexes[name] && (concurrent || partitioned) && lt.IndexDefs[name] != nil && lt.IndexDefs[name].Invalid {
            if partitioned {
                plan.Concurrent = append(plan.Concurrent, partitionedIndexStmts(live, fq, idxFq, build)...)
            } else {
                plan.Concurrent = append(plan.Concurrent, fmt.Sprintf("drop index concurrently if exists %s;", pqIdent(idxFq)), stmt)
            }
            plan.Concurrent = append(plan.Concurrent, indexComment(true)...)
            continue
        }
        if liveIndexes[name] {
            // Changed definition → drop + recreate. Rebuilding an index can be
            // expensive and briefly leaves queries without it, so gate behind
            // --unsafe. Without live definition data the index is adopted by name.
            changed := indexChanged(ix, lt, name)
            switch {
            case !unsafe || !changed:
                plan.Alters = append(plan.Alters, indexComment(false)...)
                if li := lt.IndexDefs[name]; li != nil && tablespaceDiffers(ix.Tablespace, li.Tablespace, live) {
                    switch {
                    case partitioned:
                        plan.Alters = append(plan.Alters, fmt.Sprintf("alter index %s set tablespace %s;", pqIdent(idxFq), quoteIdent(ix.Tablespace)))
                    case concurrent:
                        plan.Concurrent = append(plan.Concurrent, fmt.Sprintf("reindex (tablespace %s) index concurrently %s;", quoteIdent(ix.Tablespace), pqIdent(idxFq)))
                    default:
                        plan.Alters = append(plan.Alters, fmt.Sprintf("alter index %s set tablespace %s;", pqIdent(idxFq), quoteIdent(ix.Tablespace)))
                        plan.Warnings = append(plan.Warnings, tablespaceMoveWarning("index "+idxFq, fq))
                    }
                }
            case partitioned:
                plan.Concurrent = append(append(append(plan.Concurrent, fmt.Sprintf("drop index if exists %s;", pqIdent(idxFq))), partitionedIndexStmts(live, fq, idxFq, build)...), indexComment(true)...)
            case concurrent:
                plan.Concurrent = append(plan.Concurrent, fmt.Sprintf("drop index concurrently if exists %s;", pqIdent(idxFq)), stmt)
                plan.Concurrent = append(plan.Concurrent, indexComment(true)...)
            default:
                plan.Alters = append(append(plan.Alters, fmt.Sprintf("drop index %s;", pqIdent(idxFq)), stmt), indexComment(true)...)
            }
            continue
        }
        switch {
        case partitioned:
            plan.Concurrent = append(append(plan.Concurrent, partitionedIndexStmts(live, fq, idxFq, build)...), indexComment(true)...)
        case concurrent:
            plan.Concurrent = append(plan.Concurrent, stmt)
            plan.Concurrent = append(plan.Concurrent, indexComment(true)...)
        default:
            plan.Creates = append(plan.Creates, stmt)
            plan.Alters = append(plan.Alters, indexComment(true)...)
        }
    }

    // Named constraints (check, unique, exclude)
    for _, ct := range dt.Constraints {
        if ct == nil || ct.Type == "" { continue }
        def := renderConstraintDef(ct)
        if def == "" { continue }
        ctName := truncateIdent(ct.Name)
        validate := fmt.Sprintf("alter table %s validate constraint %s;", pqIdent(fq), quoteIdent(ctName))
        stmt := fmt.Sprintf("alter table %s add constraint %s %s;", pqIdent(fq), quoteIdent(ctName), def)
        split := lt != nil && strings.EqualFold(ct.Type, "check") && !ct.NotValid && !ct.NotEnforced
        if split {
            c := *ct
            c.NotValid = true
            stmt = fmt.Sprintf("alter table %s add constraint %s %s;", pqIdent(fq), quoteIdent(ctName), renderConstraintDef(&c))
        }
        if liveConstraints[ctName] {
            liveDef, ok := liveConstraintDefs[ctName]
            switch {
            case unsafe && ok && !constraintMatchesLive(lt, ctName, ct):
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s drop constraint %s;", pqIdent(fq), quoteIdent(ctName)), stmt)
                if split { plan.Concurrent = append(plan.Concurrent, validate) }
            case !ct.NotValid && !ct.NotEnforced && liveNotValid(liveDef):
                plan.Concurrent = append(plan.Concurrent, validate)
            default:
                plan.Alters = append(plan.Alters, keyIndexStorageStmts(schemaPrefixOf(fq)+ctName, constraintStorage(ct), lt.IndexDefs[ctName])...)
                if li := lt.IndexDefs[ctName]; li != nil && tablespaceDiffers(ct.Tablespace, li.Tablespace, live) {
                    plan.Alters = append(plan.Alters, keyIndexTablespaceStmt(fq, ctName, ct.Tablespace))
                    if !lt.Partitioned { plan.Warnings = append(plan.Warnings, tablespaceMoveWarning("constraint "+ctName+"'s index", fq)) }
                }
            }
            continue
        }
        plan.Alters = append(plan.Alters, stmt)
        if split { plan.Concurrent = append(plan.Concurrent, validate) }
    }

    // Column-level unique flags — emit as named unique constraints
    uniqueCols := []string{}
    for cn, c := range dt.Columns {
        if c != nil && c.Unique { uniqueCols = append(uniqueCols, cn) }
    }
    sort.Strings(uniqueCols)
    for _, cn := range uniqueCols {
        name := LiveUniqueKeyName(fq, cn, liveConstraints)
        if liveConstraints[name] { continue }
        plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s add constraint %s unique (%s);", pqIdent(fq), quoteIdent(name), quoteIdent(cn)))
    }

    // Triggers (deferred: emitted after all table and function creates).
    // A live trigger whose definition no longer matches the desired one is
    // dropped and recreated. Unlike constraints this is not gated behind
    // --unsafe: DROP TRIGGER is metadata-only, has no dependents, and the
    // recreate runs in the same migration transaction.
    for _, tr := range dt.Triggers {
        if tr == nil || tr.Procedure == "" { continue }
        trName := truncateIdent(tr.Name)
        stmt := renderTrigger(fq, tr)
        liveEnabled := "origin"
        if liveDef, ok := liveTriggers[trName]; ok {
            // "" means the live definition is unknown — fall back to
            // name-only matching and skip rather than churn.
            if liveDef == "" || triggerDefsEqual(stmt, liveDef) {
                if lt != nil && lt.TriggerEnabled[trName] != "" { liveEnabled = lt.TriggerEnabled[trName] }
                if s := triggerEnableStmt(fq, trName, tr.Enabled, liveEnabled); s != "" { *deferredTriggers = append(*deferredTriggers, s) }
                continue
            }
            *deferredTriggers = append(*deferredTriggers, fmt.Sprintf("drop trigger %s on %s;", quoteIdent(trName), pqIdent(fq)))
        }
        *deferredTriggers = append(*deferredTriggers, stmt)
        if s := triggerEnableStmt(fq, trName, tr.Enabled, liveEnabled); s != "" { *deferredTriggers = append(*deferredTriggers, s) }
    }

    for _, st := range dt.Statistics {
        if st == nil || len(st.Columns) == 0 { continue }
        name := truncateIdent(st.Name)
        target := pqIdent(schemaPrefixOf(fq) + name)
        liveTarget := -1
        if lt != nil {
            if ls := lt.Statistics[name]; ls != nil {
                if statisticMatchesLive(st, ls) {
                    if st.StatisticsTarget != nil && *st.StatisticsTarget != ls.Target {
                        plan.Alters = append(plan.Alters, fmt.Sprintf("alter statistics %s set statistics %d;", target, *st.StatisticsTarget))
                    }
                    continue
                }
                plan.Alters = append(plan.Alters, fmt.Sprintf("drop statistics %s;", target))
            }
        }
        plan.Alters = append(plan.Alters, renderStatistic(fq, st, dt.Columns))
        if st.StatisticsTarget != nil && *st.StatisticsTarget != liveTarget {
            plan.Alters = append(plan.Alters, fmt.Sprintf("alter statistics %s set statistics %d;", target, *st.StatisticsTarget))
        }
    }

    for _, r := range dt.Rules {
        if r == nil || r.Event == "" || r.Do == "" { continue }
        name := truncateIdent(r.Name)
        if lt != nil {
            if lr := lt.Rules[name]; lr != nil && !r.Replace && ruleMatchesLive(r, lr) { continue }
        }
        *deferredTriggers = append(*deferredTriggers, renderRule(fq, r))
    }
}

// renderStatistic renders CREATE STATISTICS in the table's schema. Plain
// or declared columns are quoted; anything else is an expression in
// parentheses.
func renderStatistic(fq string, st *schema.Statistic, cols map[string]*schema.Column) string {
    kinds := ""
    if k := statisticKinds(st.Kinds); len(k) > 0 && len(k) < 3 { kinds = " (" + strings.Join(k, ", ") + ")" }
    items := make([]string, len(st.Columns))
    for i, c := range st.Columns {
        c = strings.TrimSpace(c)
        if isIndexExpression(c) && cols[c] == nil { items[i] = "(" + c + ")" } else { items[i] = quoteIdent(c) }
    }
    return fmt.Sprintf("create statistics %s%s on %s from %s;", pqIdent(schemaPrefixOf(fq)+truncateIdent(st.Name)), kinds, strings.Join(items, ", "), pqIdent(fq))
}

// statisticKinds returns the requested kinds in PostgreSQL's order.
func statisticKinds(kinds []string) []string {
    out := []string{}
    for _, k := range []string{"ndistinct", "dependencies", "mcv"} {
        for _, want := range kinds {
            if strings.EqualFold(strings.TrimSpace(want), k) { out = append(out, k); break }
        }
    }
    return out
}

// statisticMatchesLive compares kinds (empty or all three = all) and items
// as sets, since PostgreSQL orders columns by position.
func statisticMatchesLive(st *schema.Statistic, ls *LiveStatistic) bool {
    want, have := statisticKinds(st.Kinds), statisticKinds(ls.Kinds)
    if len(want) == 3 { want = nil }
    if len(have) == 3 { have = nil }
    if strings.Join(want, ",") != strings.Join(have, ",") || len(st.Columns) != len(ls.Items) { return false }
    seen := map[string]bool{}
    for _, it := range ls.Items { seen[normalizeConstraintDef(it)] = true }
    for _, c := range st.Columns {
        if !seen[normalizeConstraintDef(c)] { return false }
    }
    return true
}

// replicaIdentityStmt emits ALTER TABLE ... REPLICA IDENTITY when the YAML
// manages it and it differs from live (new tables start at default).
func replicaIdentityStmt(fq string, dt *schema.Table, lt *LiveTable) string {
    want := dt.ReplicaIdentity
    if want == "" { return "" }
    have := "default"
    if lt != nil && lt.ReplicaIdentity != "" { have = lt.ReplicaIdentity }
    if want == have { return "" }
    clause := want
    if idx, ok := strings.CutPrefix(want, "index "); ok { clause = "using index " + quoteIdent(truncateIdent(idx)) }
    return fmt.Sprintf("alter table %s replica identity %s;", pqIdent(fq), clause)
}

// statisticsTargetOf returns a live column's statistics target, -1 for the
// default.
func statisticsTargetOf(lc *LiveColumn) int {
    if lc.StatisticsTarget == nil { return -1 }
    return *lc.StatisticsTarget
}

// parentDeclaresColumn reports whether one of dt's INHERITS parents
// declares column cn, directly or through its own parents.
func parentDeclaresColumn(d *schema.Database, dt *schema.Table, cn string, depth int) bool {
    if dt == nil || depth > 16 { return false }
    for _, p := range dt.Inherits {
        pt := d.Tables[p]
        if pt == nil { continue }
        if _, ok := pt.Columns[cn]; ok || parentDeclaresColumn(d, pt, cn, depth+1) { return true }
    }
    return false
}

// inheritStmts reconciles a table's INHERITS parents when the YAML declares
// them: missing parents are added with INHERIT, and under --unsafe parents
// the YAML no longer lists are removed with NO INHERIT.
func inheritStmts(fq string, dt *schema.Table, lt *LiveTable, unsafe bool) []string {
    if dt.Inherits == nil || lt == nil { return nil }
    out := []string{}
    for _, p := range dt.Inherits {
        if indexOf(lt.Inherits, p) < 0 { out = append(out, fmt.Sprintf("alter table %s inherit %s;", pqIdent(fq), pqIdent(p))) }
    }
    if unsafe {
        for _, p := range lt.Inherits {
            if indexOf(dt.Inherits, p) < 0 { out = append(out, fmt.Sprintf("alter table %s no inherit %s;", pqIdent(fq), pqIdent(p))) }
        }
    }
    return out
}

// clusterOnStmt emits ALTER TABLE ... CLUSTER ON / SET WITHOUT CLUSTER when
// the YAML manages it ("none" removes the mark) and it differs from live.
// It only marks the index; running CLUSTER is left to the operator.
func clusterOnStmt(fq string, dt *schema.Table, lt *LiveTable) string {
    want := dt.ClusterOn
    if want == "" { return "" }
    have := ""
    if lt != nil { have = lt.ClusteredOn }
    if strings.EqualFold(want, "none") {
        if have == "" { return "" }
        return fmt.Sprintf("alter table %s set without cluster;", pqIdent(fq))
    }
    if truncateIdent(want) == have { return "" }
    return fmt.Sprintf("alter table %s cluster on %s;", pqIdent(fq), quoteIdent(truncateIdent(want)))
}

func schemaPrefixOf(fq string) string {
    if i := strings.IndexByte(fq, '.'); i >= 0 { return fq[:i+1] }
    return ""
}

// splitTopLevel splits a comma-separated list outside parentheses and quotes.
func splitTopLevel(s string) []string {
    out, depth, start, inQ := []string{}, 0, 0, false
    for i := 0; i < len(s); i++ {
        switch c := s[i]; {
        case c == '\'':
            inQ = !inQ
        case inQ:
        case c == '(':
            depth++
        case c == ')':
            depth--
        case c == ',' && depth == 0:
            out = append(out, strings.TrimSpace(s[start:i]))
            start = i + 1
        }
    }
    if rest := strings.TrimSpace(s[start:]); rest != "" { out = append(out, rest) }
    return out
}

// renderRule renders CREATE OR REPLACE RULE for a table or view rule.
func renderRule(fq string, r *schema.Rule) string {
    stmt := fmt.Sprintf("create or replace rule %s as on %s to %s", quoteIdent(truncateIdent(r.Name)), strings.ToLower(r.Event), pqIdent(fq))
    if r.Where != "" { stmt += " where " + r.Where }
    stmt += " do "
    if r.Instead { stmt += "instead " }
    return stmt + strings.TrimSuffix(strings.TrimSpace(r.Do), ";") + ";"
}

// ruleMatchesLive compares a rule's event, INSTEAD flag and WHERE condition;
// the action is not compared because PostgreSQL rewrites it when storing.
func ruleMatchesLive(r *schema.Rule, lr *LiveRule) bool {
    return strings.ToLower(r.Event) == lr.Event && r.Instead == lr.Instead && constraintDefsEqual(r.Where, lr.Where)
}

// triggerEnableStmt emits ALTER TABLE ... ENABLE / DISABLE [REPLICA | ALWAYS]
// TRIGGER when the YAML manages the trigger's state and it differs.
func triggerEnableStmt(fq, name, want, have string) string {
    if want == "" || want == have { return "" }
    action := map[string]string{"origin": "enable", "disabled": "disable", "replica": "enable replica", "always": "enable always"}[want]
    if action == "" { return "" }
    return fmt.Sprintf("alter table %s %s trigger %s;", pqIdent(fq), action, quoteIdent(name))
}

// renderTrigger renders a table trigger's CREATE [CONSTRAINT] TRIGGER statement.
func renderTrigger(fq string, tr *schema.Trigger) string {
    trName := truncateIdent(tr.Name)
    events := renderTriggerEvents(canonicalTriggerEvents(tr.Events))
    when := ""
    if tr.When != "" { when = fmt.Sprintf(" when (%s)", tr.When) }
    if tr.Constraint {
        // constraint triggers are always AFTER ... FOR EACH ROW
        deferral := ""
        if tr.InitiallyDeferred {
            deferral = " deferrable initially deferred"
        } else if tr.Deferrable {
            deferral = " deferrable"
        }
        return fmt.Sprintf("create constraint trigger %s AFTER %s on %s%s for each row%s execute procedure %s;", quoteIdent(trName), events, pqIdent(fq), deferral, when, tr.Procedure)
    }
    level := strings.ToLower(strings.TrimSpace(tr.Level))
    if level == "" { level = "statement" }
    if strings.EqualFold(strings.Join(strings.Fields(tr.Timing), " "), "instead of") { level = "row" }
    referencing := ""
    if tr.OldTable != "" { referencing += " old table as " + quoteIdent(tr.OldTable) }
    if tr.NewTable != "" { referencing += " new table as " + quoteIdent(tr.NewTable) }
    if referencing != "" { referencing = " referencing" + referencing }
    return fmt.Sprintf("create trigger %s %s %s on %s%s for each %s%s execute procedure %s;", quoteIdent(trName), strings.ToUpper(tr.Timing), events, pqIdent(fq), referencing, level, when, tr.Procedure)
}

// planPolicies emits ENABLE/DISABLE ROW LEVEL SECURITY and policy create/drop
// for all tables. Runs after every object create/alter (policies may reference
// functions created later in the same plan than their table — dependsOn cannot
// order this when the table itself is skipped as already live).
func planPolicies(plan *PlanDiff, live *Live, desired *schema.Database, unsafe bool) {
    tableNames := make([]string, 0, len(desired.Tables))
    for k := range desired.Tables { tableNames = append(tableNames, k) }
    sort.Strings(tableNames)
    for _, fq := range tableNames {
        dt := desired.Tables[fq]
        if dt == nil { continue }
        lt := live.Tables[fq]
        if foreignKindMismatch(dt, lt) { continue }

        // Row level security. nil means unmanaged: the live state is left
        // alone. Disabling widens access — every policy on the table stops
        // filtering rows — so it is gated behind --unsafe, like DROP COLUMN.
        if dt.RowLevelSecurity != nil {
            if *dt.RowLevelSecurity {
                if lt == nil || !lt.RLSEnabled {
                    plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s enable row level security;", pqIdent(fq)))
                }
            } else if unsafe && lt != nil && lt.RLSEnabled {
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s disable row level security;", pqIdent(fq)))
            }
        }
        if f := dt.ForceRowLevelSecurity; f != nil {
            switch {
            case *f && (lt == nil || !lt.RLSForced):
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s force row level security;", pqIdent(fq)))
            case !*f && unsafe && lt != nil && lt.RLSForced:
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s no force row level security;", pqIdent(fq)))
            }
        }

        // Policies — a present policies block is authoritative: missing
        // policies are created, live policies not listed are dropped, and a
        // live policy whose definition no longer matches is dropped and
        // recreated. Like triggers (and unlike constraints) the replace is not
        // gated behind --unsafe: DROP POLICY is metadata-only, has no
        // dependents, and the recreate runs in the same migration transaction.
        livePolicies := map[string]*LivePolicy{}
        if lt != nil { livePolicies = lt.Policies }
        desiredPolicies := map[string]bool{}
        for _, pol := range dt.Policies {
            if pol == nil || pol.Name == "" { continue }
            polName := truncateIdent(pol.Name)
            desiredPolicies[polName] = true
            if lp := livePolicies[polName]; lp != nil {
                if policyMatchesLive(pol, lp, live.CurrentUser) { continue }
                plan.Alters = append(plan.Alters, fmt.Sprintf("drop policy %s on %s;", quoteIdent(polName), pqIdent(fq)))
            }
            stmt := fmt.Sprintf("create policy %s on %s", quoteIdent(polName), pqIdent(fq))
            if pol.As == "restrictive" { stmt += " as restrictive" }
            if pol.For != "" { stmt += " for " + strings.ToLower(pol.For) }
            switch {
            case policyToPublic(pol.To):
                stmt += " to public"
            case len(pol.To) > 0:
                roles := make([]string, 0, len(pol.To))
                for _, r := range pol.To { roles = append(roles, grantRole(r)) }
                stmt += " to " + strings.Join(roles, ", ")
            }
            if pol.Using != "" { stmt += fmt.Sprintf(" using (%s)", pol.Using) }
            if pol.WithCheck != "" { stmt += fmt.Sprintf(" with check (%s)", pol.WithCheck) }
            plan.Alters = append(plan.Alters, stmt+";")
        }
        if dt.Policies != nil {
            removed := []string{}
            for name := range livePolicies {
                if !desiredPolicies[name] { removed = append(removed, name) }
            }
            sort.Strings(removed)
            for _, name := range removed {
                plan.Drops = append(plan.Drops, fmt.Sprintf("drop policy %s on %s;", quoteIdent(name), pqIdent(fq)))
            }
        }
    }
}

// RenderConcurrent renders the plan's Concurrent statements as the body of
// a no-transaction migration, or "" when there are none.
func RenderConcurrent(p *PlanDiff) string {
    if len(p.Concurrent) == 0 { return "" }
    return db.NoTransactionDirective + "\n" + strings.Join(p.Concurrent, "\n") + "\n"
}

// Render renders the plan's transactional statements: the Prelude, then
// Creates, Alters and Drops. Concurrent statements are rendered by
// RenderConcurrent. RenderPrelude and RenderMain render the two parts on
// their own, for migrations that commit the Prelude first.
func Render(p *PlanDiff) string { return RenderPrelude(p) + RenderMain(p) }

// RenderPrelude renders the plan's Prelude, or "" when it has none.
func RenderPrelude(p *PlanDiff) string {
    if len(p.Prelude) == 0 { return "" }
    return strings.Join(p.Prelude, "\n") + "\n"
}

// RenderMain renders the plan's Creates, then Alters, then Drops.
func RenderMain(p *PlanDiff) string {
    statements := make([]string, 0, len(p.Creates)+len(p.Alters)+len(p.Drops))
    statements = append(statements, p.Creates...)
    statements = append(statements, p.Alters...)
    statements = append(statements, p.Drops...)
    if len(statements) == 0 { return "" }
    return strings.Join(statements, "\n") + "\n"
}

// alterColumnStmts reconciles an existing column's live attributes with its
// desired definition. Type changes can rewrite the table or fail on cast, so
// they are gated behind --unsafe (with an optional USING expression from the
// column's `using` property). A default is dropped before the type changes
// and the declared one set afterwards, since PostgreSQL has to cast a
// default left in place to the new type and gives up when no implicit or
// assignment cast exists. Identity changes go through identityAlterStmts;
// identity/serial columns keep their generated defaults and implicit NOT
// NULL.
func alterColumnStmts(fq, cn string, c *schema.Column, lc *LiveColumn, pkCols map[string]bool, unsafe bool, serverVersion int, warn func(string)) []string {
    out := []string{}
    target := fmt.Sprintf("alter table %s alter column %s", pqIdent(fq), quoteIdent(cn))

    typeChanged := c.Type != "" && normalizeColumnType(c.Type) != normalizeColumnType(lc.Type)
    collateChanged := c.Collate != "" && collationName(c.Collate) != lc.Collation
    generated := c.Identity != "" || lc.Identity != "" || isSerialType(c.Type) || c.Generated != "" || lc.Generated != ""
    sequenceDefault := strings.HasPrefix(strings.ToLower(strings.TrimSpace(lc.Default)), "nextval(")
    resetDefault := unsafe && typeChanged && !generated && lc.Default != "" && !sequenceDefault
    if resetDefault { out = append(out, target+" drop default;") }
    if unsafe && c.Type != "" && (typeChanged || collateChanged) {
        stmt := fmt.Sprintf("%s type %s", target, c.Type)
        if c.Collate != "" { stmt += " collate " + collationIdent(c.Collate) }
        if c.Using != "" { stmt += " using " + c.Using }
        out = append(out, stmt+";")
    }
    if c.Compression != "" {
        want := c.Compression
        if want == "default" { want = "" }
        if want != lc.Compression {
            out = append(out, fmt.Sprintf("%s set compression %s;", target, c.Compression))
        }
    }
    if c.Storage != "" && c.Storage != lc.Storage {
        out = append(out, fmt.Sprintf("%s set storage %s;", target, c.Storage))
    }
    if c.StatisticsTarget != nil && *c.StatisticsTarget != statisticsTargetOf(lc) {
        out = append(out, fmt.Sprintf("%s set statistics %d;", target, *c.StatisticsTarget))
    }
    out = append(out, attributeOptionStmts(target, c, lc)...)

    if c.Generated != "" || lc.Generated != "" {
        switch {
        case c.Generated != "" && lc.Generated != "" && !constraintDefsEqual(c.Generated, lc.Generated):
            switch {
            case serverVersion > 0 && serverVersion < 170000:
                if !unsafe { warn(fmt.Sprintf("generated column %s.%s changed, but PostgreSQL %d has no ALTER COLUMN ... SET EXPRESSION (added in 17); --unsafe drops the column and adds it again, along with the indexes and constraints that use it", fq, cn, serverVersion/10000)) }
            case unsafe:
                out = append(out, fmt.Sprintf("%s set expression as (%s);", target, c.Generated))
            }
        case c.Generated == "" && lc.Generated != "":
            if unsafe { out = append(out, target+" drop expression;") }
        case c.Generated != "" && lc.Generated == "":
            warn(fmt.Sprintf("column %s.%s is declared generated but is a plain column, and PostgreSQL cannot add a generation expression to an existing column; drop and re-add it, along with the indexes and constraints that use it, in a hand-written migration", fq, cn))
        }
    }
    out = append(out, serialConversionStmts(fq, cn, c, lc)...)

    if !generated {
        switch {
        case c.Default != "" && (resetDefault || !defaultsEqual(c.Default, lc.Default)):
            out = append(out, fmt.Sprintf("%s set default %s;", target, c.Default))
        case c.Default == "" && lc.Default != "" && !sequenceDefault && !resetDefault:
            out = append(out, fmt.Sprintf("%s drop default;", target))
        }
    }

    if !c.Nullable && lc.Nullable {
        if c.NotNullName != "" && serverVersion >= 180000 {
            out = append(out, fmt.Sprintf("alter table %s add constraint %s not null %s;", pqIdent(fq), quoteIdent(truncateIdent(c.NotNullName)), quoteIdent(cn)))
        } else {
            out = append(out, target+" set not null;")
        }
    }
    out = append(out, identityAlterStmts(fq, target, c, lc, unsafe)...)
    droppingIdentity := lc.Identity != "" && c.Identity == "" && unsafe && c.Generated == ""
    if c.Nullable && !lc.Nullable && !c.PrimaryKey && !pkCols[cn] && c.Identity == "" && (lc.Identity == "" || droppingIdentity) {
        out = append(out, target+" drop not null;")
    }
    return out
}

// identityAlterStmts reconciles an identity column: SET GENERATED for a
// changed kind and SET <option> for changed sequence options. Under --unsafe
// it also adds an identity to an existing column (dropping its default
// first; the new sequence starts at start, not past existing values) and
// drops one the YAML no longer declares.
func identityAlterStmts(fq, target string, c *schema.Column, lc *LiveColumn, unsafe bool) []string {
    switch {
    case c.Identity == "" && lc.Identity == "":
        return nil
    case c.Identity == "":
        if unsafe && c.Generated == "" { return []string{target + " drop identity if exists;"} }
        return nil
    case lc.Identity == "":
        if !unsafe || lc.Generated != "" || c.Generated != "" { return nil }
        out := []string{}
        if lc.Default != "" { out = append(out, target+" drop default;") }
        return append(out, fmt.Sprintf("%s add generated %s as identity%s;", target, c.Identity, identityOptionsClause(c.IdentityOptions)))
    }
    sets := []string{}
    if c.Identity != lc.Identity { sets = append(sets, "set generated "+c.Identity) }
    if o, ls := c.IdentityOptions, lc.IdentitySequence; o != nil && ls != nil {
        for _, f := range identityOptionFields(o) {
            if f.want != "" && f.want != f.live(ls) { sets = append(sets, "set "+f.clause+" "+f.want) }
        }
        if o.Cycle != nil && *o.Cycle != ls.Cycle {
            if *o.Cycle { sets = append(sets, "set cycle") } else { sets = append(sets, "set no cycle") }
        }
    }
    out := []string{}
    if len(sets) > 0 { out = append(out, target+" "+strings.Join(sets, " ")+";") }
    if o, ls := c.IdentityOptions, lc.IdentitySequence; o != nil && o.Name != "" && ls != nil && ls.Name != "" {
        if want := IdentitySequenceName(fq, o.Name); want != ls.Name { out = append(out, relocateStmts("sequence", ls.Name, want, "")...) }
    }
    return out
}

// IdentitySequenceName qualifies an identity column's sequenceName with the
// table's schema, where PostgreSQL creates an unqualified one.
func IdentitySequenceName(fq, name string) string {
    if strings.Contains(name, ".") { return name }
    return schemaPrefixOf(fq) + name
}

type identityOptionField struct {
    clause, want string
    live         func(*LiveSequence) string
}

func identityOptionFields(o *schema.Sequence) []identityOptionField {
    return []identityOptionField{
        {"increment by", strings.TrimSpace(o.Increment), func(l *LiveSequence) string { return l.Increment }},
        {"minvalue", strings.TrimSpace(o.MinValue), func(l *LiveSequence) string { return l.MinValue }},
        {"maxvalue", strings.TrimSpace(o.MaxValue), func(l *LiveSequence) string { return l.MaxValue }},
        {"start with", strings.TrimSpace(o.Start), func(l *LiveSequence) string { return l.Start }},
        {"cache", strings.TrimSpace(o.Cache), func(l *LiveSequence) string { return l.Cache }},
    }
}

// identityOptionsClause renders " (start with 1000 ...)" for GENERATED ... AS
// IDENTITY, or "" when no option is set.
func identityOptionsClause(o *schema.Sequence) string {
    if o == nil { return "" }
    parts := []string{}
    if o.Name != "" { parts = append(parts, "sequence name "+pqIdent(o.Name)) }
    for _, f := range identityOptionFields(o) {
        if f.want != "" { parts = append(parts, f.clause+" "+f.want) }
    }
    if o.Cycle != nil {
        if *o.Cycle { parts = append(parts, "cycle") } else { parts = append(parts, "no cycle") }
    }
    if len(parts) == 0 { return "" }
    return " (" + strings.Join(parts, " ") + ")"
}

// collationIdent quotes a COLLATE target ("C", app.ci), accepting parts the
// YAML already double-quoted.
func collationIdent(s string) string {
    if parts, ok := schema.SplitName(s); ok {
        for i, p := range parts { parts[i] = quoteIdent(p) }
        return strings.Join(parts, ".")
    }
    parts := strings.Split(strings.TrimSpace(s), ".")
    for i, p := range parts { parts[i] = `"` + strings.Trim(strings.TrimSpace(p), `"`) + `"` }
    return strings.Join(parts, ".")
}

// collationName returns a COLLATE target's unqualified, unquoted name, as
// pg_collation.collname reports it.
func collationName(s string) string {
    if parts, ok := schema.SplitName(s); ok { return parts[len(parts)-1] }
    s = strings.ReplaceAll(strings.TrimSpace(s), `"`, "")
    if i := strings.LastIndexByte(s, '.'); i >= 0 { s = s[i+1:] }
    return s
}

// renderStorageParams renders a table's WITH (...) storage parameters, sorted,
// or "" when there are none.
func renderStorageParams(with map[string]string) string {
    if len(with) == 0 { return "" }
    keys := make([]string, 0, len(with))
    for k := range with { keys = append(keys, k) }
    sort.Strings(keys)
    parts := make([]string, len(keys))
    for i, k := range keys { parts[i] = k + " = " + with[k] }
    return " with (" + strings.Join(parts, ", ") + ")"
}

// columnStorageStmts emits newColumnStmts for every column a newly created
// table declares itself. Columns it inherits get their settings from
// inheritedColumnStmts, so only a foreign table's column options are added
// for them here.
func columnStorageStmts(fq string, dt *schema.Table) []string {
    out := []string{}
    for _, cn := range sortedKeys(dt.Columns) {
        c := dt.Columns[cn]
        if c == nil { continue }
        if inheritedColumn(dt, c, nil) {
            if dt.Foreign != nil { out = append(out, foreignColumnOptionStmts(fq, cn, c)...) }
            continue
        }
        out = append(out, newColumnStmts(fq, cn, c)...)
    }
    return out
}

// foreignColumnOptionStmts adds the wrapper options column cn of new foreign
// table fq declares.
func foreignColumnOptionStmts(fq, cn string, c *schema.Column) []string {
    clause := fdwOptionsAlter(c.FDWOptions, nil)
    if clause == "" { return nil }
    return []string{fmt.Sprintf("alter foreign table %s alter column %s %s;", pqIdent(fq), quoteIdent(cn), clause)}
}

// newColumnStmts emits the column settings CREATE TABLE and ADD COLUMN
// cannot carry: SET STORAGE (accepted inline only from PostgreSQL 16), SET
// STATISTICS, attribute options and a foreign table column's options.
func newColumnStmts(fq, cn string, c *schema.Column) []string {
    out := foreignColumnOptionStmts(fq, cn, c)
    target := fmt.Sprintf("alter table %s alter column %s", pqIdent(fq), quoteIdent(cn))
    if c.Storage != "" { out = append(out, fmt.Sprintf("%s set storage %s;", target, c.Storage)) }
    if c.StatisticsTarget != nil && *c.StatisticsTarget >= 0 {
        out = append(out, fmt.Sprintf("%s set statistics %d;", target, *c.StatisticsTarget))
    }
    return append(out, attributeOptionStmts(target, c, &LiveColumn{})...)
}

// attributeOptionStmts emits SET (...) / RESET (...) for the attribute
// options c manages that differ from lc. A value of 0 means no override,
// which PostgreSQL's ANALYZE treats the same as an unset option.
func attributeOptionStmts(target string, c *schema.Column, lc *LiveColumn) []string {
    set, reset := []string{}, []string{}
    for _, o := range []struct {
        name string
        want *float64
    }{{"n_distinct", c.NDistinct}, {"n_distinct_inherited", c.NDistinctInherited}} {
        if o.want == nil { continue }
        cur := 0.0
        if v, ok := lc.Options[o.name]; ok { cur, _ = strconv.ParseFloat(v, 64) }
        switch *o.want {
        case cur:
        case 0:
            reset = append(reset, o.name)
        default:
            set = append(set, o.name+" = "+strconv.FormatFloat(*o.want, 'g', -1, 64))
        }
    }
    out := []string{}
    if len(set) > 0 { out = append(out, fmt.Sprintf("%s set (%s);", target, strings.Join(set, ", "))) }
    if len(reset) > 0 { out = append(out, fmt.Sprintf("%s reset (%s);", target, strings.Join(reset, ", "))) }
    return out
}

// tableStorageStmts reconciles an existing table's persistence and storage
// parameters. SET LOGGED / UNLOGGED rewrites the table, so it is gated
// behind --unsafe. A present `with` block is authoritative: live parameters
// it does not list are reset.
func tableStorageStmts(fq string, dt *schema.Table, lt *LiveTable, unsafe bool) []string {
    out := []string{}
    if dt.Unlogged != nil && *dt.Unlogged != lt.Unlogged && unsafe {
        mode := "logged"
        if *dt.Unlogged { mode = "unlogged" }
        out = append(out, fmt.Sprintf("alter table %s set %s;", pqIdent(fq), mode))
    }
    if dt.With == nil { return out }
    return append(out, storageParamStmts("table", fq, dt.With, lt)...)
}

// storageParamStmts emits ALTER <kind> ... SET (...) / RESET (...) so the
// relation's storage parameters match with exactly: live parameters with
// does not list are reset. toast.* parameters are skipped while the
// relation has no TOAST table, since PostgreSQL silently discards them.
func storageParamStmts(kind, fq string, with map[string]string, lt *LiveTable) []string {
    out := []string{}
    set, reset := []string{}, []string{}
    want := map[string]bool{}
    live := lt.Options
    for _, k := range sortedKeys(with) {
        want[strings.ToLower(k)] = true
        if lt.NoToast && strings.HasPrefix(strings.ToLower(k), "toast.") { continue }
        lv, ok := live[strings.ToLower(k)]
        if !ok || !strings.EqualFold(normalizeConfigValue(lv), normalizeConfigValue(with[k])) {
            set = append(set, k+" = "+with[k])
        }
    }
    for _, k := range sortedKeys(live) {
        if !want[k] { reset = append(reset, k) }
    }
    if len(set) > 0 { out = append(out, fmt.Sprintf("alter %s %s set (%s);", kind, pqIdent(fq), strings.Join(set, ", "))) }
    if len(reset) > 0 { out = append(out, fmt.Sprintf("alter %s %s reset (%s);", kind, pqIdent(fq), strings.Join(reset, ", "))) }
    return out
}

// columnTypeAliases maps type spellings to one canonical form for comparison.
// Ordered: longer spellings must be rewritten before their substrings.
var columnTypeAliases = [][2]string{
    {"character varying", "varchar"},
    {"char varying", "varchar"},
    {"bit varying", "varbit"},
    {"double precision", "float8"},
    {"timestamp with time zone", "timestamptz"},
    {"timestamp without time zone", "timestamp"},
    {"time with time zone", "timetz"},
    {"time without time zone", "time"},
    {"character", "char"},
    {"integer", "int"},
    {"int4", "int"},
    {"int8", "bigint"},
    {"int2", "smallint"},
    {"boolean", "bool"},
    {"float4", "real"},
    {"decimal", "numeric"},
    {"bigserial", "bigint"},
    {"smallserial", "smallint"},
    {"serial", "int"},
}

// normalizeColumnType canonicalizes a column type for comparison between YAML
// spellings (varchar(255)) and live format_type output (character varying(255)).
// serial types normalize to their base integer type so a YAML `serial` column
// never diffs against its live `integer` + nextval default form.
func normalizeColumnType(t string) string { return activeScope().columnType(t) }

// columnType is normalizeColumnType in scope sc.
func (sc *nameScope) columnType(t string) string { return memoized(&sc.columnTypes, t, sc.columnTypeUncached) }

func (sc *nameScope) columnTypeUncached(t string) string {
    s := strings.ToLower(strings.TrimSpace(t))
    s = strings.ReplaceAll(s, `"`, "")
    s = strings.Join(strings.Fields(s), " ")
    s = sc.unqualifyFolded(s)
    // move an inline modifier past a with/without suffix so
    // "timestamp(6) with time zone" aliases like "timestamptz(6)"
    if i := strings.Index(s, "("); i >= 0 {
        if j := strings.Index(s[i:], ")"); j >= 0 {
            base, mod, rest := s[:i], s[i:i+j+1], s[i+j+1:]
            if strings.HasPrefix(rest, " with") {
                s = base + rest + mod
            }
        }
    }
    for _, a := range columnTypeAliases {
        s = strings.ReplaceAll(s, a[0], a[1])
    }
    s = strings.ReplaceAll(s, " (", "(")
    s = strings.ReplaceAll(s, ", ", ",")
    s = floatTypeRe.ReplaceAllStringFunc(s, func(m string) string {
        sm := floatTypeRe.FindStringSubmatch(m)
        if sm[2] != "" {
            if n, err := strconv.Atoi(sm[2]); err == nil && n <= 24 { return "real" + sm[3] }
        }
        return "float8" + sm[3]
    })
    s = bareCharTypeRe.ReplaceAllStringFunc(s, func(m string) string {
        if strings.HasSuffix(m, "(") { return m }
        return m + "(1)"
    })
    s = arrayBoundsRe.ReplaceAllString(s, "[]")
    return s
}

var (
    // PostgreSQL maps float(1..24) to real and bare float / float(25..53) to double precision.
    floatTypeRe    = regexp.MustCompile(`\bfloat(\((\d+)\))?($|[^a-z0-9_(])`)
    bareCharTypeRe = regexp.MustCompile(`\b(char|bit)\b\(?`)
    arrayBoundsRe  = regexp.MustCompile(`(\[\d*\])+`)
)

func isSerialType(t string) bool {
    switch strings.ToLower(strings.TrimSpace(t)) {
    case "serial", "smallserial", "bigserial", "serial2", "serial4", "serial8":
        return true
    }
    return false
}

// defaultsEqual compares a YAML default with its live pg_get_expr form. The
// parenthesis-insensitive constraint normalization is the fallback, since
// PostgreSQL wraps compound defaults in parentheses ((now() + '1 day')).
// Two defaults that cast the same expression to other types differ (see
// exprDrops.retyped).
func defaultsEqual(desired, live string) bool {
    if d, l := normalizedDefaultExpr(desired), normalizedDefaultExpr(live); d.text == l.text && !d.drops.retyped(&l.drops) { return true }
    return constraintDefsEqual(desired, live)
}

// memoized returns f(s), caching it in cache. The normalizers it wraps are
// pure and see the same strings many times, especially in split plans,
// which re-diff every earlier group at each step.
func memoized(cache *sync.Map, s string, f func(string) string) string {
    if v, ok := cache.Load(s); ok { return v.(string) }
    v := f(s)
    cache.Store(s, v)
    return v
}

// memoizedExpr is memoized for a normalizer that also reports what it
// dropped.
func memoizedExpr(cache *sync.Map, s string, f func(string) *normalizedExpr) *normalizedExpr {
    if v, ok := cache.Load(s); ok { return v.(*normalizedExpr) }
    v := f(s)
    cache.Store(s, v)
    return v
}

// normalizedDefaultExpr canonicalizes a default expression for comparison:
// runs canonicalizeExpr (live defaults come back as 'x'::text, '-1'::integer
// or nextval('s'::regclass)), lowercases everything outside single-quoted
// strings, and collapses whitespace. It also returns what it dropped.
func normalizedDefaultExpr(s string) *normalizedExpr { return memoizedExpr(&activeScope().defaultExprs, s, normalizeDefaultExprUncached) }

func normalizeDefaultExprUncached(s string) *normalizedExpr {
    n := &normalizedExpr{}
    var b strings.Builder
    inQ := false
    rs := []rune(strings.TrimSpace(canonicalizeExprNoting(s, &n.drops)))
    for i := 0; i < len(rs); i++ {
        r := rs[i]
        if r == '\'' {
            inQ = !inQ
            b.WriteRune(r)
            continue
        }
        if inQ {
            b.WriteRune(r)
            continue
        }
        if r == ':' && i+1 < len(rs) && rs[i+1] == ':' {
            i++
            start := i + 1
            for i+1 < len(rs) && isCastTypeChar(rs[i+1]) { i++ }
            n.drops.noteLeftoverCast(string(rs[start : i+1]))
            continue
        }
        if r >= 'A' && r <= 'Z' { r += 'a' - 'A' }
        b.WriteRune(r)
    }
    out := strings.Join(strings.Fields(b.String()), " ")
    out = strings.ReplaceAll(out, " (", "(")
    out = n.drops.dropSequenceQualifier(out, "public")
    for _, sch := range activeScope().path { out = n.drops.dropSequenceQualifier(out, sch) }
    n.text = out
    return n
}

// renderFKDef renders the definition body of a foreign key constraint
// (everything after "add constraint <name>"). Clauses are emitted in
// pg_get_constraintdef() order and defaults (MATCH SIMPLE, NO ACTION) are
// omitted as it omits them, so a live definition compares equal.
func renderFKDef(fk *schema.ForeignKey) string {
    def := fmt.Sprintf("foreign key (%s) references %s(%s)", constraintColumns(fk.Columns), pqIdent(fk.RefTable), constraintColumns(fk.RefColumns))
    if m := strings.ToLower(strings.TrimSpace(fk.Match)); m != "" && m != "simple" { def += " match " + m }
    if a := fkAction(fk.OnUpdate); a != "" { def += " on update " + a }
    if a := fkAction(fk.OnDelete); a != "" { def += " on delete " + a }
    def += deferralClause(fk.Deferrable, fk.InitiallyDeferred)
    return def + validityClause(fk.NotValid, fk.NotEnforced)
}

// liveNotValid reports whether a pg_get_constraintdef() output marks the
// constraint NOT VALID.
func liveNotValid(def string) bool {
    return strings.HasSuffix(strings.ToUpper(strings.TrimSpace(def)), " NOT VALID")
}

// validityClause renders NOT VALID / NOT ENFORCED. A constraint that is not
// enforced is never validated, and pg_get_constraintdef() then prints only
// NOT ENFORCED.
func validityClause(notValid, notEnforced bool) string {
    switch {
    case notEnforced:
        return " not enforced"
    case notValid:
        return " not valid"
    }
    return ""
}

// constraintColumns renders a PRIMARY KEY / UNIQUE / FOREIGN KEY column list,
// quoting each column while keeping the temporal markers of PostgreSQL 18:
// a trailing WITHOUT OVERLAPS ("valid without overlaps") and a leading
// PERIOD ("period valid").
func constraintColumns(cols []string) string {
    parts := make([]string, 0, len(cols))
    for _, c := range cols {
        lc := strings.ToLower(strings.TrimSpace(c))
        switch {
        case strings.HasSuffix(lc, " without overlaps"):
            parts = append(parts, quoteIdent(constraintColumnName(c))+" without overlaps")
        case strings.HasPrefix(lc, "period "):
            parts = append(parts, "period "+quoteIdent(constraintColumnName(c)))
        default:
            parts = append(parts, quoteIdent(constraintColumnName(c)))
        }
    }
    return strings.Join(parts, ", ")
}

// constraintColumnName strips the PERIOD / WITHOUT OVERLAPS markers from a
// constraint column entry.
func constraintColumnName(c string) string {
    c = strings.TrimSpace(c)
    lc := strings.ToLower(c)
    if strings.HasSuffix(lc, " without overlaps") { return strings.TrimSpace(c[:len(c)-len(" without overlaps")]) }
    if strings.HasPrefix(lc, "period ") { return strings.TrimSpace(c[len("period "):]) }
    return c
}

// fkAction normalizes an ON DELETE / ON UPDATE action: the keywords are
// lower-cased and a SET NULL / SET DEFAULT column list is quoted like other
// column lists, keeping names that are already quoted.
func fkAction(a string) string {
    action, cols, _ := strings.Cut(a, "(")
    action = strings.Join(strings.Fields(strings.ToLower(action)), " ")
    if action == "no action" { return "" }
    if cols == "" { return action }
    names := []string{}
    for _, c := range strings.Split(strings.TrimSuffix(strings.TrimSpace(cols), ")"), ",") {
        c = strings.TrimSpace(c)
        if len(c) >= 2 && strings.HasPrefix(c, `"`) && strings.HasSuffix(c, `"`) { c = strings.ReplaceAll(c[1:len(c)-1], `""`, `"`) }
        if c != "" { names = append(names, c) }
    }
    return action + " (" + constraintColumns(names) + ")"
}

func deferralClause(deferrable, initiallyDeferred bool) string {
    switch {
    case initiallyDeferred:
        return " deferrable initially deferred"
    case deferrable:
        return " deferrable"
    }
    return ""
}

// renderConstraintDef renders the definition body of a named check/unique/
// exclude constraint. Returns "" for unknown constraint types.
// indexParameters renders the INCLUDE, WITH and USING INDEX TABLESPACE
// clauses of a primary key or unique constraint's index.
func indexParameters(include []string, with map[string]string, tablespace string) string {
    out := ""
    if len(include) > 0 { out = " include (" + constraintColumns(include) + ")" }
    out += renderStorageParams(with)
    if tablespace != "" { out += " using index tablespace " + quoteIdent(tablespace) }
    return out
}

// tablespaceClause renders a CREATE statement's TABLESPACE clause, or ""
// when the tablespace is unmanaged.
func tablespaceClause(ts string) string {
    if ts == "" { return "" }
    return " tablespace " + quoteIdent(ts)
}

// tablespaceDiffers reports whether a declared tablespace differs from a
// relation's live one, where "" means the database's default tablespace.
// An undeclared tablespace never differs.
func tablespaceDiffers(want, have string, live *Live) bool {
    if want == "" { return false }
    if have == "" {
        have = live.DefaultTablespace
        if have == "" { have = "pg_default" }
    }
    return want != have
}

// tablespaceMoveWarning is the lock warning for SET TABLESPACE, which
// rewrites the relation under an ACCESS EXCLUSIVE lock.
func tablespaceMoveWarning(what, table string) Warning {
    return Warning{Table: table, Kind: "lock", Message: fmt.Sprintf("moving %s to another tablespace rewrites it under an ACCESS EXCLUSIVE lock, blocking reads and writes until it finishes", what)}
}

// excludeWithTablespace inserts USING INDEX TABLESPACE into an exclusion
// constraint's definition, before its WHERE clause if it has one.
func excludeWithTablespace(expr, tablespace string) string {
    if tablespace == "" { return expr }
    clause := " using index tablespace " + quoteIdent(tablespace)
    depth := 0
    lower := strings.ToLower(expr)
    for i := 0; i < len(expr); i++ {
        switch expr[i] {
        case '(':
            depth++
        case ')':
            depth--
        case ' ':
            if depth == 0 && strings.HasPrefix(lower[i:], " where ") || depth == 0 && strings.HasPrefix(lower[i:], " where(") {
                return expr[:i] + clause + expr[i:]
            }
        }
    }
    return expr + clause
}

func renderConstraintDef(ct *schema.Constraint) string {
    switch strings.ToLower(ct.Type) {
    case "check":
        def := fmt.Sprintf("check (%s)", ct.Expression)
        if ct.NoInherit { def += " no inherit" }
        return def + validityClause(ct.NotValid, ct.NotEnforced)
    case "unique":
        nulls := ""
        if ct.NullsNotDistinct { nulls = " nulls not distinct" }
        return fmt.Sprintf("unique%s (%s)", nulls, constraintColumns(ct.Columns)) + indexParameters(ct.Include, ct.With, ct.Tablespace) + deferralClause(ct.Deferrable, ct.InitiallyDeferred)
    case "exclude":
        return fmt.Sprintf("exclude %s", excludeWithTablespace(excludeExpression(ct), ct.Tablespace)) + deferralClause(ct.Deferrable, ct.InitiallyDeferred)
    }
    return ""
}

// expandPolicyCmd maps pg_policy.polcmd's single-char encoding to the command
// keyword used in CREATE POLICY ... FOR <cmd>.
func expandPolicyCmd(c string) string {
    switch c {
    case "r":
        return "select"
    case "a":
        return "insert"
    case "w":
        return "update"
    case "d":
        return "delete"
    default: // "*"
        return "all"
    }
}

// policyMatchesLive reports whether a desired policy is already in place. The
// expressions are compared with normalizeConstraintDef, so the casts, parens
// and quoting PostgreSQL adds when echoing an expression back (`member_id =
// (current_setting('app.member_id'))::bigint`) do not count as a change.
//
// An empty desired As means PERMISSIVE and an empty desired For means ALL; an
// empty desired To means PUBLIC, which is how the live side reports a policy
// with no TO clause. An explicit public entry in To is ignored for the same
// reason (polroles' 0 entry is dropped).
// policyToPublic reports whether a policy's roles include PUBLIC, which
// makes PostgreSQL ignore the others.
func policyToPublic(roles []string) bool {
    for _, r := range roles {
        if strings.EqualFold(strings.TrimSpace(r), "public") { return true }
    }
    return false
}

func policyMatchesLive(pol *schema.Policy, live *LivePolicy, currentUser string) bool {
    if (pol.As == "restrictive") != live.Restrictive { return false }
    wantCmd := strings.ToLower(pol.For)
    if wantCmd == "" { wantCmd = "all" }
    if wantCmd != live.Cmd { return false }

    wantRoles := []string{}
    if !policyToPublic(pol.To) {
        for _, r := range pol.To { wantRoles = append(wantRoles, strings.ToLower(resolveRole(r, currentUser))) }
    }
    liveRoles := append([]string{}, live.Roles...)
    for i, r := range liveRoles { liveRoles[i] = strings.ToLower(r) }
    sort.Strings(wantRoles)
    sort.Strings(liveRoles)
    if len(wantRoles) != len(liveRoles) { return false }
    for i := range wantRoles {
        if wantRoles[i] != liveRoles[i] { return false }
    }

    return constraintDefsEqual(pol.Using, live.Using) &&
        constraintDefsEqual(pol.WithCheck, live.WithCheck)
}

// constraintDefsEqual compares a declared definition with a live one. Where
// they differ as written, a declared OPERATOR(pg_catalog.op) or
// OPERATOR(public.op) also matches the bare operator on the live side (see
// bareVisibleOperators).
func constraintDefsEqual(desired, live string) bool {
    if constraintDefsMatch(desired, live) { return true }
    bare := bareVisibleOperators(desired, nil)
    return bare != desired && constraintDefsMatch(bare, live)
}

// constraintDefsMatch compares two definitions with normalizeConstraintDef.
// A string literal that either side marks as an object name ('seq'::regclass)
// matches its qualified spelling on the other side, where the cast may be
// missing, when the qualifier is optional (see nameScope). Two definitions
// that cast the same expression to other types differ (see
// exprDrops.retyped).
func constraintDefsMatch(desired, live string) bool {
    d, l, ok := constraintDefsMatchDrops(desired, live)
    return ok && !d.retyped(l)
}

// constraintDefsMatchDrops reports whether two definitions normalise to the
// same text as constraintDefsMatch compares them, and what the
// normalisation that matched dropped from each.
func constraintDefsMatchDrops(desired, live string) (d, l *exprDrops, ok bool) {
    dn, ln := normalizedConstraintDef(desired), normalizedConstraintDef(live)
    if dn.text == ln.text { return &dn.drops, &ln.drops, true }
    names := objectLiteralNames(desired, live)
    if len(names) == 0 { return nil, nil, false }
    dn, ln = normalizeConstraintDefWith(desired, names), normalizeConstraintDefWith(live, names)
    return &dn.drops, &ln.drops, dn.text == ln.text
}

// triggerDefsEqual compares a desired CREATE TRIGGER statement against
// pg_get_triggerdef() output. The part before EXECUTE goes through
// normalizeConstraintDef (case, quoting, parens, casts, whitespace, public.
// qualifiers); the function call is compared by name, where EXECUTE FUNCTION
// equals EXECUTE PROCEDURE and a public or pg_catalog qualifier is ignored,
// and by argument values, since PostgreSQL stores every argument as a string.
func triggerDefsEqual(desired, live string) bool {
    dh, dc, dok := splitTriggerCall(desired)
    lh, lc, lok := splitTriggerCall(live)
    if dok && lok {
        if equal, ok := triggerCallsEqual(dc, lc); ok { return equal && constraintDefsEqual(dh, lh) }
    }
    n := func(s string) *normalizedExpr { return normalizedConstraintDef(strings.TrimSuffix(strings.TrimSpace(s), ";")) }
    text := func(n *normalizedExpr) string { return strings.ReplaceAll(n.text, "executefunction", "executeprocedure") }
    d, l := n(desired), n(live)
    return text(d) == text(l) && !d.drops.retyped(&l.drops)
}

var simpleIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

// renderTriggerEvents joins trigger events with OR, upper-casing the event
// keywords only. UPDATE OF column names keep their spelling: quoted names
// pass through, bare names that need quoting are quoted, and simple bare
// names are left for PostgreSQL to fold.
func renderTriggerEvents(events []string) string {
    out := make([]string, 0, len(events))
    for _, e := range events {
        e = strings.TrimSpace(e)
        lower := strings.ToLower(e)
        if !strings.HasPrefix(lower, "update of ") {
            out = append(out, strings.ToUpper(e))
            continue
        }
        cols := []string{}
        for _, c := range splitTopLevel(e[len("update of "):]) {
            c = strings.TrimSpace(c)
            if !strings.HasPrefix(c, `"`) && !simpleIdentRe.MatchString(c) { c = quoteIdent(c) }
            cols = append(cols, c)
        }
        out = append(out, "UPDATE OF "+strings.Join(cols, ", "))
    }
    return strings.Join(out, " OR ")
}

// canonicalTriggerEvents orders trigger events the way pg_get_triggerdef()
// prints them (INSERT, DELETE, UPDATE, TRUNCATE) so a reordered YAML event
// list does not read as a definition change. Events are ranked by their
// first word, so "update of col" sorts with "update".
func canonicalTriggerEvents(events []string) []string {
    rank := func(e string) int {
        f := strings.Fields(e)
        if len(f) == 0 { return 4 }
        switch strings.ToLower(f[0]) {
        case "insert":
            return 0
        case "delete":
            return 1
        case "update":
            return 2
        case "truncate":
            return 3
        }
        return 4
    }
    out := make([]string, len(events))
    copy(out, events)
    sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
    return out
}

// normalizeConstraintDef canonicalizes a constraint definition for comparison
// against pg_get_constraintdef() output: after canonicalizeExpr (IN/BETWEEN/
// LIKE rewrites, cast removal, the qualifiers of the search_path's schemas),
// outside single-quoted strings it strips double quotes, parentheses,
// whitespace, and "public." qualifiers, and lowercases. String literals are
// kept as written, except that one naming a database object loses an
// optional qualifier (see unqualifyObjectLiterals).
// A trailing NOT VALID is ignored: validating the constraint
// later is not a definition change. Stripping parentheses and whitespace can make
// differently-grouped expressions compare equal; that errs toward emitting no
// SQL, same as the old name-only matching.
func normalizeConstraintDef(s string) string { return normalizedConstraintDef(s).text }

// normalizedConstraintDef is normalizeConstraintDef with what it dropped.
func normalizedConstraintDef(s string) *normalizedExpr { return memoizedExpr(&activeScope().constraintDefs, s, normalizeConstraintDefUncached) }

func normalizeConstraintDefUncached(s string) *normalizedExpr { return normalizeConstraintDefWith(s, nil) }

// normalizeConstraintDefWith is normalizedConstraintDef for a definition in
// which the string literals spelling an object name in names lose their
// optional qualifier too.
func normalizeConstraintDefWith(s string, names map[string]bool) *normalizedExpr {
    n := &normalizedExpr{}
    var b, seg strings.Builder
    flush := func() {
        b.WriteString(n.drops.dropPublic(seg.String()))
        seg.Reset()
    }
    inQ := false
    rs := []rune(strings.TrimSpace(canonicalizeExprNoting(unqualifyObjectLiterals(s, names, &n.drops), &n.drops)))
    for i := 0; i < len(rs); i++ {
        r := rs[i]
        if r == '\'' {
            if !inQ { flush() }
            inQ = !inQ
            b.WriteRune(r)
            continue
        }
        if inQ {
            b.WriteRune(r)
            continue
        }
        if r == ':' && i+1 < len(rs) && rs[i+1] == ':' {
            i++
            start := i + 1
            for i+1 < len(rs) && isCastTypeChar(rs[i+1]) { i++ }
            n.drops.noteLeftoverCast(string(rs[start : i+1]))
            continue
        }
        switch r {
        case '"', '(', ')', ' ', '\t', '\n', '\r':
            continue
        }
        if r >= 'A' && r <= 'Z' { r += 'a' - 'A' }
        seg.WriteRune(r)
    }
    flush()
    n.text = strings.TrimSuffix(b.String(), "notvalid")
    return n
}

func isCastTypeChar(r rune) bool {
    return r == '_' || r == '.' || r == '[' || r == ']' ||
        (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func renderColumn(name string, c *schema.Column) string {
    parts := []string{quoteIdent(name), c.Type}
    if c.Compression != "" && c.Compression != "default" { parts = append(parts, "compression "+c.Compression) }
    if c.Collate != "" { parts = append(parts, "collate "+collationIdent(c.Collate)) }
    if !c.Nullable && !c.NotValid {
        if c.NotNullName != "" { parts = append(parts, "constraint "+quoteIdent(c.NotNullName)) }
        parts = append(parts, "not null")
    }
    if c.Generated != "" {
        kind := "stored"
        if c.Virtual { kind = "virtual" }
        parts = append(parts, "generated always as ("+c.Generated+") "+kind)
    } else if c.Identity != "" {
        // identity columns cannot also have a default expression
        parts = append(parts, "generated "+c.Identity+" as identity"+identityOptionsClause(c.IdentityOptions))
    } else if c.Default != "" {
        parts = append(parts, "default "+c.Default)
    }
    return strings.Join(parts, " ")
}

// pqIdent quotes a name, or a "schema.name" pair split at the first dot,
// as SQL identifiers, doubling embedded double quotes.
func pqIdent(fq string) string {
    if schemaName, name, ok := strings.Cut(fq, "."); ok {
        return quoteIdent(schemaName) + "." + quoteIdent(name)
    }
    return quoteIdent(fq)
}

func quoteIdent(s string) string {
    return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// partitionedIndexStmts builds index idxFq on partitioned table without
// blocking writes, since PostgreSQL cannot build it CONCURRENTLY: the index
// is created ON ONLY the parent (invalid until complete), built
// concurrently on each leaf partition, created ON ONLY each sub-partitioned
// partition, and attached bottom-up, which makes the parent valid once
// every partition is attached. Every statement is safe to rerun after an
// interruption. build renders a CREATE INDEX for (index, table, only,
// concurrently).
func partitionedIndexStmts(live *Live, table, idxFq string, build func(idx, table string, only, concurrently bool) string) []string {
    out := []string{build(idxFq, table, true, false)}
    var attach func(table, idxFq string)
    attach = func(table, idxFq string) {
        _, parentIdx := splitQualified(idxFq)
        for _, child := range live.Partitions[table] {
            sch, rel := splitQualified(child)
            childIdx := sch + "." + truncateIdent(rel+"_"+parentIdx)
            if lt := live.Tables[child]; lt != nil && lt.Partitioned {
                out = append(out, build(childIdx, child, true, false))
                attach(child, childIdx)
            } else {
                out = append(out, build(childIdx, child, false, true))
            }
            out = append(out, fmt.Sprintf("alter index %s attach partition %s;", pqIdent(idxFq), pqIdent(childIdx)))
        }
    }
    attach(table, idxFq)
    return out
}

// renderIndexElems renders index column entries: plain identifiers are quoted,
// entries containing non-identifier characters are treated as expressions and
// wrapped in parentheses, operator classes (per-index or per-column) are
// appended unquoted, and a trailing ASC/DESC/NULLS FIRST/NULLS LAST ordering
// is kept after the opclass.
func renderIndexElems(ix *schema.Index, cols map[string]*schema.Column) string {
    parts := make([]string, 0, len(ix.Columns))
    for _, c := range ix.Columns {
        base, ordering := splitIndexOrdering(c)
        elem := quoteIdent(base)
        if isIndexExpression(base) && cols[base] == nil { elem = "(" + base + ")" }
        if coll := indexColumnCollation(ix, c); coll != "" { elem += " collate " + collationIdent(coll) }
        if oc := indexColumnOpclass(ix, c); oc != "" { elem += " " + oc }
        if ordering != "" { elem += " " + ordering }
        parts = append(parts, elem)
    }
    return strings.Join(parts, ", ")
}

// renderIndexTail renders the clauses after an index's column list: INCLUDE,
// NULLS NOT DISTINCT, WITH (storage parameters, sorted), TABLESPACE and WHERE.
func renderIndexTail(ix *schema.Index) string {
    out := ""
    if len(ix.Include) > 0 { out += " include (" + joinIdentList(ix.Include) + ")" }
    if ix.NullsNotDistinct { out += " nulls not distinct" }
    if len(ix.With) > 0 {
        keys := make([]string, 0, len(ix.With))
        for k := range ix.With { keys = append(keys, k) }
        sort.Strings(keys)
        parts := make([]string, 0, len(keys))
        for _, k := range keys { parts = append(parts, k+" = "+ix.With[k]) }
        out += " with (" + strings.Join(parts, ", ") + ")"
    }
    if ix.Tablespace != "" { out += " tablespace " + quoteIdent(ix.Tablespace) }
    if ix.Where != "" { out += fmt.Sprintf(" where (%s)", ix.Where) }
    return out
}

// indexDefinitionDiffers reports whether a desired index no longer matches
// its live definition: uniqueness, access method, key columns/expressions and
// their ordering, INCLUDE columns, predicate, NULLS NOT DISTINCT or storage
// parameters. Operator classes are compared separately by
// indexOpclassesDiffer, since live default opclasses are not spelled out.
func indexDefinitionDiffers(ix *schema.Index, li *LiveIndex) bool {
    method := strings.ToLower(strings.TrimSpace(ix.Using))
    if method == "" { method = "btree" }
    if ix.Unique != li.Unique || method != li.Method || ix.NullsNotDistinct != li.NullsNotDistinct { return true }
    if len(ix.Columns) != len(li.Keys) || len(li.Descending) != len(li.Keys) || len(ix.Include) != len(li.Include) { return true }
    for i, c := range ix.Columns {
        base, ordering := splitIndexOrdering(c)
        if !constraintDefsEqual(base, li.Keys[i]) { return true }
        desc := strings.HasPrefix(ordering, "desc")
        nullsFirst := desc
        if strings.HasSuffix(ordering, "nulls first") { nullsFirst = true }
        if strings.HasSuffix(ordering, "nulls last") { nullsFirst = false }
        if desc != li.Descending[i] || nullsFirst != li.NullsFirst[i] { return true }
    }
    for i, c := range ix.Include {
        if !constraintDefsEqual(c, li.Include[i]) { return true }
    }
    if !constraintDefsEqual(ix.Where, li.Predicate) { return true }
    return !storageOptionsEqual(ix.With, li.Options)
}

// storageOptionsEqual compares a YAML with block with an index's reloptions
// (lower-cased names), ignoring quotes and case in values.
func storageOptionsEqual(want, live map[string]string) bool {
    if len(want) != len(live) { return false }
    for k, v := range want {
        lv, ok := live[strings.ToLower(k)]
        if !ok || !strings.EqualFold(normalizeConfigValue(v), normalizeConfigValue(lv)) { return false }
    }
    return true
}

var constraintWithRe = regexp.MustCompile(`(?i)\s+with\s*\(\s*[a-z_][a-z0-9_.]*\s*=[^)]*\)`)

// stripConstraintWith removes the WITH (storage parameters) clauses of a
// constraint definition.
func stripConstraintWith(s string) string {
    if !strings.Contains(strings.ToLower(s), "with") { return s }
    return constraintWithRe.ReplaceAllString(s, "")
}

// constraintMatchesLive compares named constraint ct with its live
// definition from pg_get_constraintdef, leaving out index storage
// parameters and the index's tablespace, which are changed in place.
func constraintMatchesLive(lt *LiveTable, name string, ct *schema.Constraint) bool {
    if strings.EqualFold(ct.Type, "exclude") {
        if equal, ok := excludeMatchesLive(lt, name, ct); ok { return equal }
    }
    c := *ct
    c.Tablespace = ""
    return constraintDefsEqual(stripConstraintWith(renderConstraintDef(&c)), stripConstraintWith(lt.ConstraintDefs[name]))
}

// keyIndexStorageStmts changes the storage parameters of index idxFq, which
// backs a primary key or unique constraint, from live to want with ALTER
// INDEX ... SET / RESET. A nil want leaves them unmanaged.
func keyIndexStorageStmts(idxFq string, want map[string]string, li *LiveIndex) []string {
    if want == nil || li == nil || storageOptionsEqual(want, li.Options) { return nil }
    set, reset := []string{}, []string{}
    keys := map[string]bool{}
    for _, k := range sortedKeys(want) {
        keys[strings.ToLower(k)] = true
        if lv, ok := li.Options[strings.ToLower(k)]; !ok || !strings.EqualFold(normalizeConfigValue(lv), normalizeConfigValue(want[k])) {
            set = append(set, k+" = "+want[k])
        }
    }
    for _, k := range sortedKeys(li.Options) {
        if !keys[k] { reset = append(reset, k) }
    }
    out := []string{}
    if len(set) > 0 { out = append(out, fmt.Sprintf("alter index %s set (%s);", pqIdent(idxFq), strings.Join(set, ", "))) }
    if len(reset) > 0 { out = append(out, fmt.Sprintf("alter index %s reset (%s);", pqIdent(idxFq), strings.Join(reset, ", "))) }
    return out
}

func liveIndexOpclasses(lt *LiveTable) map[string][]LiveIndexOpclass {
    if lt == nil { return nil }
    return lt.IndexOpclasses
}

// indexChanged reports whether desired index ix differs from lt's live
// index name, in its definition or its operator classes. An index whose
// live definition is unknown counts as unchanged.
func indexChanged(ix *schema.Index, lt *LiveTable, name string) bool {
    if lt == nil { return false }
    if li := lt.IndexDefs[name]; li != nil && indexDefinitionDiffers(ix, li) { return true }
    liveOCs, ok := lt.IndexOpclasses[name]
    return ok && indexOpclassesDiffer(ix, liveOCs)
}

// indexOpclassesDiffer reports whether the desired index's operator classes
// disagree with the live index's. A desired column with no explicit opclass
// matches any default opclass. Operator class parameters must be the same
// set on both sides. A key-column count mismatch means the index changed in
// ways beyond opclasses (out of scope here), so it is not treated as an
// opclass change.
func indexOpclassesDiffer(ix *schema.Index, live []LiveIndexOpclass) bool {
    if len(ix.Columns) != len(live) { return false }
    for i, c := range ix.Columns {
        oc, params := splitOpclass(indexColumnOpclass(ix, c))
        if oc == "" {
            if !live[i].Default { return true }
        } else if !opclassMatches(oc, live[i]) {
            return true
        }
        if !opclassParamsEqual(params, parseProconfig(live[i].Params)) { return true }
        want := collationName(indexColumnCollation(ix, c))
        if base, _ := splitIndexOrdering(c); want == "default" && live[i].Collation == "" && isIndexExpression(base) { want = "" }
        if want != collationName(live[i].Collation) { return true }
    }
    return false
}

// splitOpclass separates an operator class reference into its name and its
// parameters: `gist_trgm_ops (siglen = '32')` gives gist_trgm_ops and
// {siglen: 32}. Parameter names are lower-cased and the quotes around values
// are dropped.
func splitOpclass(s string) (string, map[string]string) {
    s = strings.TrimSpace(s)
    open := -1
    quoted := false
    for i, r := range s {
        if r == '"' { quoted = !quoted }
        if r == '(' && !quoted { open = i; break }
    }
    if open < 0 || !strings.HasSuffix(s, ")") { return s, nil }
    params := map[string]string{}
    for _, p := range splitTopLevelQuotedParens(s[open+1:len(s)-1]) {
        k, v, ok := strings.Cut(p, "=")
        if !ok { continue }
        v = strings.TrimSpace(v)
        if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' { v = strings.ReplaceAll(v[1:len(v)-1], "''", "'") }
        params[strings.ToLower(strings.Trim(strings.TrimSpace(k), `"`))] = v
    }
    return strings.TrimSpace(s[:open]), params
}

// opclassParamsEqual compares declared operator class parameters with a live
// index column's. Numbers are compared by value and everything else without
// regard to case, as PostgreSQL reads them.
func opclassParamsEqual(want, live map[string]string) bool {
    if len(want) != len(live) { return false }
    for k, v := range want {
        lv, ok := live[k]
        if !ok { return false }
        if a, err := strconv.ParseFloat(v, 64); err == nil {
            if b, err := strconv.ParseFloat(lv, 64); err == nil && a == b { continue }
            return false
        }
        if !strings.EqualFold(v, lv) { return false }
    }
    return true
}

// opclassName makes operator class names comparable: case and quotes are
// ignored, and so is an optional (see nameScope) or pg_catalog qualifier.
func opclassName(s string) string {
    s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), `"`, ""))
    return strings.TrimPrefix(activeScope().unqualifyFolded(s), "pg_catalog.")
}

// opclassMatches reports whether desired operator class name want names the
// live one. An unqualified want matches a live operator class from a schema
// on the session's search_path, which is the one CREATE INDEX would resolve
// it to.
func opclassMatches(want string, live LiveIndexOpclass) bool {
    return opclassName(want) == opclassName(live.Name)
}

// indexColumnCollation returns the COLLATE for one index column entry, keyed
// by the entry with or without its ordering suffix; "" means the default.
func indexColumnCollation(ix *schema.Index, col string) string {
    if v, ok := ix.Collations[col]; ok { return v }
    if base, _ := splitIndexOrdering(col); base != col {
        if v, ok := ix.Collations[base]; ok { return v }
    }
    return ""
}

// indexColumnOpclass returns the operator class for one index column entry:
// a per-column override keyed by the entry (with or without its ordering
// suffix), else the index-wide opclass.
func indexColumnOpclass(ix *schema.Index, col string) string {
    if v, ok := ix.Opclasses[col]; ok { return v }
    if base, _ := splitIndexOrdering(col); base != col {
        if v, ok := ix.Opclasses[base]; ok { return v }
    }
    return ix.Opclass
}

func isIndexExpression(s string) bool {
    for _, r := range s {
        switch {
        case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
        default:
            return true
        }
    }
    return false
}

func joinIdentList(cols []string) string {
    parts := make([]string, 0, len(cols))
    for _, c := range cols {
        if quotedIdentRe.MatchString(c) { parts = append(parts, c) } else { parts = append(parts, quoteIdent(c)) }
    }
    return strings.Join(parts, ", ")
}

func quoteString(s string) string {
    // naive single-quote escaping
    return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// renderExtension emits CREATE EXTENSION with the options set in YAML.
func renderExtension(ext *schema.Extension) string {
    stmt := "create extension "
    if ext.IfNotExists { stmt += "if not exists " }
    stmt += quoteIdent(ext.Name)
    if ext.Schema != "" { stmt += " schema " + quoteIdent(ext.Schema) }
    if ext.Version != "" { stmt += " version " + quoteString(ext.Version) }
    if ext.Cascade { stmt += " cascade" }
    return stmt + ";"
}

// normalizeFunctionSignature normalizes function signatures for comparison
// Removes default values and normalizes whitespace and type names
// Input formats: "schema.func(args)" or "schema.func (args)"
// Args may contain defaults like "key text, val jsonb default null"
func normalizeFunctionSignature(sig string) string { return activeScope().functionSignature(sig) }

// functionSignature is normalizeFunctionSignature in scope sc.
func (sc *nameScope) functionSignature(sig string) string {
    // Find the opening parenthesis
    parenIdx := strings.Index(sig, "(")
    if parenIdx < 0 {
        // No args, return normalized name only
        return strings.ToLower(strings.TrimSpace(sig))
    }
    
    funcPart := strings.TrimSpace(sig[:parenIdx])
    argsPart := strings.TrimSpace(sig[parenIdx+1:])
    // Remove closing paren if present
    argsPart = strings.TrimSuffix(argsPart, ")")
    
    // Normalize function name (lowercase, remove extra spaces)
    funcPart = strings.ToLower(strings.ReplaceAll(funcPart, " ", ""))
    if loc := orderByRe.FindStringIndex(argsPart); loc != nil {
        inner := func(args string) string {
            n := sc.functionSignature(funcPart + "(" + args + ")")
            return strings.TrimSuffix(strings.TrimPrefix(n, funcPart+"("), ")")
        }
        direct, ordered := inner(argsPart[:loc[0]]), inner(argsPart[loc[1]:])
        if direct != "" { direct += " " }
        return funcPart + "(" + direct + "order by " + ordered + ")"
    }
    
    // Parse and normalize arguments
    // Split by comma, but be careful of nested structures
    args := []string{}
    for _, a := range splitTopLevelQuotedParens(argsPart) {
        if arg := sc.functionArg(a); arg != "" { args = append(args, arg) }
    }

    // Reconstruct normalized signature with consistent spacing
    normalizedArgs := strings.Join(args, ", ")
    return fmt.Sprintf("%s(%s)", funcPart, normalizedArgs)
}

// splitTopLevelQuotedParens splits on commas outside parentheses and single
// or double quotes.
func splitTopLevelQuotedParens(s string) []string {
    out, depth, start := []string{}, 0, 0
    var q byte
    for i := 0; i < len(s); i++ {
        switch c := s[i]; {
        case q != 0:
            if c == q { q = 0 }
        case c == '\'' || c == '"':
            q = c
        case c == '(':
            depth++
        case c == ')':
            depth--
        case c == ',' && depth == 0:
            out = append(out, s[start:i])
            start = i + 1
        }
    }
    return append(out, s[start:])
}

// orderByRe finds the ORDER BY that separates an ordered-set aggregate's
// direct arguments from its aggregated ones.
var orderByRe = regexp.MustCompile(`(?i)(?:^|\s)order\s+by\s+`)

// functionArg removes default values from a function argument and normalizes types
// e.g., "key text default null" -> "key text"
// Format is typically: "param_name type_name" or "param_name type_name default value"
func (sc *nameScope) functionArg(arg string) string {
    decl, _ := splitArgDefault(arg)
    return sc.functionArgNoDefault(decl)
}

// functionArgNoDefault normalizes an argument without default clause
func (sc *nameScope) functionArgNoDefault(arg string) string {
    // Format: "param_name type_name", "param_name schema.type_name", or an
    // unnamed bare type (possibly multi-word, e.g. "timestamp with time zone")
    words := strings.Fields(arg)
    if len(words) >= 2 && strings.EqualFold(words[0], "in") {
        words = words[1:]
        arg = strings.Join(words, " ")
    }
    if len(words) < 2 {
        // Unnamed arg: the single token is a type name
        return sc.columnType(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(arg)), "public."))
    }

    // Unnamed multi-word type: pg_get_function_identity_arguments prints
    // unnamed args as bare canonical type names, so "timestamp with time
    // zone[]" must not be parsed as param "timestamp" + type "with time
    // zone[]" — it would never match the YAML's "timestamptz[]" spelling.
    if isMultiWordTypeStart(strings.ToLower(words[0]), strings.ToLower(words[1])) {
        return sc.columnType(arg)
    }

    // Parameter name (first word) - lowercase for comparison
    paramName := strings.ToLower(words[0])

    // Type name (rest) - normalize to lowercase
    typeName := strings.ToLower(strings.Join(words[1:], " "))

    // "public" is the default schema: "public.my_type" and "my_type" are the
    // same type. Introspection returns public's types unqualified, so strip
    // the qualifier before comparing; columnType does the same for the
    // other schemas of sc. Schemas outside it stay qualified on both sides.
    // Strip before the alias pass below so the substring replacements never
    // see the qualifier.
    typeName = strings.TrimPrefix(typeName, "public.")

    // Canonicalize through the same alias table as column types so alias
    // spellings (timestamptz[]) and live canonical forms (timestamp with
    // time zone[]) compare equal instead of re-emitting the function forever.
    typeName = sc.columnType(typeName)

    return paramName + " " + typeName
}

// isMultiWordTypeStart reports whether the first two tokens of an argument
// begin a multi-word Postgres type name rather than "param_name type_name".
func isMultiWordTypeStart(first, second string) bool {
    switch {
    case first == "double" && second == "precision",
        (first == "character" || first == "bit") && second == "varying",
        // covers time/timestamp, with or without an inline modifier ("timestamp(6)")
        strings.HasPrefix(first, "time") && (second == "with" || second == "without"),
        first == "interval" && (second == "year" || second == "month" || second == "day" ||
            second == "hour" || second == "minute" || strings.HasPrefix(second, "second")):
        return true
    }
    return false
}



// versionWarnings reports YAML that uses a feature the server's PostgreSQL
// version lacks, whose statements would fail when the migration runs. Only
// objects live doesn't have yet are checked, so a split plan reports each
// once.
func versionWarnings(live *Live, desired *schema.Database) []Warning {
    v := live.ServerVersion
    if v <= 0 || v >= 180000 || desired == nil { return nil }
    out := []Warning{}
    need := func(what string, version int) {
        if v < version*10000 {
            out = append(out, Warning{Message: fmt.Sprintf("%s needs PostgreSQL %d, and the server runs PostgreSQL %d, so the migration would fail", what, version, v/10000)})
        }
    }
    for _, fq := range sortedKeys(desired.Tables) {
        t := desired.Tables[fq]
        if t == nil { continue }
        lt := live.Tables[fq]
        for _, cn := range sortedKeys(t.Columns) {
            if c := t.Columns[cn]; c != nil && c.Virtual && c.Generated != "" && (lt == nil || lt.Columns[cn] == nil) {
                need(fmt.Sprintf("virtual generated column %s.%s", fq, cn), 18)
            }
            if c := t.Columns[cn]; c != nil && c.NotValid && !c.Nullable && (lt == nil || lt.Columns[cn] == nil || lt.Columns[cn].Nullable) {
                need(fmt.Sprintf("notValid on column %s.%s", fq, cn), 18)
            }
            if c := t.Columns[cn]; c != nil && c.NotNullName != "" && !c.Nullable {
                out = append(out, Warning{Message: fmt.Sprintf("notNullName on column %s.%s is ignored: PostgreSQL %d does not record a name for a column's NOT NULL constraint (PostgreSQL 18 does)", fq, cn, v/10000)})
            }
        }
        for _, ct := range t.Constraints {
            if ct != nil && ct.NotEnforced && (lt == nil || !lt.Constraints[ct.Name]) {
                need(fmt.Sprintf("constraint %s on %s (enforced: false)", ct.Name, fq), 18)
            }
        }
        for _, fk := range t.ForeignKeys {
            if fk != nil && fk.NotEnforced && (lt == nil || !lt.Constraints[fk.Name]) {
                need(fmt.Sprintf("foreign key %s on %s (enforced: false)", fk.Name, fq), 18)
            }
        }
    }
    maintain := func(obj string, grants map[string][]string, liveG map[string]map[string]bool) {
        for _, role := range sortedKeys(grants) {
            for _, p := range grants[role] {
                if strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(p), "*"), "maintain") && !liveG[strings.ToLower(role)]["maintain"] && !liveG[role]["maintain"] {
                    need(fmt.Sprintf("the maintain privilege in %s's grants", obj), 17)
                    return
                }
            }
        }
    }
    for _, fq := range sortedKeys(desired.Tables) {
        if t := desired.Tables[fq]; t != nil { maintain("table "+fq, t.Grants, live.TableGrants[fq]) }
    }
    for _, fq := range sortedKeys(desired.Views) {
        if vw := desired.Views[fq]; vw != nil { maintain("view "+fq, vw.Grants, live.TableGrants[fq]) }
    }
    for i, dp := range desired.DefaultPrivileges {
        if dp != nil { maintain(fmt.Sprintf("defaultPrivileges[%d]", i), dp.Grants["tables"], nil) }
    }
    for _, k := range sortedKeys(desired.Collations) {
        if co := desired.Collations[k]; co != nil && co.Rules != "" && !live.Collations[k] {
            need(fmt.Sprintf("collation %s's rules", k), 16)
        }
    }
    return out
}

// keyIndexTablespaceStmt moves the index behind a key or exclusion
// constraint, which shares the constraint's name, to another tablespace.
func keyIndexTablespaceStmt(fq, index, tablespace string) string {
    return fmt.Sprintf("alter index %s set tablespace %s;", pqIdent(schemaPrefixOf(fq)+index), quoteIdent(tablespace))
}
