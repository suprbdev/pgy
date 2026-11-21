package diff

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pgy/internal/schema"
)

// LiveGrantors holds the grantors of the grants on one object, or on one
// column of a relation: grantee -> privilege, spelled as in the grant maps
// ("select", "select*" with the grant option) -> the roles whose grants
// give it, sorted, "" standing for the object's owner.
type LiveGrantors map[string]map[string][]string

// grantorQ reads every grant on the objects, and on the columns, whose ACL
// holds a grant made by a role other than the object's owner, the owner's
// own privileges left out, with an empty grantor for the owner's grants.
const grantorQ = `
    select o.kind, o.nsp, o.name, o.args, o.col,
           case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end,
           lower(a.privilege_type) || case when a.is_grantable then '*' else '' end,
           case when a.grantor = o.owner then '' else pg_get_userbyid(a.grantor)::text end
    from (
        select 'rel'::text as kind, n.nspname::text as nsp, c.relname::text as name, ''::text as args, ''::text as col, c.relowner as owner, c.relacl as acl
        from pg_class c join pg_namespace n on n.oid = c.relnamespace
        where c.relacl is not null and c.relkind in ('r', 'p', 'v', 'm', 'S', 'f')
        union all
        select 'rel', n.nspname::text, c.relname::text, '', att.attname::text, c.relowner, att.attacl
        from pg_attribute att join pg_class c on c.oid = att.attrelid join pg_namespace n on n.oid = c.relnamespace
        where att.attacl is not null and att.attnum > 0 and not att.attisdropped and c.relkind in ('r', 'p', 'f', 'v', 'm')
        union all
        select 'proc', n.nspname::text, p.proname::text, pg_get_function_identity_arguments(p.oid), '', p.proowner, p.proacl
        from pg_proc p join pg_namespace n on n.oid = p.pronamespace
        where p.proacl is not null
        union all
        select 'type', n.nspname::text, t.typname::text, '', '', t.typowner, t.typacl
        from pg_type t join pg_namespace n on n.oid = t.typnamespace
        where t.typacl is not null
        union all
        select 'schema', '', n.nspname::text, '', '', n.nspowner, n.nspacl from pg_namespace n where n.nspacl is not null
        union all
        select 'srv', '', s.srvname::text, '', '', s.srvowner, s.srvacl from pg_foreign_server s where s.srvacl is not null
        union all
        select 'fdw', '', w.fdwname::text, '', '', w.fdwowner, w.fdwacl from pg_foreign_data_wrapper w where w.fdwacl is not null
        union all
        select 'language', '', l.lanname::text, '', '', l.lanowner, l.lanacl from pg_language l where l.lanacl is not null
    ) o
    cross join lateral aclexplode(o.acl) a
    where a.grantee <> o.owner
    and exists (select from aclexplode(o.acl) b where b.grantor <> o.owner)
`

// setRoleQ reads the roles the connecting role may SET ROLE to: before
// PostgreSQL 16 those it is a member of, from 16 on those whose
// memberships give it the SET option.
const setRoleQ = `
    select r.rolname::text from pg_roles r
    where pg_has_role(current_user, r.oid, case when current_setting('server_version_num')::int >= 160000 then 'SET' else 'MEMBER' end)
`

// addGrantorQueries adds to batch the queries that fill l.Grantors,
// l.ColumnGrantors and l.SetRoles.
func (l *Live) addGrantorQueries(batch *queryBatch) {
	batch.add(grantorQ, func(rows pgx.Rows) error {
		for rows.Next() {
			var kind, nsp, name, args, col, grantee, priv, grantor string
			if err := rows.Scan(&kind, &nsp, &name, &args, &col, &grantee, &priv, &grantor); err != nil {
				return err
			}
			var g LiveGrantors
			switch {
			case col != "":
				fq := nsp + "." + name
				if l.ColumnGrantors == nil {
					l.ColumnGrantors = map[string]map[string]LiveGrantors{}
				}
				if l.ColumnGrantors[fq] == nil {
					l.ColumnGrantors[fq] = map[string]LiveGrantors{}
				}
				if g = l.ColumnGrantors[fq][col]; g == nil {
					g = LiveGrantors{}
					l.ColumnGrantors[fq][col] = g
				}
			default:
				key := kind + ":" + name
				switch kind {
				case "rel", "type":
					key = kind + ":" + nsp + "." + name
				case "proc":
					key = "proc:" + publicScope.functionSignature(fmt.Sprintf("%s.%s(%s)", nsp, name, args))
				}
				if l.Grantors == nil {
					l.Grantors = map[string]LiveGrantors{}
				}
				if g = l.Grantors[key]; g == nil {
					g = LiveGrantors{}
					l.Grantors[key] = g
				}
			}
			if g[grantee] == nil {
				g[grantee] = map[string][]string{}
			}
			g[grantee][priv] = append(g[grantee][priv], grantor)
		}
		sortGrantors := func(g LiveGrantors) {
			for _, privs := range g {
				for _, grantors := range privs {
					sort.Strings(grantors)
				}
			}
		}
		for _, g := range l.Grantors {
			sortGrantors(g)
		}
		for _, cols := range l.ColumnGrantors {
			for _, g := range cols {
				sortGrantors(g)
			}
		}
		return nil
	})
	batch.add(setRoleQ, func(rows pgx.Rows) error {
		l.SetRoles = map[string]bool{}
		for rows.Next() {
			var role string
			if err := rows.Scan(&role); err != nil {
				return err
			}
			l.SetRoles[role] = true
		}
		return nil
	})
}

// grantRevoker writes the REVOKE statements a grants block plans for the
// grants on one object, or on one column, whose ACL holds grants of roles
// other than its owner (see Live.Grantors). A REVOKE takes away only the
// grants of the role it runs as, or, run by the owner, a role with the
// owner's privileges or a superuser, those of the owner. So the grants of
// any other role are revoked by naming it: GRANTED BY on PostgreSQL 19 and
// later, when the connecting role has the grantor's privileges, else
// between SET ROLE to the grantor and RESET ROLE, when the connecting role
// may SET ROLE to it. A grant it can do neither for is left out with a
// warning. A REVOKE that takes a grant option away from a role whose own
// grants depend on it needs CASCADE, and the dependent grants the block
// doesn't revoke are granted again (see grantRevoker.regrants). It also
// reports the privileges that a block keeps and that only a grant of such
// a role gives.
type grantRevoker struct {
	live *Live
	// target is the object, as a warning names it.
	target string
	// owner is the object's owner when the plan grants and revokes.
	owner    string
	grantors LiveGrantors
	warns    *[]Warning
	// keptPrivs: grantee -> privilege the block keeps -> whether it wants
	// the grant option.
	keptPrivs map[string]map[string]bool
	// cascaded: role -> the privileges whose grant option the role loses
	// to a REVOKE ... CASCADE, which takes away the grants it made of them.
	cascaded map[string][]string
	// revoked and revokedOpt: grantee -> the privileges, and the grant
	// options of privileges, the block revokes.
	revoked, revokedOpt map[string]map[string]bool
	// regranted: grantee -> the privileges regrants gives back.
	regranted map[string]map[string]bool
	// by is the object's grantedBy block, its grants of the owner left
	// out, and ownerBy those, which are the grants block's (see
	// resolveGrantedBy); nil when it has none.
	by      grantedByBlock
	ownerBy map[string][]string
	// leaveAlone reports a grantee whose grants by the grantors by lists
	// are left alone unless the grantor's entry names it: an external role.
	leaveAlone func(string) bool
	// lost: privilege -> the roles whose grants of it the plan's REVOKE
	// ... CASCADE statements take away (see grantRevoker.regrants).
	lost map[string]map[string]bool
	// revokedBy and revokedOptBy: grantor -> grantee -> the privileges,
	// and the grant options of privileges, grantedByRevokes takes away.
	revokedBy, revokedOptBy grantedByBlock
}

// grantedByRevoker returns the grantRevoker of the object with Grantors
// key key, or of its column when column is set, whose owner is owner,
// whose grantedBy block is by and which target names, adding its warnings
// to warns; nil when the object holds no grant of a role other than its
// owner and by is nil, and the plain REVOKE is the one to run.
func (l *Live) grantedByRevoker(key, column, target, owner string, by schema.GrantedBy, warns *[]Warning) *grantRevoker {
	if l == nil {
		return nil
	}
	g := l.grantorsOf(key, column)
	if len(g) == 0 && by == nil {
		return nil
	}
	rv := &grantRevoker{live: l, target: target, owner: owner, grantors: g, warns: warns}
	rv.by, rv.ownerBy = resolveGrantedBy(by, owner, l.CurrentUser)
	return rv
}

// plainGrantor returns the role whose grants a REVOKE of privs without
// GRANTED BY takes away, the one PostgreSQL picks: the owner when the
// connecting role is the owner or a superuser, else the connecting role
// when it holds the grant option of each of privs itself, else the owner
// when the connecting role has its privileges, else the connecting role.
func (rv *grantRevoker) plainGrantor(privs []string) string {
	l := rv.live
	if l.CurrentUser == rv.owner || l.superuser() {
		return rv.owner
	}
	direct := len(privs) > 0
	for _, p := range privs {
		if len(rv.grantors[l.CurrentUser][p+"*"]) == 0 {
			direct = false
		}
	}
	if !direct && (l.OwnerRoles == nil || l.OwnerRoles[rv.owner]) {
		return rv.owner
	}
	return l.CurrentUser
}

// superuser reports whether the connecting role of l is a superuser.
func (l *Live) superuser() bool {
	cur := l.RoleDefs[l.CurrentUser]
	return cur != nil && cur.Superuser
}

// grantorsOf returns the roles whose grants give role priv (with the
// grant option, when option is set), the owner standing for "", or nil
// when they are unknown.
func (rv *grantRevoker) grantorsOf(role, priv string, option bool) []string {
	if rv == nil {
		return nil
	}
	privs := rv.grantors[role]
	gs := append([]string(nil), privs[priv+"*"]...)
	if !option {
		gs = append(gs, privs[priv]...)
	}
	for i, g := range gs {
		if g == "" {
			gs[i] = rv.owner
		}
	}
	sort.Strings(gs)
	return slices.Compact(gs)
}

// revokeGroup is one REVOKE a grants block plans, in the statements that
// run it; rank orders the revokes of one object, those of grants made by
// roles further from the owner along the chain of grant options first, as
// PostgreSQL rejects revoking a grant option that other grants depend on.
type revokeGroup struct {
	rank  int
	stmts []string
}

// revoke returns the statements that take privs (their grant option, with
// option) away from role, on(privs) rendering "<privileges> on <target>".
func (rv *grantRevoker) revoke(on func([]string) string, role string, privs []string, option bool) []revokeGroup {
	head := "revoke "
	if option {
		head = "revoke grant option for "
	}
	if rv == nil {
		return []revokeGroup{{stmts: []string{fmt.Sprintf("%s%s from %s;", head, on(privs), grantRole(role))}}}
	}
	done := &rv.revoked
	if option {
		done = &rv.revokedOpt
	}
	if *done == nil {
		*done = map[string]map[string]bool{}
	}
	if (*done)[role] == nil {
		(*done)[role] = map[string]bool{}
	}
	for _, p := range privs {
		(*done)[role][p] = true
	}
	byGrantor := map[string][]string{}
	pg := rv.plainGrantor(privs)
	for _, p := range privs {
		gs := slices.DeleteFunc(rv.grantorsOf(role, p, option), rv.listed)
		if len(gs) == 0 {
			gs = []string{pg}
		}
		for _, g := range gs {
			byGrantor[g] = append(byGrantor[g], p)
		}
	}
	return rv.revokeStmts(on, role, option, byGrantor, pg)
}

// revokeStmts returns the statements that take away from role the grants
// of byGrantor, grantor -> privileges (their grant option, with option), pg
// being the grantor whose grants a REVOKE without GRANTED BY takes away
// (see grantRevoker.plainGrantor).
func (rv *grantRevoker) revokeStmts(on func([]string) string, role string, option bool, byGrantor map[string][]string, pg string) []revokeGroup {
	head := "revoke "
	if option {
		head = "revoke grant option for "
	}
	dependents := map[string]bool{}
	for _, ps := range byGrantor {
		for _, p := range ps {
			if !dependents[p] && rv.granted(role, p) {
				dependents[p] = true
				if rv.cascaded == nil {
					rv.cascaded = map[string][]string{}
				}
				rv.cascaded[role] = append(rv.cascaded[role], p)
			}
		}
	}
	stmt := func(ps []string, by string) string {
		s := head + on(ps) + " from " + grantRole(role)
		if by != "" {
			s += " granted by " + grantRole(by)
		}
		if slices.ContainsFunc(ps, func(p string) bool { return dependents[p] }) {
			s += " cascade"
		}
		return s + ";"
	}
	l := rv.live
	var out []revokeGroup
	for _, g := range sortedKeys(byGrantor) {
		ps := byGrantor[g]
		rank := rv.depth(g, map[string]bool{})
		switch {
		case g == pg:
			out = append(out, revokeGroup{rank: rank, stmts: []string{stmt(ps, "")}})
		case l.ServerVersion >= 190000 && (l.superuser() || l.OwnerRoles == nil || l.OwnerRoles[g]):
			out = append(out, revokeGroup{rank: rank, stmts: []string{stmt(ps, g)}})
		case g != l.CurrentUser && (l.superuser() || l.SetRoles[g]):
			out = append(out, revokeGroup{rank: rank, stmts: []string{"set role " + grantRole(g) + ";", stmt(ps, ""), "reset role;"}})
		default:
			rv.leftOut(g, role, strings.Join(ps, ", "), stmt(ps, g), stmt(ps, ""))
		}
	}
	return out
}

// granted reports whether role granted priv, with or without the grant
// option, to any role on the object.
func (rv *grantRevoker) granted(role, priv string) bool {
	for _, privs := range rv.grantors {
		for _, key := range []string{priv, priv + "*"} {
			if slices.Contains(privs[key], role) {
				return true
			}
		}
	}
	return false
}

// regrants returns the GRANT statements, on(privs) rendering "<privileges>
// on <target>", that give back the grants its REVOKE ... CASCADE
// statements take away (see grantRevoker.cascaded) and the block doesn't
// revoke itself: those the roles losing a grant option made, and in turn
// those of the roles that then lose theirs. A grantee keeps a privilege,
// or its grant option, that a grant of another role still gives it. What
// the block keeps gets the grant option the block declares, the rest the
// one it had.
func (rv *grantRevoker) regrants(on func([]string) string) []string {
	if rv == nil || len(rv.cascaded) == 0 {
		return nil
	}
	grant, grantOpt := map[string][]string{}, map[string][]string{}
	for _, from := range sortedKeys(rv.cascaded) {
		for _, p := range rv.cascaded[from] {
			lost := map[string]bool{from: true}
			queue := []string{from}
			for len(queue) > 0 {
				l := queue[0]
				queue = queue[1:]
				for grantee := range rv.grantors {
					opts := rv.grantorsOf(grantee, p, true)
					if lost[grantee] || !slices.Contains(opts, l) {
						continue
					}
					if !slices.ContainsFunc(opts, func(g string) bool { return !lost[g] }) {
						lost[grantee] = true
						queue = append(queue, grantee)
					}
				}
			}
			if rv.lost == nil {
				rv.lost = map[string]map[string]bool{}
			}
			if rv.lost[p] == nil {
				rv.lost[p] = map[string]bool{}
			}
			for r := range lost {
				rv.lost[p][r] = true
			}
			isLost := func(g string) bool { return lost[g] && !rv.listed(g) }
			kept := func(g string) bool { return !lost[g] }
			for _, grantee := range sortedKeys(rv.grantors) {
				gs := rv.grantorsOf(grantee, p, false)
				if grantee == from || rv.revoked[grantee][p] || !slices.ContainsFunc(gs, isLost) {
					continue
				}
				wantOpt, declared := rv.keptPrivs[grantee][p]
				if !declared {
					wantOpt = len(rv.grantorsOf(grantee, p, true)) > 0 && !rv.revokedOpt[grantee][p]
				}
				switch {
				case !slices.ContainsFunc(gs, kept) && wantOpt:
					grantOpt[grantee] = append(grantOpt[grantee], p)
				case !slices.ContainsFunc(gs, kept):
					grant[grantee] = append(grant[grantee], p)
				case wantOpt && !slices.ContainsFunc(rv.grantorsOf(grantee, p, true), kept):
					grantOpt[grantee] = append(grantOpt[grantee], p)
				}
			}
		}
	}
	rv.regranted = map[string]map[string]bool{}
	for _, m := range []map[string][]string{grant, grantOpt} {
		for grantee, ps := range m {
			if rv.regranted[grantee] == nil {
				rv.regranted[grantee] = map[string]bool{}
			}
			for _, p := range ps {
				rv.regranted[grantee][p] = true
			}
		}
	}
	var out []string
	for _, grantee := range sortedKeys(grant) {
		out = append(out, fmt.Sprintf("grant %s to %s;", on(slices.Compact(slices.Sorted(slices.Values(grant[grantee])))), grantRole(grantee)))
	}
	for _, grantee := range sortedKeys(grantOpt) {
		out = append(out, fmt.Sprintf("grant %s to %s with grant option;", on(slices.Compact(slices.Sorted(slices.Values(grantOpt[grantee])))), grantRole(grantee)))
	}
	return out
}

// depth returns how far grantor is from the owner along the chain of grant
// options of the object: 0 for the owner, one more than the role that
// gave grantor its grant option for the others. seen guards against
// cycles.
func (rv *grantRevoker) depth(grantor string, seen map[string]bool) int {
	if grantor == rv.owner || grantor == "" || seen[grantor] {
		return 0
	}
	seen[grantor] = true
	defer delete(seen, grantor)
	d := 0
	for priv, gs := range rv.grantors[grantor] {
		if !strings.HasSuffix(priv, "*") {
			continue
		}
		for _, g := range gs {
			d = max(d, rv.depth(g, seen))
		}
	}
	return d + 1
}

// leftOut warns that the plan leaves out the revoke of privs from role
// that only grantor's grant gives: grantedBy is the REVOKE ... GRANTED BY
// grantor, asGrantor the REVOKE as grantor would run it.
func (rv *grantRevoker) leftOut(grantor, role, privs, grantedBy, asGrantor string) {
	l := rv.live
	advice := "; have a superuser run " + grantedBy
	why := fmt.Sprintf("has neither %s's privileges nor the SUPERUSER attribute, one of which REVOKE ... GRANTED BY needs", grantor)
	switch {
	case l.ServerVersion >= 190000:
	case grantor == l.CurrentUser:
		advice = ""
		why = "runs a REVOKE as the owner, and before PostgreSQL 19 a REVOKE cannot name another grantor"
	default:
		advice = "; have a superuser run set role " + grantRole(grantor) + "; " + asGrantor + " reset role;"
		why = fmt.Sprintf("may not SET ROLE to %s, and before PostgreSQL 19 only a REVOKE that runs as the grantor takes its grant away", grantor)
	}
	*rv.warns = append(*rv.warns, Warning{Kind: "not_owner", Message: fmt.Sprintf("%s: %s granted %s to %s, and %s, the connecting role, %s, so the plan leaves the revoke out%s",
		rv.target, grantor, privs, role, l.CurrentUser, why, advice)})
}

// keep notes that the block keeps role's priv (wanting the grant option,
// with option) on the object, for regrants and reportKept.
func (rv *grantRevoker) keep(role, priv string, option bool) {
	if rv == nil {
		return
	}
	if rv.keptPrivs == nil {
		rv.keptPrivs = map[string]map[string]bool{}
	}
	if rv.keptPrivs[role] == nil {
		rv.keptPrivs[role] = map[string]bool{}
	}
	rv.keptPrivs[role][priv] = option
}

// reportKept warns about the privileges the block keeps, other than those
// regrants gives back, that a grant of a role other than the owner and the
// connecting role gives.
func (rv *grantRevoker) reportKept() {
	if rv == nil {
		return
	}
	var kept []string
	for _, role := range sortedKeys(rv.keptPrivs) {
		for _, priv := range sortedKeys(rv.keptPrivs[role]) {
			if rv.regranted[role][priv] {
				continue
			}
			var others []string
			for _, g := range rv.grantorsOf(role, priv, false) {
				if g != rv.owner && g != rv.live.CurrentUser && !rv.listed(g) {
					others = append(others, g)
				}
			}
			if len(others) == 0 {
				continue
			}
			what := priv
			if rv.keptPrivs[role][priv] {
				what += " with grant option"
			}
			kept = append(kept, fmt.Sprintf("%s's %s is granted by %s", role, what, strings.Join(others, " and ")))
		}
	}
	if len(kept) > 0 {
		*rv.warns = append(*rv.warns, Warning{Message: fmt.Sprintf("%s: %s; grants blocks don't record grantors, so pgy keeps such grants, but a privilege it grants is granted by the owner or the connecting role; a grantedBy block declares a grant of another role", rv.target, strings.Join(kept, "; "))})
	}
}

// ForeignGrant is a grant on an object, or on a column of a relation, made
// by a role other than the object's owner (see Live.Grantors).
type ForeignGrant struct {
	// Object is the object, as "table s.t", "function s.f(integer)" or
	// "schema s"; Schema its schema, "" for an object outside schemas; Key
	// its Live.Grantors key.
	Object, Schema, Key string
	Column         string
	Grantee        string
	// Privilege is lower case, with "*" for the grant option.
	Privilege string
	Grantor   string
}

// ForeignGrants returns the grants of l made by a role other than the
// object's owner and the connecting role, which grants blocks cannot
// express: a database built from them gets the privilege from the owner or
// the connecting role. They are sorted by object, column, grantee,
// privilege and grantor.
func (l *Live) ForeignGrants() []ForeignGrant {
	var out []ForeignGrant
	ix := newOwnerIndex(l)
	add := func(key, column string, g LiveGrantors) {
		kind, name, _ := strings.Cut(key, ":")
		nsp := ""
		switch kind {
		case "rel", "type", "proc":
			nsp, _, _ = strings.Cut(name, ".")
		case "schema":
			nsp = name
		}
		object := ix.describe(key)
		for grantee, privs := range g {
			for priv, gs := range privs {
				for _, grantor := range gs {
					if grantor == "" || grantor == l.CurrentUser {
						continue
					}
					out = append(out, ForeignGrant{Object: object, Schema: nsp, Key: key, Column: column, Grantee: grantee, Privilege: priv, Grantor: grantor})
				}
			}
		}
	}
	for key, g := range l.Grantors {
		add(key, "", g)
	}
	for fq, cols := range l.ColumnGrantors {
		for col, g := range cols {
			add("rel:"+fq, col, g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		for _, c := range [][2]string{{a.Object, b.Object}, {a.Column, b.Column}, {a.Grantee, b.Grantee}, {a.Privilege, b.Privilege}} {
			if c[0] != c[1] {
				return c[0] < c[1]
			}
		}
		return a.Grantor < b.Grantor
	})
	return out
}

// setRoleRevokesFirst returns revokes with the REVOKE statements that run
// between SET ROLE and RESET ROLE (see grantRevoker) moved ahead of the
// others, keeping the order of each part: running as the grantor, a REVOKE
// needs the grantor's privileges on the schema of its object, which a
// plain REVOKE of the plan may take away.
func setRoleRevokesFirst(revokes []string) []string {
	var first, rest []string
	for i := 0; i < len(revokes); i++ {
		if strings.HasPrefix(revokes[i], "set role ") && i+2 < len(revokes) && revokes[i+2] == "reset role;" {
			first = append(first, revokes[i:i+3]...)
			i += 2
			continue
		}
		rest = append(rest, revokes[i])
	}
	if len(first) == 0 {
		return revokes
	}
	return append(first, rest...)
}
