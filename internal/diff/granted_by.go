package diff

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// grantedByBlock is a grantedBy block as a plan reconciles it: grantor ->
// grantee -> privilege -> whether it is declared with the grant option,
// role keywords resolved and privileges lower case.
type grantedByBlock map[string]map[string]map[string]bool

// resolveGrantedBy returns by, the grantedBy block of an object that owner
// owns, as a grantedByBlock, without its entry for the owner, and that
// entry as a grants block: a grant of the owner is what a grants block
// makes. Grants to the owner, which holds every privilege, are left out.
// Both are nil when by lists no such grantor.
func resolveGrantedBy(by schema.GrantedBy, owner, currentUser string) (grantedByBlock, map[string][]string) {
	var out grantedByBlock
	var ownerBy map[string][]string
	for _, grantor := range sortedKeys(by) {
		g := resolveRole(grantor, currentUser)
		grants := resolveGrantRoles(by[grantor], currentUser)
		if g == owner {
			if ownerBy == nil {
				ownerBy = map[string][]string{}
			}
			for grantee, privs := range grants {
				ownerBy[grantee] = append(ownerBy[grantee], privs...)
			}
			continue
		}
		if out == nil {
			out = grantedByBlock{}
		}
		if out[g] == nil {
			out[g] = map[string]map[string]bool{}
		}
		for grantee, privs := range grants {
			if grantee == owner {
				continue
			}
			if out[g][grantee] == nil {
				out[g][grantee] = map[string]bool{}
			}
			for _, p := range privs {
				base, opt := strings.CutSuffix(strings.ToLower(p), "*")
				out[g][grantee][base] = out[g][grantee][base] || opt
			}
		}
	}
	return out, ownerBy
}

// listed reports whether the grantedBy block lists grantor, whose grants
// on the object it then manages instead of the grants block.
func (rv *grantRevoker) listed(grantor string) bool {
	if rv == nil {
		return false
	}
	_, ok := rv.by[grantor]
	return ok
}

// plain returns desired, the grants block, with the grants the grantedBy
// block lists for the owner added, and liveG, the live privileges, as the
// grants block sees them: without those only grantors the grantedBy block
// lists give, and without the grant option when only they give it.
func (rv *grantRevoker) plain(desired map[string][]string, liveG map[string]map[string]bool) (map[string][]string, map[string]map[string]bool) {
	if rv == nil {
		return desired, liveG
	}
	if rv.ownerBy != nil {
		d := make(map[string][]string, len(desired)+len(rv.ownerBy))
		for r, p := range desired {
			d[r] = p
		}
		for r, p := range rv.ownerBy {
			if r != rv.owner {
				d[r] = append(append([]string(nil), d[r]...), p...)
			}
		}
		desired = d
	}
	if len(rv.by) == 0 || len(liveG) == 0 {
		return desired, liveG
	}
	return desired, withoutGrantsOf(liveG, rv.grantors, rv.listed)
}

// withoutGrantsOf returns liveG, an object's live privileges, without
// those only the grantors that listed reports give (as grantors records
// them), and without the grant option when only they give it. A privilege
// grantors has nothing on is kept.
func withoutGrantsOf(liveG map[string]map[string]bool, grantors LiveGrantors, listed func(string) bool) map[string]map[string]bool {
	unlisted := func(g string) bool { return !listed(g) }
	out := map[string]map[string]bool{}
	add := func(role, p string) {
		if out[role] == nil {
			out[role] = map[string]bool{}
		}
		out[role][p] = true
	}
	for role, privs := range liveG {
		if privs != nil && len(privs) == 0 {
			out[role] = map[string]bool{}
		}
		lg := grantors[role]
		for p := range privs {
			base := strings.TrimSuffix(p, "*")
			opt, hasOpt := lg[base+"*"]
			plain, hasPlain := lg[base]
			switch {
			case !hasOpt && !hasPlain:
				add(role, p)
			case slices.ContainsFunc(opt, unlisted):
				add(role, base+"*")
			case slices.ContainsFunc(plain, unlisted):
				add(role, base)
			}
		}
	}
	return out
}

// grantedByRevokes returns the REVOKE statements, on(privs) rendering
// "<privileges> on <target>", that take away the live grants of the
// grantors the grantedBy block lists that it doesn't declare, or their
// grant option when it declares the privilege without it.
func (rv *grantRevoker) grantedByRevokes(on func([]string) string) []revokeGroup {
	if rv == nil || len(rv.by) == 0 {
		return nil
	}
	var out []revokeGroup
	for _, g := range sortedKeys(rv.by) {
		for _, grantee := range sortedKeys(rv.grantors) {
			want, named := rv.by[g][grantee]
			if !named && rv.leaveAlone != nil && rv.leaveAlone(grantee) {
				continue
			}
			var revoke, revokeOpt []string
			for _, p := range sortedKeys(rv.grantors[grantee]) {
				if !slices.Contains(rv.grantors[grantee][p], g) {
					continue
				}
				base, opt := strings.CutSuffix(p, "*")
				wantOpt, wanted := want[base]
				switch {
				case !wanted:
					revoke = append(revoke, base)
				case opt && !wantOpt:
					revokeOpt = append(revokeOpt, base)
				}
			}
			pg := rv.plainGrantor(nil)
			if g == rv.live.CurrentUser && !rv.live.superuser() {
				pg = g
			}
			if len(revoke) > 0 {
				out = append(out, rv.revokeStmts(on, grantee, false, map[string][]string{g: revoke}, pg)...)
				rv.revokedBy = rv.revokedBy.with(g, grantee, revoke)
			}
			if len(revokeOpt) > 0 {
				out = append(out, rv.revokeStmts(on, grantee, true, map[string][]string{g: revokeOpt}, pg)...)
				rv.revokedOptBy = rv.revokedOptBy.with(g, grantee, revokeOpt)
			}
		}
	}
	return out
}

// with returns b, or a new grantedByBlock for a nil one, with grantor's
// privs to grantee added.
func (b grantedByBlock) with(grantor, grantee string, privs []string) grantedByBlock {
	if b == nil {
		b = grantedByBlock{}
	}
	if b[grantor] == nil {
		b[grantor] = map[string]map[string]bool{}
	}
	if b[grantor][grantee] == nil {
		b[grantor][grantee] = map[string]bool{}
	}
	for _, p := range privs {
		b[grantor][grantee][p] = true
	}
	return b
}

// afterTableRevokes takes out of the grantors of rv, the revoker of a
// column, the column grants that the grantedBy REVOKE statements of table,
// the revoker of its table, take away with the table's: REVOKE on a table
// takes the privilege, or its grant option, away on its columns too, from
// the grants of the same grantor.
func (rv *grantRevoker) afterTableRevokes(table *grantRevoker) {
	if rv == nil || table == nil || len(rv.grantors) == 0 || table.revokedBy == nil && table.revokedOptBy == nil {
		return
	}
	g := make(LiveGrantors, len(rv.grantors))
	for grantee, privs := range rv.grantors {
		g[grantee] = map[string][]string{}
		for p, gs := range privs {
			base, opt := strings.CutSuffix(p, "*")
			for _, grantor := range gs {
				switch {
				case table.revokedBy[grantor][grantee][base]:
				case opt && table.revokedOptBy[grantor][grantee][base]:
					g[grantee][base] = append(g[grantee][base], grantor)
				default:
					g[grantee][p] = append(g[grantee][p], grantor)
				}
			}
			if g[grantee][p] == nil {
				g[grantee][p] = []string{}
			}
		}
	}
	rv.grantors = g
}

// grantedByGrants returns the GRANT statements, on(privs) rendering
// "<privileges> on <target>", that make the grants the grantedBy block
// declares and the object lacks, or that the plan's REVOKE ... CASCADE
// statements take away (see grantRevoker.lost), each as its grantor (see
// grantRevoker.grantAs). The grants of a grantor go after those of the
// grantors that give it its grant options.
func (rv *grantRevoker) grantedByGrants(on func([]string) string) []string {
	if rv == nil || len(rv.by) == 0 {
		return nil
	}
	type planned struct {
		rank  int
		stmts []string
	}
	var all []planned
	for _, g := range sortedKeys(rv.by) {
		rank := rv.desiredDepth(g, map[string]bool{})
		for _, grantee := range sortedKeys(rv.by[g]) {
			var grant, grantOpt []string
			for _, p := range sortedKeys(rv.by[g][grantee]) {
				hasOpt := slices.Contains(rv.grantors[grantee][p+"*"], g) && !rv.lost[p][g]
				has := hasOpt || slices.Contains(rv.grantors[grantee][p], g) && !rv.lost[p][g]
				switch wantOpt := rv.by[g][grantee][p]; {
				case wantOpt && !hasOpt:
					grantOpt = append(grantOpt, p)
				case !wantOpt && !has:
					grant = append(grant, p)
				}
			}
			if len(grant) > 0 {
				all = append(all, planned{rank, rv.grantAs(g, grantee, grant, fmt.Sprintf("grant %s to %s", on(grant), grantRole(grantee)))})
			}
			if len(grantOpt) > 0 {
				all = append(all, planned{rank, rv.grantAs(g, grantee, grantOpt, fmt.Sprintf("grant %s to %s with grant option", on(grantOpt), grantRole(grantee)))})
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].rank < all[j].rank })
	var out []string
	for _, p := range all {
		out = append(out, p.stmts...)
	}
	return out
}

// desiredDepth returns how far grantor is from the owner along the chain
// of grant options the grantedBy block declares: 0 for a grantor no listed
// grantor gives a grant option to, one more than the deepest of those that
// give it one for the others. seen guards against cycles.
func (rv *grantRevoker) desiredDepth(grantor string, seen map[string]bool) int {
	if seen[grantor] {
		return 0
	}
	seen[grantor] = true
	defer delete(seen, grantor)
	d := 0
	for g, grantees := range rv.by {
		if g == grantor {
			continue
		}
		for _, opt := range grantees[grantor] {
			if opt {
				d = max(d, rv.desiredDepth(g, seen)+1)
				break
			}
		}
	}
	return d
}

// grantAs returns the statements that run stmt, a GRANT without its
// semicolon of privs to grantee, so that grantor is its grantor: stmt
// itself when the connecting role is grantor and neither the owner nor a
// superuser, whose GRANT records the owner instead; GRANT ... GRANTED BY
// on PostgreSQL 19 and later, when the connecting role is a superuser or
// has grantor's privileges; else stmt between SET ROLE to grantor and
// RESET ROLE, when it may SET ROLE to it, as before PostgreSQL 19 GRANTED
// BY must name the current user. A grant it can do none of these for is
// left out with a warning.
func (rv *grantRevoker) grantAs(grantor, grantee string, privs []string, stmt string) []string {
	l := rv.live
	switch {
	case grantor == l.CurrentUser && grantor != rv.owner && !l.superuser():
		return []string{stmt + ";"}
	case l.ServerVersion >= 190000 && (l.superuser() || l.OwnerRoles == nil || l.OwnerRoles[grantor]):
		return []string{stmt + " granted by " + grantRole(grantor) + ";"}
	case grantor != l.CurrentUser && (l.superuser() || l.SetRoles == nil || l.SetRoles[grantor]):
		return []string{"set role " + grantRole(grantor) + ";", stmt + ";", "reset role;"}
	}
	advice := "; have a superuser run " + stmt + " granted by " + grantRole(grantor) + ";"
	why := fmt.Sprintf("has neither %s's privileges nor the SUPERUSER attribute, one of which GRANT ... GRANTED BY needs", grantor)
	switch {
	case l.ServerVersion >= 190000:
	case grantor == l.CurrentUser:
		advice = ""
		why = "is a superuser, whose GRANT records the owner as the grantor, and before PostgreSQL 19 a GRANT cannot name another grantor"
	default:
		advice = "; have a superuser run set role " + grantRole(grantor) + "; " + stmt + "; reset role;"
		why = fmt.Sprintf("may not SET ROLE to %s, and before PostgreSQL 19 only a GRANT that runs as the grantor records it", grantor)
	}
	*rv.warns = append(*rv.warns, Warning{Kind: "not_owner", Message: fmt.Sprintf("%s: grantedBy declares %s's grant of %s to %s, and %s, the connecting role, %s, so the plan leaves the grant out%s",
		rv.target, grantor, strings.Join(privs, ", "), grantee, l.CurrentUser, why, advice)})
	return nil
}

// simulateGrantedBy returns acl, the privileges on the object with
// Grantors key key (column for a column's) after a plan made its grants
// block's grants, with the grants of by, its grantedBy block, made as
// well, and records the grantors these leave in out. owner is the object's
// owner, and hasGrants whether it has a grants block, after which only the
// grants of the owner give the privileges acl holds.
func simulateGrantedBy(out *Live, key, column, owner string, acl map[string]map[string]bool, hasGrants bool, by schema.GrantedBy) map[string]map[string]bool {
	if by == nil {
		return acl
	}
	listedBy, ownerBy := resolveGrantedBy(by, owner, out.CurrentUser)
	listed := func(g string) bool { _, ok := listedBy[g]; return ok }
	prev := out.grantorsOf(key, column)
	if !hasGrants {
		acl = withoutGrantsOf(acl, prev, listed)
	}
	next := LiveGrantors{}
	add := func(grantee, p, grantor string) {
		if next[grantee] == nil {
			next[grantee] = map[string][]string{}
		}
		if !slices.Contains(next[grantee][p], grantor) {
			next[grantee][p] = append(next[grantee][p], grantor)
		}
	}
	result := map[string]map[string]bool{}
	for grantee, privs := range acl {
		result[grantee] = map[string]bool{}
		for p := range privs {
			result[grantee][p] = true
			kept := false
			if !hasGrants {
				for _, g := range prev[grantee][p] {
					if !listed(g) {
						add(grantee, p, g)
						kept = true
					}
				}
			}
			if !kept {
				add(grantee, p, "")
			}
		}
	}
	for grantee, privs := range ownerBy {
		if result[grantee] == nil {
			result[grantee] = map[string]bool{}
		}
		for _, p := range privs {
			p = strings.ToLower(p)
			result[grantee][p] = true
			add(grantee, p, "")
		}
	}
	nonOwner := false
	for _, g := range sortedKeys(listedBy) {
		for grantee, privs := range listedBy[g] {
			if result[grantee] == nil {
				result[grantee] = map[string]bool{}
			}
			for p, opt := range privs {
				if opt {
					p += "*"
				}
				result[grantee][p] = true
				add(grantee, p, g)
				nonOwner = true
			}
		}
	}
	for _, privs := range next {
		for _, gs := range privs {
			sort.Strings(gs)
			nonOwner = nonOwner || slices.ContainsFunc(gs, func(g string) bool { return g != "" })
		}
	}
	if column != "" {
		fq := strings.TrimPrefix(key, "rel:")
		if out.ColumnGrantors == nil {
			out.ColumnGrantors = map[string]map[string]LiveGrantors{}
		}
		cols := cloneMap(out.ColumnGrantors[fq], nil)
		if nonOwner {
			cols[column] = next
		} else {
			delete(cols, column)
		}
		out.ColumnGrantors[fq] = cols
	} else {
		if out.Grantors == nil {
			out.Grantors = map[string]LiveGrantors{}
		}
		if nonOwner {
			out.Grantors[key] = next
		} else {
			delete(out.Grantors, key)
		}
	}
	return result
}

// expandAllGrantedBy returns by with every grantor's grants passed through
// expandAllGrants with set, or by itself, with ok false, when none has ALL.
func expandAllGrantedBy(by schema.GrantedBy, set []string) (out schema.GrantedBy, ok bool) {
	for grantor, grants := range by {
		expanded, changed := expandAllGrants(grants, set)
		if !changed {
			continue
		}
		if out == nil {
			out = cloneMap(by, nil)
		}
		out[grantor] = expanded
	}
	if out == nil {
		return by, false
	}
	return out, true
}

// ForeignGrantors returns the grants on the object with Grantors key key,
// or on its column when column is set, that a role other than the
// object's owner and the connecting role made (see ForeignGrants), which
// a grantedBy block declares: grantor -> grantee -> privileges, lower case
// with "*" for the grant option, sorted; nil when there are none.
func (l *Live) ForeignGrantors(key, column string) map[string]map[string][]string {
	var out map[string]map[string][]string
	for grantee, privs := range l.grantorsOf(key, column) {
		for p, gs := range privs {
			for _, grantor := range gs {
				if grantor == "" || grantor == l.CurrentUser {
					continue
				}
				if out == nil {
					out = map[string]map[string][]string{}
				}
				if out[grantor] == nil {
					out[grantor] = map[string][]string{}
				}
				out[grantor][grantee] = append(out[grantor][grantee], p)
			}
		}
	}
	for _, grants := range out {
		for _, privs := range grants {
			sort.Strings(privs)
		}
	}
	return out
}

// WithoutForeignGrants returns acl, the privileges on the object with
// Grantors key key (column for a column's), as a grants block declares
// them next to the grantedBy block of ForeignGrantors: without the
// privileges only those grants give, and without a grant option only they
// give. It is acl itself when the object has no such grants.
func (l *Live) WithoutForeignGrants(key, column string, acl map[string]map[string]bool) map[string]map[string]bool {
	g := l.grantorsOf(key, column)
	if len(g) == 0 || len(acl) == 0 {
		return acl
	}
	return withoutGrantsOf(acl, g, func(grantor string) bool { return grantor != "" && grantor != l.CurrentUser })
}

// grantorsOf returns the grantors of the grants on the object with
// Grantors key key, or on its column when column is set.
func (l *Live) grantorsOf(key, column string) LiveGrantors {
	if column != "" {
		return l.ColumnGrantors[strings.TrimPrefix(key, "rel:")][column]
	}
	return l.Grantors[key]
}
