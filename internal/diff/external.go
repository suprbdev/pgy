package diff

import (
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// withExternal returns desired with External set to opts.External, so that
// every step of the plan that reads desired leaves external objects alone.
// desired itself is not modified.
func withExternal(desired *schema.Database, opts Options) *schema.Database {
	if desired == nil || opts.External == nil || desired.External == opts.External {
		return desired
	}
	c := *desired
	c.External = opts.External
	return &c
}

// externalPublication reports whether lp belongs to what ext lists: an
// external role owns it, or every table and schema it publishes is
// external.
func externalPublication(ext *schema.External, lp *LivePublication) bool {
	if ext == nil || lp == nil {
		return false
	}
	if ext.HasRole(lp.Owner) {
		return true
	}
	if lp.AllTables || len(lp.Tables)+len(lp.Schemas) == 0 {
		return false
	}
	for t := range lp.Tables {
		if !ext.HasRelation(t) {
			return false
		}
	}
	for _, s := range lp.Schemas {
		if !ext.HasSchema(s) {
			return false
		}
	}
	return true
}

// externalDeclaration reports whether dt declares a table the way pgy
// manages one in an external schema: without columns or anything else that
// only a table pgy creates can have, triggers aside.
func externalDeclaration(dt *schema.Table) bool {
	c := *dt
	c.Triggers = nil
	return c.AttributeOnly()
}

// externalTable returns the part of dt that applies to a table in an
// external schema: what an attribute-only declaration manages, and its
// triggers.
func externalTable(dt *schema.Table) *schema.Table {
	out := memberTable(dt)
	out.Triggers = dt.Triggers
	return out
}

// attributeOnlyTable reports whether dt, the declaration of table k,
// manages a table that already exists instead of one pgy creates: it is
// attribute-only, or declared in an external schema of ext the way
// externalDeclaration describes.
func attributeOnlyTable(ext *schema.External, k string, dt *schema.Table) bool {
	return dt.AttributeOnly() || ext.InSchema(k) && externalDeclaration(dt)
}

// withoutExternalGrantees returns liveG, the ACL of an object, as a grants
// block desired has to see it: without the grantees desired doesn't name
// that are external roles, whose privileges are then neither revoked nor
// required, or, for an object in an external schema (inExternal), without
// every grantee desired doesn't name.
func withoutExternalGrantees(ext *schema.External, inExternal bool, liveG map[string]map[string]bool, desired map[string][]string) map[string]map[string]bool {
	if ext == nil || len(liveG) == 0 {
		return liveG
	}
	out := make(map[string]map[string]bool, len(liveG))
	for role, privs := range liveG {
		if _, named := desired[role]; named || !inExternal && !ext.HasRole(role) {
			out[role] = privs
		}
	}
	return out
}

// withoutExternalSettings returns live, the settings of a database or
// role, without the external settings that desired doesn't set, so that
// they are never reset.
func withoutExternalSettings(ext *schema.External, live, desired map[string]string) map[string]string {
	if ext == nil || len(live) == 0 {
		return live
	}
	set := map[string]bool{}
	for k := range desired {
		set[strings.ToLower(k)] = true
	}
	out := make(map[string]string, len(live))
	for k, v := range live {
		if set[k] || !ext.HasSetting(k) {
			out[k] = v
		}
	}
	return out
}
