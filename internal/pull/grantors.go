package pull

import (
	"fmt"
	"maps"
	"strings"

	"github.com/suprbdev/pgy/internal/diff"
)

// withoutForeignGrants returns live, or a copy of it, whose grant maps hold
// the privileges as grants blocks declare them (see
// diff.Live.WithoutForeignGrants): the grants a role other than the
// object's owner and the connecting role made are written as grantedBy
// blocks instead (see builder.grantedBy).
func withoutForeignGrants(live *diff.Live) *diff.Live {
	if len(live.Grantors) == 0 && len(live.ColumnGrantors) == 0 {
		return live
	}
	out := *live
	out.TableGrants, out.TypeGrants, out.FunctionGrants, out.SchemaGrants = cloned(live.TableGrants), cloned(live.TypeGrants), cloned(live.FunctionGrants), cloned(live.SchemaGrants)
	out.Servers, out.Wrappers, out.Languages = cloned(live.Servers), cloned(live.Wrappers), cloned(live.Languages)
	for key := range live.Grantors {
		kind, name, _ := strings.Cut(key, ":")
		switch kind {
		case "rel":
			out.TableGrants[name] = live.WithoutForeignGrants(key, "", live.TableGrants[name])
		case "type":
			out.TypeGrants[name] = live.WithoutForeignGrants(key, "", live.TypeGrants[name])
		case "proc":
			out.FunctionGrants[name] = live.WithoutForeignGrants(key, "", live.FunctionGrants[name])
		case "schema":
			out.SchemaGrants[name] = live.WithoutForeignGrants(key, "", live.SchemaGrants[name])
		case "srv":
			if ls := live.Servers[name]; ls != nil {
				c := *ls
				c.Grants = live.WithoutForeignGrants(key, "", ls.Grants)
				out.Servers[name] = &c
			}
		case "fdw":
			if lw := live.Wrappers[name]; lw != nil {
				c := *lw
				c.Grants = live.WithoutForeignGrants(key, "", lw.Grants)
				out.Wrappers[name] = &c
			}
		case "language":
			if ll := live.Languages[name]; ll != nil {
				c := *ll
				c.Grants = live.WithoutForeignGrants(key, "", ll.Grants)
				out.Languages[name] = &c
			}
		}
	}
	if len(live.ColumnGrantors) > 0 {
		out.ColumnGrants = cloned(live.ColumnGrants)
		for fq, cols := range live.ColumnGrantors {
			if live.ColumnGrants[fq] == nil {
				continue
			}
			out.ColumnGrants[fq] = maps.Clone(live.ColumnGrants[fq])
			for col := range cols {
				out.ColumnGrants[fq][col] = live.WithoutForeignGrants("rel:"+fq, col, live.ColumnGrants[fq][col])
			}
		}
	}
	return &out
}

// cloned returns a copy of m, an empty map for a nil one.
func cloned[V any](m map[string]V) map[string]V {
	if m == nil {
		return map[string]V{}
	}
	return maps.Clone(m)
}

// grantedBy adds to m the grantedBy block of the object with
// diff.Live.Grantors key key, or of its column when column is set: the
// grants on it that a role other than its owner and the connecting role
// made (see diff.Live.ForeignGrantors). It reports whether it added one.
func (b *builder) grantedBy(m *node, key, column string) bool {
	by := b.live.ForeignGrantors(key, column)
	if len(by) == 0 {
		return false
	}
	n := flowMap()
	for _, grantor := range sortedKeys(by) {
		g := map[string]map[string]bool{}
		for grantee, privs := range by[grantor] {
			g[grantee] = map[string]bool{}
			for _, p := range privs {
				g[grantee][p] = true
			}
		}
		add(n, grantor, grantsNode(g))
	}
	add(m, "grantedBy", n)
	if b.grantedByWritten == nil {
		b.grantedByWritten = map[string]bool{}
	}
	b.grantedByWritten[key+"\x00"+column] = true
	return true
}

// foreignGrants warns about the grants on the objects of the pulled
// schemas, and on those outside schemas, that a role other than the
// object's owner and the connecting role made (see diff.Live.ForeignGrants)
// and that no grantedBy block was written for: a database built from the
// YAML gets those privileges from the owner or the connecting role instead.
func (b *builder) foreignGrants() {
	var order []string
	byObject := map[string][]string{}
	for _, g := range b.live.ForeignGrants() {
		if g.Schema != "" && !b.wantSchema(g.Schema) || b.grantedByWritten[g.Key+"\x00"+g.Column] {
			continue
		}
		object := g.Object
		if g.Column != "" {
			object = fmt.Sprintf("column %s of %s", g.Column, g.Object)
		}
		priv, option := strings.CutSuffix(g.Privilege, "*")
		if option {
			priv += " with grant option"
		}
		if byObject[object] == nil {
			order = append(order, object)
		}
		byObject[object] = append(byObject[object], fmt.Sprintf("%s's %s, granted by %s", g.Grantee, priv, g.Grantor))
	}
	for _, object := range order {
		b.warn("%s: %s; the YAML has no grantedBy block for them, so a database built from it gets these privileges granted by the owner or the connecting role", object, strings.Join(byObject[object], "; "))
	}
}
