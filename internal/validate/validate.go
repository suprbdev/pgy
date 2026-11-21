// Package validate checks pgy schema files without a database: each file
// against the editor JSON Schema, then the merged schema for references that
// cannot work (missing columns, unresolvable dependsOn, dependency cycles)
// and declarations that are probably mistakes.
package validate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/suprbdev/pgy/internal/schema"
	"github.com/suprbdev/pgy/schemas"
)

// Severity of an Issue. Errors make validation fail; warnings do not.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Issue is one validation finding. File is empty for findings about the
// merged schema; Object names the object concerned ("table app.users").
type Issue struct {
	Severity Severity `json:"severity"`
	File     string   `json:"file,omitempty"`
	Object   string   `json:"object,omitempty"`
	Message  string   `json:"message"`
}

func (i Issue) String() string {
	prefix := string(i.Severity)
	if i.File != "" {
		prefix += " " + i.File
	}
	if i.Object != "" {
		return fmt.Sprintf("%s: %s: %s", prefix, i.Object, i.Message)
	}
	return fmt.Sprintf("%s: %s", prefix, i.Message)
}

// HasErrors reports whether any issue is an error.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == Error {
			return true
		}
	}
	return false
}

// Files validates the given schema files and returns every finding, errors
// first, in a stable order.
func Files(paths []string) []Issue {
	return FilesWith(paths, nil)
}

// FilesWith is Files for a project whose config has an external: block:
// references to what ext lists are not reported (see Database).
func FilesWith(paths []string, ext *schema.External) []Issue {
	issues := []Issue{}
	compiled, err := compileSchema()
	if err != nil {
		return append(issues, Issue{Severity: Error, Message: "compiling JSON Schema: " + err.Error()})
	}
	var dbs []*schema.Database
	type owner struct {
		file string
		obj  any
	}
	owners := map[string][]owner{}
	extensions := map[string][]owner{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			issues = append(issues, Issue{Severity: Error, File: p, Message: err.Error()})
			continue
		}
		type loaded struct {
			d   *schema.Database
			err error
		}
		load := make(chan loaded, 1)
		go func() {
			d, err := schema.Load(p)
			load <- loaded{d, err}
		}()
		var doc any
		if _, err := schema.DecodeYAML(b, &doc); err != nil {
			<-load
			issues = append(issues, Issue{Severity: Error, File: p, Message: "invalid YAML: " + err.Error()})
			continue
		}
		if v, err := toJSON(doc); err != nil {
			issues = append(issues, Issue{Severity: Error, File: p, Message: "cannot be checked against the JSON Schema: " + err.Error()})
		} else if verr := compiled.Validate(v); verr != nil {
			issues = append(issues, schemaIssues(p, verr)...)
		}
		issues = append(issues, dottedSchemaIssues(p, doc)...)
		l := <-load
		d, err := l.d, l.err
		if err != nil {
			issues = append(issues, Issue{Severity: Error, File: p, Message: err.Error()})
			continue
		}
		dbs = append(dbs, d)
		for k, obj := range singleOwnerObjects(d) {
			owners[k] = append(owners[k], owner{p, obj})
		}
		for _, ext := range d.Extensions {
			if ext != nil {
				extensions["extension "+ext.Name] = append(extensions["extension "+ext.Name], owner{p, ext})
			}
		}
	}
	for _, group := range []struct {
		decls map[string][]owner
		rule  string
	}{{owners, "the last file wins"}, {extensions, "the first declaration wins"}} {
		for _, k := range sortedKeys(group.decls) {
			decls := group.decls[k]
			differ := false
			for _, o := range decls[1:] {
				if !reflect.DeepEqual(o.obj, decls[0].obj) {
					differ = true
				}
			}
			if differ {
				files := make([]string, len(decls))
				for i, o := range decls {
					files[i] = o.file
				}
				issues = append(issues, Issue{Severity: Warning, Object: k, Message: fmt.Sprintf("declared differently in %s; %s", strings.Join(files, ", "), group.rule)})
			}
		}
	}
	merged := schema.Merge(dbs)
	merged.External = ext
	issues = append(issues, Database(merged)...)
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Severity == Error && issues[j].Severity != Error })
	return issues
}

// Database checks a merged schema for broken references and likely mistakes.
// References to external objects (d.External) are not reported: tables,
// views and sequences in external schemas or matching external relations,
// and routines and types in external schemas.
func Database(d *schema.Database) []Issue {
	issues := []Issue{}
	add := func(sev Severity, obj, format string, args ...any) {
		issues = append(issues, Issue{Severity: sev, Object: obj, Message: fmt.Sprintf(format, args...)})
	}
	if _, err := schema.TopologicalSort(d); err != nil {
		add(Error, "", "%v", err)
	}
	checkRenames(d, add)
	checkWrappersAndLanguages(d, add)
	checkOwnerGrants(d, add)
	checkNameClashes(d, add)
	for _, fq := range sortedKeys(d.Views) {
		if v := d.Views[fq]; v != nil && !v.Materialized && v.Tablespace != "" {
			add(Error, "view "+fq, "only materialized views have storage, so a plain view can't have a tablespace")
		}
		if v := d.Views[fq]; v != nil && !v.Materialized && v.AccessMethod != "" {
			add(Error, "view "+fq, "only materialized views have storage, so a plain view can't have an access method")
		}
	}
	checkNameLengths(d, add)

	for _, fq := range sortedKeys(d.Tables) {
		t := d.Tables[fq]
		if t == nil {
			continue
		}
		obj := "table " + fq
		checkDeps(d, obj, t.DependsOn, add)
		if keys := t.DefinitionKeys(); len(keys) > 0 && !t.ColumnsSet && len(t.Columns) == 0 && t.Of == "" && t.PartitionOf == "" && t.Inherits == nil && (t.Foreign == nil || t.Foreign.Server == "") && (!d.External.InSchema(fq) || !slices.Equal(keys, []string{"triggers"})) {
			add(Warning, obj, "has %s but no columns, so it is created as a table without columns; without those keys a declaration without columns, of, partitionOf or inherits manages only a table that already exists, such as one an extension owns", strings.Join(keys, ", "))
		}
		hasCols := t.PartitionOf == "" && (len(t.Columns) > 0 || d.Types[t.Of] != nil)
		colExists := func(c string) bool { return tableHasColumn(d, fq, c, 0) }
		need := func(what string, cols []string) {
			if !hasCols {
				return
			}
			for _, c := range cols {
				c = strings.TrimSpace(c)
				if base := columnName(c); base != "" && !colExists(base) {
					add(Error, obj, "%s references unknown column %q", what, base)
				}
			}
		}
		unique := func(what string, cols []string) {
			seen := map[string]bool{}
			for _, c := range cols {
				if base := columnName(strings.TrimSpace(c)); base != "" {
					if seen[base] {
						add(Error, obj, "%s lists column %q twice", what, base)
					}
					seen[base] = true
				}
			}
		}
		need("primary key", t.PrimaryKey)
		unique("primary key", t.PrimaryKey)
		need("primary key include", t.PrimaryKeyInclude)
		for _, ix := range t.Indexes {
			if ix == nil {
				continue
			}
			if len(ix.Columns) == 0 {
				add(Error, obj, "index %s has no columns", ix.Name)
			}
			need("index "+ix.Name, ix.Columns)
			need("index "+ix.Name+" include", ix.Include)
			for _, k := range sortedKeys(ix.StatisticsTargets) {
				switch {
				case !slices.Contains(ix.Columns, k):
					add(Error, obj, "index %s statisticsTargets: %q is not one of its columns entries", ix.Name, k)
				case columnName(k) != "":
					add(Error, obj, "index %s statisticsTargets: %q is a plain column; PostgreSQL only sets index statistics on expressions, so set statisticsTarget on the column instead", ix.Name, k)
				}
			}
		}
		for _, ct := range t.Constraints {
			if ct == nil {
				continue
			}
			switch strings.ToLower(ct.Type) {
			case "unique":
				need("constraint "+ct.Name, ct.Columns)
				unique("constraint "+ct.Name, ct.Columns)
				need("constraint "+ct.Name+" include", ct.Include)
			case "check", "exclude":
				if strings.TrimSpace(ct.Expression) == "" {
					add(Error, obj, "constraint %s has no expression", ct.Name)
				}
			default:
				add(Error, obj, "constraint %s has unknown type %q", ct.Name, ct.Type)
			}
		}
		for _, p := range t.Policies {
			if p == nil {
				continue
			}
			switch cmd := strings.ToLower(strings.TrimSpace(p.For)); {
			case cmd == "insert" && strings.TrimSpace(p.Using) != "":
				add(Error, obj, "policy %s: an insert policy can only have withCheck, not using", p.Name)
			case (cmd == "select" || cmd == "delete") && strings.TrimSpace(p.WithCheck) != "":
				add(Error, obj, "policy %s: a %s policy can only have using, not withCheck", p.Name, cmd)
			}
		}
		for _, cn := range sortedKeys(t.Columns) {
			c := t.Columns[cn]
			if c == nil || c.Identity == "" || c.Type == "" {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(c.Type)) {
			case "smallint", "int2", "integer", "int", "int4", "bigint", "int8":
			default:
				add(Error, obj, "column %s: identity columns must be smallint, integer or bigint, not %s", cn, c.Type)
			}
		}
		if len(t.Policies) > 0 && (t.RowLevelSecurity == nil && !d.External.InSchema(fq) || t.RowLevelSecurity != nil && !*t.RowLevelSecurity) && t.PartitionOf == "" {
			add(Warning, obj, "declares policies but not rowLevelSecurity: true; PostgreSQL ignores a table's policies until row-level security is enabled")
		}
		if t.PartitionBy != nil {
			checkPartitionKeys(t, obj, add)
		}
		if t.Foreign != nil && t.Tablespace != "" {
			add(Error, obj, "a foreign table has no storage, so it can't have a tablespace")
		}
		if t.Foreign != nil && t.AccessMethod != "" {
			add(Error, obj, "a foreign table has no storage, so it can't have an access method")
		}
		checkValidateModes(t, obj, add)
		for _, fk := range t.ForeignKeys {
			if fk == nil {
				continue
			}
			need("foreign key "+fk.Name, fk.Columns)
			for _, action := range []struct{ on, spec string }{{"delete", fk.OnDelete}, {"update", fk.OnUpdate}} {
				for _, c := range setNullColumns(action.spec, fk.Columns) {
					if col := t.Columns[c]; col != nil && !col.Nullable && col.Type != "" {
						add(Warning, obj, "foreign key %s sets %s to null on %s, but %s is not nullable, so those %ss will fail", fk.Name, c, action.on, c, action.on)
					}
				}
			}
			if len(fk.Columns) != len(fk.RefColumns) {
				add(Error, obj, "foreign key %s has %d columns but references %d", fk.Name, len(fk.Columns), len(fk.RefColumns))
			}
			ref := d.Tables[qualifyPublic(fk.RefTable)]
			if ref == nil {
				if !externalRelation(d, fk.RefTable) {
					add(Warning, obj, "foreign key %s references %s, which no schema file declares", fk.Name, fk.RefTable)
				}
				continue
			}
			if ref.PartitionOf == "" && len(ref.Columns) > 0 {
				known := true
				for _, c := range fk.RefColumns {
					if base := columnName(c); base != "" && !tableHasColumn(d, qualifyPublic(fk.RefTable), base, 0) {
						add(Error, obj, "foreign key %s references unknown column %s.%s", fk.Name, fk.RefTable, base)
						known = false
					}
				}
				if known && len(fk.RefColumns) > 0 && !hasUniqueKey(ref, fk.RefColumns) {
					add(Error, obj, "foreign key %s references %s (%s), which has no primary key or unique constraint on exactly those columns", fk.Name, fk.RefTable, strings.Join(fk.RefColumns, ", "))
				}
				for i, c := range fk.Columns {
					if i >= len(fk.RefColumns) {
						break
					}
					lc, rc := t.Columns[columnName(c)], ref.Columns[columnName(fk.RefColumns[i])]
					if lc == nil || rc == nil {
						continue
					}
					if a, b := typeCategory(lc.Type), typeCategory(rc.Type); a != "" && b != "" && a != b {
						add(Error, obj, "foreign key %s: column %s is %s but references %s.%s, which is %s; PostgreSQL can't compare them", fk.Name, columnName(c), lc.Type, fk.RefTable, columnName(fk.RefColumns[i]), rc.Type)
					}
				}
			}
		}
		hasGenerated := false
		for _, c := range t.Columns {
			if c != nil && c.Generated != "" {
				hasGenerated = true
			}
		}
		for _, tr := range t.Triggers {
			if tr == nil {
				continue
			}
			checkTrigger(d, obj, tr, add)
			if hasGenerated && strings.EqualFold(tr.Timing, "before") && strings.Contains(strings.ToLower(strings.ReplaceAll(tr.When, " ", "")), "new.*") {
				add(Error, obj, "trigger %s: a BEFORE trigger's WHEN cannot reference new.* on a table with generated columns", tr.Name)
			}
		}
		if t.Of != "" && (t.PartitionOf != "" || len(t.Inherits) > 0) {
			add(Error, obj, "of cannot be combined with partitionOf or inherits")
		}
		ofType := d.Types[t.Of]
		for cn, c := range t.Columns {
			if c == nil {
				continue
			}
			if c.NotNullName != "" {
				switch {
				case t.PartitionOf != "" || t.Of != "" || strings.TrimSpace(c.Type) == "":
					add(Warning, obj, "column %s: notNullName is ignored on a column of a partition, a typed table or an inheritance child; it names the constraint of a column the table declares with a type", cn)
				case c.Nullable:
					add(Warning, obj, "column %s sets notNullName but is nullable, so there is no NOT NULL constraint to name", cn)
				}
			}
			if t.Of != "" {
				switch {
				case strings.TrimSpace(c.Type) != "":
					add(Error, obj, "column %s: a typed table's columns get their types from %s, so declare it without a type", cn, t.Of)
				case ofType != nil && ofType.Kind == "composite" && ofType.Attributes[cn] == "":
					add(Error, obj, "column %s is not an attribute of %s", cn, t.Of)
				}
				continue
			}
			if strings.TrimSpace(c.Type) == "" {
				if !inheritsColumn(d, t, cn) {
					add(Error, obj, "column %s has no type", cn)
				}
			} else if strings.Count(c.Type, "(") != strings.Count(c.Type, ")") {
				add(Error, obj, "column %s has type %q with unbalanced parentheses; in a flow mapping a comma ends the value, so quote it: {type: \"numeric(12,2)\"}", cn, c.Type)
			}
			if c.Generated != "" && (c.Default != "" || c.Identity != "") {
				add(Warning, obj, "column %s is generated; its default/identity is ignored", cn)
			}
			if c.Identity != "" && c.Default != "" {
				add(Warning, obj, "column %s is an identity column; its default is ignored", cn)
			}
			if c.Virtual && c.Generated == "" {
				add(Warning, obj, "column %s sets virtual without generated", cn)
			}
		}
		for _, cn := range sortedKeys(t.Columns) {
			c := t.Columns[cn]
			if c == nil || c.Generated == "" {
				continue
			}
			for _, ref := range exprIdentifiers(c.Generated) {
				if g := t.Columns[ref]; g != nil && g.Generated != "" {
					add(Error, obj, "generated column %s uses generated column %s; PostgreSQL doesn't allow that, so repeat %s's expression instead", cn, ref, ref)
				}
			}
		}
		if idx, ok := strings.CutPrefix(t.ReplicaIdentity, "index "); ok {
			found := false
			for _, ix := range t.Indexes {
				if ix != nil && ix.Name == idx {
					found = true
					if !ix.Unique || ix.Where != "" {
						add(Error, obj, "replica identity index %s must be unique and not partial", idx)
					}
				}
			}
			if !found {
				add(Warning, obj, "replica identity index %s is not declared on this table", idx)
			}
		}
		if idx := t.ClusterOn; idx != "" && !strings.EqualFold(idx, "none") {
			found := false
			for _, ix := range t.Indexes {
				if ix != nil && ix.Name == idx {
					found = true
					if ix.Where != "" {
						add(Error, obj, "clusterOn index %s is partial; PostgreSQL cannot cluster on it", idx)
					}
				}
			}
			if t.PartitionBy != nil {
				add(Error, obj, "clusterOn is not supported on partitioned tables (cluster each partition instead)")
			} else if !found {
				add(Warning, obj, "clusterOn index %s is not declared on this table", idx)
			}
		}
		for _, p := range t.Inherits {
			if d.Tables[p] == nil && !externalRelation(d, p) {
				add(Warning, obj, "inherits %s, which no schema file declares", p)
			}
		}
		if t.Data != nil {
			checkData(d, fq, t, add)
		}
		if t.PartitionBy != nil {
			for _, fk := range t.ForeignKeys {
				if fk != nil && fk.NotValid {
					add(Warning, obj, "foreign key %s: PostgreSQL before 18 cannot add a NOT VALID foreign key to a partitioned table", fk.Name)
				}
			}
		}
		if len(t.Inherits) > 0 && (t.PartitionOf != "" || t.PartitionBy != nil) {
			add(Error, obj, "inherits cannot be combined with partitionOf or partitionBy")
		}
		if t.PartitionOf != "" && d.Tables[t.PartitionOf] == nil && !externalRelation(d, t.PartitionOf) {
			add(Warning, obj, "partition of %s, which no schema file declares", t.PartitionOf)
		}
		if t.PartitionOf != "" && (t.Partition == nil || (!t.Partition.Default && len(t.Partition.From) == 0 && len(t.Partition.In) == 0 && t.Partition.Modulus < 0)) {
			add(Error, obj, "partition needs forValues or default: true")
		}
	}

	for _, k := range sortedKeys(d.Views) {
		if vw := d.Views[k]; vw != nil {
			kind := "view "
			if vw.Materialized {
				kind = "materialized view "
			}
			checkDeps(d, kind+k, vw.DependsOn, add)
			if !vw.Materialized && vw.With != nil {
				add(Error, kind+k, "with is for materialized views; use securityInvoker, securityBarrier and checkOption on views")
			}
			if !vw.Materialized && len(vw.Indexes) > 0 {
				add(Error, kind+k, "indexes are for materialized views; PostgreSQL cannot index a view")
			}
			if !vw.Materialized && vw.WithData != nil {
				add(Warning, kind+k, "withData is for materialized views and is ignored")
			}
			if vw.Materialized {
				for _, o := range []struct {
					key    string
					set    bool
					object bool
				}{
					{"triggers", len(vw.Triggers) > 0, true},
					{"rules", len(vw.Rules) > 0, true},
					{"securityInvoker", vw.SecurityInvoker != nil, false},
					{"securityBarrier", vw.SecurityBarrier != nil, false},
					{"checkOption", vw.CheckOption != "", false},
					{"replace", vw.Replace, false},
					{"columnDefaults", len(vw.ColumnDefaults) > 0, false},
				} {
					switch {
					case o.set && o.object:
						add(Error, kind+k, "%s are for views; PostgreSQL doesn't support them on materialized views", o.key)
					case o.set:
						add(Warning, kind+k, "%s is for views and is ignored on materialized views", o.key)
					}
				}
			}
			for _, tr := range vw.Triggers {
				if tr != nil {
					checkTrigger(d, kind+k, tr, add)
				}
			}
		}
	}
	for _, k := range sortedKeys(d.Functions) {
		if f := d.Functions[k]; f != nil {
			checkDeps(d, "function "+k, f.DependsOn, add)
			if f.HasBody() && (f.Returns == "" || f.Language == "") {
				add(Error, "function "+k, "needs returns and language")
			}
			if !f.HasBody() && (f.Returns != "" || f.Language != "") {
				add(Error, "function "+k, "needs body; without body, returns and language it manages a function that already exists")
			}
			if r := strings.ToLower(strings.TrimSpace(f.Returns)); f.Rows != nil && r != "" && !strings.HasPrefix(r, "setof ") && !strings.HasPrefix(r, "table") {
				add(Error, "function "+k, "rows applies only to set-returning functions (returns setof or table)")
			}
		}
	}
	for _, k := range sortedKeys(d.Procedures) {
		if p := d.Procedures[k]; p != nil {
			checkDeps(d, "procedure "+k, p.DependsOn, add)
			if p.HasBody() && p.Language == "" {
				add(Error, "procedure "+k, "needs language")
			}
			if !p.HasBody() && p.Language != "" {
				add(Error, "procedure "+k, "needs body; without body and language it manages a procedure that already exists")
			}
		}
	}
	for _, k := range sortedKeys(d.Types) {
		if td := d.Types[k]; td != nil {
			checkDeps(d, "type "+k, td.DependsOn, add)
			switch td.Kind {
			case "enum":
				if len(td.Labels) == 0 {
					add(Error, "type "+k, "enum has no labels")
				}
			case "composite":
				if len(td.Attributes) == 0 {
					add(Error, "type "+k, "composite type has no attributes")
				}
			case "range":
				if td.Range == nil || td.Range.Subtype == "" {
					add(Error, "type "+k, "range type needs subtype")
				}
			case "":
				if len(td.Labels) > 0 || len(td.Attributes) > 0 {
					add(Error, "type "+k, "needs type: enum, composite or range")
				}
			default:
				add(Error, "type "+k, "unknown type kind %q", td.Kind)
			}
		}
	}
	for _, k := range sortedKeys(d.Domains) {
		if dm := d.Domains[k]; dm != nil {
			checkDeps(d, "domain "+k, dm.DependsOn, add)
			if dm.Type == "" {
				add(Error, "domain "+k, "has no type")
			}
			if dm.Check != "" && dm.Checks != nil {
				name := dm.ConstraintName
				if name == "" {
					name = k[strings.IndexByte(k, '.')+1:] + "_check"
				}
				if _, dup := dm.Checks[name]; dup {
					add(Error, "domain "+k, "check %s is declared by both check and checks", name)
				}
			}
		}
	}
	columnSeqs := d.ColumnSequences()
	for _, k := range sortedKeys(d.Sequences) {
		if sq := d.Sequences[k]; sq != nil {
			checkDeps(d, "sequence "+k, sq.DependsOn, add)
			if col, ok := columnSeqs[k]; ok && sq.Owner != "" {
				add(Error, "sequence "+k, "is the sequence of column %s of table %s, which always has the owner of its table; remove owner", col.Column, col.Table)
			}
			if s, table, column, ok := schema.OwnedByParts(k, sq.OwnedBy); ok {
				if t := d.Tables[s+"."+table]; t != nil && len(t.Columns) > 0 && t.Columns[column] == nil {
					add(Error, "sequence "+k, "owned by unknown column %s", sq.OwnedBy)
				}
			}
		}
	}
	for _, name := range sortedKeys(d.EventTriggers) {
		if et := d.EventTriggers[name]; et != nil {
			fn := strings.TrimSuffix(strings.TrimSpace(et.Function), "()")
			keys := schema.ResolveDependencyKeys("function "+fn, d)
			if len(keys) == 0 {
				if !externalDependency(d, "function "+fn) {
					add(Warning, "event trigger "+name, "function %s is not declared in any schema file", et.Function)
				}
			} else if r := noArgReturns(d, keys); r != "" && r != "event_trigger" {
				add(Error, "event trigger "+name, "function %s returns %s; event trigger functions must return event_trigger", et.Function, r)
			}
		}
	}
	for i, dp := range d.DefaultPrivileges {
		if dp != nil && len(dp.Schemas) > 0 && dp.Grants["schemas"] != nil {
			add(Error, fmt.Sprintf("defaultPrivileges[%d]", i), "schemas defaults cannot be limited with in")
		}
	}
	for _, name := range sortedKeys(d.Publications) {
		p := d.Publications[name]
		if p == nil {
			continue
		}
		if p.AllTables && (len(p.Tables) > 0 || len(p.Schemas) > 0) {
			add(Error, "publication "+name, "allTables cannot be combined with tables or schemas")
		}
		listed := map[string]bool{}
		for _, pt := range p.Tables {
			listed[pt.Table] = true
		}
		for _, pt := range p.Tables {
			if t := d.Tables[pt.Table]; t != nil {
				for _, parent := range t.Inherits {
					if listed[qualifyPublic(parent)] {
						add(Warning, "publication "+name, "table %s is already published through its parent %s; list only the parent", pt.Table, parent)
					}
				}
			}
			if len(pt.Columns) > 0 && len(p.Schemas) > 0 {
				add(Error, "publication "+name, "column lists cannot be combined with schemas (table %s)", pt.Table)
			}
			if t := d.Tables[pt.Table]; t != nil && len(t.Columns) > 0 {
				for _, c := range pt.Columns {
					if !tableHasColumn(d, pt.Table, c, 0) {
						add(Error, "publication "+name, "table %s has no column %q", pt.Table, c)
					}
				}
			}
		}
	}
	return issues
}

var builtinTriggerFunctions = map[string]bool{
	"suppress_redundant_updates_trigger": true,
	"tsvector_update_trigger":            true,
	"tsvector_update_trigger_column":     true,
	"unique_key_recheck":                 true,
}

// undeclarableFunction reports whether a function no schema file declares
// may exist all the same: a PostgreSQL built-in, or one a declared extension
// could provide. An extension's functions live in its schema, which an
// unqualified name can reach through the search_path; an extension declared
// without a schema usually lands in public.
func undeclarableFunction(d *schema.Database, name string) bool {
	name = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), `"`, ""))
	sch, fn, qualified := strings.Cut(name, ".")
	if !qualified {
		return builtinTriggerFunctions[name] || len(d.Extensions) > 0
	}
	if sch == "pg_catalog" {
		return builtinTriggerFunctions[fn]
	}
	for _, ext := range d.Extensions {
		if ext == nil {
			continue
		}
		if s := strings.ToLower(strings.Trim(ext.Schema, `"`)); s == sch || (s == "" && sch == "public") {
			return true
		}
	}
	return false
}

func checkTrigger(d *schema.Database, obj string, tr *schema.Trigger, add func(Severity, string, string, ...any)) {
	proc := strings.TrimSpace(tr.Procedure)
	if proc == "" {
		add(Error, obj, "trigger %s has no procedure", tr.Name)
		return
	}
	if i := strings.IndexByte(proc, '('); i >= 0 {
		proc = proc[:i]
	}
	keys := schema.ResolveDependencyKeys("function "+proc, d)
	if len(keys) == 0 {
		if !undeclarableFunction(d, proc) && !externalDependency(d, "function "+proc) {
			add(Warning, obj, "trigger %s calls %s, which no schema file declares", tr.Name, proc)
		}
	} else if r := noArgReturns(d, keys); r != "" && r != "trigger" {
		add(Error, obj, "trigger %s calls %s, which returns %s; trigger functions must return trigger", tr.Name, proc, r)
	}
	timing := strings.ToLower(strings.Join(strings.Fields(tr.Timing), " "))
	if timing == "instead of" && !strings.HasPrefix(obj, "view ") {
		add(Error, obj, "trigger %s: instead of triggers are only allowed on views", tr.Name)
	}
	row := tr.Constraint || timing == "instead of" || strings.EqualFold(strings.TrimSpace(tr.Level), "row")
	events := map[string]bool{}
	columnList := false
	for _, e := range tr.Events {
		e = strings.ToLower(strings.TrimSpace(e))
		if strings.HasPrefix(e, "update of ") {
			columnList = true
			e = "update"
		}
		events[e] = true
	}
	if !row && tr.When != "" {
		for _, id := range exprIdentifiers(tr.When) {
			if id == "new" || id == "old" {
				add(Error, obj, "trigger %s: a statement-level trigger's when can't refer to new or old; set level: row", tr.Name)
				break
			}
		}
	}
	if row && events["truncate"] {
		add(Error, obj, "trigger %s: truncate triggers must be statement-level", tr.Name)
	}
	if tr.OldTable == "" && tr.NewTable == "" {
		return
	}
	switch {
	case timing != "after" || tr.Constraint:
		add(Error, obj, "trigger %s: transition tables (oldTable, newTable) need an after trigger", tr.Name)
	case len(events) > 1:
		add(Error, obj, "trigger %s: transition tables can't be used with more than one event", tr.Name)
	case columnList:
		add(Error, obj, "trigger %s: transition tables can't be used with update of a column list", tr.Name)
	case tr.OldTable != "" && !events["update"] && !events["delete"]:
		add(Error, obj, "trigger %s: oldTable needs an update or delete trigger", tr.Name)
	case tr.NewTable != "" && !events["update"] && !events["insert"]:
		add(Error, obj, "trigger %s: newTable needs an insert or update trigger", tr.Name)
	}
}

// checkDeps reports dependsOn entries that resolve to no declared object.
// schema and extension entries are informational and never resolve.
func checkDeps(d *schema.Database, obj string, deps []string, add func(Severity, string, string, ...any)) {
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if dep == "" || strings.HasPrefix(dep, "schema ") || strings.HasPrefix(dep, "extension ") || externalDependency(d, dep) {
			continue
		}
		if len(schema.ResolveDependencyKeys(dep, d)) == 0 {
			add(Error, obj, "dependsOn %q matches nothing declared", dep)
		}
	}
}

// externalRelation reports whether the relation a reference names (schema
// qualified or in public, quoted or not) is external.
func externalRelation(d *schema.Database, ref string) bool {
	if d.External == nil {
		return false
	}
	parts := referenceParts(ref)
	if len(parts) == 1 {
		parts = []string{"public", parts[0]}
	}
	return len(parts) == 2 && d.External.HasRelation(parts[0]+"."+parts[1])
}

// referenceParts splits a reference to an object, schema.name or a name
// alone, either part quoted or not, into its names.
func referenceParts(ref string) []string {
	if parts, ok := schema.SplitName(ref); ok {
		return parts
	}
	return strings.SplitN(strings.TrimSpace(ref), ".", 2)
}

// externalDependency reports whether a dependsOn entry ("table x", "function
// x(args)", ...) names an external object: a relation externalRelation
// accepts, or another object in an external schema.
func externalDependency(d *schema.Database, dep string) bool {
	if d.External == nil {
		return false
	}
	for _, kind := range []string{"table ", "view ", "materialized view ", "sequence ", "foreign table "} {
		if name, ok := strings.CutPrefix(dep, kind); ok {
			return externalRelation(d, strings.TrimSpace(name))
		}
	}
	_, name, ok := strings.Cut(dep, " ")
	if !ok {
		return false
	}
	name = strings.TrimSpace(name)
	if i := strings.IndexByte(name, '('); i >= 0 {
		name = name[:i]
	}
	parts := referenceParts(name)
	return len(parts) == 2 && d.External.HasSchema(parts[0])
}

// inheritsColumn reports whether table t gets column cn from a parent, so
// that a declaration without a type sets what t keeps of it: t is a
// partition, or one of its INHERITS parents declares the column (or isn't
// declared, so pgy can't tell).
func inheritsColumn(d *schema.Database, t *schema.Table, cn string) bool {
	if t.PartitionOf != "" || t.Of != "" {
		return true
	}
	for _, p := range t.Inherits {
		pt := d.Tables[qualifyPublic(p)]
		if pt == nil || pt.Columns[cn] != nil || inheritsColumn(d, pt, cn) {
			return true
		}
	}
	return false
}

// columnName returns the column an index/constraint entry names, or "" when
// the entry is an expression (which cannot be checked offline).
func columnName(entry string) string {
	base := strings.TrimSpace(entry)
	lower := strings.ToLower(base)
	for _, suffix := range []string{" nulls first", " nulls last", " asc", " desc", " without overlaps"} {
		if strings.HasSuffix(lower, suffix) {
			base, lower = strings.TrimSpace(base[:len(base)-len(suffix)]), lower[:len(lower)-len(suffix)]
		}
	}
	if strings.HasPrefix(lower, "period ") {
		base = strings.TrimSpace(base[len("period "):])
	}
	for _, r := range base {
		if !isIdentRune(r) {
			return ""
		}
	}
	return base
}

func isIdentRune(r rune) bool {
	return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// singleOwnerObjects returns the objects that a later file replaces
// wholesale instead of merging (everything except tables), keyed by
// "<kind> <name>".
func singleOwnerObjects(d *schema.Database) map[string]any {
	out := map[string]any{}
	for k, v := range d.Functions {
		out["function "+k] = v
	}
	for k, v := range d.Procedures {
		out["procedure "+k] = v
	}
	for k, v := range d.Views {
		out["view "+k] = v
	}
	for k, v := range d.Types {
		out["type "+k] = v
	}
	for k, v := range d.Domains {
		out["domain "+k] = v
	}
	for k, v := range d.Sequences {
		out["sequence "+k] = v
	}
	for k, v := range d.Collations {
		out["collation "+k] = v
	}
	return out
}

func compileSchema() (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	if err := c.AddResource("pgy.schema.json", bytes.NewReader(schemas.JSONSchema)); err != nil {
		return nil, err
	}
	return c.Compile("pgy.schema.json")
}

func toJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	return out, json.Unmarshal(b, &out)
}

// dottedSchemaIssues reports the schemas file declares, as `schema <name>:`
// blocks or under `schemas:`, whose name contains a dot. pgy keys every
// object as schema.name and reads the first dot as the separator, so it
// cannot manage the objects of such a schema.
func dottedSchemaIssues(file string, doc any) []Issue {
	root, ok := doc.(map[string]any)
	if !ok {
		return nil
	}
	names := []string{}
	for k := range root {
		if name, ok := strings.CutPrefix(k, "schema "); ok {
			names = append(names, strings.TrimSpace(name))
		}
	}
	if m, ok := root["schemas"].(map[string]any); ok {
		for name := range m {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var out []Issue
	for _, name := range names {
		bare := name
		if parts, ok := schema.SplitName(name); ok && len(parts) == 1 {
			bare = parts[0]
		}
		if strings.Contains(bare, ".") {
			out = append(out, Issue{Severity: Error, File: file, Object: "schema " + name, Message: "the schema's name contains a dot, which pgy reads as the separator between a schema and an object's name, so it cannot manage the objects of this schema"})
		}
	}
	return out
}

// schemaIssues flattens a JSON Schema validation error to its leaf causes.
func schemaIssues(file string, err error) []Issue {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []Issue{{Severity: Error, File: file, Message: err.Error()}}
	}
	var leaves []*jsonschema.ValidationError
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			leaves = append(leaves, e)
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	seen := map[string]bool{}
	out := []Issue{}
	for _, e := range leaves {
		// A oneOf/anyOf alternative that fails at a shallower location than
		// another leaf ("expected array" for the map form) is noise.
		shadowed := false
		for _, o := range leaves {
			if len(o.InstanceLocation) > len(e.InstanceLocation) && strings.HasPrefix(o.InstanceLocation, e.InstanceLocation+"/") {
				shadowed = true
				break
			}
		}
		if shadowed {
			continue
		}
		loc := strings.TrimPrefix(e.InstanceLocation, "/")
		if u, err := url.PathUnescape(loc); err == nil {
			loc = u
		}
		loc = strings.ReplaceAll(loc, "/", " > ")
		if loc == "" {
			loc = "(document)"
		}
		msg := loc + ": " + e.Message
		if !seen[msg] {
			seen[msg] = true
			out = append(out, Issue{Severity: Error, File: file, Message: msg})
		}
	}
	return out
}

func qualifyPublic(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, ".") {
		return name
	}
	return "public." + name
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// renameGroup collects the renamedFrom declarations sharing one namespace:
// each source must not itself be declared, and no two objects may be
// renamed from the same source.
type renameGroup struct {
	declared map[string]bool
	sources  map[string]string
}

func newRenameGroup() *renameGroup {
	return &renameGroup{declared: map[string]bool{}, sources: map[string]string{}}
}

func (g *renameGroup) check(obj, source string, add func(Severity, string, string, ...any)) {
	if g.declared[source] {
		add(Error, obj, "renamedFrom %s names an object the YAML also declares", source)
	}
	if prev, dup := g.sources[source]; dup {
		add(Error, obj, "renamedFrom %s is also claimed by %s", source, prev)
	}
	g.sources[source] = obj
}

func qualifiedSource(key, from string) string {
	if strings.Contains(from, ".") {
		return from
	}
	return key[:strings.IndexByte(key, '.')+1] + from
}

func checkRenames(d *schema.Database, add func(Severity, string, string, ...any)) {
	rels, types, routines := newRenameGroup(), newRenameGroup(), newRenameGroup()
	for k := range d.Tables {
		rels.declared[k] = true
	}
	for k := range d.Views {
		rels.declared[k] = true
	}
	for k := range d.Sequences {
		rels.declared[k] = true
	}
	for k := range d.Types {
		types.declared[k] = true
	}
	for k := range d.Domains {
		types.declared[k] = true
	}
	for k := range d.Functions {
		routines.declared[k] = true
	}
	for k := range d.Procedures {
		routines.declared[k] = true
	}
	for _, k := range sortedKeys(d.Tables) {
		if t := d.Tables[k]; t != nil && t.Foreign != nil && d.Servers != nil && d.Servers[t.Foreign.Server] == nil {
			add(Error, "foreign table "+k, "uses server %s, which servers: does not declare", t.Foreign.Server)
		}
	}
	servers := newRenameGroup()
	for k := range d.Servers {
		servers.declared[k] = true
	}
	for _, k := range sortedKeys(d.Servers) {
		if s := d.Servers[k]; s != nil && s.RenamedFrom != "" {
			servers.check("server "+k, s.RenamedFrom, add)
		}
	}
	for _, k := range sortedKeys(d.Tables) {
		t := d.Tables[k]
		if t == nil {
			continue
		}
		obj := "table " + k
		if t.RenamedFrom != "" {
			rels.check(obj, qualifiedSource(k, t.RenamedFrom), add)
		}
		cols := newRenameGroup()
		for cn := range t.Columns {
			cols.declared[cn] = true
		}
		for _, cn := range sortedKeys(t.Columns) {
			if c := t.Columns[cn]; c != nil && c.RenamedFrom != "" {
				cols.check(obj+" column "+cn, c.RenamedFrom, add)
			}
		}
		named := newRenameGroup()
		for _, ix := range t.Indexes {
			if ix != nil {
				named.declared[ix.Name] = true
			}
		}
		for _, c := range t.Constraints {
			if c != nil {
				named.declared[c.Name] = true
			}
		}
		for _, fk := range t.ForeignKeys {
			if fk != nil {
				named.declared[fk.Name] = true
			}
		}
		for _, ix := range t.Indexes {
			if ix != nil && ix.RenamedFrom != "" {
				named.check(obj+" index "+ix.Name, ix.RenamedFrom, add)
			}
		}
		for _, c := range t.Constraints {
			if c != nil && c.RenamedFrom != "" {
				named.check(obj+" constraint "+c.Name, c.RenamedFrom, add)
			}
		}
		for _, fk := range t.ForeignKeys {
			if fk != nil && fk.RenamedFrom != "" {
				named.check(obj+" foreign key "+fk.Name, fk.RenamedFrom, add)
			}
		}
	}
	for _, k := range sortedKeys(d.Views) {
		if v := d.Views[k]; v != nil && v.RenamedFrom != "" {
			rels.check("view "+k, qualifiedSource(k, v.RenamedFrom), add)
		}
	}
	for _, k := range sortedKeys(d.Sequences) {
		if sq := d.Sequences[k]; sq != nil && sq.RenamedFrom != "" {
			rels.check("sequence "+k, qualifiedSource(k, sq.RenamedFrom), add)
		}
	}
	for _, k := range sortedKeys(d.Types) {
		td := d.Types[k]
		if td == nil {
			continue
		}
		if td.RenamedFrom != "" {
			types.check("type "+k, qualifiedSource(k, td.RenamedFrom), add)
		}
		labels := map[string]bool{}
		for _, l := range td.Labels {
			labels[l] = true
		}
		for _, label := range sortedKeys(td.RenamedLabels) {
			if !labels[label] {
				add(Error, "type "+k, "renamedLabels names %s, which is not one of its labels", label)
			}
			if old := td.RenamedLabels[label]; labels[old] {
				add(Error, "type "+k, "renamedLabels source %s is still one of its labels", old)
			}
		}
	}
	for _, k := range sortedKeys(d.Domains) {
		if dm := d.Domains[k]; dm != nil && dm.RenamedFrom != "" {
			types.check("domain "+k, qualifiedSource(k, dm.RenamedFrom), add)
		}
	}
	for _, k := range sortedKeys(d.Functions) {
		if f := d.Functions[k]; f != nil && f.RenamedFrom != "" {
			routines.check("function "+k, qualifiedSource(k, f.RenamedFrom)+f.ArgsSig, add)
		}
	}
	for _, k := range sortedKeys(d.Procedures) {
		if p := d.Procedures[k]; p != nil && p.RenamedFrom != "" {
			routines.check("procedure "+k, qualifiedSource(k, p.RenamedFrom)+p.ArgsSig, add)
		}
	}
}

// untrustedLanguages are the languages of PostgreSQL and its own extensions
// that only superusers can use, on which GRANT and REVOKE fail.
var untrustedLanguages = map[string]bool{"internal": true, "c": true, "plperlu": true, "pltclu": true, "plpythonu": true, "plpython2u": true, "plpython3u": true}

// checkWrappersAndLanguages reports foreignDataWrappers entries that define
// or rename a wrapper a declared extension creates, renames that clash,
// languages entries for a language that takes no privileges, and grants of
// anything but USAGE, the only privilege wrappers and languages have, or
// ALL, which stands for it.
func checkWrappersAndLanguages(d *schema.Database, add func(Severity, string, string, ...any)) {
	usageOnly := func(obj string, grants map[string][]string) {
		for _, role := range sortedKeys(grants) {
			for _, priv := range grants[role] {
				switch strings.TrimSuffix(priv, "*") {
				case "usage", "all", "all privileges":
				default:
					add(Error, obj, "grants %s to %s; write usage (or all), the only privilege there is here", strings.TrimSuffix(priv, "*"), role)
				}
			}
		}
	}
	wrappers := newRenameGroup()
	for k := range d.ForeignDataWrappers {
		wrappers.declared[k] = true
	}
	for _, k := range sortedKeys(d.ForeignDataWrappers) {
		w := d.ForeignDataWrappers[k]
		if w == nil {
			continue
		}
		obj := "foreign-data wrapper " + k
		usageOnly(obj, w.Grants)
		if ext := d.WrapperExtension(k); ext != "" && (w.ManagesHandler() || w.ManagesValidator() || w.ManagesConnection()) {
			add(Error, obj, "extension %s creates this wrapper with its own handler, validator and connection function, so the entry cannot set them; it manages the wrapper's options, owner, grants and comment", ext)
		}
		if w.RenamedFrom == "" {
			continue
		}
		wrappers.check(obj, w.RenamedFrom, add)
		if ext := d.WrapperExtension(w.RenamedFrom); ext != "" {
			add(Error, obj, "renamedFrom %s names the wrapper of extension %s, and pgy does not rename an extension's objects", w.RenamedFrom, ext)
		}
	}
	for _, k := range sortedKeys(d.Languages) {
		l := d.Languages[k]
		if l == nil {
			continue
		}
		if untrustedLanguages[k] {
			add(Error, "language "+k, "is not a trusted language: only superusers can use it, and PostgreSQL rejects GRANT and REVOKE on it")
		}
		usageOnly("language "+k, l.Grants)
	}
}

// tableHasColumn reports whether table fq declares column c, directly or
// through a declared INHERITS parent.
func tableHasColumn(d *schema.Database, fq, c string, depth int) bool {
	t := d.Tables[fq]
	if t == nil || depth > 16 {
		return false
	}
	if _, ok := t.Columns[c]; ok {
		return true
	}
	if t.Of != "" {
		ty := d.Types[t.Of]
		return ty == nil || ty.Kind != "composite" || ty.Attributes[c] != ""
	}
	for _, p := range t.Inherits {
		if d.Tables[p] == nil || tableHasColumn(d, p, c, depth+1) {
			return true
		}
	}
	return false
}

// checkData checks a table's data: rows against its key and columns.
func checkData(d *schema.Database, fq string, t *schema.Table, add func(Severity, string, string, ...any)) {
	obj := "table " + fq
	key := t.DataKey()
	if t.AttributeOnly() {
		if len(t.Data.Key) == 0 {
			add(Error, obj, "data needs data.key on a table declared without columns")
		}
		checkDataKeys(obj, key, t.Data.Rows, add)
		return
	}
	if len(key) == 0 {
		add(Error, obj, "data needs a key: declare data.key or a primary key")
		return
	}
	for _, k := range key {
		if !tableHasColumn(d, fq, k, 0) {
			add(Error, obj, "data key column %s is not declared", k)
		} else if c := t.Columns[k]; c != nil && unorderedType(c.Type) {
			add(Error, obj, "data key column %s has type %s, which has no default btree operator class, so rows cannot be matched or ordered by it; use key columns of another type", k, strings.TrimSpace(c.Type))
		}
	}
	if len(t.Data.Key) > 0 && !uniqueOn(t, key) {
		add(Warning, obj, "data key (%s) is not the primary key or a unique constraint; rows cannot be upserted, and duplicates are not prevented", strings.Join(key, ", "))
	}
	checkDataKeys(obj, key, t.Data.Rows, add)
	for i, row := range t.Data.Rows {
		for _, col := range sortedKeys(row) {
			if !tableHasColumn(d, fq, col, 0) {
				add(Error, obj, "data row %d sets undeclared column %s", i+1, col)
			} else if c := t.Columns[col]; c != nil && c.Generated != "" {
				add(Error, obj, "data row %d sets generated column %s", i+1, col)
			}
		}
	}
}

// checkDataKeys checks that each data row has a value for every key column
// and that no two rows have the same key.
func checkDataKeys(obj string, key []string, rows []map[string]any, add func(Severity, string, string, ...any)) {
	seen := map[string]int{}
	for i, row := range rows {
		parts := []string{}
		for _, k := range key {
			v, ok := row[k]
			if !ok || v == nil {
				add(Error, obj, "data row %d has no value for key column %s", i+1, k)
			}
			parts = append(parts, fmt.Sprint(v))
		}
		id := strings.Join(parts, "\x00")
		if prev, dup := seen[id]; dup {
			add(Error, obj, "data rows %d and %d have the same key", prev, i+1)
		}
		seen[id] = i + 1
	}
}

// unorderedType reports whether typ is a built-in type without a default
// btree operator class, or an array of one. Extension types are left to
// pgy diff, which looks the class up in the database.
func unorderedType(typ string) bool {
	typ = strings.ToLower(strings.TrimSpace(typ))
	for strings.HasSuffix(typ, "[]") {
		typ = strings.TrimSpace(strings.TrimSuffix(typ, "[]"))
	}
	switch strings.TrimPrefix(typ, "pg_catalog.") {
	case "json", "jsonpath", "xml", "point", "line", "lseg", "box", "path", "polygon", "circle":
		return true
	}
	return false
}

// uniqueOn reports whether cols are exactly t's primary key or the columns
// of one of its unique constraints, unique indexes or unique columns.
func uniqueOn(t *schema.Table, cols []string) bool {
	same := func(a []string) bool {
		if len(a) != len(cols) {
			return false
		}
		set := map[string]bool{}
		for _, c := range a {
			set[strings.Fields(c)[0]] = true
		}
		for _, c := range cols {
			if !set[c] {
				return false
			}
		}
		return true
	}
	if same(t.PrimaryKey) {
		return true
	}
	if len(cols) == 1 {
		if c := t.Columns[cols[0]]; c != nil && (c.Unique || c.PrimaryKey) {
			return true
		}
	}
	for _, c := range t.Constraints {
		if c != nil && strings.EqualFold(c.Type, "unique") && same(c.Columns) {
			return true
		}
	}
	for _, ix := range t.Indexes {
		if ix != nil && ix.Unique && ix.Where == "" && same(ix.Columns) {
			return true
		}
	}
	return false
}

// checkOwnerGrants warns about grants to an object's declared owner, which
// holds every privilege implicitly; pgy ignores them.
func checkOwnerGrants(d *schema.Database, add func(Severity, string, string, ...any)) {
	check := func(obj, owner string, grants map[string][]string) {
		if owner == "" {
			return
		}
		if _, ok := grants[owner]; ok {
			add(Warning, obj, "grants to its owner %s are implicit and ignored", owner)
		}
	}
	for _, k := range sortedKeys(d.Tables) {
		if t := d.Tables[k]; t != nil {
			check("table "+k, t.Owner, t.Grants)
		}
	}
	for _, k := range sortedKeys(d.Views) {
		if v := d.Views[k]; v != nil {
			check("view "+k, v.Owner, v.Grants)
		}
	}
	for _, k := range sortedKeys(d.Sequences) {
		if sq := d.Sequences[k]; sq != nil {
			check("sequence "+k, sq.Owner, sq.Grants)
		}
	}
	for _, k := range sortedKeys(d.Functions) {
		if f := d.Functions[k]; f != nil {
			check("function "+k, f.Owner, f.Grants)
		}
	}
	for _, k := range sortedKeys(d.Procedures) {
		if p := d.Procedures[k]; p != nil {
			check("procedure "+k, p.Owner, p.Grants)
		}
	}
	for _, k := range sortedKeys(d.Aggregates) {
		if ag := d.Aggregates[k]; ag != nil {
			check("aggregate "+k, ag.Owner, ag.Grants)
		}
	}
	for _, s := range sortedKeys(d.SchemaGrants) {
		check("schema "+s, d.SchemaOwners[s], d.SchemaGrants[s])
	}
}

// hasUniqueKey reports whether t has a primary key, unique constraint,
// unique column or full unique index on exactly cols, in any order, as a
// foreign key referencing them requires.
func hasUniqueKey(t *schema.Table, cols []string) bool {
	want := map[string]bool{}
	for _, c := range cols {
		want[columnName(c)] = true
	}
	same := func(key []string) bool {
		if len(key) != len(want) {
			return false
		}
		for _, c := range key {
			if !want[columnName(c)] {
				return false
			}
		}
		return true
	}
	pk := append([]string{}, t.PrimaryKey...)
	for name, c := range t.Columns {
		if c == nil {
			continue
		}
		if c.PrimaryKey && !slices.Contains(pk, name) {
			pk = append(pk, name)
		}
		if c.Unique && same([]string{name}) {
			return true
		}
	}
	if same(pk) {
		return true
	}
	for _, ct := range t.Constraints {
		if ct != nil && strings.EqualFold(ct.Type, "unique") && same(ct.Columns) {
			return true
		}
	}
	for _, ix := range t.Indexes {
		if ix != nil && ix.Unique && strings.TrimSpace(ix.Where) == "" && same(ix.Columns) {
			return true
		}
	}
	return false
}

// typeCategory groups a column type with the types a foreign key can
// compare it to: "number", "text", "datetime", "time", "uuid" or "bool",
// or "" for types it doesn't know (domains, arrays, enums, ...).
func typeCategory(typ string) string {
	t := strings.ToLower(strings.TrimSpace(typ))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i]) + strings.TrimSpace(t[strings.LastIndexByte(t, ')')+1:])
	}
	t = strings.TrimPrefix(t, "pg_catalog.")
	switch t {
	case "smallint", "int2", "integer", "int", "int4", "bigint", "int8", "numeric", "decimal", "real", "float4", "double precision", "float8", "float", "smallserial", "serial", "serial2", "serial4", "bigserial", "serial8":
		return "number"
	case "text", "varchar", "character varying", "char", "character", "bpchar", "name":
		return "text"
	case "date", "timestamp", "timestamptz", "timestamp with time zone", "timestamp without time zone":
		return "datetime"
	case "time", "timetz", "time with time zone", "time without time zone":
		return "time"
	case "uuid":
		return "uuid"
	case "bool", "boolean":
		return "bool"
	}
	return ""
}

// checkNameClashes reports two objects of a schema that need the same name
// in pg_class (tables, views, sequences, indexes, and the indexes behind
// named primary keys and unique and exclusion constraints) or in pg_type
// (relations and types), which PostgreSQL rejects with "already exists".
// Default primary key names are left out, since PostgreSQL picks another
// one when <table>_pkey is taken.
func checkNameClashes(d *schema.Database, add func(Severity, string, string, ...any)) {
	relations, types := map[string]string{}, map[string]string{}
	claim := func(names map[string]string, fq, what string) {
		if prev, ok := names[fq]; ok && prev != what {
			add(Error, what, "has the same name as %s; PostgreSQL needs a different name for each", prev)
			return
		}
		names[fq] = what
	}
	schemaOf := func(fq string) string {
		if i := strings.IndexByte(fq, '.'); i >= 0 {
			return fq[:i]
		}
		return "public"
	}
	for _, fq := range sortedKeys(d.Tables) {
		t := d.Tables[fq]
		if t == nil {
			continue
		}
		what := "table " + fq
		claim(relations, fq, what)
		claim(types, fq, what)
		s := schemaOf(fq)
		if name := t.PrimaryKeyName; name != "" {
			claim(relations, s+"."+name, "primary key "+name+" on "+fq)
		}
		for _, ix := range t.Indexes {
			if ix != nil && ix.Name != "" {
				claim(relations, s+"."+ix.Name, "index "+ix.Name+" on "+fq)
			}
		}
		for _, ct := range t.Constraints {
			if ct != nil && ct.Name != "" && (strings.EqualFold(ct.Type, "unique") || strings.EqualFold(ct.Type, "exclude")) {
				claim(relations, s+"."+ct.Name, "constraint "+ct.Name+" on "+fq)
			}
		}
	}
	for _, fq := range sortedKeys(d.Views) {
		if v := d.Views[fq]; v != nil {
			what := "view " + fq
			if v.Materialized {
				what = "materialized view " + fq
			}
			claim(relations, fq, what)
			claim(types, fq, what)
			for _, ix := range v.Indexes {
				if ix != nil && ix.Name != "" {
					claim(relations, schemaOf(fq)+"."+ix.Name, "index "+ix.Name+" on "+fq)
				}
			}
		}
	}
	for _, fq := range sortedKeys(d.Sequences) {
		if d.Sequences[fq] != nil {
			claim(relations, fq, "sequence "+fq)
			claim(types, fq, "sequence "+fq)
		}
	}
	for _, fq := range sortedKeys(d.Types) {
		if d.Types[fq] != nil {
			claim(types, fq, "type "+fq)
		}
	}
	for _, fq := range sortedKeys(d.Domains) {
		if d.Domains[fq] != nil {
			claim(types, fq, "domain "+fq)
		}
	}
}

// maxIdentifier is the longest name PostgreSQL keeps (NAMEDATALEN - 1
// bytes); longer ones are truncated, so pgy would never find the object
// under its declared name again.
const maxIdentifier = 63

// checkNameLengths reports declared names longer than maxIdentifier bytes.
func checkNameLengths(d *schema.Database, add func(Severity, string, string, ...any)) {
	check := func(obj, what, name string) {
		if len(name) > maxIdentifier {
			add(Error, obj, "%s %q is %d bytes; PostgreSQL truncates names to %d bytes, so pgy would never find it again", what, name, len(name), maxIdentifier)
		}
	}
	objectName := func(fq string) string {
		if i := strings.IndexByte(fq, '.'); i >= 0 {
			return fq[i+1:]
		}
		return fq
	}
	schemas := map[string]bool{}
	qualified := func(kind, fq string) {
		obj := kind + " " + fq
		if i := strings.IndexByte(fq, '.'); i >= 0 {
			schemas[fq[:i]] = true
		}
		check(obj, kind, objectName(fq))
	}
	for _, fq := range sortedKeys(d.Tables) {
		t := d.Tables[fq]
		if t == nil {
			continue
		}
		obj := "table " + fq
		qualified("table", fq)
		for _, c := range sortedKeys(t.Columns) {
			check(obj, "column", c)
		}
		check(obj, "primary key", t.PrimaryKeyName)
		for _, ix := range t.Indexes {
			if ix != nil {
				check(obj, "index", ix.Name)
			}
		}
		for _, ct := range t.Constraints {
			if ct != nil {
				check(obj, "constraint", ct.Name)
			}
		}
		for _, fk := range t.ForeignKeys {
			if fk != nil {
				check(obj, "foreign key", fk.Name)
			}
		}
		for _, tg := range t.Triggers {
			if tg != nil {
				check(obj, "trigger", tg.Name)
			}
		}
		for _, r := range t.Rules {
			if r != nil {
				check(obj, "rule", r.Name)
			}
		}
		for _, st := range t.Statistics {
			if st != nil {
				check(obj, "statistics", st.Name)
			}
		}
		for _, p := range t.Policies {
			if p != nil {
				check(obj, "policy", p.Name)
			}
		}
	}
	for _, fq := range sortedKeys(d.Views) {
		if v := d.Views[fq]; v != nil {
			qualified("view", fq)
			for _, ix := range v.Indexes {
				if ix != nil {
					check("view "+fq, "index", ix.Name)
				}
			}
		}
	}
	for _, fq := range sortedKeys(d.Sequences) {
		qualified("sequence", fq)
	}
	for _, fq := range sortedKeys(d.Types) {
		td := d.Types[fq]
		if td == nil {
			continue
		}
		qualified("type", fq)
		for _, l := range td.Labels {
			check("type "+fq, "enum label", l)
		}
		for _, a := range sortedKeys(td.Attributes) {
			check("type "+fq, "attribute", a)
		}
	}
	for _, fq := range sortedKeys(d.Domains) {
		if dm := d.Domains[fq]; dm != nil {
			qualified("domain", fq)
			for _, c := range sortedKeys(dm.Checks) {
				check("domain "+fq, "constraint", c)
			}
		}
	}
	routine := func(kind string, keys []string) {
		for _, k := range keys {
			name := schema.RoutineName(k)
			if i := strings.IndexByte(name, '.'); i >= 0 {
				schemas[name[:i]] = true
			}
			check(kind+" "+k, kind, objectName(name))
		}
	}
	routine("function", sortedKeys(d.Functions))
	routine("procedure", sortedKeys(d.Procedures))
	routine("aggregate", sortedKeys(d.Aggregates))
	for _, r := range sortedKeys(d.Roles) {
		check("role "+r, "role", r)
	}
	for _, s := range sortedKeys(schemas) {
		check("schema "+s, "schema", s)
	}
}

// noArgReturns returns the lower-cased return type of the argument-less
// function among keys (the one a trigger calls), or "" when there is none
// or it doesn't declare one.
func noArgReturns(d *schema.Database, keys []string) string {
	for _, k := range keys {
		f := d.Functions[k]
		if f == nil || strings.TrimSpace(strings.Trim(strings.TrimSpace(f.ArgsSig), "()")) != "" {
			continue
		}
		return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(f.Returns)), "pg_catalog.")
	}
	return ""
}

// setNullColumns returns the columns an ON DELETE/UPDATE SET NULL action
// sets: the listed ones of "set null (a, b)", else all of cols. It returns
// nil for other actions.
func setNullColumns(action string, cols []string) []string {
	a := strings.ToLower(strings.Join(strings.Fields(action), " "))
	if !strings.HasPrefix(a, "set null") {
		return nil
	}
	rest := strings.TrimSpace(strings.TrimPrefix(a, "set null"))
	if !strings.HasPrefix(rest, "(") {
		out := []string{}
		for _, c := range cols {
			out = append(out, columnName(c))
		}
		return out
	}
	out := []string{}
	for _, c := range strings.Split(strings.Trim(rest, "()"), ",") {
		if c = strings.Trim(strings.TrimSpace(c), `"`); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// checkValidateModes reports constraints of t that ask to be validated
// immediately while notValid leaves them unvalidated.
func checkValidateModes(t *schema.Table, obj string, add func(Severity, string, string, ...any)) {
	for _, ct := range t.Constraints {
		if ct != nil && ct.NotValid && ct.Validate == schema.ValidateImmediate {
			add(Error, obj, "constraint %s sets both notValid: true and validate: immediate; notValid leaves it unvalidated, so drop one of them", ct.Name)
		}
	}
	for _, fk := range t.ForeignKeys {
		if fk != nil && fk.NotValid && fk.Validate == schema.ValidateImmediate {
			add(Error, obj, "foreign key %s sets both notValid: true and validate: immediate; notValid leaves it unvalidated, so drop one of them", fk.Name)
		}
	}
	for _, cn := range sortedKeys(t.Columns) {
		if c := t.Columns[cn]; c != nil && c.NotValid && c.Validate == schema.ValidateImmediate {
			add(Error, obj, "column %s sets both notValid: true and validate: immediate; notValid leaves its NOT NULL unvalidated, so drop one of them", cn)
		}
	}
}

// checkPartitionKeys reports primary keys, unique constraints and unique
// indexes of a partitioned table that leave out a partition key column, or
// that exist at all when the partition key uses an expression; PostgreSQL
// rejects both.
func checkPartitionKeys(t *schema.Table, obj string, add func(Severity, string, string, ...any)) {
	keyCols := []string{}
	for _, c := range t.PartitionBy.Columns {
		base := columnName(strings.TrimSpace(c))
		if base == "" {
			keyCols = nil
			break
		}
		keyCols = append(keyCols, base)
	}
	check := func(what string, cols []string) {
		if len(cols) == 0 {
			return
		}
		if keyCols == nil {
			add(Error, obj, "%s: a partitioned table can't have unique keys when its partition key uses an expression", what)
			return
		}
		have := map[string]bool{}
		for _, c := range cols {
			have[columnName(strings.TrimSpace(c))] = true
		}
		for _, k := range keyCols {
			if !have[k] {
				add(Error, obj, "%s must include partition key column %s; PostgreSQL requires unique keys on a partitioned table to contain the whole partition key", what, k)
			}
		}
	}
	pk := append([]string{}, t.PrimaryKey...)
	for _, name := range sortedKeys(t.Columns) {
		if c := t.Columns[name]; c != nil {
			if c.PrimaryKey && !slices.Contains(pk, name) {
				pk = append(pk, name)
			}
			if c.Unique {
				check("unique column "+name, []string{name})
			}
		}
	}
	check("primary key", pk)
	for _, ct := range t.Constraints {
		if ct != nil && strings.EqualFold(ct.Type, "unique") {
			check("constraint "+ct.Name, ct.Columns)
		}
	}
	for _, ix := range t.Indexes {
		if ix != nil && ix.Unique {
			check("unique index "+ix.Name, ix.Columns)
		}
	}
}

// exprIdentifiers returns the identifiers an SQL expression refers to,
// outside string literals and leaving out function names: unquoted ones
// lower-cased, quoted ones as written.
func exprIdentifiers(expr string) []string {
	out := []string{}
	isStart := func(b byte) bool { return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }
	isPart := func(b byte) bool { return isStart(b) || b == '$' || b >= '0' && b <= '9' }
	for i := 0; i < len(expr); {
		switch c := expr[i]; {
		case c == '\'':
			j := i + 1
			for j < len(expr) && (expr[j] != '\'' || j+1 < len(expr) && expr[j+1] == '\'') {
				if expr[j] == '\'' {
					j++
				}
				j++
			}
			i = j + 1
		case c == '"':
			j := strings.IndexByte(expr[i+1:], '"')
			if j < 0 {
				return out
			}
			out = append(out, expr[i+1:i+1+j])
			i += j + 2
		case isStart(c):
			j := i
			for j < len(expr) && isPart(expr[j]) {
				j++
			}
			k := j
			for k < len(expr) && expr[k] == ' ' {
				k++
			}
			if k >= len(expr) || expr[k] != '(' {
				out = append(out, strings.ToLower(expr[i:j]))
			}
			i = j
		case c >= '0' && c <= '9':
			for i < len(expr) && isPart(expr[i]) {
				i++
			}
		default:
			i++
		}
	}
	return out
}
