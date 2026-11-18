package diff

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/schema"
)

// ExtensionObjects is what the CREATE EXTENSION statements of a plan add to
// the database, as ProbeExtensionObjects reads it.
type ExtensionObjects struct {
	// Declared names the declared extensions the statements create, and
	// Extensions those together with the ones they cascade to.
	Declared, Extensions map[string]bool
	// Objects holds the entries of the snapshot read after the statements
	// that the snapshot read before them lacks: the objects the extensions'
	// scripts create, with their privileges, owners, comments, policies,
	// triggers, indexes, constraints and membership.
	Objects *Live
}

// setupError is a setup statement of a queryBatch that failed.
type setupError struct {
	stmt string
	err  error
}

func (e *setupError) Error() string {
	return strings.TrimSuffix(e.stmt, ";") + ": " + e.err.Error()
}

func (e *setupError) Unwrap() error { return e.err }

// ProbeExtensionObjects learns what the extensions that desired declares
// and live lacks create, so that a plan creating them treats those objects
// as existing ones (see withExtensionObjects): it runs the plan's CREATE
// SCHEMA and CREATE EXTENSION statements for them in a transaction that is
// always rolled back, reads the database there as Introspect does, and
// keeps the difference in live.ExtensionObjects. A table an extension
// creates is then managed the way an attribute-only declaration manages it
// even when the YAML declares its columns, with the warning
// resolveAttributeOnly gives for an installed extension's table, and what
// the YAML declares on the extension's objects (policies, triggers,
// privileges, owners, comments, rows) is compared with what its script gave
// them. Nothing is probed without a connection, for a plan that creates no
// extension, or when the server lacks one of them or the declared version,
// which the plan reports. A failing statement, such as one the role lacks
// a privilege for, is returned and leaves live.ExtensionObjects nil.
func ProbeExtensionObjects(ctx context.Context, pool *pgxpool.Pool, live *Live, desired *schema.Database, opts Options) error {
	if live == nil || desired == nil || pool == nil || live.Offline {
		return nil
	}
	desired = withExternal(desired, opts)
	setup, declared, schemas := extensionProbeSetup(live, desired)
	if len(setup) == 0 {
		return nil
	}
	after, err := introspectAfter(ctx, pool, setup)
	if err != nil {
		return err
	}
	eo := &ExtensionObjects{Declared: declared, Extensions: map[string]bool{}, Objects: &Live{}}
	for name := range after.Extensions {
		if !live.Extensions[name] {
			eo.Extensions[name] = true
		}
	}
	mergeObjects(eo.Objects, after, live, schemas)
	live.ExtensionObjects = eo
	return nil
}

// extensionProbeSetup returns the statements that create the extensions
// desired declares and live lacks the way the plan does: CREATE SCHEMA for
// the schemas they are declared in that live lacks, then the plan's CREATE
// EXTENSION statements. It also returns the names of those extensions and
// the schemas the statements create. It returns nothing when there is no
// such extension, or when live lists the server's extensions and one of
// them, or its declared version, is not among them, or its control file
// fixes another schema than the declared one.
func extensionProbeSetup(live *Live, desired *schema.Database) (setup []string, declared, schemas map[string]bool) {
	plan := &PlanDiff{}
	planExtensions(plan, live, desired)
	creates := []string{}
	for _, s := range plan.Creates {
		if strings.HasPrefix(s, "create extension ") {
			creates = append(creates, s)
		}
	}
	if len(creates) == 0 {
		return nil, nil, nil
	}
	declared, schemas = map[string]bool{}, map[string]bool{}
	for _, ext := range desired.Extensions {
		if ext == nil || ext.Name == "" || live.Extensions[ext.Name] {
			continue
		}
		if live.AvailableExtensions != nil {
			av := live.AvailableExtensions[ext.Name]
			if av == nil {
				return nil, nil, nil
			}
			if _, ok := av.Requires[ext.Version]; ext.Version != "" && !ok {
				return nil, nil, nil
			}
			if fixed := av.Schemas[cmp.Or(ext.Version, av.DefaultVersion)]; ext.Schema != "" && fixed != "" && fixed != ext.Schema {
				return nil, nil, nil
			}
		}
		declared[ext.Name] = true
		if s := ext.Schema; s != "public" && s != "" && !systemSchema(s) && !live.Schemas[s] && !desired.External.HasSchema(s) {
			schemas[s] = true
		}
	}
	for _, s := range sortedKeys(schemas) {
		setup = append(setup, "create schema if not exists "+quoteIdent(s)+";")
	}
	return append(setup, creates...), declared, schemas
}

// withExtensionObjects returns live with the objects of
// live.ExtensionObjects added, for a plan of desired that creates every
// extension the probe created that desired declares, or follows a step of
// the same plan that created it (see Simulate): those objects exist once
// the plan's CREATE EXTENSION statements have run, before anything else
// the plan does to them. Otherwise, and without ExtensionObjects, live is
// returned as it is. live itself is not modified.
func withExtensionObjects(live *Live, desired *schema.Database) *Live {
	if live == nil || live.ExtensionObjects == nil || desired == nil || live.created != nil {
		return live
	}
	eo := live.ExtensionObjects
	if len(eo.Declared) == 0 {
		return live
	}
	inPlan := map[string]bool{}
	for _, ext := range desired.Extensions {
		if ext != nil {
			inPlan[ext.Name] = true
		}
	}
	for name := range eo.Declared {
		le := live.ExtensionDefs[name]
		if live.Extensions[name] && (le == nil || !le.Created) || !live.Extensions[name] && !inPlan[name] {
			return live
		}
	}
	objs := eo.Objects
	if live.scope != nil {
		c := *objs
		c.SearchPath = live.SearchPath
		objs = c.scoped()
	}
	out := *live
	mergeObjects(&out, objs, live, nil)
	out.created = cloneMap(eo.Extensions, nil)
	return &out
}

// createdBy reports whether stmt is the CREATE EXTENSION statement of an
// extension whose objects live holds already (see withExtensionObjects),
// so that its objects are not pending.
func createdBy(live *Live, stmt string) bool {
	rest, ok := strings.CutPrefix(stmt, "create extension ")
	if !ok || live == nil {
		return false
	}
	rest = strings.TrimPrefix(rest, "if not exists ")
	for name := range live.created {
		if q := quoteIdent(name); strings.HasPrefix(rest, q+" ") || rest == q+";" {
			return true
		}
	}
	return false
}

// addNew adds to *dst the entries of src whose key have lacks and skip
// doesn't hold, on a copy of *dst, which is not modified.
func addNew[V any](dst *map[string]V, src, have map[string]V, skip map[string]bool) {
	var out map[string]V
	for k, v := range src {
		if _, ok := have[k]; ok || skip[k] {
			continue
		}
		if out == nil {
			out = make(map[string]V, len(*dst)+len(src))
			for dk, dv := range *dst {
				out[dk] = dv
			}
		}
		out[k] = v
	}
	if out != nil {
		*dst = out
	}
}

// mergeObjects adds to dst the entries of src's object maps that have
// lacks, those of schemas and of what they key aside: everything the
// snapshot holds about tables, views, sequences, types, routines, schemas,
// languages, foreign-data wrappers, text search objects, collations and
// event triggers, and about which extension owns them. Extensions, roles,
// settings, default privileges, publications, foreign servers and the
// results of probes are left out.
func mergeObjects(dst, src, have *Live, schemas map[string]bool) {
	owners := map[string]bool{}
	for s := range schemas {
		owners["schema:"+s] = true
	}
	addNew(&dst.Schemas, src.Schemas, have.Schemas, schemas)
	addNew(&dst.SchemaGrants, src.SchemaGrants, have.SchemaGrants, schemas)
	addNew(&dst.SchemaComments, src.SchemaComments, have.SchemaComments, schemas)
	addNew(&dst.Owners, src.Owners, have.Owners, owners)
	addNew(&dst.Tables, src.Tables, have.Tables, nil)
	addNew(&dst.Types, src.Types, have.Types, nil)
	addNew(&dst.BaseTypes, src.BaseTypes, have.BaseTypes, nil)
	addNew(&dst.Functions, src.Functions, have.Functions, nil)
	addNew(&dst.FunctionDefs, src.FunctionDefs, have.FunctionDefs, nil)
	addNew(&dst.Procedures, src.Procedures, have.Procedures, nil)
	addNew(&dst.ProcedureDefs, src.ProcedureDefs, have.ProcedureDefs, nil)
	addNew(&dst.AggregateDefs, src.AggregateDefs, have.AggregateDefs, nil)
	addNew(&dst.FunctionGrants, src.FunctionGrants, have.FunctionGrants, nil)
	addNew(&dst.FunctionPublicExec, src.FunctionPublicExec, have.FunctionPublicExec, nil)
	addNew(&dst.FunctionComments, src.FunctionComments, have.FunctionComments, nil)
	addNew(&dst.Views, src.Views, have.Views, nil)
	addNew(&dst.MatViews, src.MatViews, have.MatViews, nil)
	addNew(&dst.ViewOptions, src.ViewOptions, have.ViewOptions, nil)
	addNew(&dst.ViewColumnComments, src.ViewColumnComments, have.ViewColumnComments, nil)
	addNew(&dst.ViewColumnDefaults, src.ViewColumnDefaults, have.ViewColumnDefaults, nil)
	addNew(&dst.ViewDependents, src.ViewDependents, have.ViewDependents, nil)
	addNew(&dst.MatViewTables, src.MatViewTables, have.MatViewTables, nil)
	addNew(&dst.ViewTables, src.ViewTables, have.ViewTables, nil)
	addNew(&dst.Sequences, src.Sequences, have.Sequences, nil)
	addNew(&dst.SequenceDefs, src.SequenceDefs, have.SequenceDefs, nil)
	addNew(&dst.OwnedSequences, src.OwnedSequences, have.OwnedSequences, nil)
	addNew(&dst.Domains, src.Domains, have.Domains, nil)
	addNew(&dst.DomainDefs, src.DomainDefs, have.DomainDefs, nil)
	addNew(&dst.EnumLabels, src.EnumLabels, have.EnumLabels, nil)
	addNew(&dst.CompositeAttributes, src.CompositeAttributes, have.CompositeAttributes, nil)
	addNew(&dst.CompositeCollations, src.CompositeCollations, have.CompositeCollations, nil)
	addNew(&dst.CompositeComments, src.CompositeComments, have.CompositeComments, nil)
	addNew(&dst.Collations, src.Collations, have.Collations, nil)
	addNew(&dst.CollationComments, src.CollationComments, have.CollationComments, nil)
	addNew(&dst.TSConfigs, src.TSConfigs, have.TSConfigs, nil)
	addNew(&dst.TSDictionaries, src.TSDictionaries, have.TSDictionaries, nil)
	addNew(&dst.TSDictionaryDefs, src.TSDictionaryDefs, have.TSDictionaryDefs, nil)
	addNew(&dst.TSComments, src.TSComments, have.TSComments, nil)
	addNew(&dst.EventTriggers, src.EventTriggers, have.EventTriggers, nil)
	addNew(&dst.Wrappers, src.Wrappers, have.Wrappers, nil)
	addNew(&dst.Languages, src.Languages, have.Languages, nil)
	addNew(&dst.TableGrants, src.TableGrants, have.TableGrants, nil)
	addNew(&dst.TypeGrants, src.TypeGrants, have.TypeGrants, nil)
	addNew(&dst.TypePublicRevoked, src.TypePublicRevoked, have.TypePublicRevoked, nil)
	addNew(&dst.ColumnGrants, src.ColumnGrants, have.ColumnGrants, nil)
	addNew(&dst.RelComments, src.RelComments, have.RelComments, nil)
	addNew(&dst.TypeComments, src.TypeComments, have.TypeComments, nil)
	addNew(&dst.ColumnDependents, src.ColumnDependents, have.ColumnDependents, nil)
	addNew(&dst.Partitions, src.Partitions, have.Partitions, nil)
	addNew(&dst.PartitionBounds, src.PartitionBounds, have.PartitionBounds, nil)
	addNew(&dst.ExtensionMembers, src.ExtensionMembers, have.ExtensionMembers, nil)
	addNew(&dst.MemberExtension, src.MemberExtension, have.MemberExtension, nil)
	addNew(&dst.InitPrivs, src.InitPrivs, have.InitPrivs, nil)
	addNew(&dst.ExtensionConfig, src.ExtensionConfig, have.ExtensionConfig, nil)
}

// offlineExtensionTablesWarning returns the warning for a plan without a
// database (diff --from-empty) that creates extensions, which may create
// tables pgy cannot know of, and declares tables with columns and data
// rows, the way to add rows to an extension's configuration table: if one
// of them is a table such an extension creates, its CREATE TABLE IF NOT
// EXISTS does nothing and the keys, constraints and indexes declared for it
// fail. creates are the plan's extension statements. It returns nil when
// there is no such table.
func offlineExtensionTablesWarning(live *Live, desired *schema.Database, creates []string) []Warning {
	if live == nil || !live.Offline || desired == nil || !slices.ContainsFunc(creates, func(s string) bool { return strings.HasPrefix(s, "create extension ") }) {
		return nil
	}
	tables := []string{}
	for _, k := range sortedKeys(desired.Tables) {
		dt := desired.Tables[k]
		if dt != nil && dt.Data != nil && len(dt.Data.Rows) > 0 && !attributeOnlyTable(desired.External, k, dt) {
			tables = append(tables, k)
		}
	}
	if len(tables) == 0 {
		return nil
	}
	names := []string{}
	for _, ext := range desired.Extensions {
		if ext != nil && ext.Name != "" && !live.Extensions[ext.Name] && !slices.Contains(names, ext.Name) {
			names = append(names, ext.Name)
		}
	}
	sort.Strings(names)
	return []Warning{{Message: fmt.Sprintf("diff --from-empty cannot tell which tables extension %s creates, and the YAML declares %s with columns and data rows: if an extension creates one of them, CREATE TABLE IF NOT EXISTS leaves the extension's table as it is and the keys, constraints and indexes declared for it fail, so declare an extension's table without columns, or diff against a database, where pgy finds the extension's tables", strings.Join(names, ", "), strings.Join(tables, ", "))}}
}
