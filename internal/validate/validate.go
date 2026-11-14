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
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/suprbdev/pgy/internal/schema"
	"github.com/suprbdev/pgy/schemas"
	yaml "gopkg.in/yaml.v3"
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
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			issues = append(issues, Issue{Severity: Error, File: p, Message: err.Error()})
			continue
		}
		var doc any
		if err := yaml.Unmarshal(b, &doc); err != nil {
			issues = append(issues, Issue{Severity: Error, File: p, Message: "invalid YAML: " + err.Error()})
			continue
		}
		if v, err := toJSON(doc); err == nil {
			if verr := compiled.Validate(v); verr != nil {
				issues = append(issues, schemaIssues(p, verr)...)
			}
		}
		d, err := schema.Load(p)
		if err != nil {
			issues = append(issues, Issue{Severity: Error, File: p, Message: err.Error()})
			continue
		}
		dbs = append(dbs, d)
		for k, obj := range singleOwnerObjects(d) {
			owners[k] = append(owners[k], owner{p, obj})
		}
	}
	for _, k := range sortedKeys(owners) {
		decls := owners[k]
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
			issues = append(issues, Issue{Severity: Warning, Object: k, Message: fmt.Sprintf("declared differently in %s; the last file wins", strings.Join(files, ", "))})
		}
	}
	issues = append(issues, Database(schema.Merge(dbs))...)
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Severity == Error && issues[j].Severity != Error })
	return issues
}

// Database checks a merged schema for broken references and likely mistakes.
func Database(d *schema.Database) []Issue {
	issues := []Issue{}
	add := func(sev Severity, obj, format string, args ...any) {
		issues = append(issues, Issue{Severity: sev, Object: obj, Message: fmt.Sprintf(format, args...)})
	}
	if _, err := schema.TopologicalSort(d); err != nil {
		add(Error, "", "%v", err)
	}

	for _, fq := range sortedKeys(d.Tables) {
		t := d.Tables[fq]
		if t == nil {
			continue
		}
		obj := "table " + fq
		checkDeps(d, obj, t.DependsOn, add)
		hasCols := t.PartitionOf == "" && len(t.Columns) > 0
		colExists := func(c string) bool { _, ok := t.Columns[c]; return ok }
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
		need("primary key", t.PrimaryKey)
		for _, ix := range t.Indexes {
			if ix == nil {
				continue
			}
			if len(ix.Columns) == 0 {
				add(Error, obj, "index %s has no columns", ix.Name)
			}
			need("index "+ix.Name, ix.Columns)
			need("index "+ix.Name+" include", ix.Include)
		}
		for _, ct := range t.Constraints {
			if ct == nil {
				continue
			}
			switch strings.ToLower(ct.Type) {
			case "unique":
				need("constraint "+ct.Name, ct.Columns)
			case "check", "exclude":
				if strings.TrimSpace(ct.Expression) == "" {
					add(Error, obj, "constraint %s has no expression", ct.Name)
				}
			default:
				add(Error, obj, "constraint %s has unknown type %q", ct.Name, ct.Type)
			}
		}
		for _, fk := range t.ForeignKeys {
			if fk == nil {
				continue
			}
			need("foreign key "+fk.Name, fk.Columns)
			if len(fk.Columns) != len(fk.RefColumns) {
				add(Error, obj, "foreign key %s has %d columns but references %d", fk.Name, len(fk.Columns), len(fk.RefColumns))
			}
			ref := d.Tables[qualifyPublic(fk.RefTable)]
			if ref == nil {
				add(Warning, obj, "foreign key %s references %s, which no schema file declares", fk.Name, fk.RefTable)
				continue
			}
			if ref.PartitionOf == "" && len(ref.Columns) > 0 {
				for _, c := range fk.RefColumns {
					if base := columnName(c); base != "" && ref.Columns[base] == nil {
						add(Error, obj, "foreign key %s references unknown column %s.%s", fk.Name, fk.RefTable, base)
					}
				}
			}
		}
		for _, tr := range t.Triggers {
			if tr != nil {
				checkTrigger(d, obj, tr, add)
			}
		}
		for cn, c := range t.Columns {
			if c == nil {
				continue
			}
			if strings.TrimSpace(c.Type) == "" {
				add(Error, obj, "column %s has no type", cn)
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
		if t.PartitionOf != "" && d.Tables[t.PartitionOf] == nil {
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
			if strings.TrimSpace(vw.Query) == "" {
				add(Error, kind+k, "has no query")
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
			if strings.TrimSpace(f.Body) != "" && (f.Returns == "" || f.Language == "") {
				add(Error, "function "+k, "needs returns and language")
			}
		}
	}
	for _, k := range sortedKeys(d.Procedures) {
		if p := d.Procedures[k]; p != nil {
			checkDeps(d, "procedure "+k, p.DependsOn, add)
			if strings.TrimSpace(p.Body) == "" || p.Language == "" {
				add(Error, "procedure "+k, "needs body and language")
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
		}
	}
	for _, k := range sortedKeys(d.Sequences) {
		if sq := d.Sequences[k]; sq != nil {
			checkDeps(d, "sequence "+k, sq.DependsOn, add)
			if owner := sq.OwnedBy; owner != "" && !strings.EqualFold(owner, "none") {
				parts := strings.Split(owner, ".")
				if len(parts) >= 2 {
					table := qualifyPublic(strings.Join(parts[:len(parts)-1], "."))
					if t := d.Tables[table]; t != nil && len(t.Columns) > 0 && t.Columns[parts[len(parts)-1]] == nil {
						add(Error, "sequence "+k, "owned by unknown column %s", owner)
					}
				}
			}
		}
	}
	for _, name := range sortedKeys(d.EventTriggers) {
		if et := d.EventTriggers[name]; et != nil {
			fn := strings.TrimSuffix(strings.TrimSpace(et.Function), "()")
			if len(schema.ResolveDependencyKeys("function "+fn, d)) == 0 {
				add(Warning, "event trigger "+name, "function %s is not declared in any schema file", et.Function)
			}
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
		for _, pt := range p.Tables {
			if len(pt.Columns) > 0 && len(p.Schemas) > 0 {
				add(Error, "publication "+name, "column lists cannot be combined with schemas (table %s)", pt.Table)
			}
			if t := d.Tables[pt.Table]; t != nil && len(t.Columns) > 0 {
				for _, c := range pt.Columns {
					if t.Columns[c] == nil {
						add(Error, "publication "+name, "table %s has no column %q", pt.Table, c)
					}
				}
			}
		}
	}
	return issues
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
	if len(schema.ResolveDependencyKeys("function "+proc, d)) == 0 {
		add(Warning, obj, "trigger %s calls %s, which no schema file declares", tr.Name, proc)
	}
	if strings.EqualFold(strings.Join(strings.Fields(tr.Timing), " "), "instead of") && !strings.HasPrefix(obj, "view ") {
		add(Error, obj, "trigger %s: instead of triggers are only allowed on views", tr.Name)
	}
}

// checkDeps reports dependsOn entries that resolve to no declared object.
// schema and extension entries are informational and never resolve.
func checkDeps(d *schema.Database, obj string, deps []string, add func(Severity, string, string, ...any)) {
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if dep == "" || strings.HasPrefix(dep, "schema ") || strings.HasPrefix(dep, "extension ") {
			continue
		}
		if len(schema.ResolveDependencyKeys(dep, d)) == 0 {
			add(Error, obj, "dependsOn %q matches nothing declared", dep)
		}
	}
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
