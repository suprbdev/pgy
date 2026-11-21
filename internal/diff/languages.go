package diff

import (
	"fmt"
	"strings"

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

// planLanguages reconciles the privileges of the declared languages. The
// statements go to plan.Creates, after the extension statements that create
// languages and ahead of the routines written in them. Languages are never
// created or dropped. One the database lacks is reported, unless the plan
// creates or updates an extension, which may bring it under any name
// (pljava brings java): its statements then run in a DO block that skips
// them unless the language exists and is trusted, since PostgreSQL rejects
// GRANT and REVOKE on an untrusted one. PUBLIC's USAGE is a default
// privilege, so a block covers PUBLIC only when it lists it, as for
// routines and types in planGrantsWarned, or when the language's extension
// took that USAGE away (see publicRevokedByExtension).
func planLanguages(plan *PlanDiff, live *Live, desired *schema.Database, mode PublicGrants) {
	warn := func(format string, args ...any) {
		plan.Warnings = append(plan.Warnings, Warning{Message: fmt.Sprintf(format, args...)})
	}
	pending := membersPending(live, append(append([]string{}, plan.ExtensionUpdates...), plan.Creates...))
	allRevokes, allGrants, guarded := []string{}, []string{}, []string{}
	for _, name := range sortedKeys(desired.Languages) {
		l := desired.Languages[name]
		if l == nil {
			continue
		}
		desiredG := resolveGrantRoles(l.Grants, live.CurrentUser)
		owner := live.CurrentUser
		var liveG map[string]map[string]bool
		ll := liveLanguage(live, name)
		switch {
		case ll == nil && !pending:
			warn("language %s is declared under languages, which manages the privileges of a language that exists already (pgy creates no languages), but the database has no such language and this plan creates or updates no extension that would bring it; nothing is planned for it", name)
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
		covers, why := coversPublic(mode, false, desiredG), ""
		if reason, ok := publicRevokedByExtension(live, mode, "language:"+name, "usage", desiredG); ok {
			covers, why = true, reason
		}
		revokes, grants := ownedGrantDiffBy("language "+quoteIdent(name), desiredG, liveG, owner, covers, live.revoker("language:"+name, "", "language "+quoteIdent(name), owner, &plan.Warnings))
		if _, listed := desiredG["public"]; !listed {
			plan.Warnings = append(plan.Warnings, publicRevokeWarnings(revokes, mode, why)...)
		}
		if ll == nil {
			if stmts := append(revokes, grants...); len(stmts) > 0 {
				guarded = append(guarded, ifTrustedLanguage(name, stmts))
			}
			continue
		}
		allRevokes, allGrants = append(allRevokes, revokes...), append(allGrants, grants...)
	}
	plan.Creates = append(append(append(plan.Creates, allRevokes...), allGrants...), guarded...)
}

// ifTrustedLanguage wraps stmts, the GRANT and REVOKE statements on language
// name, in a DO block that runs them only once the language exists and is
// trusted.
func ifTrustedLanguage(name string, stmts []string) string {
	body := fmt.Sprintf("begin if exists (select from pg_catalog.pg_language where lanname = %s and lanpltrusted) then %s end if; end", quoteString(name), strings.Join(stmts, " "))
	return "do " + dollarQuoteInline(body) + ";"
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
