package diff

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pgy/internal/schema"
)

// LiveWrapper is a foreign-data wrapper from pg_foreign_data_wrapper.
type LiveWrapper struct {
	// Handler, Validator and Connection are "schema.function", or "" for
	// none. Connection is always "" before PostgreSQL 19.
	Handler    string
	Validator  string
	Connection string
	Options    map[string]string
	Owner      string
	Comment    string
	// Grants: fdwacl, owner excluded: role -> privilege ("usage", "usage*"
	// with grant option)
	Grants map[string]map[string]bool
	// ExtensionOwner is the owner of the extension the wrapper belongs to,
	// which is the role the wrapper was created as; "" for a wrapper no
	// extension owns.
	ExtensionOwner string
}

// wrapperQ reads every foreign-data wrapper, one row per privilege held by
// a role other than its owner and one with an empty grantee for a wrapper
// without any. fdwconnection (PostgreSQL 19) is read through to_jsonb, as
// older servers lack the column.
const wrapperQ = `
    select w.fdwname, pg_get_userbyid(w.fdwowner),
           coalesce(hn.nspname || '.' || h.proname, ''), coalesce(vn.nspname || '.' || v.proname, ''),
           coalesce(kn.nspname || '.' || k.proname, ''),
           coalesce(w.fdwoptions, '{}'::text[]), coalesce(c.description, ''),
           coalesce(pg_get_userbyid(x.extowner), ''),
           coalesce(case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end, ''),
           coalesce(lower(a.privilege_type) || case when a.is_grantable then '*' else '' end, '')
    from pg_foreign_data_wrapper w
    left join pg_proc h on h.oid = w.fdwhandler
    left join pg_namespace hn on hn.oid = h.pronamespace
    left join pg_proc v on v.oid = w.fdwvalidator
    left join pg_namespace vn on vn.oid = v.pronamespace
    left join pg_proc k on k.oid = (to_jsonb(w) ->> 'fdwconnection')::oid
    left join pg_namespace kn on kn.oid = k.pronamespace
    left join pg_description c on c.classoid = 'pg_foreign_data_wrapper'::regclass and c.objoid = w.oid and c.objsubid = 0
    left join pg_depend m on m.classid = 'pg_foreign_data_wrapper'::regclass and m.objid = w.oid and m.deptype = 'e'
    left join pg_extension x on x.oid = m.refobjid
    left join lateral aclexplode(w.fdwacl) a on a.grantee <> w.fdwowner
`

func (l *Live) scanWrappers(rows pgx.Rows) error {
	for rows.Next() {
		var name, grantee, priv string
		var options []string
		w := &LiveWrapper{}
		if err := rows.Scan(&name, &w.Owner, &w.Handler, &w.Validator, &w.Connection, &options, &w.Comment, &w.ExtensionOwner, &grantee, &priv); err != nil {
			return err
		}
		if have := l.Wrappers[name]; have != nil {
			w = have
		} else {
			w.Options = parseFDWOptions(options)
			l.Wrappers[name] = w
		}
		if grantee == "" || priv == "" {
			continue
		}
		if w.Grants == nil {
			w.Grants = map[string]map[string]bool{}
		}
		if w.Grants[grantee] == nil {
			w.Grants[grantee] = map[string]bool{}
		}
		w.Grants[grantee][priv] = true
	}
	return nil
}

// wrapperExtension returns the extension that owns foreign-data wrapper
// name, and whether one does, so that pgy neither creates nor drops the
// wrapper: the extension the live wrapper is a member of, or, for a wrapper
// the database lacks, the extension that creates it (see
// schema.Database.WrapperExtension) unless that one is installed already
// and evidently brought no such wrapper.
func wrapperExtension(live *Live, desired *schema.Database, name string) (extension string, owned bool) {
	key := "fdw:" + name
	if live.ExtensionMembers[key] {
		return live.MemberExtension[key], true
	}
	if live.Wrappers[name] != nil {
		return "", false
	}
	ext := desired.WrapperExtension(name)
	if le := live.ExtensionDefs[ext]; ext == "" || live.Extensions[ext] && (le == nil || !le.Created) {
		return "", false
	}
	return ext, true
}

// sameWrapperFunction reports whether want, a declared handler or
// validator, names the live function have ("schema.function", "" for none).
// The schema is optional on either side for pg_catalog and for the schemas
// of the active name scope.
func sameWrapperFunction(want, have string) bool {
	bare := func(ref string) string {
		name, _ := activeScope().unqualifyLiteral(strings.TrimPrefix(ref, "pg_catalog."))
		return name
	}
	return bare(want) == bare(have)
}

// planWrappers creates and reconciles the declared foreign-data wrappers,
// ahead of the servers that use them. A wrapper an extension owns (see
// wrapperExtension) is never created, and of its declaration only the
// options, owner, privileges and comment are applied. Any other wrapper is
// created when the database lacks it. The handler, validator and connection
// function of a live one are compared only when the declaration manages them
// (see schema.ForeignDataWrapper.ManagesHandler), and a handler changed under
// foreign tables is reported. Privileges follow the rules of
// planGrantsWarned for objects that give PUBLIC nothing by default.
// Dropping undeclared wrappers is part of pruning; see pruneWrappers.
func planWrappers(plan *PlanDiff, live *Live, desired *schema.Database, mode PublicGrants) {
	warn := func(format string, args ...any) {
		plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf(format, args...)})
	}
	for _, name := range sortedKeys(desired.ForeignDataWrappers) {
		w := desired.ForeignDataWrappers[name]
		if w == nil {
			continue
		}
		lw := live.Wrappers[name]
		if lw == nil && w.RenamedFrom != "" && live.ExtensionMembers["fdw:"+w.RenamedFrom] {
			warn("foreign-data wrapper %s: renamedFrom names %s, which an extension owns, and pgy does not rename an extension's objects; nothing is planned for it", name, w.RenamedFrom)
			continue
		}
		extension, owned := wrapperExtension(live, desired, name)
		if owned && lw == nil && !live.Extensions[extension] && !desired.DeclaresExtension(extension) {
			warn("foreign-data wrapper %s comes with extension %s, which the database lacks and the extensions block does not list; nothing is planned for it", name, extension)
			continue
		}
		target := "foreign data wrapper " + quoteIdent(name)
		desiredG := resolveGrantRoles(w.Grants, live.CurrentUser)
		liveOwner := live.CurrentUser
		var liveG map[string]map[string]bool
		switch {
		case owned:
			if w.ManagesHandler() || w.ManagesValidator() || w.ManagesConnection() {
				warn("foreign-data wrapper %s belongs to extension %s, which defines its handler, validator and connection function; the ones declared for it are ignored", name, extension)
			}
			var liveOptions map[string]string
			if lw != nil {
				liveOptions, liveOwner = lw.Options, lw.Owner
				liveG = memberLiveGrants(live, mode, "fdw:"+name, "", lw.Grants, desiredG)
			} else {
				liveG = pendingGrants(nil, desiredG)
			}
			if clause := fdwOptionsAlter(w.Options, liveOptions); clause != "" {
				plan.Creates = append(plan.Creates, fmt.Sprintf("alter %s %s;", target, clause))
			}
		case lw == nil:
			stmt := "create " + target
			if w.Handler != "" {
				stmt += " handler " + pqIdent(w.Handler)
			}
			if w.Validator != "" {
				stmt += " validator " + pqIdent(w.Validator)
			}
			if w.Connection != "" {
				stmt += " connection " + pqIdent(w.Connection)
			}
			plan.Creates = append(plan.Creates, stmt+fdwOptionsClause(w.Options)+";")
		default:
			liveOwner, liveG = lw.Owner, lw.Grants
			clauses := []string{}
			for _, f := range []struct {
				keyword    string
				managed    bool
				want, have string
			}{{"handler", w.ManagesHandler(), w.Handler, lw.Handler}, {"validator", w.ManagesValidator(), w.Validator, lw.Validator}, {"connection", w.ManagesConnection(), w.Connection, lw.Connection}} {
				switch {
				case !f.managed || sameWrapperFunction(f.want, f.have):
				case f.want == "":
					clauses = append(clauses, "no "+f.keyword)
				default:
					clauses = append(clauses, f.keyword+" "+pqIdent(f.want))
				}
			}
			if tables := wrapperForeignTables(live, name); len(tables) > 0 && w.ManagesHandler() && !sameWrapperFunction(w.Handler, lw.Handler) {
				effect := "are read through the new handler from this migration on"
				if w.Handler == "" {
					effect = "cannot be read until the wrapper has a handler again"
				}
				warn("foreign-data wrapper %s: its handler changes, and the foreign tables on its servers %s: %s", name, effect, strings.Join(tables, ", "))
			}
			if clause := fdwOptionsAlter(w.Options, lw.Options); clause != "" {
				clauses = append(clauses, clause)
			}
			if len(clauses) > 0 {
				plan.Creates = append(plan.Creates, fmt.Sprintf("alter %s %s;", target, strings.Join(clauses, " ")))
			}
		}
		owner := liveOwner
		if w.Owner != "" {
			owner = resolveRole(w.Owner, live.CurrentUser)
			if owner != liveOwner {
				plan.Creates = append(plan.Creates, fmt.Sprintf("alter %s owner to %s;", target, grantRole(w.Owner)))
			}
		}
		if w.Grants != nil || w.GrantedBy != nil {
			liveG = withoutExternalGrantees(desired.External, false, liveG, desiredG)
			revokes, grants := ownedGrantDiffBy(target, desiredG, liveG, owner, coversPublic(mode, true, desiredG), live.grantedByRevoker("fdw:"+name, "", target, owner, w.GrantedBy, &plan.Warnings))
			if _, listed := desiredG["public"]; !listed {
				plan.Warnings = append(plan.Warnings, publicRevokeWarnings(revokes, mode, "a grants block on a foreign-data wrapper covers PUBLIC")...)
			}
			plan.Creates = append(append(plan.Creates, revokes...), grants...)
		}
		if w.Comment != "" && (lw == nil || w.Comment != lw.Comment) {
			plan.Alters = append(plan.Alters, fmt.Sprintf("comment on %s is %s;", target, quoteString(w.Comment)))
		}
	}
}

// wrapperForeignTables returns the live foreign tables that read through a
// server on foreign-data wrapper name, sorted.
func wrapperForeignTables(live *Live, name string) []string {
	out := []string{}
	for _, fq := range sortedKeys(live.Tables) {
		lt := live.Tables[fq]
		if lt == nil || lt.Foreign == nil {
			continue
		}
		if ls := live.Servers[lt.Foreign.Server]; ls != nil && ls.Wrapper == name {
			out = append(out, fq)
		}
	}
	return out
}

// pruneWrappers returns the DROP statements for the live foreign-data
// wrappers a foreignDataWrappers block does not list. A wrapper an
// extension owns is never dropped. One that a server still uses once
// serverDrops, the plan's DROP SERVER statements, have run, or that a
// declared server is yet to be created on, or that the connecting role may
// not drop (see Live.OwnerRoles), is kept and reported instead, since
// dropping it would fail. Without the block, wrappers are unmanaged.
func pruneWrappers(live *Live, desired *schema.Database, serverDrops []string) (drops []string, warnings []Warning) {
	if desired.ForeignDataWrappers == nil {
		return nil, nil
	}
	dropped := droppedObjects(serverDrops)
	users := map[string][]string{}
	for _, name := range sortedKeys(live.Servers) {
		if ls := live.Servers[name]; ls != nil && !dropped["drop server "+quoteIdent(name)] {
			users[ls.Wrapper] = append(users[ls.Wrapper], name)
		}
	}
	for _, name := range sortedKeys(desired.Servers) {
		if s := desired.Servers[name]; s != nil && live.Servers[name] == nil {
			users[s.Wrapper] = append(users[s.Wrapper], name)
		}
	}
	for _, name := range sortedKeys(live.Wrappers) {
		if desired.ForeignDataWrappers[name] != nil || live.ExtensionMembers["fdw:"+name] || desired.External.HasRole(live.Wrappers[name].Owner) {
			continue
		}
		if left := users[name]; len(left) > 0 {
			warnings = append(warnings, Warning{Message: fmt.Sprintf("foreign-data wrapper %s is not in the foreignDataWrappers block but servers this plan keeps still use it (%s), so it is not dropped; drop them first, or declare the wrapper", name, strings.Join(left, ", "))})
			continue
		}
		if owner := live.Wrappers[name].Owner; !live.mayDrop(owner) {
			warnings = append(warnings, notOwnerWarning(live, "foreign-data wrapper "+name, owner, ""))
			continue
		}
		drops = append(drops, fmt.Sprintf("drop foreign data wrapper %s;", quoteIdent(name)))
	}
	return drops, warnings
}

func (r *renamer) wrappers(d *schema.Database) {
	l := r.l
	for _, name := range sortedKeys(d.ForeignDataWrappers) {
		w := d.ForeignDataWrappers[name]
		if w == nil || w.RenamedFrom == "" || w.RenamedFrom == name {
			continue
		}
		old := w.RenamedFrom
		if l.Wrappers[old] == nil || l.Wrappers[name] != nil || l.ExtensionMembers["fdw:"+old] {
			continue
		}
		r.stmts = append(r.stmts, fmt.Sprintf("alter foreign data wrapper %s rename to %s;", quoteIdent(old), quoteIdent(name)))
		moveKey(l.Wrappers, old, name)
		for _, s := range l.Servers {
			if s != nil && s.Wrapper == old {
				s.Wrapper = name
			}
		}
	}
}

// simulateWrappers records in out the foreign-data wrappers a plan for
// desired leaves behind.
func simulateWrappers(out *Live, desired *schema.Database) {
	for name, w := range desired.ForeignDataWrappers {
		if w == nil {
			continue
		}
		lw := &LiveWrapper{Owner: out.CurrentUser}
		prev := out.Wrappers[name]
		if prev != nil {
			*lw = *prev
		}
		if _, owned := wrapperExtension(out, desired, name); !owned {
			if w.ManagesHandler() {
				lw.Handler = w.Handler
			}
			if w.ManagesValidator() {
				lw.Validator = w.Validator
			}
			if w.ManagesConnection() {
				lw.Connection = w.Connection
			}
		} else if prev == nil {
			out.ExtensionMembers["fdw:"+name] = true
		}
		if w.Options != nil {
			lw.Options = cloneMap(w.Options, nil)
		}
		if w.Owner != "" {
			lw.Owner = resolveRole(w.Owner, out.CurrentUser)
		}
		if w.Comment != "" {
			lw.Comment = w.Comment
		}
		if w.Grants != nil {
			lw.Grants = simulateACL(nil, resolveGrantRoles(w.Grants, out.CurrentUser))
		}
		if w.GrantedBy != nil {
			lw.Grants = simulateGrantedBy(out, "fdw:"+name, "", lw.Owner, lw.Grants, w.Grants != nil, w.GrantedBy)
		}
		if out.Wrappers == nil {
			out.Wrappers = map[string]*LiveWrapper{}
		}
		out.Wrappers[name] = lw
	}
}

// simulateACL is the ACL a grants block leaves behind: the privileges it
// lists, and those of public when the block neither lists nor covers
// PUBLIC.
func simulateACL(public map[string]bool, desired map[string][]string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for role, privs := range desired {
		if len(privs) == 0 {
			continue
		}
		out[role] = map[string]bool{}
		for _, p := range privs {
			out[role][strings.ToLower(p)] = true
		}
	}
	if _, listed := desired["public"]; !listed && len(public) > 0 {
		out["public"] = cloneSet(public)
	}
	return out
}
