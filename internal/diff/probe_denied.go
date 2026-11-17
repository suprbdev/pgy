package diff

import (
	"slices"
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// insufficientPrivilege is the SQLSTATE of PostgreSQL's "permission denied"
// and "must be owner" errors.
const insufficientPrivilege = "42501"

// DeniedError is what ProbeExpressions and ProbeViews return when the role
// lacks a privilege that comparing some declared definitions through the
// server needs, such as USAGE on a schema they name or SELECT on the table
// the expression probe copies. The comparisons that could run have their
// results all the same. The others are compared as text: an expression or
// a type PostgreSQL stores in another spelling may be reported as changed,
// and a view or materialized view is left as it is, never replaced.
type DeniedError struct {
	// Objects lists what couldn't be compared, sorted: tables, views,
	// materialized views and domains as "schema.name", functions and
	// procedures by name, and types as the YAML writes them. A view whose
	// query matches the live one as text is not listed.
	Objects []string
	// Reason is PostgreSQL's message for the first of them.
	Reason string
}

func (e *DeniedError) Error() string {
	return strings.Join(e.Objects, ", ") + ": " + e.Reason
}

// denials collects the objects of a DeniedError with PostgreSQL's message
// for each.
type denials map[string]string

func (d denials) add(object, reason string) {
	if _, ok := d[object]; !ok {
		d[object] = reason
	}
}

// err returns the DeniedError for d, or nil when d is empty.
func (d denials) err() error {
	if len(d) == 0 {
		return nil
	}
	objects := sortedKeys(d)
	return &DeniedError{Objects: objects, Reason: d[objects[0]]}
}

// deniedExprProbes collects the objects of the probes in probes and the
// types in reply that PostgreSQL refused for a missing privilege.
func deniedExprProbes(probes map[ExprKey]*ExprProbe, reply *probeReply) denials {
	out := denials{}
	for key, p := range probes {
		if p == nil || !p.Denied {
			continue
		}
		object := key.Object
		if key.Kind == ExprFunctionArgs || key.Kind == ExprProcedureArgs {
			object = schema.RoutineName(object)
		}
		out.add(object, p.Err)
	}
	for _, t := range reply.Types {
		if t.Code == insufficientPrivilege {
			out.add(t.Type, t.Err)
		}
	}
	return out
}

// sameViewText reports whether a declared view query and the live
// definition pg_get_viewdef printed are the same text, apart from
// whitespace and a trailing semicolon: what pgy pull writes.
func sameViewText(declared, live string) bool {
	norm := func(s string) string {
		return strings.Join(strings.Fields(strings.TrimRight(strings.TrimSpace(s), "; \t\r\n")), " ")
	}
	return norm(declared) == norm(live)
}

// JoinDenied returns one DeniedError with the objects of errs, sorted and
// without duplicates, and the reason of the first, or nil when errs holds
// none.
func JoinDenied(errs ...*DeniedError) *DeniedError {
	var out *DeniedError
	for _, e := range errs {
		if e == nil || len(e.Objects) == 0 {
			continue
		}
		if out == nil {
			out = &DeniedError{Reason: e.Reason}
		}
		out.Objects = append(out.Objects, e.Objects...)
	}
	if out != nil {
		slices.Sort(out.Objects)
		out.Objects = slices.Compact(out.Objects)
	}
	return out
}
