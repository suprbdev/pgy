package diff

import (
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pgy/internal/schema"
)

// LiveLanguage is a language from pg_language.
type LiveLanguage struct {
	Owner string
	// Trusted reports a trusted language. The others can only be used by
	// superusers, and PostgreSQL rejects GRANT and REVOKE on them.
	Trusted bool
	// Grants: lanacl, owner excluded: role -> privilege ("usage", "usage*"
	// with grant option). The USAGE that PUBLIC has on a language whose ACL
	// was never changed is spelled out.
	Grants map[string]map[string]bool
}

// languageQ reads every language, one row per privilege held by a role
// other than its owner and one with an empty grantee for a language without
// any.
const languageQ = `
    select l.lanname, pg_get_userbyid(l.lanowner), l.lanpltrusted,
           coalesce(case when a.grantee = 0 then 'public' else pg_get_userbyid(a.grantee) end, ''),
           coalesce(lower(a.privilege_type) || case when a.is_grantable then '*' else '' end, '')
    from pg_language l
    left join lateral aclexplode(coalesce(l.lanacl, acldefault('l', l.lanowner))) a on a.grantee <> l.lanowner
`

func (l *Live) scanLanguages(rows pgx.Rows) error {
	for rows.Next() {
		var name, grantee, priv string
		ll := &LiveLanguage{}
		if err := rows.Scan(&name, &ll.Owner, &ll.Trusted, &grantee, &priv); err != nil {
			return err
		}
		if have := l.Languages[name]; have != nil {
			ll = have
		} else {
			l.Languages[name] = ll
		}
		if grantee == "" || priv == "" {
			continue
		}
		if ll.Grants == nil {
			ll.Grants = map[string]map[string]bool{}
		}
		if ll.Grants[grantee] == nil {
			ll.Grants[grantee] = map[string]bool{}
		}
		ll.Grants[grantee][priv] = true
	}
	return nil
}

// builtinLanguages are the languages every database has, with whether they
// are trusted.
var builtinLanguages = map[string]bool{"internal": false, "c": false, "sql": true, "plpgsql": true}

// liveLanguage returns the language name of live, or nil when the database
// has none. Without a server to ask, as in diff --from-empty, the languages
// every database has are taken to exist with their default privileges.
func liveLanguage(live *Live, name string) *LiveLanguage {
	if ll := live.Languages[name]; ll != nil {
		return ll
	}
	if trusted, ok := builtinLanguages[name]; ok && live.ServerVersion == 0 {
		return &LiveLanguage{Owner: live.CurrentUser, Trusted: trusted, Grants: map[string]map[string]bool{"public": {"usage": true}}}
	}
	return nil
}

// languagePending reports whether the language name, which live lacks,
// comes with an extension that is not installed yet: the declared extension
// of that name, which is how the extensions of procedural languages are
// called.
func languagePending(live *Live, desired *schema.Database, name string) bool {
	if !desired.DeclaresExtension(name) {
		return false
	}
	le := live.ExtensionDefs[name]
	return !live.Extensions[name] || le != nil && le.Created
}

// planLanguages reconciles the privileges of the declared languages. The
// statements go to plan.Creates, after the extension statements that create
// languages and ahead of the routines written in them. Languages are never
// created or dropped. One the database lacks is reported, unless the plan
// installs its extension (see languagePending). PUBLIC's USAGE is a default
// privilege, so a block covers PUBLIC only when it lists it, as for
// routines and types in planGrantsWarned.
func planLanguages(plan *PlanDiff, live *Live, desired *schema.Database, mode PublicGrants) {
	warn := func(format string, args ...any) {
		plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf(format, args...)})
	}
	allRevokes, allGrants := []string{}, []string{}
	for _, name := range sortedKeys(desired.Languages) {
		l := desired.Languages[name]
		if l == nil {
			continue
		}
		desiredG := resolveGrantRoles(l.Grants, live.CurrentUser)
		owner := live.CurrentUser
		var liveG map[string]map[string]bool
		switch ll := liveLanguage(live, name); {
		case ll == nil && !languagePending(live, desired, name):
			warn("language %s is declared under languages, which manages the privileges of a language that exists already (pgy creates no languages), but the database has no such language and this plan creates no extension %s that would bring it; nothing is planned for it", name, name)
			continue
		case ll == nil:
			liveG = pendingGrants(nil, desiredG)
		case !ll.Trusted:
			warn("language %s is not trusted: only superusers can use it and PostgreSQL rejects GRANT and REVOKE on it; nothing is planned for it", name)
			continue
		default:
			owner, liveG = ll.Owner, memberLiveGrants(live, mode, "language:"+name, "usage", ll.Grants, desiredG)
		}
		if l.Grants == nil {
			continue
		}
		liveG = withoutExternalGrantees(desired.External, false, liveG, desiredG)
		revokes, grants := ownedGrantDiff("language "+quoteIdent(name), desiredG, liveG, owner, coversPublic(mode, false, desiredG))
		if _, listed := desiredG["public"]; !listed {
			plan.Warnings = append(plan.Warnings, publicRevokeWarnings(revokes, mode, "")...)
		}
		allRevokes, allGrants = append(allRevokes, revokes...), append(allGrants, grants...)
	}
	plan.Creates = append(append(plan.Creates, allRevokes...), allGrants...)
}

// simulateLanguages records in out the language privileges a plan for
// desired leaves behind.
func simulateLanguages(out *Live, desired *schema.Database) {
	for name, l := range desired.Languages {
		if l == nil || l.Grants == nil {
			continue
		}
		ll := &LiveLanguage{Owner: out.CurrentUser, Trusted: true, Grants: map[string]map[string]bool{"public": {"usage": true}}}
		if prev := liveLanguage(out, name); prev != nil {
			*ll = *prev
		}
		if !ll.Trusted {
			continue
		}
		ll.Grants = simulateACL(ll.Grants["public"], resolveGrantRoles(l.Grants, out.CurrentUser))
		if out.Languages == nil {
			out.Languages = map[string]*LiveLanguage{}
		}
		out.Languages[name] = ll
	}
}
