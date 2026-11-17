package pull

import (
	"strings"

	"github.com/suprbdev/pgy/internal/diff"
)

// languages renders the trusted languages whose privileges are no longer
// the initial ones: what its extension left for a language an extension
// owns, as plpgsql is, and PUBLIC's USAGE for any other. Only the grantees
// that changed are written, one that lost its privileges with an empty
// list.
func (b *builder) languages() *node {
	out := newMap()
	for _, name := range sortedKeys(b.live.Languages) {
		ll := b.live.Languages[name]
		if ll == nil || !ll.Trusted {
			continue
		}
		initial, owned := b.live.InitialGrants("language:"+name, "usage")
		if !owned {
			initial = map[string]map[string]bool{"public": {"usage": true}}
		}
		if g := grantsNode(diff.GrantChanges(ll.Grants, initial)); g != nil {
			l := newMap()
			add(l, "grants", g)
			add(out, name, l)
		}
	}
	if len(out.Content) == 0 {
		return nil
	}
	return out
}

// withoutRole returns acl without the entry of role. The initial ACL of an
// extension's object lists the role that owned it then, which stops being a
// grantee, not a grantee that lost its privileges, once the owner changes.
func withoutRole(acl map[string]map[string]bool, role string) map[string]map[string]bool {
	if _, ok := acl[role]; !ok {
		return acl
	}
	out := make(map[string]map[string]bool, len(acl))
	for r, privs := range acl {
		if r != role {
			out[r] = privs
		}
	}
	return out
}

// wrappers renders the foreign-data wrappers. One that no extension owns is
// written in full. Of an extension's wrapper only what is no longer as the
// extension left it is written, and nothing at all for an untouched one:
// its options, the grantees whose privileges changed, its comment, and with
// Options.Owners an owner other than the extension's.
func (b *builder) wrappers() *node {
	out := newMap()
	for _, name := range sortedKeys(b.live.Wrappers) {
		lw := b.live.Wrappers[name]
		if lw == nil {
			continue
		}
		w := newMap()
		grants, owner := lw.Grants, b.live.CurrentUser
		initial, owned := b.live.InitialGrants("fdw:"+name, "")
		if owned {
			grants, owner = diff.GrantChanges(lw.Grants, withoutRole(initial, lw.ExtensionOwner)), lw.ExtensionOwner
			if len(lw.Options) > 0 {
				add(w, "options", stringsNode(lw.Options))
			}
		} else {
			addStr(w, "handler", strings.TrimPrefix(lw.Handler, "pg_catalog."))
			addStr(w, "validator", strings.TrimPrefix(lw.Validator, "pg_catalog."))
			add(w, "options", stringsNode(lw.Options))
		}
		if b.opts.Owners && lw.Owner != owner {
			addStr(w, "owner", lw.Owner)
		}
		if g := grantsNode(grants); g != nil {
			add(w, "grants", g)
		}
		addStr(w, "comment", lw.Comment)
		if owned && len(w.Content) == 0 {
			continue
		}
		add(out, name, w)
	}
	if len(out.Content) == 0 {
		return nil
	}
	return out
}
