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
    Views      map[string]bool
    MatViews   map[string]bool
    // ViewOptions: "schema.view" -> reloptions (security_invoker, security_barrier, check_option)
    ViewOptions map[string]map[string]string
    // ViewColumnComments: "schema.view" -> column -> comment
    ViewColumnComments map[string]map[string]string
    // ViewColumnDefaults: "schema.view" -> column -> DEFAULT expression
    ViewColumnDefaults map[string]map[string]string
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
    // Details is filled by IntrospectDetails (pgy pull) and nil otherwise.
    Details *LiveDetails
    // RoleMembers: member role -> parent role -> exists (pg_auth_members)
    RoleMembers map[string]map[string]bool
    // ColumnDependents: "schema.relation.column" -> the views, materialized
    // views, triggers, policies and rules whose definitions use the column,
    // which PostgreSQL won't let ALTER COLUMN ... TYPE change under them.
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
    // ColumnGrants: "schema.table" -> column -> role -> privilege -> exists (pg_attribute.attacl)
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
    // pruned. Keys are "rel:schema.name", "type:schema.name" and
    // "proc:<normalized signature>".
    ExtensionMembers map[string]bool
    // OwnedSequences: "schema.seq" -> owned by a column (serial / identity /
    // OWNED BY), so it goes away with its column
    OwnedSequences map[string]bool
    // Partitions: "schema.parent" -> its direct partitions, sorted
    Partitions map[string][]string
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
// materialized view (Name is "schema.view"), or a trigger, policy or rule
// (Name is its name, Table the relation it is on).
type ColumnDependent struct {
    Kind, Name, Table string
}

// LiveMembership is a role membership's options. Before PostgreSQL 16,
// Inherit is the member's INHERIT attribute and Set is always true.
type LiveMembership struct {
    Admin, Inherit, Set bool
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
}
type LiveProcedure struct{
    Signature string // "schema.name(identity args)" as introspected
    Args     string // pg_get_function_arguments: the arguments with their defaults
    Body     string
    SQLBody  bool // SQL-standard body; see LiveFunction.SQLBody
    Security string // definer|invoker
    Config   map[string]string // proconfig: lower-cased parameter name -> value
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
type LiveAggregate struct {
    SFunc, SType, FinalFunc, InitCond, CombineFunc, Parallel string
    Options map[string]string
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
}

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
        DomainDefs: map[string]*LiveDomain{},
        Roles: map[string]bool{},
        RoleDefs: map[string]*LiveRole{},
        Owners: map[string]string{},
        DefaultACLs: map[string]map[string]map[string]bool{},
        DefaultACLRows: map[string]bool{},
        RoleMembers: map[string]map[string]bool{},
        RoleMemberOptions: map[string]map[string]*LiveMembership{},
        ColumnDependents: map[string][]ColumnDependent{},
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
        CompositeAttributes: map[string]map[string]string{},
        CompositeCollations: map[string]map[string]string{},
        CompositeComments: map[string]map[string]string{},
        ExtensionMembers: map[string]bool{},
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
               coalesce((select tn.nspname || '.' || ty.typname from pg_type ty join pg_namespace tn on tn.oid = ty.typnamespace where ty.oid = c.reloftype), '')
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
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
            var replident, ofType string
            if err := tableRows.Scan(&schemaName, &tableName, &isPartition, &partitioned, &unlogged, &noToast, &foreign, &server, &ftoptions, &reloptions, &replident, &ofType); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, tableName)
            lt := newLiveTable()
            lt.NoToast = noToast
            if foreign { lt.Foreign = &LiveForeignTable{Server: server, Options: parseFDWOptions(ftoptions)} }
            lt.IsPartition, lt.Partitioned, lt.Unlogged, lt.Options, lt.ReplicaIdentity, lt.OfType = isPartition, partitioned, unlogged, parseProconfig(reloptions), replident, ofType
            l.Tables[key] = lt
        }
        return nil
    })
    
    // Query existing materialized views
    matViewQ := `
        select n.nspname, c.relname, c.reltoastrelid = 0,
               coalesce(c.reloptions, '{}'::text[]) ||
               coalesce((select array_agg('toast.' || o) from pg_class tc, unnest(tc.reloptions) o where tc.oid = c.reltoastrelid), '{}'::text[])
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
        where c.relkind = 'm'
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(matViewQ, func(matViewRows pgx.Rows) error {
        for matViewRows.Next() {
            var schemaName, viewName string
            var noToast bool
            var reloptions []string
            if err := matViewRows.Scan(&schemaName, &viewName, &noToast, &reloptions); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, viewName)
            l.MatViews[key] = true
            lt := newLiveTable()
            lt.NoToast = noToast
            lt.Options = parseProconfig(reloptions)
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
               coalesce(a.attfdwoptions, '{}'::text[])
        from pg_attribute a
        join pg_class c on c.oid = a.attrelid
        join pg_namespace n on n.oid = c.relnamespace
        join pg_type ty on ty.oid = a.atttypid
        left join pg_collation co on co.oid = a.attcollation and a.attcollation <> ty.typcollation
        left join pg_namespace cn on cn.oid = co.collnamespace
        left join pg_attrdef ad on ad.adrelid = a.attrelid and ad.adnum = a.attnum
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
            if err := rows.Scan(&schemaName, &tableName, &colName, &dataType, &nullable, &def, &identity, &generated, &collation, &compression, &storage, &statTarget, &collSchema, &typeStorage, &inherited, &attoptions, &fdwoptions); err != nil { return err }
            key := fmt.Sprintf("%s.%s", schemaName, tableName)
            t := l.Tables[key]
            if t == nil { t = newLiveTable(); l.Tables[key] = t }
            lc := &LiveColumn{Type: dataType, Nullable: nullable, Default: def, Identity: identity, Collation: collation, Compression: compression, Storage: storage, TypeStorage: typeStorage, StatisticsTarget: statTarget, CollationSchema: collSchema, Inherited: inherited}
            if generated != "" { lc.Generated, lc.Default, lc.Virtual = def, "", generated == "v" }
            if len(attoptions) > 0 { lc.Options = parseProconfig(attoptions) }
            if len(fdwoptions) > 0 { lc.FDWOptions = parseFDWOptions(fdwoptions) }
            t.Columns[colName] = lc
        }
        return nil
    })

    nnQ := `
        select n.nspname, c.relname, a.attname, con.conname
        from pg_constraint con
        join pg_class c on c.oid = con.conrelid
        join pg_namespace n on n.oid = c.relnamespace
        join pg_attribute a on a.attrelid = con.conrelid and a.attnum = con.conkey[1]
        where con.contype = 'n' and not con.convalidated
        and n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(nnQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var schemaName, tableName, colName, conName string
            if err := rows.Scan(&schemaName, &tableName, &colName, &conName); err != nil { return err }
            if t := l.Tables[schemaName+"."+tableName]; t != nil {
                if lc := t.Columns[colName]; lc != nil { lc.NotNullNotValid = conName }
            }
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
    // flagged so a desired index without an explicit opclass matches.
    opcQ := `
        select n.nspname, t.relname, i.relname,
               case when opn.nspname in ('pg_catalog', 'public') then op.opcname::text else quote_ident(opn.nspname) || '.' || quote_ident(op.opcname) end,
               op.opcdefault,
               case when x.indcollation[k.ord] = 0 or x.indcollation[k.ord] = coalesce(case when x.indkey[k.ord] > 0 then ta.attcollation end, ity.typcollation)
                    then '' else case when cn.nspname = 'pg_catalog' then '' else cn.nspname || '.' end || co.collname end,
               coalesce(ia.attstattarget::int, -1)
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
            if err := opcRows.Scan(&schemaName, &tableName, &idxName, &opcName, &opcDefault, &collation, &target); err != nil {
                return err
            }
            if t := l.indexedRelation(fmt.Sprintf("%s.%s", schemaName, tableName)); t != nil {
                t.IndexOpclasses[idxName] = append(t.IndexOpclasses[idxName], LiveIndexOpclass{Name: opcName, Default: opcDefault, Collation: collation, StatisticsTarget: target})
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
               not x.indisvalid
        from pg_index x
        join pg_class i on i.oid = x.indexrelid
        join pg_class t on t.oid = x.indrelid
        join pg_am am on am.oid = i.relam
        join pg_namespace n on n.oid = t.relnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(idefQ, func(idefRows pgx.Rows) error {
        for idefRows.Next() {
            var schemaName, tableName, idxName, method, predicate string
            var unique, nullsNotDistinct, constraint, inherited, clustered, invalid bool
            var nKey int16
            var cols, reloptions []string
            var indoption []int16
            if err := idefRows.Scan(&schemaName, &tableName, &idxName, &unique, &method, &nKey, &cols, &indoption, &predicate, &nullsNotDistinct, &reloptions, &constraint, &inherited, &clustered, &invalid); err != nil {
                return err
            }
            t := l.indexedRelation(fmt.Sprintf("%s.%s", schemaName, tableName))
            if t == nil || int(nKey) > len(cols) || int(nKey) > len(indoption) { continue }
            li := &LiveIndex{Unique: unique, Method: method, Keys: cols[:nKey], Include: cols[nKey:], Predicate: predicate, NullsNotDistinct: nullsNotDistinct, Options: parseProconfig(reloptions), Constraint: constraint, Inherited: inherited, Invalid: invalid}
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
        where not c.relispartition and c.relkind in ('r', 'p') and p.relkind in ('r', 'p')
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
        select pn.nspname || '.' || p.relname, cn.nspname || '.' || c.relname
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
            var parent, child string
            if err := rows.Scan(&parent, &child); err != nil { return err }
            l.Partitions[parent] = append(l.Partitions[parent], child)
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
               (a.aggkind = 'h')::text
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
            if err := aggRows.Scan(dest...); err != nil { return err }
            la.Options = map[string]string{}
            for i, o := range schema.AggregateOptions { la.Options[o[1]] = opts[i] }
            l.AggregateDefs[normalizeFunctionSignature(fmt.Sprintf("%s.%s(%s)", schemaName, name, args))] = la
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
        where t.typtype in ('e', 'c', 'r', 'd') and (t.typrelid = 0 or (select c.relkind from pg_class c where c.oid = t.typrelid) = 'c')
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
        where t.typtype in ('e', 'c', 'r', 'd') and (t.typrelid = 0 or (select c.relkind from pg_class c where c.oid = t.typrelid) = 'c')
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
        select n.nspname, t.typname
        from pg_type t
        join pg_namespace n on n.oid = t.typnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        and t.typtype in ('e', 'c', 'r')
        and (t.typrelid = 0 or (select c.relkind from pg_class c where c.oid = t.typrelid) = 'c')
    `
    batch.add(typeQ, func(typeRows pgx.Rows) error {
        for typeRows.Next() {
            var schemaName, typeName string
            if err := typeRows.Scan(&schemaName, &typeName); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, typeName)
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
               coalesce(pg_get_function_arguments(p.oid), '')
        from pg_proc p
        join pg_namespace n on n.oid = p.pronamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
    `
    batch.add(funcQ, func(funcRows pgx.Rows) error {
        for funcRows.Next() {
            var schemaName, funcName, args, src, vol, kind, parallel, returns, fullArgs string
            var secdef, strict, leakproof, internal, sqlBody bool
            var config []string
            var cost, rows float64
            if err := funcRows.Scan(&schemaName, &funcName, &args, &src, &sqlBody, &vol, &secdef, &strict, &leakproof, &kind, &config, &internal, &parallel, &cost, &rows, &returns, &fullArgs); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s(%s)", schemaName, funcName, args)
            if kind == "p" {
                norm := normalizeFunctionSignature(key)
                l.Procedures[norm] = true
                lp := &LiveProcedure{Signature: key, Args: fullArgs, Body: src, SQLBody: sqlBody, Security: "invoker", Config: parseProconfig(config)}
                if secdef { lp.Security = "definer" }
                l.ProcedureDefs[norm] = lp
                continue
            }
            l.Functions[key] = true
            lf := &LiveFunction{Signature: key, Kind: kind, Internal: internal, Body: src, Strict: strict, Leakproof: leakproof, Parallel: parallel, Cost: cost, Rows: rows, Security: "invoker", Volatility: "volatile", Config: parseProconfig(config), Returns: returns, SQLBody: sqlBody, Args: fullArgs}
            if secdef { lf.Security = "definer" }
            switch vol {
            case "i": lf.Volatility = "immutable"
            case "s": lf.Volatility = "stable"
            }
            l.FunctionDefs[normalizeFunctionSignature(key)] = lf
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
                         from aclexplode(s.srvacl) a where a.grantee <> s.srvowner), '{}'::text[])
        from pg_foreign_server s
        join pg_foreign_data_wrapper w on w.oid = s.srvfdw
        where not exists (select 1 from pg_depend d where d.classid = 'pg_foreign_server'::regclass and d.objid = s.oid and d.deptype = 'e')
    `
    batch.add(serverQ, func(rows pgx.Rows) error {
        for rows.Next() {
            var name string
            var options, acl []string
            ls := &LiveServer{}
            if err := rows.Scan(&name, &ls.Wrapper, &ls.Type, &ls.Version, &options, &ls.Owner, &ls.Comment, &ls.UserMapping, &acl); err != nil { return err }
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

    pubRelQ := `
        select p.pubname, n.nspname || '.' || c.relname, coalesce(pg_get_expr(pr.prqual, pr.prrelid), ''),
               (select array_agg(a.attname::text order by a.attname) from pg_attribute a
                where a.attrelid = pr.prrelid and a.attnum = any(pr.prattrs))
        from pg_publication_rel pr
        join pg_publication p on p.oid = pr.prpubid
        join pg_class c on c.oid = pr.prrelid
        join pg_namespace n on n.oid = c.relnamespace
    `
    batch.add(pubRelQ, func(pubRelRows pgx.Rows) error {
        for pubRelRows.Next() {
            var pub, table, where string
            var cols []string
            if err := pubRelRows.Scan(&pub, &table, &where, &cols); err != nil {
                return err
            }
            if lp := l.Publications[pub]; lp != nil { lp.Tables[table] = LivePublicationTable{Columns: cols, Where: where} }
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
               coalesce((select case when cn.nspname = 'pg_catalog' then '' else cn.nspname || '.' end || co.collname
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
               coalesce((select array_agg(coalesce(obj_description(c.oid, 'pg_constraint'), '') order by c.conname) from pg_constraint c where c.contypid = t.oid and c.contype = 'c'), '{}')
        from pg_type t
        join pg_namespace n on n.oid = t.typnamespace
        where n.nspname not in ('pg_catalog', 'information_schema', 'pg_toast')
        and n.nspname !~ '^pg_(toast_)?temp_'
        and t.typtype = 'd'
    `
    batch.add(domQ, func(domRows pgx.Rows) error {
        for domRows.Next() {
            var schemaName, domName, def string
            var notNull bool
            var conNames, conDefs, conComments []string
            if err := domRows.Scan(&schemaName, &domName, &def, &notNull, &conNames, &conDefs, &conComments); err != nil {
                return err
            }
            key := fmt.Sprintf("%s.%s", schemaName, domName)
            l.Domains[key] = true
            ld := &LiveDomain{Default: def, NotNull: notNull, Checks: map[string]string{}, CheckComments: map[string]string{}}
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
               coalesce((select a.comment from pg_available_extensions a where a.name = e.extname), '')
        from pg_extension e
        join pg_namespace n on n.oid = e.extnamespace
    `
    batch.add(extQ, func(extRows pgx.Rows) error {
        for extRows.Next() {
            var extName, extSchema, extVersion, comment, defaultComment string
            var relocatable bool
            if err := extRows.Scan(&extName, &extSchema, &extVersion, &relocatable, &comment, &defaultComment); err != nil {
                return err
            }
            l.Extensions[extName] = true
            l.ExtensionDefs[extName] = &LiveExtension{Schema: extSchema, Version: extVersion, Relocatable: relocatable, Comment: comment, DefaultComment: defaultComment}
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
        where c.relkind in ('r', 'p', 'f')
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
            key := normalizeFunctionSignature(fmt.Sprintf("%s.%s(%s)", schemaName, funcName, args))
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
               bool_or(coalesce((to_jsonb(am) ->> 'set_option')::boolean, true))
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
                    when ev.relkind = 'v' then 'view' when ev.relkind = 'm' then 'matview' else 'rule' end,
               case when d.classid = 'pg_trigger'::regclass then tg.tgname::text
                    when d.classid = 'pg_policy'::regclass then po.polname::text
                    when ev.relkind in ('v', 'm') then evn.nspname || '.' || ev.relname
                    else rw.rulename::text end,
               case when d.classid = 'pg_trigger'::regclass then tgn.nspname || '.' || tgc.relname
                    when d.classid = 'pg_policy'::regclass then pon.nspname || '.' || poc.relname
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
        where d.refclassid = 'pg_class'::regclass and d.refobjsubid > 0 and d.deptype = 'n'
        and d.classid in ('pg_rewrite'::regclass, 'pg_trigger'::regclass, 'pg_policy'::regclass)
        and not (d.classid = 'pg_rewrite'::regclass and ev.oid = c.oid and ev.relkind in ('v', 'm'))
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

    batch.add(memQ, func(memRows pgx.Rows) error {
        for memRows.Next() {
            var member, parent string
            lm := &LiveMembership{}
            if err := memRows.Scan(&member, &parent, &lm.Admin, &lm.Inherit, &lm.Set); err != nil {
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
            key := normalizeFunctionSignature(fmt.Sprintf("%s.%s(%s)", schemaName, funcName, args))
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
        where t.typtype in ('e', 'c', 'r', 'd') and (t.typrelid = 0 or (select c.relkind from pg_class c where c.oid = t.typrelid) = 'c')
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
            if sig, ok := strings.CutPrefix(key, "proc:"); ok { key = "proc:" + normalizeFunctionSignature(sig) }
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
                                where d.datname = current_database() and s.setrole = 0), '{}'::text[])`, func(rows pgx.Rows) error {
        for rows.Next() {
            var config []string
            if err := rows.Scan(&l.CurrentUser, &l.ServerVersion, &config); err != nil { return err }
            l.DatabaseConfig = parseProconfig(config)
        }
        return nil
    })

    extMemQ := `
        select 'rel:' || n.nspname || '.' || c.relname
        from pg_depend d join pg_class c on c.oid = d.objid join pg_namespace n on n.oid = c.relnamespace
        where d.deptype = 'e' and d.classid = 'pg_class'::regclass
        union all
        select 'type:' || n.nspname || '.' || t.typname
        from pg_depend d join pg_type t on t.oid = d.objid join pg_namespace n on n.oid = t.typnamespace
        where d.deptype = 'e' and d.classid = 'pg_type'::regclass
        union all
        select 'tscfg:' || n.nspname || '.' || c.cfgname
        from pg_depend d join pg_ts_config c on c.oid = d.objid join pg_namespace n on n.oid = c.cfgnamespace
        where d.deptype = 'e' and d.classid = 'pg_ts_config'::regclass
        union all
        select 'tsdict:' || n.nspname || '.' || c.dictname
        from pg_depend d join pg_ts_dict c on c.oid = d.objid join pg_namespace n on n.oid = c.dictnamespace
        where d.deptype = 'e' and d.classid = 'pg_ts_dict'::regclass
        union all
        select 'coll:' || n.nspname || '.' || c.collname
        from pg_depend d join pg_collation c on c.oid = d.objid join pg_namespace n on n.oid = c.collnamespace
        where d.deptype = 'e' and d.classid = 'pg_collation'::regclass
        union all
        select 'proc:' || n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')'
        from pg_depend d join pg_proc p on p.oid = d.objid join pg_namespace n on n.oid = p.pronamespace
        where d.deptype = 'e' and d.classid = 'pg_proc'::regclass
    `
    batch.add(extMemQ, func(extMemRows pgx.Rows) error {
        for extMemRows.Next() {
            var key string
            if err := extMemRows.Scan(&key); err != nil {
                return err
            }
            if sig, ok := strings.CutPrefix(key, "proc:"); ok { key = "proc:" + normalizeFunctionSignature(sig) }
            l.ExtensionMembers[key] = true
        }
        return nil
    })

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
    return l, nil
}

// procedureChanged reports whether a live procedure's definition differs from
// desired. Security is compared only when the YAML sets it; body comparison
// is whitespace-trimmed (prosrc stores the body verbatim).
func procedureChanged(p *schema.Procedure, lp *LiveProcedure) bool {
    if routineBodyChanged(p.Language, p.Body, p.Replace, lp.Body, lp.SQLBody) { return true }
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

// configClauses returns SET / RESET action clauses that bring a live
// function's or procedure's configuration in line with a YAML set block. A
// nil block leaves configuration unmanaged; an empty block resets every
// live setting. Clauses are sorted for deterministic output.
var (
    configNumberRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
    configIdentRe  = regexp.MustCompile(`^[a-z_][a-z0-9_$]*$`)
)

// configValueSQL renders a SET value. Each comma-separated element that is
// not already quoted, a number or a lower-case identifier is quoted, so
// values such as 5s, 64MB or America/New_York (which PostgreSQL stores, and
// pgy pull emits, unquoted) form valid SQL, and UTC isn't folded to utc.
func configValueSQL(v string) string {
    parts := splitTopLevelQuoted(v)
    for i, p := range parts {
        p = strings.TrimSpace(p)
        switch {
        case p == "", strings.HasPrefix(p, "'"), strings.HasPrefix(p, `"`), configNumberRe.MatchString(p), configIdentRe.MatchString(p):
        default:
            p = quoteString(p)
        }
        parts[i] = p
    }
    return strings.Join(parts, ", ")
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
            out = append(out, fmt.Sprintf("set %s = %s", k, configValueSQL(desired[k])))
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

// functionAttrClauses returns ALTER FUNCTION action clauses for attribute
// settings the YAML manages (non-empty volatility/security, non-nil
// strict/leakproof/set) that differ from live. Used when the body is unchanged
// (or unmanaged) so an attribute flip doesn't require CREATE OR REPLACE —
// essential for extension-owned functions (e.g. PostGIS LANGUAGE c) whose
// bodies pgy cannot express.
func functionAttrClauses(f *schema.Function, lf *LiveFunction) []string {
    out := []string{}
    if f.Volatility != "" && strings.ToLower(f.Volatility) != lf.Volatility {
        out = append(out, strings.ToLower(f.Volatility))
    }
    if f.Security != "" && strings.ToLower(f.Security) != lf.Security {
        out = append(out, "security "+strings.ToLower(f.Security))
    }
    if f.Strict != nil && *f.Strict != lf.Strict {
        if *f.Strict {
            out = append(out, "strict")
        } else {
            out = append(out, "called on null input")
        }
    }
    if f.Leakproof != nil && *f.Leakproof != lf.Leakproof {
        if *f.Leakproof {
            out = append(out, "leakproof")
        } else {
            out = append(out, "not leakproof")
        }
    }
    if f.Parallel != "" && f.Parallel != lf.Parallel {
        out = append(out, "parallel "+f.Parallel)
    }
    if f.Cost != nil && !float4Equal(*f.Cost, lf.Cost) {
        out = append(out, "cost "+formatFloat(*f.Cost))
    }
    if f.Rows != nil && !float4Equal(*f.Rows, lf.Rows) {
        out = append(out, "rows "+formatFloat(*f.Rows))
    }
    out = append(out, configClauses(f.Set, lf.Config)...)
    return out
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
            out = append(out, fmt.Sprintf("alter type %s add attribute %s %s%s", pqIdent(fq), pqIdent(n), t+attributeCollate(td, n), tail))
        case unsafe && (normalizeColumnType(t) != normalizeColumnType(lt) || collationName(td.AttributeCollations[n]) != collationName(liveColl[n])):
            out = append(out, fmt.Sprintf("alter type %s alter attribute %s type %s%s", pqIdent(fq), pqIdent(n), t+attributeCollate(td, n), tail))
        }
    }
    if unsafe {
        for _, n := range sortedKeys(live) {
            if _, ok := td.Attributes[n]; !ok { out = append(out, fmt.Sprintf("alter type %s drop attribute %s%s", pqIdent(fq), pqIdent(n), tail)) }
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
    unsafe := opts.Unsafe
    live, renames := applyRenames(live, desired)
    live, recreated, recreateWarnings := recreateChangedFunctions(live, desired, unsafe)
    live, renamedArgs, blockedRoutines, argWarnings := recreateRenamedArguments(live, desired, unsafe)
    for k, v := range renamedArgs {
        if recreated == nil { recreated = map[string]string{} }
        recreated[k] = v
    }
    recreateWarnings = append(recreateWarnings, argWarnings...)
    live, typeChangePre, typeChangePost, dependentWarnings := recreateColumnDependents(live, desired, unsafe)
    recreateWarnings = append(recreateWarnings, dependentWarnings...)
    // Foreign key alters are deferred until all tables are processed so that
    // every PK/unique constraint exists before any FK references it
    // (required for circular FK pairs, which topological sort cannot order).
    deferredFKs := []string{}
    // Trigger creates are deferred until after all other creates so a trigger
    // function may declare dependsOn on the trigger's own table (needed for
    // SQL-language bodies, which are validated at CREATE time) without forming
    // a table->function->table cycle.
    deferredTriggers := []string{}
    plan := &PlanDiff{Warnings: recreateWarnings}

    // ROLES HAVE HIGHEST PRIORITY - cluster-level, referenced by grants,
    // policies, and memberships later in the plan
    planRoles(plan, live, desired)

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
        if s == "public" { continue } // public schema always exists
        schemaNames = append(schemaNames, s)
    }
    sort.Strings(schemaNames)
    for _, schemaName := range schemaNames {
        if !live.Schemas[schemaName] {
            plan.Creates = append(plan.Creates, fmt.Sprintf("create schema if not exists %s;", pqIdent(schemaName)))
        }
    }
    plan.Creates = append(plan.Creates, renames...)
    
    plan.Creates = append(plan.Creates, planDefaultPrivileges(live, desired)...)
    for _, c := range configClauses(desired.DatabaseSettings, live.DatabaseConfig) {
        plan.Creates = append(plan.Creates, inCurrentDatabase("alter database %I ", c))
    }
    for _, role := range sortedKeys(desired.DatabaseRoleSettings) {
        prefix := "alter role " + strings.ReplaceAll(grantRole(role), "%", "%%") + " in database %I "
        for _, c := range configClauses(desired.DatabaseRoleSettings[role], live.DatabaseRoleConfig[resolveRole(role, live.CurrentUser)]) {
            plan.Alters = append(plan.Alters, inCurrentDatabase(prefix, c))
        }
    }

    // EXTENSIONS HAVE SECOND PRIORITY - created after schemas, before everything else
    extNames := make([]string, 0, len(desired.Extensions))
    for _, ext := range desired.Extensions {
        if ext != nil && ext.Name != "" { extNames = append(extNames, ext.Name) }
    }
    sort.Strings(extNames)
    for _, extName := range extNames {
        ext := findExtension(desired, extName)
        if ext == nil { continue }
        if live.Extensions[ext.Name] {
            plan.Alters = append(plan.Alters, extensionAlterStmts(ext, live.ExtensionDefs[ext.Name])...)
            continue
        }
        plan.Creates = append(plan.Creates, renderExtension(ext))
        if ext.Comment != "" { plan.Alters = append(plan.Alters, fmt.Sprintf("comment on extension %s is %s;", pqIdent(ext.Name), quoteString(ext.Comment))) }
    }
    planServers(plan, live, desired)
    
    // Topologically sort remaining entities (types, functions, tables) respecting dependsOn.
    // On a cycle the sort still returns every entity (cycle members appended in
    // name order) — use it as-is; the CLI surfaces the cycle error before
    // planning, so reaching here with err != nil means the caller opted in.
    sorted, _ := schema.TopologicalSort(desired)
    liveFuncs := normalizedLiveFunctions(live)
    createsRelations := plansNewRelations(live, desired)
    
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
                        attrs = append(attrs, fmt.Sprintf("%s %s%s", pqIdent(an), at, attributeCollate(td, an)))
                        emitted[an] = true
                    }
                }
                keys := make([]string, 0, len(td.Attributes))
                for k := range td.Attributes { if !emitted[k] { keys = append(keys, k) } }
                sort.Strings(keys)
                for _, an := range keys { attrs = append(attrs, fmt.Sprintf("%s %s%s", pqIdent(an), td.Attributes[an], attributeCollate(td, an))) }
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
                if defaultsRemoved && strings.TrimSpace(f.Body) != "" { continue }
                // Empty YAML body means the body is unmanaged (attribute-only
                // declaration, e.g. marking an extension function leakproof).
                bodyChanged := routineBodyChanged(f.Language, f.Body, f.Replace, lf.Body, lf.SQLBody) || defaultsChanged && strings.TrimSpace(f.Body) != ""
                if !bodyChanged {
                    if clauses := functionAttrClauses(f, lf); len(clauses) > 0 {
                        plan.Alters = append(plan.Alters, fmt.Sprintf("alter function %s %s;",
                            pqIdent(fname)+grantFuncArgs(f.ArgsSig), strings.Join(clauses, " ")))
                    }
                    continue
                }
                // fall through: body changed, emit CREATE OR REPLACE
            } else if strings.TrimSpace(f.Body) == "" {
                // Attribute-only declaration for a function that doesn't exist
                // yet (extension not installed on this DB) — nothing to create.
                continue
            }
            setClauses := ""
            if len(f.Set) > 0 {
                keys := make([]string, 0, len(f.Set))
                for k := range f.Set { keys = append(keys, k) }
                sort.Strings(keys)
                for _, k := range keys {
                    setClauses += fmt.Sprintf(" set %s = %s", k, configValueSQL(f.Set[k]))
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
            stmt := fmt.Sprintf("%s %s returns %s language %s%s %s;", verb, pqIdent(fname)+f.ArgsSig, f.Returns, f.Language, attrsStr+setClauses, routineBodySQL(f.Language, body))
            plan.Creates = append(plan.Creates, routineCreateStmts(f.Language, body, stmt, createsRelations)...)
        case "procedure":
            pr, ok := desired.Procedures[e.Key]
            if !ok || pr == nil { continue }
            prname := schema.RoutineName(e.Key)
            norm := normalizeFunctionSignature(prname + pr.ArgsSig)
            if blockedRoutines[norm] { continue }
            found := live.Procedures[norm]
            if found {
                lp := live.ProcedureDefs[norm]
                if lp == nil || !procedureChanged(pr, lp) { continue }
                // fall through: definition changed, emit CREATE OR REPLACE
            }
            setClauses := ""
            if len(pr.Set) > 0 {
                keys := make([]string, 0, len(pr.Set))
                for k := range pr.Set { keys = append(keys, k) }
                sort.Strings(keys)
                for _, k := range keys {
                    setClauses += fmt.Sprintf(" set %s = %s", k, configValueSQL(pr.Set[k]))
                }
            }
            attrsStr := ""
            if pr.Security != "" { attrsStr = " security " + pr.Security }
            verb := "create procedure"
            if found { verb = "create or replace procedure" }
            if drop := recreated[norm]; drop != "" { plan.Creates = append(plan.Creates, drop) }
            stmt := fmt.Sprintf("%s %s language %s%s %s;", verb, pqIdent(prname)+pr.ArgsSig, pr.Language, attrsStr+setClauses, routineBodySQL(pr.Language, pr.Body))
            plan.Creates = append(plan.Creates, routineCreateStmts(pr.Language, pr.Body, stmt, createsRelations)...)
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
                    stmt := fmt.Sprintf("create materialized view if not exists %s%s as %s", pqIdent(e.Key), renderStorageParams(vw.With), vw.Query)
                    if vw.WithData != nil && !*vw.WithData { stmt += " with no data" }
                    plan.Creates = append(plan.Creates, stmt+";")
                } else if vw.With != nil {
                    lt := live.MatViewTables[e.Key]
                    if lt == nil { lt = newLiveTable() }
                    plan.Alters = append(plan.Alters, storageParamStmts("materialized view", e.Key, vw.With, lt)...)
                }
                if len(vw.Indexes) > 0 {
                    applyTableConstraints(plan, live, e.Key, &schema.Table{Indexes: vw.Indexes}, live.MatViewTables[e.Key], &deferredFKs, &deferredTriggers, unsafe)
                }
            } else {
                opts := viewOptions(vw)
                // live view definitions can't be reliably text-compared (pg_get_viewdef
                // rewrites the query), so replacement is opt-in via `replace: true`
                if live.Views[e.Key] && !vw.Replace {
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
                    plan.Alters = append(plan.Alters, fmt.Sprintf("alter view %s alter column %s set default %s;", pqIdent(e.Key), pqIdent(col), want))
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
                        plan.Creates = append(plan.Creates, fmt.Sprintf("create %stable if not exists %s partition of %s %s%s%s;", unlogged, pqIdent(fq), pqIdent(dt.PartitionOf), renderPartitionBound(dt.Partition), sub, renderStorageParams(dt.With)))
                    }
                    for _, cn := range sortedKeys(dt.Columns) {
                        if c := dt.Columns[cn]; c != nil { plan.Alters = append(plan.Alters, inheritedColumnStmts(fq, cn, c, nil)...) }
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
                if dt.Foreign != nil {
                    plan.Creates = append(plan.Creates, fmt.Sprintf("create foreign table if not exists %s (%s) server %s%s;", pqIdent(fq), strings.Join(cols, ", "), quoteIdent(dt.Foreign.Server), fdwOptionsClause(dt.Foreign.Options)))
                    plan.Alters = append(plan.Alters, columnStorageStmts(fq, dt)...)
                    applyTableConstraints(plan, live, fq, dt, nil, &deferredFKs, &deferredTriggers, unsafe)
                    continue
                }
                unlogged := ""
                if dt.Unlogged != nil && *dt.Unlogged { unlogged = "unlogged " }
                create := fmt.Sprintf("create %stable if not exists %s (%s)", unlogged, pqIdent(fq), strings.Join(cols, ", "))
                if dt.Of != "" { create = fmt.Sprintf("create %stable if not exists %s of %s", unlogged, pqIdent(fq), pqIdent(dt.Of)) }
                if len(dt.Inherits) > 0 {
                    parents := make([]string, len(dt.Inherits))
                    for i, p := range dt.Inherits { parents[i] = pqIdent(p) }
                    create += " inherits (" + strings.Join(parents, ", ") + ")"
                }
                if dt.PartitionBy != nil {
                    create += " partition by " + renderPartitionBy(dt.PartitionBy)
                }
                plan.Creates = append(plan.Creates, create+renderStorageParams(dt.With)+";")
                plan.Alters = append(plan.Alters, columnStorageStmts(fq, dt)...)
                applyTableConstraints(plan, live, fq, dt, nil, &deferredFKs, &deferredTriggers, unsafe)
                if stmt := replicaIdentityStmt(fq, dt, nil); stmt != "" { plan.Alters = append(plan.Alters, stmt) }
                if stmt := clusterOnStmt(fq, dt, nil); stmt != "" { plan.Alters = append(plan.Alters, stmt) }
            } else {
                // existing table: add missing columns, reconcile attributes
                // of columns present in both
                pkCols := map[string]bool{}
                for _, pc := range dt.PrimaryKey { pkCols[constraintColumnName(pc)] = true }
                plan.Creates = append(plan.Creates, typeChangePre[fq]...)
                if attachesPartition(dt, lt) {
                    plan.Creates = append(plan.Creates, fmt.Sprintf("alter table %s attach partition %s %s;", pqIdent(dt.PartitionOf), pqIdent(fq), renderPartitionBound(dt.Partition)))
                    plan.Warnings = append(plan.Warnings, Warning{Table: fq, Kind: "lock", Message: fmt.Sprintf("ATTACH PARTITION %s scans it under an ACCESS EXCLUSIVE lock to check its rows fit the bound, unless a validated CHECK constraint already implies the bound", fq)})
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
                        plan.Alters = append(plan.Alters, newColumnStmts(fq, cn, c)...)
                        continue
                    }
                    if name := notNullNotValidName(fq, cn, c, lc, lt, dt, pkCols, live.ServerVersion); name != "" {
                        plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s add constraint %s not null %s not valid;", pqIdent(fq), quoteIdent(name), pqIdent(cn)))
                        plan.Concurrent = append(plan.Concurrent, fmt.Sprintf("alter table %s validate constraint %s;", pqIdent(fq), quoteIdent(name)))
                        enforced := *lc
                        enforced.Nullable = false
                        lc = &enforced
                    } else if lc.NotNullNotValid != "" && !c.Nullable {
                        plan.Concurrent = append(plan.Concurrent, fmt.Sprintf("alter table %s validate constraint %s;", pqIdent(fq), quoteIdent(lc.NotNullNotValid)))
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
                    for _, cn := range dropCols {
                        plan.Drops = append(plan.Drops, fmt.Sprintf("alter table %s drop column %s;", pqIdent(fq), pqIdent(cn)))
                    }
                }
            }
        }
    }
    plan.Creates = append(plan.Creates, deferredTriggers...)
    planEventTriggers(plan, live, desired, opts)
    dataWrites, dataDeletes := planData(live, desired, opts.Unsafe && opts.Prune)
    plan.Alters = append(plan.Alters, dataWrites...)
    plan.Alters = append(plan.Alters, deferredFKs...)
    planPolicies(plan, live, desired, unsafe)
    planPublications(plan, live, desired, opts)
    plan.Alters = append(plan.Alters, planOwners(live, desired)...)
    grantStmts, grantWarnings := planGrantsWarned(live, desired, opts.PublicGrants)
    plan.Alters = append(plan.Alters, grantStmts...)
    plan.Warnings = append(plan.Warnings, grantWarnings...)
    plan.Alters = append(plan.Alters, planComments(live, desired, unsafe)...)
    plan.Alters = append(plan.Alters, typeChangePost...)
    plan.Drops = append(dataDeletes, plan.Drops...)
    if opts.Unsafe && opts.Prune {
        early, late := planPrune(live, desired, opts.MigrationsTable)
        plan.Drops = append(append(early, plan.Drops...), late...)
        if desired.Servers != nil {
            for _, name := range sortedKeys(live.Servers) {
                if desired.Servers[name] == nil { plan.Drops = append(plan.Drops, fmt.Sprintf("drop server %s;", quoteIdent(name))) }
            }
        }
    }
    plan.Warnings = append(plan.Warnings, lockWarnings(live, plan)...)
    return plan
}

// planRoles emits CREATE ROLE for roles missing from live and GRANT <parent>
// TO <role> for missing memberships. Forward-only: existing roles are never
// altered or dropped; removed memberships are not revoked.
func planRoles(plan *PlanDiff, live *Live, desired *schema.Database) {
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
    }
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
    s = strings.TrimSpace(s)
    if strings.EqualFold(s, "none") { return "" }
    if strings.Count(s, ".") == 1 {
        seqSchema, _, _ := strings.Cut(seq, ".")
        return seqSchema + "." + s
    }
    return s
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
    }
    want := desiredDomainChecks(fq, dm)
    drop := func(name string) string { return fmt.Sprintf("%s drop constraint %s;", target, pqIdent(name)) }
    if unsafe {
        for _, name := range sortedKeys(ld.Checks) {
            if _, ok := want[name]; ok { continue }
            if dm.Checks != nil || name == domainCheckName(fq, dm) { out = append(out, drop(name)) }
        }
    }
    for _, name := range sortedKeys(want) {
        add := fmt.Sprintf("%s add constraint %s check (%s);", target, pqIdent(name), want[name])
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
    return pqIdent(s)
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

// sameType compares two "schema.name" type names, ignoring quotes and
// treating an unqualified name as public's.
func sameType(a, b string) bool {
    norm := func(s string) string {
        s = strings.ReplaceAll(strings.TrimSpace(s), `"`, "")
        if !strings.Contains(s, ".") { s = "public." + s }
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
    target := fmt.Sprintf("alter table %s alter column %s", pqIdent(fq), pqIdent(cn))
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
        ident := pqIdent(truncateIdent(name))
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
                    for i, s := range lp.Schemas { schemas[i] = pqIdent(s) }
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
        if desired.Publications[name] == nil { plan.Drops = append(plan.Drops, fmt.Sprintf("drop publication %s;", pqIdent(name))) }
    }
}

// publicationMembers renders the TABLE ... / TABLES IN SCHEMA ... list.
func publicationMembers(p *schema.Publication) string {
    parts := []string{}
    for _, t := range p.Tables {
        entry := "table " + pqIdent(t.Table)
        if len(t.Columns) > 0 { entry += " (" + joinIdentList(t.Columns) + ")" }
        if t.Where != "" { entry += " where (" + t.Where + ")" }
        parts = append(parts, entry)
    }
    for _, s := range p.Schemas { parts = append(parts, "tables in schema "+pqIdent(s)) }
    return strings.Join(parts, ", ")
}

func publicationMembersMatch(p *schema.Publication, lp *LivePublication) bool {
    if len(p.Tables) != len(lp.Tables) { return false }
    for _, t := range p.Tables {
        lt, ok := lp.Tables[t.Table]
        if !ok || !constraintDefsEqual(t.Where, lt.Where) { return false }
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
        ident := pqIdent(truncateIdent(name))
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
        plan.Drops = append(plan.Drops, fmt.Sprintf("drop event trigger %s;", pqIdent(name)))
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
// changed wrapper or type cannot be altered and is reported as a warning.
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
        if s.Wrapper == "postgres_fdw" && (ls == nil || !ls.UserMapping) {
            plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf("server %s has no user mapping for the connected role or PUBLIC; create one outside pgy (its options hold credentials) before querying its foreign tables", name)})
        }
    }
}

// foreignTableAlterStmts reconciles a live foreign table's options and its
// columns' options. A changed server cannot be altered and is reported
// through warn.
func foreignTableAlterStmts(fq string, dt *schema.Table, lt *LiveTable, warn func(string)) []string {
    if lt.Foreign == nil {
        warn(fmt.Sprintf("%s is declared as a foreign table but is a table in the database; it is left as it is", fq))
        return nil
    }
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
            out = append(out, fmt.Sprintf("alter foreign table %s alter column %s %s;", pqIdent(fq), pqIdent(cn), clause))
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
func notNullNotValidName(fq, cn string, c *schema.Column, lc *LiveColumn, lt *LiveTable, dt *schema.Table, pkCols map[string]bool, serverVersion int) string {
    switch {
    case serverVersion < 180000, c.Nullable, !lc.Nullable:
        return ""
    case c.PrimaryKey, pkCols[cn], c.Identity != "", c.Generated != "", isSerialType(c.Type):
        return ""
    case dt.Foreign != nil, dt.PartitionOf != "", lt.IsPartition, hasNotNullCheck(lt, cn):
        return ""
    }
    _, table := splitQualified(fq)
    return makeObjectName(table+"_"+cn, "not_null")
}

// routineBodySQL renders a routine body for CREATE: a SQL-standard body
// (BEGIN ATOMIC / RETURN) as written, anything else as a dollar-quoted
// string after AS.
func routineBodySQL(language, body string) string {
    if schema.IsSQLStandardBody(language, body) {
        return strings.TrimRight(strings.TrimSpace(body), "; \n\t")
    }
    return "as " + dollarQuote(body)
}

// routineBodyChanged reports whether a routine's declared body differs from
// the live one. An empty body is unmanaged. A change between a string body
// and a SQL-standard one always counts; two SQL-standard bodies are compared
// only with replace, since PostgreSQL stores a rewritten form.
func routineBodyChanged(language, body string, replace bool, liveBody string, liveSQLBody bool) bool {
    if strings.TrimSpace(body) == "" { return false }
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
        fmt.Sprintf("create sequence if not exists %s as %s owned by %s.%s;", pqIdent(seq), seqType, pqIdent(fq), pqIdent(cn)),
        fmt.Sprintf("select setval(%s, (select coalesce(max(%s), 0) + 1 from %s), false);", quoteString(pqIdent(seq)), pqIdent(cn), pqIdent(fq)),
        fmt.Sprintf("alter table %s alter column %s set default nextval(%s::regclass);", pqIdent(fq), pqIdent(cn), quoteString(pqIdent(seq))),
    }
}

// plansNewRelations reports whether desired declares a table, view or
// materialized view that live lacks.
func plansNewRelations(live *Live, desired *schema.Database) bool {
    for k, t := range desired.Tables {
        if t != nil && live.Tables[k] == nil { return true }
    }
    for k, v := range desired.Views {
        if v != nil && !live.Views[k] && !live.MatViews[k] { return true }
    }
    return false
}

// routineCreateStmts returns stmt, the CREATE of a function or procedure
// written in language. PostgreSQL checks a SQL-language string body against
// the catalog at CREATE time, so when the plan also creates relations the
// body may read, stmt is wrapped in SET / RESET check_function_bodies, as
// pg_dump does; the body is then checked when the routine first runs.
// SQL-standard bodies are always parsed at CREATE, so they are not wrapped:
// their dependencies must be created first.
func routineCreateStmts(language, body, stmt string, createsRelations bool) []string {
    if !createsRelations || !strings.EqualFold(strings.TrimSpace(language), "sql") || schema.IsSQLStandardBody(language, body) { return []string{stmt} }
    return []string{"set check_function_bodies = off;", stmt, "reset check_function_bodies;"}
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
        if f == nil || strings.TrimSpace(f.Body) == "" { continue }
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
        if p == nil { continue }
        pname := schema.RoutineName(k)
        norm := normalizeFunctionSignature(pname + p.ArgsSig)
        lp := live.ProcedureDefs[norm]
        if lp == nil { continue }
        if _, removed := argDefaultsDiff(p.ArgsSig, lp.Args); !removed { continue }
        if !unsafe {
            warns = append(warns, Warning{Message: fmt.Sprintf("procedure %s%s: an argument default was removed, which CREATE OR REPLACE can't do; that drops and recreates the procedure, which needs --unsafe", pname, p.ArgsSig)})
            if out == live { out = cloneLive(live) }
            out.ProcedureDefs[norm] = &LiveProcedure{Signature: lp.Signature, Body: p.Body, SQLBody: lp.SQLBody, Security: lp.Security, Config: lp.Config, Args: p.ArgsSig}
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
        if f := desired.Functions[k]; f != nil && strings.TrimSpace(f.Body) != "" { check("function", schema.RoutineName(k), f.ArgsSig) }
    }
    for _, k := range sortedKeys(desired.Procedures) {
        if p := desired.Procedures[k]; p != nil { check("procedure", schema.RoutineName(k), p.ArgsSig) }
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
    if !strings.Contains(body, "$$") { return "$$" }
    for i := 0; ; i++ {
        tag := "$body$"
        if i > 0 { tag = fmt.Sprintf("$body%d$", i) }
        if !strings.Contains(body, tag) { return tag }
    }
}

// dollarQuote quotes a routine body on lines of its own.
func dollarQuote(body string) string {
    tag := dollarTag(body)
    return tag + "\n" + body + "\n" + tag
}

// dollarQuoteInline quotes body on one line.
func dollarQuoteInline(body string) string {
    tag := dollarTag(body)
    return tag + " " + body + " " + tag
}

// qualifyName qualifies an unqualified object name with public.
func qualifyName(s string) string {
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
    if ag.InitCond != "" { opts = append(opts, "initcond = "+quoteString(ag.InitCond)) }
    if ag.Parallel != "" { opts = append(opts, "parallel = "+ag.Parallel) }
    for _, o := range schema.AggregateOptions {
        v, ok := ag.Options[o[1]]
        switch {
        case !ok || v == "":
        case strings.HasSuffix(o[1], "_extra") || o[1] == "hypothetical":
            if strings.EqualFold(v, "true") { opts = append(opts, o[1]) }
        case o[1] == "minitcond":
            opts = append(opts, "minitcond = "+quoteString(v))
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
            if want != have { return false }
        case k == "sortop":
            op := func(s string) string {
                s = strings.TrimSpace(s)
                if l := strings.ToLower(s); strings.HasPrefix(l, "operator(") && strings.HasSuffix(s, ")") { s = s[len("operator(") : len(s)-1] }
                return strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "pg_catalog."), "public.")
            }
            if op(want) != op(have) { return false }
        default:
            if fn(want) != fn(have) { return false }
        }
    }
    return true
}

// aggregateMatchesLive compares an aggregate's functions (ignoring public
// and pg_catalog qualifiers), state type, initial condition and, when set,
// parallel safety.
func aggregateMatchesLive(ag *schema.Aggregate, la *LiveAggregate) bool {
    fn := func(s string) string {
        s = strings.ToLower(strings.ReplaceAll(strings.TrimSuffix(strings.TrimSpace(s), "()"), `"`, ""))
        return strings.TrimPrefix(strings.TrimPrefix(s, "public."), "pg_catalog.")
    }
    return fn(ag.SFunc) == fn(la.SFunc) && normalizeColumnType(ag.SType) == normalizeColumnType(la.SType) &&
        fn(ag.FinalFunc) == fn(la.FinalFunc) && fn(ag.CombineFunc) == fn(la.CombineFunc) &&
        ag.InitCond == la.InitCond && (ag.Parallel == "" || ag.Parallel == la.Parallel) && aggregateOptionsMatch(ag, la, fn)
}

// tsDictionaryAlterStmts brings live dictionary ld's options in line with
// td's: ALTER TEXT SEARCH DICTIONARY sets changed options and drops those td
// doesn't list. Options are unmanaged when td has none. A changed template
// can't be altered, so it is reported instead.
func tsDictionaryAlterStmts(fq string, td *schema.TSDictionary, ld *schema.TSDictionary) ([]string, string) {
    if ld == nil { return nil, "" }
    warn := ""
    if t := strings.TrimPrefix(td.Template, "pg_catalog."); !strings.EqualFold(t, ld.Template) {
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
    for i, d := range dicts { parts[i] = pqIdent(strings.TrimSpace(d)) }
    return strings.Join(parts, ", ")
}

// sameTSDicts compares dictionary lists by unqualified name, in order.
func sameTSDicts(want, have []string) bool {
    if len(want) != len(have) { return false }
    for i := range want {
        if collationName(want[i]) != collationName(have[i]) { return false }
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
    if dm.NotNull { parts = append(parts, "not null") }
    if dm.Check != "" {
        if dm.ConstraintName != "" { parts = append(parts, "constraint "+pqIdent(dm.ConstraintName)) }
        parts = append(parts, fmt.Sprintf("check (%s)", dm.Check))
    }
    for _, n := range sortedKeys(dm.Checks) {
        if dm.Checks[n] != "" { parts = append(parts, fmt.Sprintf("constraint %s check (%s)", pqIdent(n), dm.Checks[n])) }
    }
    return strings.Join(parts, " ") + ";"
}

// ownedByIdent quotes sequence seq's table.column (or schema.table.column)
// OWNED BY target, schema-qualified.
func ownedByIdent(seq, s string) string {
    if strings.EqualFold(strings.TrimSpace(s), "none") { return "none" }
    parts := strings.Split(qualifyOwnedBy(seq, s), ".")
    for i := range parts { parts[i] = `"` + parts[i] + `"` }
    return strings.Join(parts, ".")
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
        comment("schema "+pqIdent(s), desired.SchemaComments[s], live.SchemaComments[s])
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
        if dt == nil { continue }
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
            comment(fmt.Sprintf("column %s.%s", pqIdent(k), pqIdent(cn)), c.Comment, have)
        }
        lt := live.Tables[k]
        if dt.PrimaryKeyComment != "" {
            name, have := truncateIdent(dt.PrimaryKeyName), ""
            if ln := LivePrimaryKeyName(lt); ln != "" {
                have = lt.ConstraintComments[ln]
                if name == "" { name = ln }
            }
            if name == "" { name = DefaultPrimaryKeyName(k) }
            comment(fmt.Sprintf("constraint %s on %s", pqIdent(name), pqIdent(k)), dt.PrimaryKeyComment, have)
        }
        for _, fk := range dt.ForeignKeys {
            if fk == nil || fk.Comment == "" || len(fk.Columns) == 0 || fk.RefTable == "" { continue }
            fkName := truncateIdent(fk.Name)
            have := ""
            if lt != nil && !constraintRecreated(lt, fkName, renderFKDef(fk), unsafe) { have = lt.ConstraintComments[fkName] }
            comment(fmt.Sprintf("constraint %s on %s", pqIdent(fkName), pqIdent(k)), fk.Comment, have)
        }
        for _, ct := range dt.Constraints {
            if ct == nil || ct.Comment == "" || renderConstraintDef(ct) == "" { continue }
            name := truncateIdent(ct.Name)
            have := ""
            if lt != nil && (!unsafe || !lt.Constraints[name] || lt.ConstraintDefs[name] == "" || constraintMatchesLive(lt, name, ct)) { have = lt.ConstraintComments[name] }
            comment(fmt.Sprintf("constraint %s on %s", pqIdent(name), pqIdent(k)), ct.Comment, have)
        }
        for _, tr := range dt.Triggers {
            if tr == nil || tr.Comment == "" || tr.Procedure == "" { continue }
            name := truncateIdent(tr.Name)
            have := ""
            if lt != nil {
                if def, ok := lt.Triggers[name]; ok && (def == "" || triggerDefsEqual(renderTrigger(k, tr), def)) { have = lt.TriggerComments[name] }
            }
            comment(fmt.Sprintf("trigger %s on %s", pqIdent(name), pqIdent(k)), tr.Comment, have)
        }
        for _, r := range dt.Rules {
            if r == nil || r.Comment == "" || r.Do == "" { continue }
            name := truncateIdent(r.Name)
            have := ""
            if lt != nil {
                if lr := lt.Rules[name]; lr != nil && !r.Replace && ruleMatchesLive(r, lr) { have = lt.RuleComments[name] }
            }
            comment(fmt.Sprintf("rule %s on %s", pqIdent(name), pqIdent(k)), r.Comment, have)
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
            comment(fmt.Sprintf("policy %s on %s", pqIdent(name), pqIdent(k)), pol.Comment, have)
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
            comment(fmt.Sprintf("column %s.%s", pqIdent(k), pqIdent(col)), vw.ColumnComments[col], live.ViewColumnComments[k][col])
        }
        if vt := live.ViewTables[k]; !vw.Materialized {
            for _, tr := range vw.Triggers {
                if tr == nil || tr.Comment == "" || tr.Procedure == "" { continue }
                name := truncateIdent(tr.Name)
                have := ""
                if vt != nil {
                    if def, ok := vt.Triggers[name]; ok && (def == "" || triggerDefsEqual(renderTrigger(k, tr), def)) { have = vt.TriggerComments[name] }
                }
                comment(fmt.Sprintf("trigger %s on %s", pqIdent(name), pqIdent(k)), tr.Comment, have)
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
                comment(fmt.Sprintf("rule %s on %s", pqIdent(name), pqIdent(k)), r.Comment, have)
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
        owner("schema", pqIdent(s), desired.SchemaOwners[s], "schema:"+s)
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
                if nsp != "" { prefix += " in schema " + pqIdent(nsp) }
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
        for _, s := range rv {
            if strings.HasSuffix(s, " from public;") {
                why := "grants blocks cover PUBLIC on tables, views, sequences, columns, servers and schemas since v0.43.0"
                if mode == PublicAuthoritative { why = "policy.public_grants: authoritative makes every grants block cover PUBLIC" }
                warns = append(warns, Warning{Kind: "public_revoke", Message: fmt.Sprintf("%s: the grants block doesn't list public, so this revokes PUBLIC's privileges (%s; list public to keep them)", strings.TrimSuffix(s, ";"), why)})
            }
        }
        add(rv, gr)
    }
    listsPublic := func(g map[string][]string) bool { _, ok := g["public"]; return ok }
    covers := func(byKind bool, g map[string][]string) bool {
        switch mode {
        case PublicAuthoritative:
            return true
        case PublicExplicit:
            return listsPublic(g)
        }
        return byKind || listsPublic(g)
    }
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
        addFor(desiredG)(ownedGrantDiff("schema "+pqIdent(s), desiredG, live.SchemaGrants[s], objectOwner(live, "schema:"+s, desired.SchemaOwners[s]), covers(s != "public", desiredG)))
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
            addFor(desiredG)(ownedGrantDiff("sequence "+pqIdent(k), desiredG, liveG, objectOwner(live, "rel:"+k, sq.Owner), covers(true, desiredG)))
        }
    }
    typeGrants := func(kind, k string, grantsBlock map[string][]string, revokePublic, isLive bool, owner string) {
        desiredG := resolveGrantRoles(grantsBlock, live.CurrentUser)
        defaultUsage := !isLive || !live.TypePublicRevoked[k]
        if revokePublic && (grantsBlock == nil || !covers(false, desiredG)) && defaultUsage {
            revokes = append(revokes, fmt.Sprintf("revoke usage on %s %s from public;", kind, pqIdent(k)))
        }
        if grantsBlock == nil { return }
        liveG := live.TypeGrants[k]
        if !isLive { liveG = created(k, "types") }
        addFor(desiredG)(ownedGrantDiff(kind+" "+pqIdent(k), desiredG, withPublicDefault(liveG, "usage", defaultUsage), owner, covers(false, desiredG)))
    }
    for _, k := range sortedKeys(desired.Types) {
        if td := desired.Types[k]; td != nil {
            typeGrants("type", k, td.Grants, td.RevokePublic, live.Types[k], objectOwner(live, "type:"+k, td.Owner))
        }
    }
    for _, k := range sortedKeys(desired.Domains) {
        if dm := desired.Domains[k]; dm != nil {
            typeGrants("domain", k, dm.Grants, dm.RevokePublic, live.Domains[k], objectOwner(live, "type:"+k, dm.Owner))
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
        if !live.Views[k] && !live.MatViews[k] { liveG = created(k, "tables") }
        desiredG := resolveGrantRoles(vw.Grants, live.CurrentUser)
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
            liveG := live.FunctionGrants[norm]
            if !liveFuncs[norm] { liveG = created(name, "functions") }
            addFor(desiredG)(ownedGrantDiff(target, desiredG, withPublicDefault(liveG, "execute", defaultExec), objectOwner(live, "proc:"+norm, f.Owner), covers(false, desiredG)))
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
            liveG := live.FunctionGrants[norm]
            if !live.Procedures[norm] { liveG = created(name, "functions") }
            addFor(desiredG)(ownedGrantDiff(target, desiredG, withPublicDefault(liveG, "execute", defaultExec), objectOwner(live, "proc:"+norm, pr.Owner), covers(false, desiredG)))
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
    colIdent := pqIdent(col)
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
            plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s %s (%s)%s;", pqIdent(fq), pkAdd, constraintColumns(dt.PrimaryKey), indexParameters(dt.PrimaryKeyInclude, dt.PrimaryKeyWith)))
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
        validate := fmt.Sprintf("alter table %s validate constraint %s;", pqIdent(fq), pqIdent(fkName))
        stmt := fmt.Sprintf("alter table %s add constraint %s %s;", pqIdent(fq), pqIdent(fkName), def)
        partitionedBefore18 := dt.PartitionBy != nil && live.ServerVersion > 0 && live.ServerVersion < 180000
        split := lt != nil && !fk.NotValid && !fk.NotEnforced && !partitionedBefore18
        if split {
            c := *fk
            c.NotValid = true
            stmt = fmt.Sprintf("alter table %s add constraint %s %s;", pqIdent(fq), pqIdent(fkName), renderFKDef(&c))
        }
        if liveConstraints[fkName] {
            liveDef, ok := liveConstraintDefs[fkName]
            switch {
            case unsafe && ok && !constraintDefsEqual(def, liveDef):
                *deferredFKs = append(*deferredFKs, fmt.Sprintf("alter table %s drop constraint %s;", pqIdent(fq), pqIdent(fkName)), stmt)
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
        stmt := fmt.Sprintf("create%s%s if not exists %s on %s%s(%s)%s;", uniq, how, pqIdent(name), pqIdent(fq), using, elems, tail)
        build := func(idx, table string, only, conc bool) string {
            h, o := " index", ""
            if conc { h = " index concurrently" }
            if only { o = "only " }
            _, bare := splitQualified(idx)
            return fmt.Sprintf("create%s%s if not exists %s on %s%s%s(%s)%s;", uniq, h, pqIdent(bare), o, pqIdent(table), using, elems, tail)
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
            changed := false
            if lt != nil && lt.IndexDefs[name] != nil { changed = indexDefinitionDiffers(ix, lt.IndexDefs[name]) }
            if liveOCs, ok := liveIndexOpclasses(lt)[name]; ok && indexOpclassesDiffer(ix, liveOCs) { changed = true }
            switch {
            case !unsafe || !changed:
                plan.Alters = append(plan.Alters, indexComment(false)...)
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
        validate := fmt.Sprintf("alter table %s validate constraint %s;", pqIdent(fq), pqIdent(ctName))
        stmt := fmt.Sprintf("alter table %s add constraint %s %s;", pqIdent(fq), pqIdent(ctName), def)
        split := lt != nil && strings.EqualFold(ct.Type, "check") && !ct.NotValid && !ct.NotEnforced
        if split {
            c := *ct
            c.NotValid = true
            stmt = fmt.Sprintf("alter table %s add constraint %s %s;", pqIdent(fq), pqIdent(ctName), renderConstraintDef(&c))
        }
        if liveConstraints[ctName] {
            liveDef, ok := liveConstraintDefs[ctName]
            switch {
            case unsafe && ok && !constraintMatchesLive(lt, ctName, ct):
                plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s drop constraint %s;", pqIdent(fq), pqIdent(ctName)), stmt)
                if split { plan.Concurrent = append(plan.Concurrent, validate) }
            case !ct.NotValid && !ct.NotEnforced && liveNotValid(liveDef):
                plan.Concurrent = append(plan.Concurrent, validate)
            default:
                plan.Alters = append(plan.Alters, keyIndexStorageStmts(schemaPrefixOf(fq)+ctName, ct.With, lt.IndexDefs[ctName])...)
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
        plan.Alters = append(plan.Alters, fmt.Sprintf("alter table %s add constraint %s unique (%s);", pqIdent(fq), pqIdent(name), pqIdent(cn)))
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
            *deferredTriggers = append(*deferredTriggers, fmt.Sprintf("drop trigger %s on %s;", pqIdent(trName), pqIdent(fq)))
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
        if isIndexExpression(c) && cols[c] == nil { items[i] = "(" + c + ")" } else { items[i] = pqIdent(c) }
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
    if idx, ok := strings.CutPrefix(want, "index "); ok { clause = "using index " + pqIdent(truncateIdent(idx)) }
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
    return fmt.Sprintf("alter table %s cluster on %s;", pqIdent(fq), pqIdent(truncateIdent(want)))
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
    stmt := fmt.Sprintf("create or replace rule %s as on %s to %s", pqIdent(truncateIdent(r.Name)), strings.ToLower(r.Event), pqIdent(fq))
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
    return fmt.Sprintf("alter table %s %s trigger %s;", pqIdent(fq), action, pqIdent(name))
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
        return fmt.Sprintf("create constraint trigger %s AFTER %s on %s%s for each row%s execute procedure %s;", pqIdent(trName), events, pqIdent(fq), deferral, when, tr.Procedure)
    }
    level := strings.ToLower(strings.TrimSpace(tr.Level))
    if level == "" { level = "statement" }
    if strings.EqualFold(strings.Join(strings.Fields(tr.Timing), " "), "instead of") { level = "row" }
    referencing := ""
    if tr.OldTable != "" { referencing += " old table as " + pqIdent(tr.OldTable) }
    if tr.NewTable != "" { referencing += " new table as " + pqIdent(tr.NewTable) }
    if referencing != "" { referencing = " referencing" + referencing }
    return fmt.Sprintf("create trigger %s %s %s on %s%s for each %s%s execute procedure %s;", pqIdent(trName), strings.ToUpper(tr.Timing), events, pqIdent(fq), referencing, level, when, tr.Procedure)
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
                plan.Alters = append(plan.Alters, fmt.Sprintf("drop policy %s on %s;", pqIdent(polName), pqIdent(fq)))
            }
            stmt := fmt.Sprintf("create policy %s on %s", pqIdent(polName), pqIdent(fq))
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
                plan.Drops = append(plan.Drops, fmt.Sprintf("drop policy %s on %s;", pqIdent(name), pqIdent(fq)))
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
// column's `using` property). Identity changes go through
// identityAlterStmts; identity/serial columns keep their generated defaults
// and implicit NOT NULL.
func alterColumnStmts(fq, cn string, c *schema.Column, lc *LiveColumn, pkCols map[string]bool, unsafe bool, serverVersion int, warn func(string)) []string {
    out := []string{}
    target := fmt.Sprintf("alter table %s alter column %s", pqIdent(fq), pqIdent(cn))

    typeChanged := c.Type != "" && normalizeColumnType(c.Type) != normalizeColumnType(lc.Type)
    collateChanged := c.Collate != "" && collationName(c.Collate) != lc.Collation
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
            case !unsafe:
            case serverVersion > 0 && serverVersion < 170000:
                warn(fmt.Sprintf("generated column %s.%s changed, but PostgreSQL %d has no ALTER COLUMN ... SET EXPRESSION (added in 17); drop and re-add the column, along with the indexes and constraints that use it, in a hand-written migration", fq, cn, serverVersion/10000))
            default:
                out = append(out, fmt.Sprintf("%s set expression as (%s);", target, c.Generated))
            }
        case c.Generated == "" && lc.Generated != "":
            if unsafe { out = append(out, target+" drop expression;") }
        case c.Generated != "" && lc.Generated == "":
            warn(fmt.Sprintf("column %s.%s is declared generated but is a plain column, and PostgreSQL cannot add a generation expression to an existing column; drop and re-add it, along with the indexes and constraints that use it, in a hand-written migration", fq, cn))
        }
    }
    out = append(out, serialConversionStmts(fq, cn, c, lc)...)

    generated := c.Identity != "" || lc.Identity != "" || isSerialType(c.Type) || c.Generated != "" || lc.Generated != ""
    if !generated {
        switch {
        case c.Default != "" && !defaultsEqual(c.Default, lc.Default):
            out = append(out, fmt.Sprintf("%s set default %s;", target, c.Default))
        case c.Default == "" && lc.Default != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(lc.Default)), "nextval("):
            out = append(out, fmt.Sprintf("%s drop default;", target))
        }
    }

    if !c.Nullable && lc.Nullable {
        out = append(out, target+" set not null;")
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
    parts := strings.Split(strings.TrimSpace(s), ".")
    for i, p := range parts { parts[i] = `"` + strings.Trim(strings.TrimSpace(p), `"`) + `"` }
    return strings.Join(parts, ".")
}

// collationName returns a COLLATE target's unqualified, unquoted name, as
// pg_collation.collname reports it.
func collationName(s string) string {
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

// columnStorageStmts emits newColumnStmts for every column of a newly
// created table.
func columnStorageStmts(fq string, dt *schema.Table) []string {
    out := []string{}
    for _, cn := range sortedKeys(dt.Columns) {
        if c := dt.Columns[cn]; c != nil { out = append(out, newColumnStmts(fq, cn, c)...) }
    }
    return out
}

// newColumnStmts emits the column settings CREATE TABLE and ADD COLUMN
// cannot carry: SET STORAGE (accepted inline only from PostgreSQL 16), SET
// STATISTICS, attribute options and a foreign table column's options.
func newColumnStmts(fq, cn string, c *schema.Column) []string {
    out := []string{}
    target := fmt.Sprintf("alter table %s alter column %s", pqIdent(fq), pqIdent(cn))
    if clause := fdwOptionsAlter(c.FDWOptions, nil); clause != "" {
        out = append(out, fmt.Sprintf("alter foreign table %s alter column %s %s;", pqIdent(fq), pqIdent(cn), clause))
    }
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
func normalizeColumnType(t string) string { return memoized(&normalizeColumnTypeCache, t, normalizeColumnTypeUncached) }

var normalizeColumnTypeCache sync.Map

func normalizeColumnTypeUncached(t string) string {
    s := strings.ToLower(strings.TrimSpace(t))
    s = strings.ReplaceAll(s, `"`, "")
    s = strings.Join(strings.Fields(s), " ")
    s = publicQualifierRe.ReplaceAllString(s, "$1")
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
    publicQualifierRe = regexp.MustCompile(`(^|[^a-z0-9_$.])public\.`)
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
func defaultsEqual(desired, live string) bool {
    return normalizeDefaultExpr(desired) == normalizeDefaultExpr(live) ||
        normalizeConstraintDef(desired) == normalizeConstraintDef(live)
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

// normalizeDefaultExpr canonicalizes a default expression for comparison:
// runs canonicalizeExpr (live defaults come back as 'x'::text, '-1'::integer
// or nextval('s'::regclass)), lowercases everything outside single-quoted
// strings, and collapses whitespace.
func normalizeDefaultExpr(s string) string { return memoized(&normalizeDefaultExprCache, s, normalizeDefaultExprUncached) }

var normalizeDefaultExprCache sync.Map

func normalizeDefaultExprUncached(s string) string {
    var b strings.Builder
    inQ := false
    rs := []rune(strings.TrimSpace(canonicalizeExpr(s)))
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
            for i+1 < len(rs) && isCastTypeChar(rs[i+1]) { i++ }
            continue
        }
        if r >= 'A' && r <= 'Z' { r += 'a' - 'A' }
        b.WriteRune(r)
    }
    out := strings.Join(strings.Fields(b.String()), " ")
    out = strings.ReplaceAll(out, " (", "(")
    out = strings.ReplaceAll(out, "nextval('public.", "nextval('")
    return out
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
            parts = append(parts, pqIdent(constraintColumnName(c))+" without overlaps")
        case strings.HasPrefix(lc, "period "):
            parts = append(parts, "period "+pqIdent(constraintColumnName(c)))
        default:
            parts = append(parts, pqIdent(constraintColumnName(c)))
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
// indexParameters renders the INCLUDE and WITH clauses of a primary key or
// unique constraint's index.
func indexParameters(include []string, with map[string]string) string {
    out := ""
    if len(include) > 0 { out = " include (" + constraintColumns(include) + ")" }
    return out + renderStorageParams(with)
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
        return fmt.Sprintf("unique%s (%s)", nulls, constraintColumns(ct.Columns)) + indexParameters(ct.Include, ct.With) + deferralClause(ct.Deferrable, ct.InitiallyDeferred)
    case "exclude":
        return fmt.Sprintf("exclude %s", ct.Expression) + deferralClause(ct.Deferrable, ct.InitiallyDeferred)
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

func constraintDefsEqual(desired, live string) bool {
    return normalizeConstraintDef(desired) == normalizeConstraintDef(live)
}

// triggerDefsEqual compares a desired CREATE TRIGGER statement against
// pg_get_triggerdef() output. Both sides go through normalizeConstraintDef
// (case, quoting, parens, casts, whitespace, public. qualifiers); on top of
// that the trailing semicolon is ignored and EXECUTE FUNCTION (emitted by
// PostgreSQL 11+) is treated as equal to EXECUTE PROCEDURE.
func triggerDefsEqual(desired, live string) bool {
    n := func(s string) string {
        s = normalizeConstraintDef(strings.TrimSuffix(strings.TrimSpace(s), ";"))
        return strings.ReplaceAll(s, "executefunction", "executeprocedure")
    }
    return n(desired) == n(live)
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
            if !strings.HasPrefix(c, `"`) && !simpleIdentRe.MatchString(c) { c = pqIdent(c) }
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
// LIKE rewrites, cast removal), outside single-quoted strings it strips
// double quotes, parentheses, whitespace, and "public." qualifiers, and
// lowercases. A trailing NOT VALID is ignored: validating the constraint
// later is not a definition change. Stripping parentheses and whitespace can make
// differently-grouped expressions compare equal; that errs toward emitting no
// SQL, same as the old name-only matching.
func normalizeConstraintDef(s string) string { return memoized(&normalizeConstraintDefCache, s, normalizeConstraintDefUncached) }

var normalizeConstraintDefCache sync.Map

func normalizeConstraintDefUncached(s string) string {
    var b strings.Builder
    inQ := false
    rs := []rune(strings.TrimSpace(canonicalizeExpr(s)))
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
            for i+1 < len(rs) && isCastTypeChar(rs[i+1]) { i++ }
            continue
        }
        switch r {
        case '"', '(', ')', ' ', '\t', '\n', '\r':
            continue
        }
        if r >= 'A' && r <= 'Z' { r += 'a' - 'A' }
        b.WriteRune(r)
    }
    return strings.TrimSuffix(strings.ReplaceAll(b.String(), "public.", ""), "notvalid")
}

func isCastTypeChar(r rune) bool {
    return r == '_' || r == '.' || r == '[' || r == ']' ||
        (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func renderColumn(name string, c *schema.Column) string {
    parts := []string{pqIdent(name), c.Type}
    if c.Compression != "" && c.Compression != "default" { parts = append(parts, "compression "+c.Compression) }
    if c.Collate != "" { parts = append(parts, "collate "+collationIdent(c.Collate)) }
    if !c.Nullable { parts = append(parts, "not null") }
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
        elem := pqIdent(base)
        if isIndexExpression(base) && cols[base] == nil { elem = "(" + base + ")" }
        if coll := indexColumnCollation(ix, c); coll != "" { elem += " collate " + collationIdent(coll) }
        if oc := indexColumnOpclass(ix, c); oc != "" { elem += " " + oc }
        if ordering != "" { elem += " " + ordering }
        parts = append(parts, elem)
    }
    return strings.Join(parts, ", ")
}

// renderIndexTail renders the clauses after an index's column list: INCLUDE,
// NULLS NOT DISTINCT, WITH (storage parameters, sorted) and WHERE.
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

// constraintMatchesLive compares named constraint ct with its live
// definition from pg_get_constraintdef, leaving out index storage
// parameters, which keyIndexStorageStmts changes in place.
func constraintMatchesLive(lt *LiveTable, name string, ct *schema.Constraint) bool {
    strip := func(s string) string { return constraintWithRe.ReplaceAllString(s, "") }
    return constraintDefsEqual(strip(renderConstraintDef(ct)), strip(lt.ConstraintDefs[name]))
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

// indexOpclassesDiffer reports whether the desired index's operator classes
// disagree with the live index's. A desired column with no explicit opclass
// matches any default opclass. A key-column count mismatch means the index
// changed in ways beyond opclasses (out of scope here), so it is not treated
// as an opclass change.
func indexOpclassesDiffer(ix *schema.Index, live []LiveIndexOpclass) bool {
    if len(ix.Columns) != len(live) { return false }
    for i, c := range ix.Columns {
        oc := indexColumnOpclass(ix, c)
        if oc == "" {
            if !live[i].Default { return true }
        } else if opclassName(oc) != opclassName(live[i].Name) {
            return true
        }
        if collationName(indexColumnCollation(ix, c)) != collationName(live[i].Collation) { return true }
    }
    return false
}

// opclassName makes operator class names comparable: case and quotes are
// ignored, and so is a public or pg_catalog qualifier.
func opclassName(s string) string {
    s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), `"`, ""))
    return strings.TrimPrefix(strings.TrimPrefix(s, "public."), "pg_catalog.")
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
        if quotedIdentRe.MatchString(c) { parts = append(parts, c) } else { parts = append(parts, pqIdent(c)) }
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
    stmt += pqIdent(ext.Name)
    if ext.Schema != "" { stmt += " schema " + pqIdent(ext.Schema) }
    if ext.Version != "" { stmt += " version " + quoteString(ext.Version) }
    if ext.Cascade { stmt += " cascade" }
    return stmt + ";"
}

// extensionAlterStmts moves a live extension to its declared schema (when it
// is relocatable; others cannot be moved and are left alone) and updates it
// to its declared version. Unset schema/version are unmanaged.
func extensionAlterStmts(ext *schema.Extension, le *LiveExtension) []string {
    if le == nil { return nil }
    out := []string{}
    if ext.Schema != "" && ext.Schema != le.Schema && le.Relocatable {
        out = append(out, fmt.Sprintf("alter extension %s set schema %s;", pqIdent(ext.Name), pqIdent(ext.Schema)))
    }
    if ext.Version != "" && ext.Version != le.Version {
        out = append(out, fmt.Sprintf("alter extension %s update to %s;", pqIdent(ext.Name), quoteString(ext.Version)))
    }
    if ext.Comment != "" && ext.Comment != le.Comment {
        out = append(out, fmt.Sprintf("comment on extension %s is %s;", pqIdent(ext.Name), quoteString(ext.Comment)))
    }
    return out
}

func findExtension(db *schema.Database, name string) *schema.Extension {
    for _, ext := range db.Extensions {
        if ext != nil && ext.Name == name {
            return ext
        }
    }
    return nil
}

// normalizeFunctionSignature normalizes function signatures for comparison
// Removes default values and normalizes whitespace and type names
// Input formats: "schema.func(args)" or "schema.func (args)"
// Args may contain defaults like "key text, val jsonb default null"
func normalizeFunctionSignature(sig string) string {
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
            n := normalizeFunctionSignature(funcPart + "(" + args + ")")
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
        if arg := normalizeArg(a); arg != "" { args = append(args, arg) }
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

// normalizeArg removes default values from a function argument and normalizes types
// e.g., "key text default null" -> "key text"
// Format is typically: "param_name type_name" or "param_name type_name default value"
func normalizeArg(arg string) string {
    decl, _ := splitArgDefault(arg)
    return normalizeArgNoDefault(decl)
}

// normalizeArgNoDefault normalizes an argument without default clause
func normalizeArgNoDefault(arg string) string {
    // Format: "param_name type_name", "param_name schema.type_name", or an
    // unnamed bare type (possibly multi-word, e.g. "timestamp with time zone")
    words := strings.Fields(arg)
    if len(words) >= 2 && strings.EqualFold(words[0], "in") {
        words = words[1:]
        arg = strings.Join(words, " ")
    }
    if len(words) < 2 {
        // Unnamed arg: the single token is a type name
        return normalizeColumnType(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(arg)), "public."))
    }

    // Unnamed multi-word type: pg_get_function_identity_arguments prints
    // unnamed args as bare canonical type names, so "timestamp with time
    // zone[]" must not be parsed as param "timestamp" + type "with time
    // zone[]" — it would never match the YAML's "timestamptz[]" spelling.
    if isMultiWordTypeStart(strings.ToLower(words[0]), strings.ToLower(words[1])) {
        return normalizeColumnType(arg)
    }

    // Parameter name (first word) - lowercase for comparison
    paramName := strings.ToLower(words[0])

    // Type name (rest) - normalize to lowercase
    typeName := strings.ToLower(strings.Join(words[1:], " "))

    // "public" is the default schema: "public.my_type" and "my_type" are the
    // same type. Introspection returns types on the search_path unqualified,
    // so strip the qualifier before comparing. Other schemas stay qualified
    // on both sides. Strip before the alias pass below so the substring
    // replacements never see the qualifier.
    typeName = strings.TrimPrefix(typeName, "public.")

    // Canonicalize through the same alias table as column types so alias
    // spellings (timestamptz[]) and live canonical forms (timestamp with
    // time zone[]) compare equal instead of re-emitting the function forever.
    typeName = normalizeColumnType(typeName)

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


