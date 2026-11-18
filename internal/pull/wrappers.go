package pull

import (
	"strings"

	"github.com/suprbdev/pgy/internal/diff"
)

// languages renders the trusted languages whose privileges are no longer
// the initial ones: what its extension left for a language an extension
// owns, as plpgsql is, and PUBLIC's USAGE for any other. Only the grantees
// that changed are written, one that lost its privileges with an empty
// list. Languages that an external role or an external extension owns are
// left out.
func (b *builder) languages() *node {
	out := newMap()
	for _, name := range sortedKeys(b.live.Languages) {
		ll := b.live.Languages[name]
		if ll == nil || !ll.Trusted || b.opts.External.HasRole(ll.Owner) || b.externalExtension(b.live.MemberExtension["language:"+name]) {
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

// wrappers renders the foreign-data wrappers. One that no extension owns is
// written in full, with an empty handler or validator where it has none, so
// that one set later is noticed. Of an extension's wrapper only what is no
// longer as the extension left it is written, and nothing at all for an
// untouched one: its options, the grantees whose privileges changed, its
// comment, and with Options.Owners an owner other than the extension's.
// Wrappers that an external role or an external extension owns are left
// out.
func (b *builder) wrappers() *node {
	out := newMap()
	for _, name := range sortedKeys(b.live.Wrappers) {
		lw := b.live.Wrappers[name]
		if lw == nil || b.opts.External.HasRole(lw.Owner) || b.externalExtension(b.live.MemberExtension["fdw:"+name]) {
			continue
		}
		w := newMap()
		grants, owner := lw.Grants, b.live.CurrentUser
		initial, owned := b.live.InitialGrants("fdw:"+name, "")
		if owned {
			grants, owner = diff.GrantChanges(lw.Grants, initial), lw.ExtensionOwner
			if len(lw.Options) > 0 {
				add(w, "options", stringsNode(lw.Options))
			}
		} else {
			add(w, "handler", str(strings.TrimPrefix(lw.Handler, "pg_catalog.")))
			add(w, "validator", str(strings.TrimPrefix(lw.Validator, "pg_catalog.")))
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
