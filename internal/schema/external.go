package schema

import (
	"fmt"
	"strings"
)

// External holds the external: block of the config file: glob patterns for
// the schemas, roles, settings and relations a database has that the
// application does not own, such as a hosting platform's. pgy pull leaves
// them out, pgy diff leaves them as they are and never prunes them, and
// pgy validate does not report references to them. A nil *External
// matches nothing.
//
// A pattern matches a whole name: * matches any run of characters, ? any
// one character, and every other character itself. Schema, role and
// relation names are matched case-sensitively, setting names in any case,
// as PostgreSQL compares them. A relation pattern is "schema.relation",
// split at its first dot, and each part is matched against the same part
// of the relation's name.
type External struct {
	Schemas   []string
	Roles     []string
	Settings  []string
	Relations []string
}

// Check reports the first pattern that cannot match anything: an empty
// one, or a relation pattern without a schema part.
func (e *External) Check() error {
	if e == nil {
		return nil
	}
	for _, l := range []struct {
		key      string
		patterns []string
	}{{"schemas", e.Schemas}, {"roles", e.Roles}, {"settings", e.Settings}, {"relations", e.Relations}} {
		for _, p := range l.patterns {
			if strings.TrimSpace(p) == "" {
				return fmt.Errorf("external.%s: a pattern is empty", l.key)
			}
			if l.key == "relations" {
				if s, r, ok := strings.Cut(p, "."); !ok || s == "" || r == "" {
					return fmt.Errorf("external.relations: %q needs a schema and a relation name, as schema.relation (*.name matches name in every schema)", p)
				}
			}
		}
	}
	return nil
}

// Empty reports whether e lists no pattern.
func (e *External) Empty() bool {
	return e == nil || len(e.Schemas)+len(e.Roles)+len(e.Settings)+len(e.Relations) == 0
}

// HasSchema reports whether the schema named name is external.
func (e *External) HasSchema(name string) bool {
	return e != nil && name != "" && matchAny(e.Schemas, name)
}

// HasRole reports whether the role named name is external. PUBLIC never
// is.
func (e *External) HasRole(name string) bool {
	return e != nil && name != "" && !strings.EqualFold(name, "public") && matchAny(e.Roles, name)
}

// HasSetting reports whether the configuration parameter name (a database
// or role setting) is external.
func (e *External) HasSetting(name string) bool {
	if e == nil || name == "" {
		return false
	}
	name = strings.ToLower(name)
	for _, p := range e.Settings {
		if globMatch(strings.ToLower(p), name) {
			return true
		}
	}
	return false
}

// InSchema reports whether the object with model key fq ("schema.name",
// or a routine's "schema.name(args)") is in an external schema.
func (e *External) InSchema(fq string) bool {
	s, _, ok := strings.Cut(fq, ".")
	return ok && e.HasSchema(s)
}

// HasRelation reports whether the table, view, materialized view, sequence
// or foreign table with model key fq ("schema.name") is external: it is in
// an external schema or a relations pattern matches it.
func (e *External) HasRelation(fq string) bool {
	if e == nil {
		return false
	}
	s, name, ok := strings.Cut(fq, ".")
	if !ok {
		return false
	}
	if e.HasSchema(s) {
		return true
	}
	for _, p := range e.Relations {
		ps, pr, ok := strings.Cut(p, ".")
		if ok && globMatch(ps, s) && globMatch(pr, name) {
			return true
		}
	}
	return false
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if globMatch(p, name) {
			return true
		}
	}
	return false
}

// globMatch reports whether pattern, in which * matches any run of
// characters and ? any one character, matches all of name.
func globMatch(pattern, name string) bool {
	p, n := []rune(pattern), []rune(name)
	pi, ni, star, mark := 0, 0, -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ni
			pi++
		case pi < len(p) && (p[pi] == '?' || p[pi] == n[ni]):
			pi++
			ni++
		case star >= 0:
			pi = star + 1
			mark++
			ni = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
