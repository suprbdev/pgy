package diff

import (
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// eachDeclaredType calls visit for each type the YAML declares for a column,
// a composite type's attribute or a domain's base type that exists in live
// and that the text comparison takes for different from the live type: the
// declared type, the live one, and spell, which gives the live snapshot of
// an edit the declared spelling.
func eachDeclaredType(live *Live, desired *schema.Database, visit func(declared, have string, spell func(*liveEdit))) {
	for _, fq := range sortedKeys(desired.Tables) {
		dt, lt := desired.Tables[fq], live.Tables[fq]
		if dt == nil || lt == nil {
			continue
		}
		for _, cn := range sortedKeys(dt.Columns) {
			c, lc := dt.Columns[cn], lt.Columns[cn]
			if c == nil || lc == nil || c.Type == "" || lc.Type == "" || isSerialType(c.Type) || normalizeColumnType(c.Type) == normalizeColumnType(lc.Type) {
				continue
			}
			visit(c.Type, lc.Type, func(e *liveEdit) {
				if lc := e.table(fq).Columns[cn]; lc != nil {
					lc.Type = c.Type
				}
			})
		}
	}
	for _, fq := range sortedKeys(desired.Types) {
		td, attrs := desired.Types[fq], live.CompositeAttributes[fq]
		if td == nil || attrs == nil {
			continue
		}
		for _, n := range sortedKeys(td.Attributes) {
			t, have := td.Attributes[n], attrs[n]
			if t == "" || have == "" || normalizeColumnType(t) == normalizeColumnType(have) {
				continue
			}
			visit(t, have, func(e *liveEdit) { e.compositeAttributes(fq)[n] = t })
		}
	}
	for _, fq := range sortedKeys(desired.Domains) {
		dm, ld := desired.Domains[fq], live.DomainDefs[fq]
		if dm == nil || ld == nil || !domainTypeChanged(dm.Type, ld.Type) {
			continue
		}
		visit(dm.Type, ld.Type, func(e *liveEdit) { e.domain(fq).Type = dm.Type })
	}
}

// typesToProbe returns the declared types eachDeclaredType visits, sorted
// and without duplicates.
func typesToProbe(live *Live, desired *schema.Database) []string {
	seen := map[string]bool{}
	eachDeclaredType(live, desired, func(declared, _ string, _ func(*liveEdit)) {
		seen[strings.TrimSpace(declared)] = true
	})
	return sortedKeys(seen)
}

// applyTypeProbes gives the snapshot of e the declared spelling of each
// type eachDeclaredType visits that PostgreSQL prints exactly as the live
// one (see Live.TypeProbes), so every comparison of the plan finds the two
// equal.
func applyTypeProbes(e *liveEdit, live *Live, desired *schema.Database) {
	if len(live.TypeProbes) == 0 {
		return
	}
	eachDeclaredType(live, desired, func(declared, have string, spell func(*liveEdit)) {
		if shown, ok := live.TypeProbes[strings.TrimSpace(declared)]; ok && shown == have {
			spell(e)
		}
	})
}

// probeType is what pg_temp.pgy_probe_types made of one declared type: the
// way format_type prints it, or why it couldn't resolve it, with the
// error's SQLSTATE.
type probeType struct {
	Type  string `json:"type"`
	Shown string `json:"shown"`
	Err   string `json:"err"`
	Code  string `json:"code"`
}

// typeResults returns the types of reply that PostgreSQL resolved, by the
// declared type.
func typeResults(reply *probeReply) map[string]string {
	out := make(map[string]string, len(reply.Types))
	for _, t := range reply.Types {
		if t.Err == "" && t.Shown != "" {
			out[t.Type] = t.Shown
		}
	}
	return out
}

// typeProbeFuncs creates the functions that canonicalise declared types,
// one statement each.
//
// pgy_probe_types resolves each type on the session's own search_path,
// which is how the migration will resolve it, with its modifiers: with
// to_regtype and to_regtypemod on PostgreSQL 17 and later, and as a column
// of a temporary table before. pgy_probe_type prints the result with
// format_type on the pinned search_path, as Introspect prints the live
// types (see pinSearchPathQ). Each type runs in a subtransaction, so one
// PostgreSQL rejects fails alone.
var typeProbeFuncs = []string{`
create function pg_temp.pgy_probe_type(typ oid, tmod int) returns text language sql stable set search_path = public as $f$
    select format_type(typ, tmod)
$f$`, `
create function pg_temp.pgy_probe_types(types jsonb) returns jsonb language plpgsql as $f$
declare
    t text;
    i int := 0;
    typ oid;
    tmod int;
    modern boolean := current_setting('server_version_num')::int >= 170000;
    resolved jsonb[] := '{}';
begin
    if not modern and jsonb_array_length(types) > 0 then
        create temporary table pgy_probe_types ();
    end if;
    for t in select * from jsonb_array_elements_text(types) loop
        i := i + 1;
        begin
            if modern then
                execute 'select to_regtype($1), to_regtypemod($1)' into typ, tmod using t;
                if typ is null then
                    raise exception 'type "%" does not exist', t using errcode = 'undefined_object';
                end if;
            else
                execute format('alter table pg_temp.pgy_probe_types add column %I %s', 'c' || i, t);
                select a.atttypid, a.atttypmod into typ, tmod from pg_attribute a
                where a.attrelid = 'pg_temp.pgy_probe_types'::regclass and a.attname = 'c' || i;
            end if;
            resolved := resolved || jsonb_build_object('type', t, 'shown', pg_temp.pgy_probe_type(typ, tmod));
        exception when others then
            resolved := resolved || jsonb_build_object('type', t, 'err', sqlerrm, 'code', sqlstate);
        end;
    end loop;
    return to_jsonb(resolved);
end
$f$`}
