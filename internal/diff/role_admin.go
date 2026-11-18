package diff

import (
	"fmt"
	"slices"
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// roleChangeKind is what a planned statement does to a role.
type roleChangeKind int

const (
	// roleAttributes is ALTER ROLE ... WITH.
	roleAttributes roleChangeKind = iota
	// roleSettings is ALTER ROLE ... SET or RESET, in a database or not.
	roleSettings
	// roleComment is COMMENT ON ROLE.
	roleComment
	// roleMembership is a GRANT or REVOKE of the role, or of an option of
	// a membership in it.
	roleMembership
)

// roleChange is the role a planned statement changes and how. attrs holds
// the attributes an ALTER ROLE ... WITH sets or clears (superuser,
// createdb, replication, bypassrls, ...), grantor the role a REVOKE ...
// GRANTED BY names.
type roleChange struct {
	role    string
	kind    roleChangeKind
	attrs   map[string]bool
	grantor string
}

// readRoleSpec reads a role specification as the planner writes it, a
// quoted name or CURRENT_USER, CURRENT_ROLE or SESSION_USER, which stand
// for currentUser.
func readRoleSpec(s, currentUser string) (string, string, bool) {
	if name, rest, ok := readIdent(s); ok {
		return name, rest, true
	}
	for _, kw := range []string{"current_user", "current_role", "session_user"} {
		if rest, ok := strings.CutPrefix(s, kw); ok && (rest == "" || rest[0] == ' ' || rest[0] == ';') {
			return currentUser, rest, true
		}
	}
	return "", s, false
}

// roleStatement reads the role a statement pgy plans changes. It knows the
// forms planRoles, planComments and the database role settings write, and
// reports false for the others.
func roleStatement(stmt, currentUser string) (roleChange, bool) {
	s := strings.TrimSpace(stmt)
	switch {
	case strings.HasPrefix(s, "alter role "):
		name, rest, ok := readRoleSpec(s[len("alter role "):], currentUser)
		switch {
		case !ok:
		case strings.HasPrefix(rest, " with "):
			return roleChange{role: name, kind: roleAttributes, attrs: roleOptionWords(rest[len(" with "):])}, true
		case strings.HasPrefix(rest, " set ") || strings.HasPrefix(rest, " reset ") || strings.HasPrefix(rest, " in database "):
			return roleChange{role: name, kind: roleSettings}, true
		}
	case strings.HasPrefix(s, "do ") && strings.Contains(s, "'alter role "):
		_, lit, _ := strings.Cut(s, "'alter role ")
		lit = strings.NewReplacer("''", "'", "%%", "%").Replace(lit)
		if name, rest, ok := readRoleSpec(lit, currentUser); ok && strings.HasPrefix(rest, " in database ") {
			return roleChange{role: name, kind: roleSettings}, true
		}
	case strings.HasPrefix(s, "comment on role "):
		if name, rest, ok := readRoleSpec(s[len("comment on role "):], currentUser); ok && strings.HasPrefix(rest, " is ") {
			return roleChange{role: name, kind: roleComment}, true
		}
	case strings.HasPrefix(s, "grant "):
		if name, rest, ok := readRoleSpec(s[len("grant "):], currentUser); ok && strings.HasPrefix(rest, " to ") {
			return roleChange{role: name, kind: roleMembership}, true
		}
	case strings.HasPrefix(s, "revoke "):
		body := trimWords(s[len("revoke "):], "admin option for ", "inherit option for ", "set option for ")
		name, rest, ok := readRoleSpec(body, currentUser)
		if !ok || !strings.HasPrefix(rest, " from ") {
			break
		}
		c := roleChange{role: name, kind: roleMembership}
		if at := strings.LastIndex(rest, " granted by "); at >= 0 {
			c.grantor, _, _ = readRoleSpec(rest[at+len(" granted by "):], currentUser)
		}
		return c, true
	}
	return roleChange{}, false
}

// roleOptionWords returns the attributes the options of an ALTER ROLE ...
// WITH set or clear, without their NO prefix.
func roleOptionWords(opts string) map[string]bool {
	if at := strings.Index(opts, "valid until "); at >= 0 {
		opts = opts[:at]
	}
	out := map[string]bool{}
	for _, w := range strings.Fields(strings.TrimSuffix(strings.TrimSpace(opts), ";")) {
		switch w = strings.TrimPrefix(strings.ToLower(w), "no"); w {
		case "login", "superuser", "createdb", "createrole", "replication", "bypassrls", "inherit":
			out[w] = true
		}
	}
	return out
}

// adminOf reports whether the connecting role of l holds the ADMIN option
// on role: whether it, or a role it is a member of directly or through
// other roles, whatever the memberships' INHERIT and SET options, is a
// member of role WITH ADMIN OPTION. No role holds it on itself.
func (l *Live) adminOf(role string) bool {
	if role == l.CurrentUser {
		return false
	}
	seen := map[string]bool{l.CurrentUser: true}
	queue := []string{l.CurrentUser}
	for len(queue) > 0 {
		member := queue[0]
		queue = queue[1:]
		for parent, lm := range l.RoleMemberOptions[member] {
			if parent == role && lm != nil && lm.Admin {
				return true
			}
			if !seen[parent] {
				seen[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	return false
}

// roleDenied returns why PostgreSQL rejects c when the connecting role of
// live runs it, cur being that role's attributes, as the clause that
// follows "<role>, the connecting role," in a warning; "" when it doesn't.
// cur is not a superuser. A role live doesn't have is one the plan
// creates, which its creator may change (see Live.CreatorGrant), unless
// it is a predefined pg_ role, which introspection leaves out.
func roleDenied(live *Live, cur *LiveRole, c roleChange) string {
	t := live.RoleDefs[c.role]
	if t == nil {
		if !strings.HasPrefix(c.role, "pg_") {
			return ""
		}
		t = &LiveRole{}
	}
	if t.Superuser {
		return "is not a superuser, and only a superuser may change a superuser role"
	}
	pre16 := live.ServerVersion > 0 && live.ServerVersion < 160000
	admin := live.adminOf(c.role)
	alter := func() string {
		switch {
		case !cur.CreateRole && (pre16 || admin):
			return "does not have the CREATEROLE attribute, which altering a role or commenting on it needs"
		case !cur.CreateRole:
			return "has neither the CREATEROLE attribute nor the ADMIN option on it, which PostgreSQL 16 and later need to alter a role or comment on it"
		case !pre16 && !admin:
			return "does not have the ADMIN option on it, which PostgreSQL 16 and later need, with the CREATEROLE attribute, to alter a role or comment on it"
		}
		return ""
	}
	switch c.kind {
	case roleMembership:
		switch {
		case pre16 && !admin && !cur.CreateRole:
			return "has neither the CREATEROLE attribute nor the ADMIN option on it, one of which granting or revoking a role needs"
		case !pre16 && !admin:
			return "does not have the ADMIN option on it, which PostgreSQL 16 and later need to grant or revoke a role"
		case !pre16 && c.grantor != "" && live.OwnerRoles != nil && !live.OwnerRoles[c.grantor]:
			return "does not have the privileges of " + c.grantor + ", which granted the membership the statement revokes"
		}
	case roleSettings:
		if c.role != live.CurrentUser {
			return alter()
		}
	case roleComment:
		return alter()
	case roleAttributes:
		if c.attrs["superuser"] {
			return "is not a superuser, and only a superuser may change the SUPERUSER attribute"
		}
		if pre16 && (t.Replication || c.attrs["replication"] || c.attrs["bypassrls"]) {
			return "is not a superuser, and before PostgreSQL 16 only a superuser may alter a replication role or change the REPLICATION or BYPASSRLS attribute"
		}
		if why := alter(); why != "" || pre16 {
			return why
		}
		var lacks []string
		for _, a := range []struct {
			name string
			has  bool
		}{{"createdb", cur.CreateDB}, {"replication", cur.Replication}, {"bypassrls", cur.BypassRLS}} {
			if c.attrs[a.name] && !a.has {
				lacks = append(lacks, strings.ToUpper(a.name))
			}
		}
		switch len(lacks) {
		case 0:
		case 1:
			return "does not have the " + lacks[0] + " attribute, which PostgreSQL 16 and later need to change it on another role"
		default:
			return "does not have the " + strings.Join(lacks[:len(lacks)-1], ", ") + " and " + lacks[len(lacks)-1] + " attributes, which PostgreSQL 16 and later need to change them on another role"
		}
	}
	return ""
}

// leaveOutUnadministrable removes from plan the statements on existing
// roles that PostgreSQL would reject because the connecting role, which
// is not a superuser, may not run them: ALTER ROLE, COMMENT ON ROLE, and
// GRANT and REVOKE of a role or of a membership's options. Nobody but a
// superuser may change a superuser role or the SUPERUSER attribute. On
// PostgreSQL 16 and later, altering a role or commenting on it takes the
// CREATEROLE attribute and the ADMIN option on it (see Live.adminOf), which
// a role holds on the roles it created; granting or revoking it takes the
// ADMIN option, a REVOKE ... GRANTED BY the privileges of the grantor, and
// changing CREATEDB, REPLICATION or BYPASSRLS those attributes. On
// PostgreSQL 15, CREATEROLE is enough, or the ADMIN option for a GRANT and
// REVOKE, except that only a superuser may alter a replication role or
// change REPLICATION or BYPASSRLS. A role may change its own settings. Each
// statement goes on its own, and one warning of kind not_owner per role
// names it, what the connecting role lacks and the first statement left
// out. base is the snapshot the plan was made from; without a connection
// (OwnerRoles nil), or without the connecting role's attributes, nothing is
// left out.
func leaveOutUnadministrable(plan *PlanDiff, desired *schema.Database, base *Live) {
	if base == nil || base.Offline || base.OwnerRoles == nil {
		return
	}
	cur := base.RoleDefs[base.CurrentUser]
	if cur == nil || cur.Superuser {
		return
	}
	removed := map[string][]string{}
	lacks := map[string][]string{}
	removedStmts := map[string]bool{}
	for _, sec := range []*[]string{&plan.ExtensionUpdates, &plan.Prelude, &plan.Creates, &plan.Alters, &plan.Drops, &plan.Concurrent} {
		var out []string
		for _, s := range *sec {
			c, ok := roleStatement(s, base.CurrentUser)
			why := ""
			if ok {
				why = roleDenied(base, cur, c)
			}
			if why == "" {
				out = append(out, s)
				continue
			}
			removed[c.role] = append(removed[c.role], s)
			removedStmts[s] = true
			if !slices.Contains(lacks[c.role], why) {
				lacks[c.role] = append(lacks[c.role], why)
			}
		}
		if len(out) < len(*sec) {
			*sec = out
		}
	}
	if len(removed) == 0 {
		return
	}
	kept := plan.Warnings[:0:0]
	for _, w := range plan.Warnings {
		if w.Statement == "" || !removedStmts[w.Statement] {
			kept = append(kept, w)
		}
	}
	plan.Warnings = kept
	for _, role := range sortedKeys(removed) {
		plan.Warnings = append(plan.Warnings, roleLeftOutWarning(base, role, removed[role], lacks[role], desired.External.HasRole(role)))
	}
}

// roleLeftOutWarning returns the not_owner warning for role, for which the
// plan leaves out stmts because the connecting role lacks what lacks
// says; external says that the config's external.roles lists role.
func roleLeftOutWarning(live *Live, role string, stmts, lacks []string, external bool) Warning {
	count, them := "the statement", "it"
	if len(stmts) > 1 {
		count, them = fmt.Sprintf("the %d statements", len(stmts)), "them"
	}
	first, _, more := strings.Cut(strings.TrimSpace(stmts[0]), "\n")
	if more || len(stmts) > 1 {
		first += " ..."
	}
	listed := ""
	if external {
		listed = ", which the config's external.roles lists"
	}
	return Warning{Kind: "not_owner", Message: fmt.Sprintf("role %s%s: %s, the connecting role, %s, so the plan leaves out %s (%s); have a superuser run %s",
		role, listed, live.CurrentUser, strings.Join(lacks, ", and "), count, first, them)}
}
