package diff

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pgy/internal/schema"
)

// ExtensionDependent is an object outside an extension that uses one of its
// members, so DROP EXTENSION without CASCADE fails while the object exists.
type ExtensionDependent struct {
	// Description names the object the way PostgreSQL does, such as "column
	// email of table app.users".
	Description string
	// Extension is the other extension the object belongs to, or "".
	Extension string
	// RemovedBy holds the statements that remove the object or what it is
	// part of, each without its semicolon and dropping a single object.
	RemovedBy []string
}

// maxListedDependents is how many dependents a warning about a kept
// extension names before it counts the rest.
const maxListedDependents = 3

// prunableExtensions returns the installed extensions that --unsafe --prune
// may drop, before their dependents are looked at: the ones a present
// extensions block does not list, living in a schema the YAML declares
// objects in. plpgsql, the extensions in opts.KeepExtensions, those an
// external role owns and the ones an extension that stays installed
// requires are never among them.
func prunableExtensions(live *Live, desired *schema.Database, opts Options) map[string]bool {
	drop := map[string]bool{}
	if live == nil || desired == nil || desired.Extensions == nil || !opts.Unsafe || !opts.Prune {
		return drop
	}
	desired = withExternal(desired, opts)
	keep := map[string]bool{"plpgsql": true}
	for _, name := range opts.KeepExtensions {
		keep[name] = true
	}
	for _, ext := range desired.Extensions {
		if ext != nil {
			keep[ext.Name] = true
		}
	}
	managed := managedSchemas(desired)
	for name, installed := range live.Extensions {
		le := live.ExtensionDefs[name]
		if installed && !keep[name] && le != nil && managed[le.Schema] && !desired.External.HasRole(le.Owner) {
			drop[name] = true
		}
	}
	keepRequired(drop, live, desired)
	return drop
}

// keepRequired removes from drop every extension that one staying installed
// needs: an installed extension that requires it or owns objects that use
// its members, or a declared one that requires it.
func keepRequired(drop map[string]bool, live *Live, desired *schema.Database) {
	users := map[string][]string{}
	for name, le := range live.ExtensionDefs {
		if le == nil {
			continue
		}
		for _, required := range le.Requires {
			users[required] = append(users[required], name)
		}
		for _, dep := range le.Dependents {
			if dep.Extension != "" {
				users[name] = append(users[name], dep.Extension)
			}
		}
	}
	for _, ext := range desired.Extensions {
		if ext == nil {
			continue
		}
		for _, required := range extensionRequires(live, ext) {
			users[required] = append(users[required], ext.Name)
		}
	}
	for changed := true; changed; {
		changed = false
		for name := range drop {
			for _, user := range users[name] {
				if !drop[user] {
					delete(drop, name)
					changed = true
					break
				}
			}
		}
	}
}

// planExtensionDrops appends one DROP EXTENSION, after every other drop,
// for the extensions prunableExtensions returns. An extension that objects
// outside it still use is dropped only when plan.Drops removes all of them.
// Otherwise it is kept, along with the extensions it requires, and a
// warning names what uses it, since PostgreSQL would reject the statement.
// An extension is also kept, with the same warning, while desired declares
// one of its own objects (see resolveAttributeOnly).
// An extension whose dependents were not loaded (see IntrospectExtensions)
// is dropped unchecked.
func planExtensionDrops(plan *PlanDiff, live *Live, desired *schema.Database, opts Options) {
	drop := prunableExtensions(live, desired, opts)
	if len(drop) == 0 {
		return
	}
	dropped := droppedObjects(plan.Drops)
	dropPartitions(dropped, live)
	declared := declaredMembers(live, desired)
	for _, name := range sortedKeys(drop) {
		users, seen := append([]string{}, declared[name]...), map[string]bool{}
		for _, dep := range live.ExtensionDefs[name].Dependents {
			if dep.Extension != "" || dep.removedBy(dropped) || seen[dep.Description] {
				continue
			}
			seen[dep.Description] = true
			users = append(users, dep.Description)
		}
		if len(users) == 0 {
			continue
		}
		delete(drop, name)
		listed := strings.Join(users[:min(len(users), maxListedDependents)], ", ")
		if more := len(users) - maxListedDependents; more > 0 {
			listed += fmt.Sprintf(" and %d more", more)
		}
		plan.Warnings = append(plan.Warnings, Warning{Kind: "dependents", Message: fmt.Sprintf("extension %s is not listed under extensions but still in use (%s), so it is kept; pgy diff drops it once nothing depends on it, or list it under extensions or prune.keep_extensions to keep it for good", name, listed)})
	}
	keepRequired(drop, live, desired)
	if len(drop) == 0 {
		return
	}
	names := sortedKeys(drop)
	for i, name := range names {
		names[i] = quoteIdent(name)
	}
	plan.Drops = append(plan.Drops, "drop extension "+strings.Join(names, ", ")+";")
}

// declaredMembers returns, for each installed extension, the objects it
// owns that desired declares, described for a warning.
func declaredMembers(live *Live, desired *schema.Database) map[string][]string {
	out := map[string][]string{}
	add := func(key, object string) {
		if ext := live.MemberExtension[key]; ext != "" {
			out[ext] = append(out[ext], object+", which the YAML declares")
		}
	}
	for _, k := range sortedKeys(desired.Functions) {
		if f := desired.Functions[k]; f != nil {
			name := schema.RoutineName(k)
			add("proc:"+normalizeFunctionSignature(name+f.ArgsSig), "function "+name+f.ArgsSig)
		}
	}
	for _, k := range sortedKeys(desired.Procedures) {
		if p := desired.Procedures[k]; p != nil {
			name := schema.RoutineName(k)
			add("proc:"+normalizeFunctionSignature(name+p.ArgsSig), "procedure "+name+p.ArgsSig)
		}
	}
	for _, k := range sortedKeys(desired.Aggregates) {
		if ag := desired.Aggregates[k]; ag != nil {
			name := schema.RoutineName(k)
			add("proc:"+normalizeFunctionSignature(name+aggregateArgs(ag.ArgsSig)), "aggregate "+name+ag.ArgsSig)
		}
	}
	for _, k := range sortedKeys(desired.Tables) {
		add("rel:"+k, "table "+k)
	}
	for _, k := range sortedKeys(desired.Views) {
		add("rel:"+k, "view "+k)
	}
	for _, k := range sortedKeys(desired.Sequences) {
		add("rel:"+k, "sequence "+k)
	}
	for _, k := range sortedKeys(desired.Types) {
		add("type:"+k, "type "+k)
	}
	for _, k := range sortedKeys(desired.Domains) {
		add("type:"+k, "domain "+k)
	}
	for _, k := range sortedKeys(desired.TSDictionaries) {
		add("tsdict:"+k, "text search dictionary "+k)
	}
	declaredSchemas := map[string]bool{}
	for _, keys := range [][]string{sortedKeys(desired.SchemaGrants), sortedKeys(desired.SchemaOwners), sortedKeys(desired.SchemaComments)} {
		for _, s := range keys {
			declaredSchemas[s] = true
		}
	}
	for _, s := range sortedKeys(declaredSchemas) {
		add("schema:"+s, "schema "+s)
	}
	for _, k := range sortedKeys(desired.Languages) {
		add("language:"+k, "language "+k)
	}
	for _, k := range sortedKeys(desired.ForeignDataWrappers) {
		add("fdw:"+k, "foreign-data wrapper "+k)
	}
	return out
}

// droppedObjects returns what the statements in drops remove, as one
// statement per object without its semicolon: a DROP that lists several
// objects counts once for each of them.
func droppedObjects(drops []string) map[string]bool {
	out := map[string]bool{}
	for _, stmt := range drops {
		stmt = strings.TrimSuffix(stmt, ";")
		first := strings.IndexByte(stmt, '"')
		if !strings.HasPrefix(stmt, "drop ") || first < 0 {
			out[stmt] = true
			continue
		}
		for _, object := range splitTopLevelQuotedParens(stmt[first:]) {
			out[stmt[:first]+strings.TrimSpace(object)] = true
		}
	}
	return out
}

// dropPartitions adds to dropped what PostgreSQL removes along with a
// partitioned table or with a column, constraint or trigger of one: the
// same table, column, constraint or trigger of every partition below it.
func dropPartitions(dropped map[string]bool, live *Live) {
	var below func(parent string) []string
	below = func(parent string) []string {
		out := []string{}
		for _, child := range live.Partitions[parent] {
			out = append(append(out, child), below(child)...)
		}
		return out
	}
	implied := []string{}
	for parent := range live.Partitions {
		table := pqIdent(parent)
		for stmt := range dropped {
			before, after := "", ""
			switch {
			case stmt == "drop table "+table:
				before = "drop table "
			case strings.HasPrefix(stmt, "alter table "+table+" drop "):
				before, after = "alter table ", strings.TrimPrefix(stmt, "alter table "+table)
			case strings.HasPrefix(stmt, "drop trigger ") && strings.HasSuffix(stmt, " on "+table):
				before = strings.TrimSuffix(stmt, table)
			default:
				continue
			}
			for _, partition := range below(parent) {
				implied = append(implied, before+pqIdent(partition)+after)
			}
		}
	}
	for _, stmt := range implied {
		dropped[stmt] = true
	}
}

// removedBy reports whether one of the statements in dropped removes d.
func (d ExtensionDependent) removedBy(dropped map[string]bool) bool {
	for _, stmt := range d.RemovedBy {
		if dropped[stmt] {
			return true
		}
	}
	return false
}

// dependentAddress identifies an object the way
// pg_identify_object_as_address does, by kind and names, plus what the
// statements that remove it need beyond those.
type dependentAddress struct {
	kind  string
	names []string
	// table is the "schema.name" of the relation an index or a statistics
	// object is on.
	table string
	// root is the "schema.name" of the partitioned index at the top of the
	// tree an index is a partition of.
	root string
	// args holds the identity arguments of a routine.
	args string
	// typeName is the "schema.name" of a type, or of the domain a
	// constraint is on.
	typeName string
}

// newExtensionDependent returns the dependent at address a, which
// PostgreSQL describes as description.
func newExtensionDependent(a dependentAddress, description string) ExtensionDependent {
	d := ExtensionDependent{Description: description}
	add := func(format string, args ...any) {
		d.RemovedBy = append(d.RemovedBy, fmt.Sprintf(format, args...))
	}
	relation := func(fq string) {
		for _, relkind := range []string{"table", "foreign table", "view", "materialized view"} {
			add("drop %s %s", relkind, pqIdent(fq))
		}
	}
	owner := strings.Join(a.names[:min(len(a.names), 2)], ".")
	fq := pqIdent(owner)
	part := ""
	if len(a.names) == 3 {
		part = quoteIdent(a.names[2])
	}
	switch a.kind {
	case "table", "foreign table", "view", "materialized view", "view column", "materialized view column":
		relation(owner)
	case "table column", "foreign table column", "default value":
		relation(owner)
		add("alter table %s drop column %s", fq, part)
	case "table constraint":
		relation(owner)
		add("alter table %s drop constraint %s", fq, part)
	case "trigger", "policy":
		relation(owner)
		add("drop %s %s on %s", a.kind, part, fq)
	case "rule":
		relation(owner)
		if part == `"_RETURN"` {
			d.Description = strings.TrimPrefix(description, "rule _RETURN on ")
		} else {
			add("drop rule %s on %s", part, fq)
		}
	case "index", "statistics object":
		add("drop %s %s", strings.TrimSuffix(a.kind, " object"), fq)
		if a.root != "" {
			add("drop index %s", pqIdent(a.root))
		}
		if a.table != "" {
			relation(a.table)
		}
	case "composite type", "composite type column":
		add("drop type %s", fq)
	case "type", "domain constraint":
		if a.typeName != "" {
			add("drop domain %s", pqIdent(a.typeName))
			add("drop type %s", pqIdent(a.typeName))
		}
	case "function", "procedure":
		add("drop %s %s(%s)", a.kind, fq, a.args)
	case "aggregate":
		add("drop aggregate %s", aggregateDropArgs(fq+"("+a.args+")"))
	case "sequence", "text search configuration", "text search dictionary":
		add("drop %s %s", a.kind, fq)
	case "server", "event trigger":
		add("drop %s %s", a.kind, quoteIdent(owner))
	case "foreign-data wrapper":
		add("drop foreign data wrapper %s", quoteIdent(owner))
	}
	return d
}

// queryExtensionDependents adds to batch the query that fills Dependents of
// the named installed extensions. A dependent is an object with a normal
// dependency on a member of the extension. Objects PostgreSQL drops with
// the extension are left out: its members, and what is part of one (the
// rule of its view, the operators of its operator family, an index on its
// table). An object that belongs to another extension the same way is
// reported with that extension's name. Objects another session dropped
// after the query's snapshot was taken have no address and are skipped.
func queryExtensionDependents(batch *queryBatch, live *Live, names []string) {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quoteString(name)
		if le := live.ExtensionDefs[name]; le != nil {
			le.Dependents = []ExtensionDependent{}
		}
	}
	batch.add(`
        select x.extname::text, x.owner, a.type, a.object_names,
               coalesce(tn.nspname || '.' || t.relname, ''),
               coalesce(rn.nspname || '.' || r.relname, ''),
               case when x.classid = 'pg_proc'::regclass then coalesce(pg_get_function_identity_arguments(x.objid), '') else '' end,
               coalesce(yn.nspname || '.' || y.typname, ''),
               coalesce(pg_describe_object(x.classid, x.objid, x.objsubid), '')
        from (
            select e.extname, d.classid, d.objid, d.objsubid, coalesce(max(o.extname::text), '') as owner
            from pg_extension e
            join pg_depend m on m.refclassid = 'pg_extension'::regclass and m.refobjid = e.oid and m.deptype = 'e'
            join pg_depend d on d.refclassid = m.classid and d.refobjid = m.objid and d.deptype = 'n'
                 and d.classid <> 'pg_extension'::regclass
            left join pg_depend s on s.classid = d.classid and s.objid = d.objid and s.deptype in ('e', 'a', 'i', 'P', 'S')
            left join pg_depend sm on s.deptype <> 'e' and sm.classid = s.refclassid and sm.objid = s.refobjid and sm.deptype = 'e'
            left join pg_extension o on o.oid = case when s.deptype = 'e' then s.refobjid else sm.refobjid end
            where e.extname in (`+strings.Join(quoted, ", ")+`)
            group by e.oid, e.extname, d.classid, d.objid, d.objsubid
            having not coalesce(bool_or(o.oid = e.oid), false)
        ) x
        cross join lateral pg_identify_object_as_address(x.classid, x.objid, x.objsubid) a
        left join pg_index i on x.classid = 'pg_class'::regclass and i.indexrelid = x.objid
        left join pg_statistic_ext st on x.classid = 'pg_statistic_ext'::regclass and st.oid = x.objid
        left join pg_class t on t.oid = coalesce(i.indrelid, st.stxrelid)
        left join pg_namespace tn on tn.oid = t.relnamespace
        left join pg_class r on r.oid = pg_partition_root(i.indexrelid) and r.oid <> i.indexrelid
        left join pg_namespace rn on rn.oid = r.relnamespace
        left join pg_constraint con on x.classid = 'pg_constraint'::regclass and con.oid = x.objid
        left join pg_type y on y.oid = case when x.classid = 'pg_type'::regclass then x.objid else nullif(con.contypid, 0) end
        left join pg_namespace yn on yn.oid = y.typnamespace
        where a.type is not null
        order by 1, 9`, func(rows pgx.Rows) error {
		for rows.Next() {
			var ext, owner, description string
			var a dependentAddress
			if err := rows.Scan(&ext, &owner, &a.kind, &a.names, &a.table, &a.root, &a.args, &a.typeName, &description); err != nil {
				return err
			}
			le := live.ExtensionDefs[ext]
			if le == nil {
				continue
			}
			dep := newExtensionDependent(a, description)
			dep.Extension = owner
			le.Dependents = append(le.Dependents, dep)
		}
		return nil
	})
}
