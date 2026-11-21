package validate

import (
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// grantedByPrivileges lists the privileges each kind of object has, as a
// grantedBy block may grant them; ALL stands for every one.
var grantedByPrivileges = map[string][]string{
	"table":    {"select", "insert", "update", "delete", "truncate", "references", "trigger", "maintain"},
	"column":   {"select", "insert", "update", "references"},
	"sequence": {"usage", "select", "update"},
	"routine":  {"execute"},
	"schema":   {"usage", "create"},
	"usage":    {"usage"},
}

// checkGrantedBy reports the grantedBy blocks that name PUBLIC or no role
// as a grantor, grant a privilege the object's kind doesn't have, or list
// the declared owner, whose grants a grants block makes. Where the object
// also has a grants block, which then decides the grants of every grantor
// the grantedBy block doesn't list, it warns about a grantor that neither
// block gives the grant option of a privilege it grants: PostgreSQL then
// grants nothing, with a warning only. A column's grantor may hold the
// grant option on its table instead (tableGrants and tableBy).
func checkGrantedBy(d *schema.Database, add func(Severity, string, string, ...any)) {
	checkColumn := func(obj, kind, owner string, grants map[string][]string, by schema.GrantedBy, tableGrants map[string][]string, tableBy schema.GrantedBy) {
		if by == nil {
			return
		}
		valid := map[string]bool{"all": true, "all privileges": true}
		for _, p := range grantedByPrivileges[kind] {
			valid[p] = true
		}
		options := func(role string) map[string]bool {
			out := map[string]bool{}
			lists := [][]string{grants[role], tableGrants[role]}
			for _, b := range []schema.GrantedBy{by, tableBy} {
				for g, gs := range b {
					if g != role {
						lists = append(lists, gs[role])
					}
				}
			}
			for _, l := range lists {
				for _, p := range l {
					if base, ok := strings.CutSuffix(p, "*"); ok {
						out[base] = true
					}
				}
			}
			return out
		}
		for _, grantor := range sortedKeys(by) {
			switch g := strings.TrimSpace(grantor); {
			case g == "" || strings.EqualFold(g, "public"):
				add(Error, obj, "grantedBy names %q as a grantor; a grantor is a role, and PUBLIC grants nothing", grantor)
				continue
			case owner != "" && g == owner:
				add(Warning, obj, "grantedBy lists the owner %s, whose grants are what the grants block makes; list them under grants", owner)
				continue
			}
			opts := options(grantor)
			for _, grantee := range sortedKeys(by[grantor]) {
				for _, priv := range by[grantor][grantee] {
					base := strings.TrimSuffix(priv, "*")
					if !valid[base] {
						add(Error, obj, "grantedBy: %s grants %s to %s, a privilege %ss don't have; write %s (or all)", grantor, base, grantee, objectKind(obj), strings.Join(grantedByPrivileges[kind], ", "))
						continue
					}
					if (grants != nil || tableGrants != nil) && grantor != owner && !opts[base] && !opts["all"] && !opts["all privileges"] {
						add(Warning, obj, "grantedBy: %s grants %s to %s, but neither the grants block nor another grantor gives %s %s with grant option, which the grant needs; PostgreSQL grants nothing then and only warns", grantor, base, grantee, grantor, base)
					}
				}
			}
		}
	}
	check := func(obj, kind, owner string, grants map[string][]string, by schema.GrantedBy) {
		checkColumn(obj, kind, owner, grants, by, nil, nil)
	}
	for _, k := range sortedKeys(d.Tables) {
		t := d.Tables[k]
		if t == nil {
			continue
		}
		check("table "+k, "table", t.Owner, t.Grants, t.GrantedBy)
		for _, cn := range sortedKeys(t.Columns) {
			if c := t.Columns[cn]; c != nil {
				checkColumn("column "+cn+" of table "+k, "column", t.Owner, c.Grants, c.GrantedBy, t.Grants, t.GrantedBy)
			}
		}
	}
	for _, k := range sortedKeys(d.Views) {
		v := d.Views[k]
		if v == nil {
			continue
		}
		check("view "+k, "table", v.Owner, v.Grants, v.GrantedBy)
		for _, col := range sortedKeys(v.ColumnGrantedBy) {
			checkColumn("column "+col+" of view "+k, "column", v.Owner, v.ColumnGrants[col], v.ColumnGrantedBy[col], v.Grants, v.GrantedBy)
		}
	}
	for _, k := range sortedKeys(d.Sequences) {
		if sq := d.Sequences[k]; sq != nil {
			check("sequence "+k, "sequence", sq.Owner, sq.Grants, sq.GrantedBy)
		}
	}
	for _, k := range sortedKeys(d.Functions) {
		if f := d.Functions[k]; f != nil {
			check("function "+k, "routine", f.Owner, f.Grants, f.GrantedBy)
		}
	}
	for _, k := range sortedKeys(d.Procedures) {
		if p := d.Procedures[k]; p != nil {
			check("procedure "+k, "routine", p.Owner, p.Grants, p.GrantedBy)
		}
	}
	for _, k := range sortedKeys(d.Aggregates) {
		if ag := d.Aggregates[k]; ag != nil {
			check("aggregate "+k, "routine", ag.Owner, ag.Grants, ag.GrantedBy)
		}
	}
	for _, k := range sortedKeys(d.Types) {
		if td := d.Types[k]; td != nil {
			check("type "+k, "usage", td.Owner, td.Grants, td.GrantedBy)
		}
	}
	for _, k := range sortedKeys(d.Domains) {
		if dm := d.Domains[k]; dm != nil {
			check("domain "+k, "usage", dm.Owner, dm.Grants, dm.GrantedBy)
		}
	}
	for _, s := range sortedKeys(d.SchemaGrantedBy) {
		check("schema "+s, "schema", d.SchemaOwners[s], d.SchemaGrants[s], d.SchemaGrantedBy[s])
	}
	for _, k := range sortedKeys(d.Servers) {
		if s := d.Servers[k]; s != nil {
			check("server "+k, "usage", s.Owner, s.Grants, s.GrantedBy)
		}
	}
	for _, k := range sortedKeys(d.ForeignDataWrappers) {
		if w := d.ForeignDataWrappers[k]; w != nil {
			check("foreign-data wrapper "+k, "usage", w.Owner, w.Grants, w.GrantedBy)
		}
	}
	for _, k := range sortedKeys(d.Languages) {
		if l := d.Languages[k]; l != nil {
			check("language "+k, "usage", "", l.Grants, l.GrantedBy)
		}
	}
}

// objectKind returns the kind of obj, a name as checkGrantedBy writes it:
// "table" for "table s.t", "column" for "column c of table s.t".
func objectKind(obj string) string {
	for _, k := range []string{"foreign-data wrapper", "column", "table", "view", "sequence", "function", "procedure", "aggregate", "type", "domain", "schema", "server", "language"} {
		if strings.HasPrefix(obj, k+" ") {
			return k
		}
	}
	return obj
}
