package diff

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pgy/internal/schema"
)

// mayDrop reports whether the connecting role may drop and alter the objects
// owner owns (see Live.OwnerRoles). An unknown owner or role set counts as
// its own.
func (l *Live) mayDrop(owner string) bool {
	return l == nil || l.OwnerRoles == nil || owner == "" || l.OwnerRoles[owner]
}

// notOwnerWarning returns the warning, of kind not_owner, for an object
// owned by a role whose privileges the connecting role lacks, which --prune
// therefore does not drop, or, when parts is not empty, does not drop parts
// of: PostgreSQL would reject the statement with "must be owner of".
func notOwnerWarning(live *Live, object, owner, parts string) Warning {
	what, it := "it", "it"
	if parts != "" {
		what, it = parts, "them"
	}
	return Warning{Kind: "not_owner", Message: fmt.Sprintf("%s is owned by %s, and %s, the connecting role, does not have that role's privileges, so --prune does not drop %s; declare %s, or have %s drop %s", object, owner, live.CurrentUser, what, it, owner, it)}
}

// prunableRoutines returns the routines planPrune considers dropping, before
// their owners and dependents are looked at (see routinePruneCandidates).
func prunableRoutines(live *Live, desired *schema.Database, opts Options) []*routineCandidate {
	if live == nil || desired == nil || !opts.Unsafe || !opts.Prune {
		return nil
	}
	live = live.scoped()
	desired = withUnmovedExtensions(live, desired)
	return routinePruneCandidates(live, desired, pruneScope(live, desired))
}

// queryRoutineDependents adds to batch the query that fills the Dependents
// of the live routines candidates names, like queryExtensionDependents does
// for extensions: the objects with a normal dependency on one of them,
// reported with the extension they belong to, if any.
func queryRoutineDependents(batch *queryBatch, candidates []*routineCandidate) {
	schemas, names, args := make([]string, len(candidates)), make([]string, len(candidates)), make([]string, len(candidates))
	for i, c := range candidates {
		name := schema.RoutineName(c.signature)
		schemas[i], names[i], _ = strings.Cut(name, ".")
		args[i] = strings.TrimSuffix(strings.TrimPrefix(c.signature[len(name):], "("), ")")
		*c.dependents = []ExtensionDependent{}
	}
	batch.add(dependentsQuery(`
            select c.ord::int, d.classid, d.objid, d.objsubid, coalesce(max(o.extname::text), '') as owner
            from unnest($1::text[], $2::text[], $3::text[]) with ordinality c(nsp, name, args, ord)
            join pg_namespace n on n.nspname = c.nsp
            join pg_proc p on p.pronamespace = n.oid and p.proname = c.name and pg_get_function_identity_arguments(p.oid) = c.args
            join pg_depend d on d.refclassid = 'pg_proc'::regclass and d.refobjid = p.oid and d.deptype = 'n'
            left join pg_depend s on s.classid = d.classid and s.objid = d.objid and s.deptype in ('e', 'a', 'i', 'P', 'S')
            left join pg_depend sm on s.deptype <> 'e' and sm.classid = s.refclassid and sm.objid = s.refobjid and sm.deptype = 'e'
            left join pg_extension o on o.oid = case when s.deptype = 'e' then s.refobjid else sm.refobjid end
            group by c.ord, d.classid, d.objid, d.objsubid`), func(rows pgx.Rows) error {
		for rows.Next() {
			var ord int
			var owner, description string
			var a dependentAddress
			if err := rows.Scan(&ord, &owner, &a.kind, &a.names, &a.table, &a.root, &a.args, &a.typeName, &description); err != nil {
				return err
			}
			if ord < 1 || ord > len(candidates) {
				continue
			}
			dep := newExtensionDependent(a, description)
			dep.Extension = owner
			c := candidates[ord-1]
			*c.dependents = append(*c.dependents, dep)
		}
		return nil
	}, schemas, names, args)
}
