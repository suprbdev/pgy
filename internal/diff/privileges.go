package diff

import (
	"sort"
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// allPrivileges returns the privileges GRANT ALL gives on an object of kind
// on a server running version (as Live.ServerVersion): table (which views,
// materialized views and foreign tables share), column, sequence, routine,
// schema, or any other kind, whose only privilege is USAGE. Tables gained
// MAINTAIN in PostgreSQL 17. The list is sorted.
func allPrivileges(kind string, version int) []string {
	switch kind {
	case "table":
		if version >= 170000 {
			return []string{"delete", "insert", "maintain", "references", "select", "trigger", "truncate", "update"}
		}
		return []string{"delete", "insert", "references", "select", "trigger", "truncate", "update"}
	case "column":
		return []string{"insert", "references", "select", "update"}
	case "sequence":
		return []string{"select", "update", "usage"}
	case "routine":
		return []string{"execute"}
	case "schema":
		return []string{"create", "usage"}
	}
	return []string{"usage"}
}

// defaultPrivilegeKinds maps the object kinds of a defaultPrivileges entry
// to the kinds of allPrivileges.
var defaultPrivilegeKinds = map[string]string{"tables": "table", "sequences": "sequence", "functions": "routine", "types": "type", "schemas": "schema"}

// expandAllList returns privs, one role's list from a grants block, with
// ALL and ALL PRIVILEGES replaced by the privileges in set, keeping a grant
// option ("all*") on each of them. A privilege the list names twice, once
// with a grant option, is kept once with it. Without a set, as for a plan
// with no server to ask, ALL stays and swallows the privileges it covers.
// ok is false, and privs is returned as it is, when the list has no ALL.
func expandAllList(privs, set []string) (out []string, ok bool) {
	all, allOpt := false, false
	for _, p := range privs {
		switch strings.Join(strings.Fields(strings.ToLower(p)), " ") {
		case "all", "all privileges":
			all = true
		case "all*", "all privileges*":
			all, allOpt = true, true
		}
	}
	if !all {
		return privs, false
	}
	want := map[string]bool{}
	mark := func(base string, opt bool) { want[base] = want[base] || opt }
	if set == nil {
		mark("all", allOpt)
	}
	for _, p := range set {
		mark(p, allOpt)
	}
	for _, p := range privs {
		p = strings.Join(strings.Fields(strings.ToLower(p)), " ")
		base := strings.TrimSuffix(p, "*")
		if base == "all" || base == "all privileges" || set == nil && (allOpt || base == p) {
			continue
		}
		mark(base, base != p)
	}
	for base, opt := range want {
		if opt {
			base += "*"
		}
		out = append(out, base)
	}
	sort.Strings(out)
	return out, true
}

// expandAllGrants returns grants with every role's list passed through
// expandAllList, or grants itself, with ok false, when no list has ALL.
func expandAllGrants(grants map[string][]string, set []string) (out map[string][]string, ok bool) {
	for role, privs := range grants {
		expanded, changed := expandAllList(privs, set)
		if !changed {
			continue
		}
		if out == nil {
			out = make(map[string][]string, len(grants))
			for r, p := range grants {
				out[r] = p
			}
		}
		out[role] = expanded
	}
	if out == nil {
		return grants, false
	}
	return out, true
}

// rewriteEach returns m with the values rewrite returns a replacement for
// (a non-nil one) replaced, in a copy of m, or m itself, with ok false,
// when rewrite replaces none.
func rewriteEach[T any](m map[string]*T, rewrite func(*T) *T) (out map[string]*T, ok bool) {
	for k, v := range m {
		if v == nil {
			continue
		}
		if nv := rewrite(v); nv != nil {
			if out == nil {
				out = cloneMap(m, nil)
			}
			out[k] = nv
		}
	}
	if out == nil {
		return m, false
	}
	return out, true
}

// expandGrantsField returns m with the grants block field points to
// expanded by expandAllGrants with set, in copies of the objects it
// changes.
func expandGrantsField[T any](m map[string]*T, field func(*T) *map[string][]string, set []string) map[string]*T {
	out, _ := rewriteEach(m, func(v *T) *T {
		g, ok := expandAllGrants(*field(v), set)
		if !ok {
			return nil
		}
		c := *v
		*field(&c) = g
		return &c
	})
	return out
}

// expandAllPrivileges returns desired with ALL and ALL PRIVILEGES in its
// grants blocks replaced by the privileges they stand for on the object's
// kind on a server running version, which is how PostgreSQL stores them,
// so that they compare with live ACLs. Objects it changes are copied;
// desired is left as it is. With version 0 (no server, as in diff
// --from-empty) ALL is kept, and GRANT ALL gives whatever the server that
// runs the migration has.
func expandAllPrivileges(desired *schema.Database, version int) *schema.Database {
	if desired == nil {
		return nil
	}
	set := func(kind string) []string {
		if version <= 0 {
			return nil
		}
		return allPrivileges(kind, version)
	}
	out := *desired
	out.Tables, _ = rewriteEach(desired.Tables, func(t *schema.Table) *schema.Table {
		g, grantsChanged := expandAllGrants(t.Grants, set("table"))
		cols, colsChanged := rewriteEach(t.Columns, func(c *schema.Column) *schema.Column {
			cg, ok := expandAllGrants(c.Grants, set("column"))
			if !ok {
				return nil
			}
			cc := *c
			cc.Grants = cg
			return &cc
		})
		if !grantsChanged && !colsChanged {
			return nil
		}
		c := *t
		c.Grants, c.Columns = g, cols
		return &c
	})
	out.Views, _ = rewriteEach(desired.Views, func(v *schema.View) *schema.View {
		g, grantsChanged := expandAllGrants(v.Grants, set("table"))
		var cols map[string]map[string][]string
		for col, cg := range v.ColumnGrants {
			eg, ok := expandAllGrants(cg, set("column"))
			if !ok {
				continue
			}
			if cols == nil {
				cols = cloneMap(v.ColumnGrants, nil)
			}
			cols[col] = eg
		}
		if !grantsChanged && cols == nil {
			return nil
		}
		c := *v
		c.Grants = g
		if cols != nil {
			c.ColumnGrants = cols
		}
		return &c
	})
	out.Sequences = expandGrantsField(desired.Sequences, func(v *schema.Sequence) *map[string][]string { return &v.Grants }, set("sequence"))
	out.Functions = expandGrantsField(desired.Functions, func(v *schema.Function) *map[string][]string { return &v.Grants }, set("routine"))
	out.Procedures = expandGrantsField(desired.Procedures, func(v *schema.Procedure) *map[string][]string { return &v.Grants }, set("routine"))
	out.Types = expandGrantsField(desired.Types, func(v *schema.TypeDef) *map[string][]string { return &v.Grants }, set("type"))
	out.Domains = expandGrantsField(desired.Domains, func(v *schema.Domain) *map[string][]string { return &v.Grants }, set("type"))
	out.Servers = expandGrantsField(desired.Servers, func(v *schema.Server) *map[string][]string { return &v.Grants }, set("server"))
	out.ForeignDataWrappers = expandGrantsField(desired.ForeignDataWrappers, func(v *schema.ForeignDataWrapper) *map[string][]string { return &v.Grants }, set("fdw"))
	out.Languages = expandGrantsField(desired.Languages, func(v *schema.Language) *map[string][]string { return &v.Grants }, set("language"))
	schemaGrantsCopied := false
	for s, g := range desired.SchemaGrants {
		if eg, ok := expandAllGrants(g, set("schema")); ok {
			if !schemaGrantsCopied {
				out.SchemaGrants, schemaGrantsCopied = cloneMap(desired.SchemaGrants, nil), true
			}
			out.SchemaGrants[s] = eg
		}
	}
	defaultsCopied := false
	for i, dp := range desired.DefaultPrivileges {
		if dp == nil {
			continue
		}
		var grants map[string]map[string][]string
		for kind, g := range dp.Grants {
			eg, ok := expandAllGrants(g, set(defaultPrivilegeKinds[kind]))
			if !ok {
				continue
			}
			if grants == nil {
				grants = cloneMap(dp.Grants, nil)
			}
			grants[kind] = eg
		}
		if grants == nil {
			continue
		}
		if !defaultsCopied {
			out.DefaultPrivileges, defaultsCopied = append([]*schema.DefaultPrivilege(nil), desired.DefaultPrivileges...), true
		}
		c := *dp
		c.Grants = grants
		out.DefaultPrivileges[i] = &c
	}
	return &out
}
