package diff

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pgy/internal/schema"
)

// IntrospectSettings records in live.UnknownSettings the settings desired
// sets that the server does not recognise: those of the database, of roles
// in the database and on their own, and of functions and procedures.
// Setting one fails with "unrecognized configuration parameter", as
// escape_string_warning does on PostgreSQL 19, which removed it. A name with
// a dot is a placeholder for an extension's or the application's own
// parameter, which PostgreSQL accepts whether or not it knows it yet, and is
// not checked.
func IntrospectSettings(ctx context.Context, pool *pgxpool.Pool, live *Live, desired *schema.Database) error {
	if live == nil || desired == nil || pool == nil {
		return nil
	}
	seen := map[string]bool{}
	names := []string{}
	withoutSettings(desired, live, func(_ string, set, _ map[string]string) (map[string]string, bool) {
		for k := range set {
			if n := settingName(k); n != "" && !strings.Contains(n, ".") && !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
		return set, false
	})
	unknown := map[string]bool{}
	if len(names) > 0 {
		sort.Strings(names)
		batch := &queryBatch{}
		batch.add(`select n from unnest($1::text[]) n where pg_settings_get_flags(n) is null`, func(rows pgx.Rows) error {
			for rows.Next() {
				var n string
				if err := rows.Scan(&n); err != nil {
					return err
				}
				unknown[n] = true
			}
			return nil
		}, names)
		if err := batch.run(ctx, pool); err != nil {
			return err
		}
	}
	live.UnknownSettings = unknown
	return nil
}

// settingName is the name PostgreSQL looks a setting up by.
func settingName(k string) string { return strings.ToLower(strings.TrimSpace(k)) }

// withoutUnknownSettings returns desired without the settings in
// live.UnknownSettings, with a warning for each one left out. One live
// already has keeps its live value without a warning: a split plan's
// earlier step reported it, and the simulated database it plans the next
// step against has it. desired is not modified.
func withoutUnknownSettings(live *Live, desired *schema.Database) (*schema.Database, []Warning) {
	if live == nil || desired == nil || len(live.UnknownSettings) == 0 {
		return desired, nil
	}
	version := "the server"
	if live.ServerVersion > 0 {
		version = fmt.Sprintf("PostgreSQL %d", live.ServerVersion/10000)
	}
	var warnings []Warning
	out := withoutSettings(desired, live, func(where string, set, have map[string]string) (map[string]string, bool) {
		var kept map[string]string
		for _, k := range sortedKeys(set) {
			if !live.UnknownSettings[settingName(k)] {
				continue
			}
			if kept == nil {
				kept = cloneMap(set, nil)
			}
			if v, ok := have[settingName(k)]; ok {
				kept[k] = v
				continue
			}
			warnings = append(warnings, Warning{Message: fmt.Sprintf("%s: %s is not a parameter %s knows, so setting it would fail with \"unrecognized configuration parameter\"; it is left out of the plan", where, k, version)})
			delete(kept, k)
		}
		return kept, kept != nil
	})
	return out, warnings
}

// withoutSettings calls edit on each block of settings desired declares,
// with the block's place as a warning names it and the settings live has
// for it (lower-cased names), and returns desired with the blocks edit
// reports changed replaced by the ones it returns. desired is copied only
// where a block changed.
func withoutSettings(desired *schema.Database, live *Live, edit func(where string, set, have map[string]string) (map[string]string, bool)) *schema.Database {
	c := *desired
	changed := false
	if desired.DatabaseSettings != nil {
		if s, ok := edit("database.set", desired.DatabaseSettings, live.DatabaseConfig); ok {
			c.DatabaseSettings, changed = s, true
		}
	}
	dbRolesCopied := false
	for _, role := range sortedKeys(desired.DatabaseRoleSettings) {
		if s, ok := edit("database.roles."+role, desired.DatabaseRoleSettings[role], live.DatabaseRoleConfig[resolveRole(role, live.CurrentUser)]); ok {
			if !dbRolesCopied {
				c.DatabaseRoleSettings, dbRolesCopied = cloneMap(desired.DatabaseRoleSettings, nil), true
			}
			c.DatabaseRoleSettings[role], changed = s, true
		}
	}
	rolesCopied := false
	for _, name := range sortedKeys(desired.Roles) {
		r := desired.Roles[name]
		if r == nil || r.Set == nil {
			continue
		}
		var have map[string]string
		if lr := live.RoleDefs[name]; lr != nil {
			have = lr.Config
		}
		if s, ok := edit("role "+name+"'s set", r.Set, have); ok {
			if !rolesCopied {
				c.Roles, rolesCopied = cloneMap(desired.Roles, nil), true
			}
			rc := *r
			rc.Set = s
			c.Roles[name], changed = &rc, true
		}
	}
	functionsCopied := false
	for _, key := range sortedKeys(desired.Functions) {
		f := desired.Functions[key]
		if f == nil || f.Set == nil {
			continue
		}
		var have map[string]string
		if lf := live.FunctionDefs[normalizeFunctionSignature(schema.RoutineName(key)+f.ArgsSig)]; lf != nil {
			have = lf.Config
		}
		if s, ok := edit("function "+key+"'s set", f.Set, have); ok {
			if !functionsCopied {
				c.Functions, functionsCopied = cloneMap(desired.Functions, nil), true
			}
			fc := *f
			fc.Set, fc.SetOrder = s, keysIn(f.SetOrder, s)
			c.Functions[key], changed = &fc, true
		}
	}
	proceduresCopied := false
	for _, key := range sortedKeys(desired.Procedures) {
		p := desired.Procedures[key]
		if p == nil || p.Set == nil {
			continue
		}
		var have map[string]string
		if lp := live.ProcedureDefs[normalizeFunctionSignature(schema.RoutineName(key)+p.ArgsSig)]; lp != nil {
			have = lp.Config
		}
		if s, ok := edit("procedure "+key+"'s set", p.Set, have); ok {
			if !proceduresCopied {
				c.Procedures, proceduresCopied = cloneMap(desired.Procedures, nil), true
			}
			pc := *p
			pc.Set, pc.SetOrder = s, keysIn(p.SetOrder, s)
			c.Procedures[key], changed = &pc, true
		}
	}
	if !changed {
		return desired
	}
	return &c
}

// keysIn returns the entries of keys that set has, in their order.
func keysIn(keys []string, set map[string]string) []string {
	if keys == nil {
		return nil
	}
	out := []string{}
	for _, k := range keys {
		if _, ok := set[k]; ok {
			out = append(out, k)
		}
	}
	return out
}
