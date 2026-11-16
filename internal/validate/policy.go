package validate

import (
	"fmt"
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// Policy holds the schema rules a project's .pgy.yml policy: block enables.
// Its findings are warnings, which --strict turns into failures.
type Policy struct {
	// SecurityDefinerHardened requires SECURITY DEFINER routines to set
	// search_path and to revoke PUBLIC's EXECUTE.
	SecurityDefinerHardened bool
	// RequireGrants lists the object kinds that must declare a grants block.
	RequireGrants map[string]bool
	// RequireOwners lists the object kinds that must declare an owner.
	RequireOwners map[string]bool
	// RowLevelSecurity lists the schemas whose tables must enable row-level
	// security and declare at least one policy.
	RowLevelSecurity map[string]bool
}

// PolicyKinds are the object kinds require_grants and require_owners accept.
var PolicyKinds = []string{"schemas", "tables", "views", "sequences", "functions", "procedures", "types", "domains", "servers"}

// Empty reports whether the policy enables no rule.
func (p Policy) Empty() bool {
	return !p.SecurityDefinerHardened && len(p.RequireGrants) == 0 && len(p.RequireOwners) == 0 && len(p.RowLevelSecurity) == 0
}

// CheckPolicy returns a warning for each object in d that breaks a rule
// policy enables.
func CheckPolicy(d *schema.Database, p Policy) []Issue {
	issues := []Issue{}
	warn := func(obj, format string, args ...any) {
		issues = append(issues, Issue{Severity: Warning, Object: obj, Message: "policy: " + fmt.Sprintf(format, args...)})
	}
	type object struct {
		kind, key string
		grants    bool
		owner     string
	}
	objects := []object{}
	schemas := map[string]bool{}
	note := func(kind, key string, grants bool, owner string) {
		objects = append(objects, object{kind, key, grants, owner})
		if i := strings.IndexByte(key, '.'); i > 0 {
			schemas[key[:i]] = true
		}
	}
	for _, k := range sortedKeys(d.Tables) {
		if t := d.Tables[k]; t != nil && t.PartitionOf == "" {
			note("tables", k, t.Grants != nil, t.Owner)
		}
	}
	for _, k := range sortedKeys(d.Views) {
		if v := d.Views[k]; v != nil {
			note("views", k, v.Grants != nil, v.Owner)
		}
	}
	for _, k := range sortedKeys(d.Sequences) {
		if s := d.Sequences[k]; s != nil {
			note("sequences", k, s.Grants != nil, s.Owner)
		}
	}
	for _, k := range sortedKeys(d.Functions) {
		if f := d.Functions[k]; f != nil {
			note("functions", k, f.Grants != nil, f.Owner)
		}
	}
	for _, k := range sortedKeys(d.Procedures) {
		if pr := d.Procedures[k]; pr != nil {
			note("procedures", k, pr.Grants != nil, pr.Owner)
		}
	}
	for _, k := range sortedKeys(d.Types) {
		if td := d.Types[k]; td != nil {
			note("types", k, td.Grants != nil, td.Owner)
		}
	}
	for _, k := range sortedKeys(d.Domains) {
		if dm := d.Domains[k]; dm != nil {
			note("domains", k, dm.Grants != nil, dm.Owner)
		}
	}
	for _, k := range sortedKeys(d.Servers) {
		if s := d.Servers[k]; s != nil {
			note("servers", k, s.Grants != nil, s.Owner)
		}
	}
	for _, s := range sortedKeys(schemas) {
		_, grants := d.SchemaGrants[s]
		objects = append(objects, object{"schemas", s, grants, d.SchemaOwners[s]})
	}
	singular := map[string]string{"schemas": "schema", "tables": "table", "views": "view", "sequences": "sequence", "functions": "function", "procedures": "procedure", "types": "type", "domains": "domain", "servers": "server"}
	for _, o := range objects {
		obj := singular[o.kind] + " " + o.key
		if p.RequireGrants[o.kind] && !o.grants {
			warn(obj, "%s must declare a grants block (require_grants)", o.kind)
		}
		if p.RequireOwners[o.kind] && o.owner == "" {
			warn(obj, "%s must declare an owner (require_owners)", o.kind)
		}
	}

	if p.SecurityDefinerHardened {
		check := func(obj, security string, set map[string]string, revokePublic bool, grants map[string][]string) {
			if !strings.EqualFold(security, "definer") {
				return
			}
			if strings.TrimSpace(set["search_path"]) == "" {
				warn(obj, "SECURITY DEFINER routines must set search_path, so callers can't redirect the names they use (security_definer: hardened)")
			}
			publicExec := !revokePublic
			if privs, ok := grants["public"]; ok {
				publicExec = false
				for _, pv := range privs {
					publicExec = publicExec || strings.HasPrefix(strings.ToLower(strings.TrimSpace(pv)), "execute")
				}
			}
			if publicExec {
				warn(obj, "SECURITY DEFINER routines must revoke PUBLIC's EXECUTE (revokePublic: true, or grants: {public: []}) (security_definer: hardened)")
			}
		}
		for _, k := range sortedKeys(d.Functions) {
			if f := d.Functions[k]; f != nil {
				check("function "+k, f.Security, f.Set, f.RevokePublic, f.Grants)
			}
		}
		for _, k := range sortedKeys(d.Procedures) {
			if pr := d.Procedures[k]; pr != nil {
				check("procedure "+k, pr.Security, pr.Set, pr.RevokePublic, pr.Grants)
			}
		}
	}

	for _, k := range sortedKeys(d.Tables) {
		t := d.Tables[k]
		s, _, _ := strings.Cut(k, ".")
		if t == nil || t.PartitionOf != "" || t.Foreign != nil || !p.RowLevelSecurity[s] {
			continue
		}
		if t.RowLevelSecurity == nil || !*t.RowLevelSecurity {
			warn("table "+k, "tables in schema %s must enable row-level security (row_level_security)", s)
		} else if len(t.Policies) == 0 {
			warn("table "+k, "tables in schema %s must declare at least one policy (row_level_security)", s)
		}
	}
	return issues
}
