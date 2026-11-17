package diff

import (
	"maps"
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// createTimeOnlyParam reports whether storage parameter k has a namespace
// other than toast (tsdb.hypertable, timescaledb.continuous). PostgreSQL
// keeps no such parameter in reloptions, so one that CREATE accepts was
// taken by an extension when the relation was created. It is passed to
// CREATE TABLE and CREATE MATERIALIZED VIEW, and compared afterwards only
// while the live relation shows it.
func createTimeOnlyParam(k string) bool {
	ns, _, ok := strings.Cut(k, ".")
	return ok && !strings.EqualFold(strings.TrimSpace(ns), "toast")
}

// viewsCreatedAsViews returns desired with each materialized view that is
// declared with a create-time-only storage parameter but exists in live as
// a plain view turned into a view declared without a query. An extension
// made it a view when CREATE MATERIALIZED VIEW ran (a TimescaleDB
// continuous aggregate), so it counts as created, and its comment, column
// comments, grants and owner are managed on the view that exists.
func viewsCreatedAsViews(live *Live, desired *schema.Database) *schema.Database {
	out := desired
	for _, fq := range sortedKeys(desired.Views) {
		vw := desired.Views[fq]
		if vw == nil || !vw.Materialized || !live.Views[fq] || live.MatViews[fq] {
			continue
		}
		createTimeOnly := false
		for k := range vw.With {
			createTimeOnly = createTimeOnly || createTimeOnlyParam(k)
		}
		if !createTimeOnly {
			continue
		}
		if out == desired {
			c := *desired
			c.Views = maps.Clone(desired.Views)
			out = &c
		}
		out.Views[fq] = &schema.View{Schema: vw.Schema, Name: vw.Name, Comment: vw.Comment, ColumnComments: vw.ColumnComments, Grants: vw.Grants, Owner: vw.Owner, DependsOn: vw.DependsOn}
	}
	return out
}
