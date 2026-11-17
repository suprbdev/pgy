package diff

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pgy/internal/schema"
)

// UpdateDependent is an object outside an extension that can make ALTER
// EXTENSION ... UPDATE fail: an update script that drops one of the
// extension's objects to create it again is rejected while the object
// exists.
type UpdateDependent struct {
	// Kind is "view" or "matview", "function" or "procedure" for a routine
	// with a SQL-standard body, and "other" for anything else, which is
	// only listed when it uses a dependent view or materialized view.
	Kind string
	// Name is "schema.name" for a view or materialized view, the signature
	// of a routine, and PostgreSQL's description of any other object.
	Name string
	// On is the dependent view or materialized view the object uses, or ""
	// when it uses an object of the extension itself.
	On string
}

// queryUpdateDependents adds to batch the query that fills UpdateDependents
// of the named installed extensions. It starts from the views, materialized
// views and routines with a SQL-standard body that have a normal dependency
// on a member of the extension, or on the row type of one of its relations,
// and follows the views and materialized views among them to everything
// that uses those in turn. Objects an extension owns, and the parts of the
// dependent views themselves (their rules), are left out.
func queryUpdateDependents(batch *queryBatch, live *Live, names []string) {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quoteString(name)
		if le := live.ExtensionDefs[name]; le != nil {
			le.UpdateDependents = []UpdateDependent{}
		}
	}
	batch.add(`
        with recursive target(ext, classid, objid, rel, via) as (
            select e.extname::text, p.classid, p.objid, 0::oid, '' collate "C"
            from pg_extension e
            join pg_depend m on m.refclassid = 'pg_extension'::regclass and m.refobjid = e.oid and m.deptype = 'e'
            left join pg_class mc on m.classid = 'pg_class'::regclass and mc.oid = m.objid
            cross join lateral (values (m.classid, m.objid), ('pg_type'::regclass, mc.reltype)) p(classid, objid)
            where e.extname in (`+strings.Join(quoted, ", ")+`) and p.objid <> 0
          union
            select t.ext, p.classid, p.objid, v.oid, vn.nspname || '.' || v.relname
            from target t
            join pg_depend d on d.refclassid = t.classid and d.refobjid = t.objid and d.deptype = 'n'
                 and d.classid = 'pg_rewrite'::regclass
            join pg_rewrite r on r.oid = d.objid and r.rulename = '_RETURN'
            join pg_class v on v.oid = r.ev_class and v.relkind in ('v', 'm') and v.oid <> t.rel
            join pg_namespace vn on vn.oid = v.relnamespace
            left join pg_depend o on o.classid = 'pg_class'::regclass and o.objid = v.oid and o.deptype = 'e'
            cross join lateral (values ('pg_class'::regclass, v.oid), ('pg_type'::regclass, v.reltype)) p(classid, objid)
            where o.objid is null
        )
        select t.ext, t.via,
               case when r.rulename = '_RETURN' and v.relkind = 'v' then 'view'
                    when r.rulename = '_RETURN' and v.relkind = 'm' then 'matview'
                    when f.prosqlbody is not null and f.prokind = 'p' then 'procedure'
                    when f.prosqlbody is not null then 'function'
                    else 'other' end,
               coalesce(case when r.rulename = '_RETURN' and v.relkind in ('v', 'm') then vn.nspname || '.' || v.relname
                             when f.prosqlbody is not null then fn.nspname || '.' || f.proname || '(' || pg_get_function_identity_arguments(f.oid) || ')'
                             else pg_describe_object(d.classid, d.objid, d.objsubid) end, '')
        from target t
        join pg_depend d on d.refclassid = t.classid and d.refobjid = t.objid and d.deptype = 'n'
        left join pg_rewrite r on d.classid = 'pg_rewrite'::regclass and r.oid = d.objid
        left join pg_class v on v.oid = r.ev_class
        left join pg_namespace vn on vn.oid = v.relnamespace
        left join pg_proc f on d.classid = 'pg_proc'::regclass and f.oid = d.objid
        left join pg_namespace fn on fn.oid = f.pronamespace
        left join pg_depend s on s.classid = d.classid and s.objid = d.objid and s.deptype in ('e', 'a', 'i', 'P', 'S')
        left join pg_depend sm on s.deptype <> 'e' and sm.classid = s.refclassid and sm.objid = s.refobjid and sm.deptype = 'e'
        where t.via <> '' or f.prosqlbody is not null or r.rulename = '_RETURN' and v.relkind in ('v', 'm')
        group by t.ext, t.via, t.rel, d.classid, d.objid, d.objsubid, r.rulename, v.relkind, vn.nspname, v.relname,
                 f.oid, f.prosqlbody, f.prokind, fn.nspname, f.proname
        having not coalesce(bool_or(s.deptype = 'e' or sm.objid is not null
                                    or s.refclassid = 'pg_class'::regclass and s.refobjid = t.rel), false)
        order by 1, 2, 3, 4`, func(rows pgx.Rows) error {
		for rows.Next() {
			var ext string
			var d UpdateDependent
			if err := rows.Scan(&ext, &d.On, &d.Kind, &d.Name); err != nil {
				return err
			}
			if le := live.ExtensionDefs[ext]; le != nil && d.Name != "" {
				le.UpdateDependents = append(le.UpdateDependents, d)
			}
		}
		return nil
	})
}

// recreateUpdateDependents makes room for the ALTER EXTENSION ... UPDATE
// statements in extensions.Creates. An update script may drop an object of
// the extension to create it again, which PostgreSQL refuses while a view
// uses the object, and only the server knows which objects a script
// replaces. So under --unsafe every plain view that uses an object of an
// updated extension, directly or through other views, is dropped right
// before the update, dependents first. The returned live is a copy without
// those views, so the plan recreates them from the YAML afterwards, and
// post restores the grants, comments, column defaults and owner that the
// YAML leaves unmanaged. The views and materialized views among the
// dependents that --prune drops anyway are dropped there too.
//
// Everything else stays and is reported, since the update fails if it
// replaces what one of them uses: a materialized view, a routine with a
// SQL-standard body, a view the YAML doesn't declare, any other object that
// uses a dependent view, and with them the views they use. Without --unsafe
// nothing is dropped, and a warning names the views --unsafe would recreate.
// A view an earlier statement of the plan already drops (see
// planViewChanges) is not in the way. An extension whose dependents were
// not loaded (see IntrospectExtensions) is updated without looking for
// them.
func recreateUpdateDependents(extensions *PlanDiff, live *Live, desired *schema.Database, opts Options) (out *Live, post []string, warns []Warning) {
	out = live
	if live == nil || desired == nil {
		return out, nil, nil
	}
	renamed := map[string]string{}
	for k, vw := range desired.Views {
		if vw != nil && vw.RenamedFrom != "" {
			renamed[renameSource(k, vw.RenamedFrom)] = k
		}
	}
	exists := func(v string) bool { return live.Views[v] || live.MatViews[v] }
	current := func(name string) string {
		if to := renamed[name]; to != "" && !exists(name) {
			return to
		}
		return name
	}
	var pruned map[string]bool
	if opts.Unsafe && opts.Prune {
		pruned = managedSchemas(desired)
	}
	dropped := map[string]bool{}
	for _, ext := range orderedExtensions(live, desired) {
		le := live.ExtensionDefs[ext.Name]
		if ext.Version == "" || le == nil || len(le.UpdateDependents) == 0 {
			continue
		}
		at := slices.Index(extensions.Creates, fmt.Sprintf("alter extension %s update to %s;", quoteIdent(ext.Name), quoteString(ext.Version)))
		if at < 0 {
			continue
		}
		users := map[string][]UpdateDependent{}
		for _, d := range le.UpdateDependents {
			if d.Kind == "view" || d.Kind == "matview" {
				d.Name = current(d.Name)
			}
			d.On = current(d.On)
			users[d.On] = append(users[d.On], d)
		}

		kept, drops, settled := []string{}, map[string]bool{}, map[string]bool{}
		keep := func(what string) {
			if !slices.Contains(kept, what) {
				kept = append(kept, what)
			}
		}
		var stays func(d UpdateDependent) bool
		stays = func(d UpdateDependent) bool {
			switch d.Kind {
			case "view", "matview":
			case "function", "procedure":
				keep(d.Kind + " " + d.Name)
				return true
			default:
				keep(d.Name)
				return true
			}
			v := d.Name
			if !exists(v) {
				return false
			}
			if settled[v] {
				return !drops[v]
			}
			settled[v] = true
			vw := desired.Views[v]
			schemaName, _, _ := strings.Cut(v, ".")
			recreate := live.Views[v] && vw != nil && !vw.Materialized && strings.TrimSpace(vw.Query) != ""
			switch {
			case recreate || vw == nil && pruned[schemaName]:
			case live.MatViews[v]:
				keep("materialized view " + v)
				return true
			default:
				keep("view " + v + " (not declared)")
				return true
			}
			blocked := false
			for _, u := range users[v] {
				blocked = stays(u) || blocked
			}
			drops[v] = !blocked
			return blocked
		}
		for _, d := range users[""] {
			stays(d)
		}

		order, seen := []string{}, map[string]bool{}
		var visit func(v string)
		visit = func(v string) {
			if seen[v] || !drops[v] {
				return
			}
			seen[v] = true
			for _, u := range users[v] {
				visit(u.Name)
			}
			order = append(order, v)
		}
		for _, d := range users[""] {
			visit(d.Name)
		}

		if !opts.Unsafe && len(order) > 0 {
			names := append([]string{}, order...)
			sort.Strings(names)
			listed := strings.Join(names[:min(len(names), maxListedDependents)], ", ")
			if more := len(names) - maxListedDependents; more > 0 {
				listed += fmt.Sprintf(" and %d more", more)
			}
			warns = append(warns, Warning{Kind: "dependents", Message: fmt.Sprintf("extension %s: views depend on it, directly or through other views (%s); PostgreSQL rejects the update to %s if it replaces an object they use, and --unsafe drops them before the update and recreates them after it", ext.Name, listed, ext.Version)})
		}
		if len(kept) > 0 {
			warns = append(warns, Warning{Kind: "dependents", Message: fmt.Sprintf("extension %s: objects that pgy doesn't drop for an update depend on it, directly or through views (%s); PostgreSQL rejects the update to %s if it replaces an object they use, so drop them in a hand-written migration first if it does", ext.Name, strings.Join(kept, ", "), ext.Version)})
		}
		if !opts.Unsafe {
			continue
		}
		stmts := []string{}
		for _, v := range order {
			if dropped[v] {
				continue
			}
			dropped[v] = true
			kind := "view"
			if live.MatViews[v] {
				kind = "materialized view"
			}
			stmts = append(stmts, fmt.Sprintf("drop %s %s;", kind, pqIdent(v)))
			if vw := desired.Views[v]; vw != nil {
				post = append(post, restoreViewAttributes(live, vw, v, kind)...)
			}
			if out == live {
				out = cloneLive(live)
			}
			delete(out.Views, v)
			delete(out.MatViews, v)
			delete(out.ViewOptions, v)
			delete(out.ViewColumnComments, v)
			delete(out.ViewColumnDefaults, v)
			delete(out.MatViewTables, v)
			delete(out.ViewTables, v)
			delete(out.RelComments, v)
			delete(out.TableGrants, v)
			delete(out.Owners, "rel:"+v)
		}
		extensions.Creates = slices.Insert(extensions.Creates, at, stmts...)
	}
	return out, post, warns
}
