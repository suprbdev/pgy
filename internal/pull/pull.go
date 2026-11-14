// Package pull reconstructs pgy schema YAML from an introspected database,
// so an existing database can be adopted without writing YAML by hand. The
// output is meant to diff clean against the database it came from.
package pull

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/suprbdev/pgy/internal/diff"
	yaml "gopkg.in/yaml.v3"
)

// Options controls what Build emits.
type Options struct {
	// Schemas limits output to these schemas; empty means every schema.
	Schemas []string
	// MigrationsTable ("schema.table") is left out; empty means pgy.migrations.
	MigrationsTable string
	// Owners emits owner: for objects not owned by the connecting role.
	Owners bool
	// Roles emits the cluster's non-system roles.
	Roles bool
	// Single puts everything in one document (schema.yml) instead of a file
	// per schema plus database.yml.
	Single bool
}

// File is one generated YAML document.
type File struct {
	Name    string // "<schema>.yml" or "database.yml"
	Content []byte
}

// Result is Build's output: the files plus notes about objects pull could
// not express.
type Result struct {
	Files    []File
	Warnings []string
}

// Build turns live (introspected with diff.Introspect and
// diff.IntrospectDetails) into one YAML file per schema plus database.yml
// for database-wide objects.
func Build(live *diff.Live, opts Options) (*Result, error) {
	if live.Details == nil {
		return nil, fmt.Errorf("pull needs diff.IntrospectDetails")
	}
	if opts.MigrationsTable == "" {
		opts.MigrationsTable = "pgy.migrations"
	}
	b := &builder{live: live, d: live.Details, opts: opts, res: &Result{}, blocks: map[string]*node{}}
	b.collectSerials()
	b.schemas()
	b.types()
	b.domains()
	b.collations()
	b.textSearch()
	b.sequences()
	b.routines()
	b.tables()
	b.views()

	single := newMap()
	emit := func(name string, doc *node) error {
		if opts.Single {
			single.Content = append(single.Content, doc.Content...)
			return nil
		}
		out, err := render(doc)
		if err != nil {
			return err
		}
		b.res.Files = append(b.res.Files, File{Name: name, Content: out})
		return nil
	}
	if db := b.database(); len(db.Content) > 0 {
		if err := emit("database.yml", db); err != nil {
			return nil, err
		}
	}
	for _, name := range sortedKeys(b.blocks) {
		blk := b.blocks[name]
		if len(blk.Content) == 0 && !b.declaredSchema[name] {
			continue
		}
		doc := newMap()
		add(doc, "schema "+name, blk)
		if err := emit(name+".yml", doc); err != nil {
			return nil, err
		}
	}
	if opts.Single {
		out, err := render(single)
		if err != nil {
			return nil, err
		}
		b.res.Files = []File{{Name: "schema.yml", Content: out}}
	}
	return b.res, nil
}

type builder struct {
	live           *diff.Live
	d              *diff.LiveDetails
	opts           Options
	res            *Result
	blocks         map[string]*node
	declaredSchema map[string]bool
	// serials: "schema.table.column" -> serial type, for columns whose
	// default is nextval of a sequence they own
	serials map[string]string
	// serialSeqs: sequences represented by a serial column
	serialSeqs map[string]bool
}

func (b *builder) warn(format string, args ...any) {
	b.res.Warnings = append(b.res.Warnings, fmt.Sprintf(format, args...))
}

func (b *builder) wantSchema(s string) bool {
	if len(b.opts.Schemas) == 0 {
		return s != "pgy" || b.schemaHasObjectsBesidesMigrations(s)
	}
	for _, w := range b.opts.Schemas {
		if w == s {
			return true
		}
	}
	return false
}

func (b *builder) schemaHasObjectsBesidesMigrations(s string) bool {
	for k := range b.live.Tables {
		if strings.HasPrefix(k, s+".") && k != b.opts.MigrationsTable {
			return true
		}
	}
	return false
}

func (b *builder) block(fq string) (*node, string, bool) {
	s, name, ok := strings.Cut(fq, ".")
	if !ok || !b.wantSchema(s) {
		return nil, "", false
	}
	blk := b.blocks[s]
	if blk == nil {
		blk = newMap()
		b.blocks[s] = blk
	}
	return blk, name, true
}

func (b *builder) member(kind, key string) bool {
	return b.live.ExtensionMembers[kind+":"+key]
}

func (b *builder) owner(m *node, key string) {
	if !b.opts.Owners {
		return
	}
	if o := b.live.Owners[key]; o != "" && o != b.live.CurrentUser {
		add(m, "owner", str(o))
	}
}

func (b *builder) schemas() {
	b.declaredSchema = map[string]bool{}
	for _, s := range sortedKeys(b.live.Schemas) {
		if s == "pg_toast" || !b.wantSchema(s) || b.extensionSchema(s) {
			continue
		}
		blk := newMap()
		b.blocks[s] = blk
		b.declaredSchema[s] = s != "public"
		b.owner(blk, "schema:"+s)
		if c := b.live.SchemaComments[s]; c != "" && !(s == "public" && c == "standard public schema") {
			add(blk, "comment", str(c))
		}
		if g := grantsNode(b.live.SchemaGrants[s]); g != nil {
			add(blk, "grants", g)
		}
	}
}

// extensionSchema reports whether a schema holds only extension members.
func (b *builder) extensionSchema(s string) bool {
	for _, le := range b.live.ExtensionDefs {
		if le != nil && le.Schema == s && s != "public" {
			for k := range b.live.Tables {
				if strings.HasPrefix(k, s+".") && !b.member("rel", k) {
					return false
				}
			}
			for k, lf := range b.live.FunctionDefs {
				if strings.HasPrefix(k, s+".") && lf != nil && !b.member("proc", k) {
					return false
				}
			}
			return true
		}
	}
	return false
}

func (b *builder) collectSerials() {
	b.serials, b.serialSeqs = map[string]string{}, map[string]bool{}
	nextvalRe := regexp.MustCompile(`^nextval\('("?[^']+?"?)'::regclass\)$`)
	for fq, lt := range b.live.Tables {
		for cn, lc := range lt.Columns {
			m := nextvalRe.FindStringSubmatch(strings.TrimSpace(lc.Default))
			if m == nil {
				continue
			}
			seq := strings.ReplaceAll(m[1], `"`, "")
			if !strings.Contains(seq, ".") {
				seq = schemaOf(fq) + "." + seq
			}
			ls := b.live.SequenceDefs[seq]
			if ls == nil || ls.Owner != fq+"."+cn {
				continue
			}
			kind := map[string]string{"integer": "serial", "bigint": "bigserial", "smallint": "smallserial"}[lc.Type]
			if kind == "" {
				continue
			}
			b.serials[fq+"."+cn] = kind
			b.serialSeqs[seq] = true
		}
	}
}

func (b *builder) types() {
	for _, k := range sortedKeys(b.live.Types) {
		if b.member("type", k) {
			continue
		}
		blk, name, ok := b.block(k)
		if !ok {
			continue
		}
		t := newMap()
		switch {
		case b.live.EnumLabels[k] != nil:
			add(t, "type", str("enum"))
			add(t, "labels", list(b.live.EnumLabels[k]))
		case b.d.Ranges[k] != nil:
			r := b.d.Ranges[k]
			add(t, "type", str("range"))
			add(t, "subtype", str(r.Subtype))
			addStr(t, "subtypeOpclass", r.SubtypeOpclass)
			addStr(t, "collation", r.Collation)
			addStr(t, "canonical", r.Canonical)
			addStr(t, "subtypeDiff", r.SubtypeDiff)
			addStr(t, "multirangeTypeName", r.MultirangeTypeName)
		case b.live.CompositeAttributes[k] != nil:
			add(t, "type", str("composite"))
			attrs := newMap()
			for _, a := range b.d.CompositeOrder[k] {
				spec := flowMap()
				add(spec, "type", str(b.live.CompositeAttributes[k][a]))
				add(attrs, a, spec)
			}
			add(t, "attributes", attrs)
		default:
			continue
		}
		addStr(t, "comment", b.live.TypeComments[k])
		b.owner(t, "type:"+k)
		add(blk, "type "+name, t)
	}
}

func (b *builder) domains() {
	for _, k := range sortedKeys(b.live.Domains) {
		if b.member("type", k) {
			continue
		}
		blk, name, ok := b.block(k)
		if !ok {
			continue
		}
		dt := b.d.DomainTypes[k]
		ld := b.live.DomainDefs[k]
		m := newMap()
		add(m, "type", str(dt.Type))
		addStr(m, "collate", dt.Collation)
		if ld != nil {
			addStr(m, "default", ld.Default)
			if ld.NotNull {
				add(m, "notNull", boolean(true))
			}
			names := sortedKeys(ld.Checks)
			if len(names) > 1 {
				b.warn("domain %s has %d check constraints; only %s is pulled", k, len(names), names[0])
			}
			if len(names) > 0 {
				add(m, "check", str(stripCheck(ld.Checks[names[0]])))
				if names[0] != name+"_check" {
					add(m, "constraintName", str(names[0]))
				}
			}
		}
		addStr(m, "comment", b.live.TypeComments[k])
		b.owner(m, "type:"+k)
		add(blk, "domain "+name, m)
	}
}

func (b *builder) collations() {
	for _, k := range sortedKeys(b.live.Collations) {
		co := b.d.Collations[k]
		if co == nil || b.member("coll", k) {
			continue
		}
		blk, name, ok := b.block(k)
		if !ok {
			continue
		}
		m := newMap()
		addStr(m, "provider", co.Provider)
		addStr(m, "locale", co.Locale)
		addStr(m, "lcCollate", co.LcCollate)
		addStr(m, "lcCtype", co.LcCtype)
		if co.Deterministic != nil {
			add(m, "deterministic", boolean(*co.Deterministic))
		}
		addStr(m, "rules", co.Rules)
		addStr(m, "comment", b.live.CollationComments[k])
		add(blk, "collation "+name, m)
	}
}

func (b *builder) textSearch() {
	for _, k := range sortedKeys(b.live.TSDictionaries) {
		td := b.d.TSDictionaries[k]
		if td == nil || b.member("tsdict", k) {
			continue
		}
		blk, name, ok := b.block(k)
		if !ok {
			continue
		}
		m := newMap()
		add(m, "template", str(td.Template))
		if len(td.Options) > 0 {
			opts := flowMap()
			for _, o := range sortedKeys(td.Options) {
				add(opts, o, str(td.Options[o]))
			}
			add(m, "options", opts)
		}
		addStr(m, "comment", b.live.TSComments["dict:"+k])
		add(blk, "text search dictionary "+name, m)
	}
	for _, k := range sortedKeys(b.live.TSConfigs) {
		if b.member("tscfg", k) {
			continue
		}
		blk, name, ok := b.block(k)
		if !ok {
			continue
		}
		m := newMap()
		add(m, "parser", str(strings.TrimPrefix(b.d.TSParsers[k], "pg_catalog.")))
		maps := newMap()
		for _, tok := range sortedKeys(b.live.TSConfigs[k]) {
			add(maps, tok, list(b.live.TSConfigs[k][tok]))
		}
		add(m, "mappings", maps)
		addStr(m, "comment", b.live.TSComments["config:"+k])
		add(blk, "text search configuration "+name, m)
	}
}

func (b *builder) sequences() {
	for _, k := range sortedKeys(b.live.Sequences) {
		ls := b.live.SequenceDefs[k]
		if ls == nil || b.member("rel", k) || b.serialSeqs[k] {
			continue
		}
		if b.live.OwnedSequences[k] && ls.Owner == "" {
			continue
		}
		blk, name, ok := b.block(k)
		if !ok {
			continue
		}
		m := newMap()
		typeMax := map[string]string{"smallint": "32767", "integer": "2147483647", "bigint": "9223372036854775807"}[ls.Type]
		if ls.Type != "bigint" {
			add(m, "as", str(ls.Type))
		}
		if ls.Increment != "1" {
			add(m, "increment", num(ls.Increment))
		}
		if ls.MinValue != "1" {
			add(m, "minValue", num(ls.MinValue))
		}
		if ls.MaxValue != typeMax {
			add(m, "maxValue", num(ls.MaxValue))
		}
		if ls.Start != ls.MinValue {
			add(m, "start", num(ls.Start))
		}
		if ls.Cache != "1" {
			add(m, "cache", num(ls.Cache))
		}
		if ls.Cycle {
			add(m, "cycle", boolean(true))
		}
		if ls.Owner != "" {
			add(m, "ownedBy", str(ls.Owner))
		}
		addStr(m, "comment", b.live.RelComments[k])
		b.owner(m, "rel:"+k)
		add(blk, "sequence "+name, m)
	}
}

func (b *builder) routines() {
	for _, norm := range sortedKeys(b.d.Routines) {
		r := b.d.Routines[norm]
		if b.member("proc", norm) {
			continue
		}
		blk, _, ok := b.block(r.Schema + "." + r.Name)
		if !ok {
			continue
		}
		lang := strings.ToLower(r.Language)
		if lang == "c" || lang == "internal" {
			b.warn("%s %s.%s is written in %s and cannot be pulled", kindOf(r), r.Schema, r.Name, lang)
			continue
		}
		m := newMap()
		args := r.Arguments
		if r.Procedure {
			args = procInRe.ReplaceAllString(args, "$1")
		}
		key := fmt.Sprintf("%s %s(%s)", kindOf(r), r.Name, args)
		if r.Procedure {
			lp := b.live.ProcedureDefs[norm]
			if lp == nil {
				continue
			}
			add(m, "language", str(r.Language))
			if lp.Security == "definer" {
				add(m, "security", str("definer"))
			}
			addSet(m, lp.Config)
			add(m, "body", literal(lp.Body))
		} else {
			lf := b.live.FunctionDefs[norm]
			if lf == nil || lf.Kind != "f" {
				continue
			}
			add(m, "returns", str(r.Returns))
			add(m, "language", str(r.Language))
			if lf.Security == "definer" {
				add(m, "security", str("definer"))
			}
			if lf.Volatility != "volatile" {
				add(m, lf.Volatility, boolean(true))
			}
			if lf.Strict {
				add(m, "strict", boolean(true))
			}
			if lf.Leakproof {
				add(m, "leakproof", boolean(true))
			}
			addSet(m, lf.Config)
			add(m, "body", literal(lf.Body))
		}
		if !b.live.FunctionPublicExec[norm] {
			add(m, "revokePublic", boolean(true))
		}
		if g := grantsNode(b.live.FunctionGrants[norm]); g != nil {
			add(m, "grants", g)
		}
		addStr(m, "comment", b.live.FunctionComments[norm])
		b.owner(m, "proc:"+norm)
		add(blk, key, m)
	}
}

var procInRe = regexp.MustCompile(`(^|, )IN `)

func kindOf(r *diff.LiveRoutine) string {
	if r.Procedure {
		return "procedure"
	}
	return "function"
}

func addSet(m *node, config map[string]string) {
	if len(config) == 0 {
		return
	}
	set := newMap()
	for _, k := range sortedKeys(config) {
		add(set, k, str(config[k]))
	}
	add(m, "set", set)
}

func (b *builder) tables() {
	for _, fq := range sortedKeys(b.live.Tables) {
		lt := b.live.Tables[fq]
		if lt == nil || fq == b.opts.MigrationsTable || b.member("rel", fq) {
			continue
		}
		blk, name, ok := b.block(fq)
		if !ok {
			continue
		}
		t := newMap()
		parent := b.d.PartitionOf[fq]
		if parent == "" {
			add(t, "columns", b.columns(fq, lt))
			if pk := pkColumns(lt); len(pk) > 0 {
				add(t, "primaryKey", list(pk))
			}
		} else {
			add(t, "partitionOf", str(parent))
			b.partitionBound(t, fq)
		}
		if key := b.d.PartitionKeys[fq]; key != "" {
			add(t, "partitionBy", partitionBy(key))
		}
		if lt.Unlogged {
			add(t, "unlogged", boolean(true))
		}
		if len(lt.Options) > 0 {
			w := flowMap()
			for _, k := range sortedKeys(lt.Options) {
				add(w, k, str(lt.Options[k]))
			}
			add(t, "with", w)
		}
		b.indexes(t, fq, lt)
		b.constraints(t, fq, lt)
		b.triggers(t, fq, lt)
		b.rules(t, lt)
		if lt.RLSEnabled {
			add(t, "rowLevelSecurity", boolean(true))
		}
		b.policies(t, lt)
		if g := grantsNode(b.live.TableGrants[fq]); g != nil {
			add(t, "grants", g)
		}
		addStr(t, "comment", b.live.RelComments[fq])
		b.owner(t, "rel:"+fq)
		add(blk, "table "+name, t)
	}
}

func (b *builder) columns(fq string, lt *diff.LiveTable) *node {
	cols := newMap()
	for _, cn := range b.d.ColumnOrder[fq] {
		lc := lt.Columns[cn]
		if lc == nil {
			continue
		}
		c := flowMap()
		serial := b.serials[fq+"."+cn]
		if serial != "" {
			add(c, "type", str(serial))
		} else {
			add(c, "type", str(lc.Type))
		}
		addStr(c, "collate", lc.Collation)
		addStr(c, "compression", lc.Compression)
		if lc.Nullable {
			add(c, "nullable", boolean(true))
		}
		switch {
		case lc.Generated != "":
			add(c, "generated", str(lc.Generated))
			if lc.Virtual {
				add(c, "virtual", boolean(true))
			}
		case lc.Identity != "":
			add(c, "identity", str(lc.Identity))
		case serial == "" && lc.Default != "":
			add(c, "default", str(lc.Default))
		}
		if g := grantsNode(b.live.ColumnGrants[fq][cn]); g != nil {
			add(c, "grants", g)
		}
		addStr(c, "comment", lc.Comment)
		add(cols, cn, c)
	}
	return cols
}

var (
	pkRe       = regexp.MustCompile(`(?s)^PRIMARY KEY \((.*)\)`)
	fkRe       = regexp.MustCompile(`(?s)^FOREIGN KEY \((.*?)\) REFERENCES (.+?)\((.*?)\)(.*)$`)
	uniqueRe   = regexp.MustCompile(`(?s)^UNIQUE( NULLS NOT DISTINCT)? \((.*?)\)(.*)$`)
	checkRe    = regexp.MustCompile(`(?s)^CHECK (.*?)( NO INHERIT)?( NOT VALID)?( NOT ENFORCED)?$`)
	fkActionRe = regexp.MustCompile(`ON (UPDATE|DELETE) (CASCADE|RESTRICT|SET NULL(?: \([^)]*\))?|SET DEFAULT(?: \([^)]*\))?|NO ACTION)`)
)

func pkColumns(lt *diff.LiveTable) []string {
	for _, def := range lt.ConstraintDefs {
		if m := pkRe.FindStringSubmatch(def); m != nil {
			return splitIdents(m[1])
		}
	}
	return nil
}

func (b *builder) constraints(t *node, fq string, lt *diff.LiveTable) {
	fks, cons := newMap(), newMap()
	for _, name := range sortedKeys(lt.ConstraintDefs) {
		if lt.ConstraintInherited[name] {
			continue
		}
		def := lt.ConstraintDefs[name]
		comment := lt.ConstraintComments[name]
		switch {
		case strings.HasPrefix(def, "PRIMARY KEY"), strings.HasPrefix(def, "NOT NULL"):
		case strings.HasPrefix(def, "FOREIGN KEY"):
			m := fkRe.FindStringSubmatch(def)
			if m == nil {
				b.warn("table %s: cannot parse foreign key %s: %s", fq, name, def)
				continue
			}
			fk := newMap()
			add(fk, "columns", list(splitIdents(m[1])))
			ref := flowMap()
			add(ref, "table", str(qualifyRef(strings.ReplaceAll(m[2], `"`, ""), fq)))
			add(ref, "columns", list(splitIdents(m[3])))
			add(fk, "references", ref)
			tail := m[4]
			if mm := regexp.MustCompile(`MATCH (FULL|PARTIAL)`).FindStringSubmatch(tail); mm != nil {
				add(fk, "match", str(strings.ToLower(mm[1])))
			}
			for _, a := range fkActionRe.FindAllStringSubmatch(tail, -1) {
				key := "onDelete"
				if a[1] == "UPDATE" {
					key = "onUpdate"
				}
				add(fk, key, str(strings.ToLower(a[2])))
			}
			deferral(fk, tail)
			if strings.Contains(tail, "NOT ENFORCED") {
				add(fk, "enforced", boolean(false))
			} else if strings.Contains(tail, "NOT VALID") {
				add(fk, "notValid", boolean(true))
			}
			addStr(fk, "comment", comment)
			add(fks, name, fk)
		case strings.HasPrefix(def, "UNIQUE"):
			m := uniqueRe.FindStringSubmatch(def)
			if m == nil {
				b.warn("table %s: cannot parse constraint %s: %s", fq, name, def)
				continue
			}
			c := newMap()
			add(c, "type", str("unique"))
			add(c, "columns", list(splitIdents(m[2])))
			if m[1] != "" {
				add(c, "nullsNotDistinct", boolean(true))
			}
			deferral(c, m[3])
			addStr(c, "comment", comment)
			add(cons, name, c)
		case strings.HasPrefix(def, "CHECK"):
			m := checkRe.FindStringSubmatch(def)
			c := newMap()
			add(c, "type", str("check"))
			add(c, "expression", str(stripParens(m[1])))
			if m[2] != "" {
				add(c, "noInherit", boolean(true))
			}
			if m[4] != "" {
				add(c, "enforced", boolean(false))
			} else if m[3] != "" {
				add(c, "notValid", boolean(true))
			}
			addStr(c, "comment", comment)
			add(cons, name, c)
		case strings.HasPrefix(def, "EXCLUDE"):
			c := newMap()
			add(c, "type", str("exclude"))
			expr, tail := def[len("EXCLUDE "):], ""
			for _, s := range []string{" DEFERRABLE INITIALLY DEFERRED", " DEFERRABLE"} {
				if strings.HasSuffix(expr, s) {
					expr, tail = strings.TrimSuffix(expr, s), s
					break
				}
			}
			add(c, "expression", str(expr))
			deferral(c, tail)
			addStr(c, "comment", comment)
			add(cons, name, c)
		default:
			b.warn("table %s: constraint %s not pulled: %s", fq, name, def)
		}
	}
	if len(fks.Content) > 0 {
		add(t, "foreignKeys", fks)
	}
	if len(cons.Content) > 0 {
		add(t, "constraints", cons)
	}
}

func deferral(m *node, tail string) {
	switch {
	case strings.Contains(tail, "INITIALLY DEFERRED"):
		add(m, "initiallyDeferred", boolean(true))
	case strings.Contains(tail, "DEFERRABLE"):
		add(m, "deferrable", boolean(true))
	}
}

func (b *builder) indexes(t *node, fq string, lt *diff.LiveTable) {
	idx := newMap()
	for _, name := range sortedKeys(lt.IndexDefs) {
		li := lt.IndexDefs[name]
		if li == nil || li.Constraint || li.Inherited {
			continue
		}
		m := newMap()
		cols := make([]string, len(li.Keys))
		opcs := lt.IndexOpclasses[name]
		var perCol *node
		for i, k := range li.Keys {
			col := k
			if li.Descending[i] {
				col += " desc"
				if !li.NullsFirst[i] {
					col += " nulls last"
				}
			} else if li.NullsFirst[i] {
				col += " nulls first"
			}
			cols[i] = col
			if i < len(opcs) && !opcs[i].Default {
				if perCol == nil {
					perCol = flowMap()
				}
				add(perCol, col, str(opcs[i].Name))
			}
		}
		add(m, "columns", list(cols))
		if li.Unique {
			add(m, "unique", boolean(true))
		}
		if li.Method != "btree" {
			add(m, "using", str(li.Method))
		}
		if perCol != nil {
			add(m, "opclasses", perCol)
		}
		if len(li.Include) > 0 {
			add(m, "include", list(li.Include))
		}
		if li.NullsNotDistinct {
			add(m, "nullsNotDistinct", boolean(true))
		}
		if len(li.Options) > 0 {
			w := flowMap()
			for _, k := range sortedKeys(li.Options) {
				add(w, k, str(li.Options[k]))
			}
			add(m, "with", w)
		}
		addStr(m, "where", li.Predicate)
		addStr(m, "comment", lt.IndexComments[name])
		add(idx, name, m)
	}
	if len(idx.Content) > 0 {
		add(t, "indexes", idx)
	}
}

var triggerRe = regexp.MustCompile(`(?s)^CREATE (CONSTRAINT )?TRIGGER (\S+) (BEFORE|AFTER|INSTEAD OF) (.+?) ON (\S+)(.*?) FOR EACH (ROW|STATEMENT)(?: WHEN \((.*)\))? EXECUTE (?:FUNCTION|PROCEDURE) (.+)$`)

func (b *builder) triggers(t *node, fq string, lt *diff.LiveTable) {
	trs := newMap()
	for _, name := range sortedKeys(lt.Triggers) {
		if lt.TriggerInherited[name] {
			continue
		}
		m := triggerRe.FindStringSubmatch(lt.Triggers[name])
		if m == nil {
			b.warn("table %s: cannot parse trigger %s: %s", fq, name, lt.Triggers[name])
			continue
		}
		tr := newMap()
		if m[1] != "" {
			add(tr, "constraint", boolean(true))
		}
		add(tr, "timing", str(strings.ToLower(m[3])))
		events := []string{}
		for _, e := range strings.Split(m[4], " OR ") {
			e = strings.TrimSpace(e)
			if strings.HasPrefix(e, "UPDATE OF ") {
				events = append(events, "update of "+e[len("UPDATE OF "):])
			} else {
				events = append(events, strings.ToLower(e))
			}
		}
		add(tr, "events", list(events))
		add(tr, "level", str(strings.ToLower(m[7])))
		middle := m[6]
		if mm := regexp.MustCompile(`OLD TABLE AS (\S+)`).FindStringSubmatch(middle); mm != nil || strings.Contains(middle, "NEW TABLE AS") {
			ref := flowMap()
			if mm != nil {
				add(ref, "old", str(mm[1]))
			}
			if nn := regexp.MustCompile(`NEW TABLE AS (\S+)`).FindStringSubmatch(middle); nn != nil {
				add(ref, "new", str(nn[1]))
			}
			add(tr, "referencing", ref)
		}
		deferral(tr, middle)
		addStr(tr, "when", m[8])
		add(tr, "procedure", str(qualifyRef(m[9], fq)))
		addStr(tr, "comment", lt.TriggerComments[name])
		add(trs, name, tr)
	}
	if len(trs.Content) > 0 {
		add(t, "triggers", trs)
	}
}

var ruleDoRe = regexp.MustCompile(`(?s)\sDO\s+(INSTEAD\s+|ALSO\s+)?(.*?);?\s*$`)

func (b *builder) rules(t *node, lt *diff.LiveTable) {
	rs := newMap()
	for _, name := range sortedKeys(lt.Rules) {
		lr := lt.Rules[name]
		m := newMap()
		add(m, "event", str(lr.Event))
		if lr.Instead {
			add(m, "instead", boolean(true))
		}
		addStr(m, "where", stripParens(lr.Where))
		if mm := ruleDoRe.FindStringSubmatch(lr.Def); mm != nil {
			add(m, "do", str(strings.Join(strings.Fields(mm[2]), " ")))
		}
		addStr(m, "comment", lt.RuleComments[name])
		add(rs, name, m)
	}
	if len(rs.Content) > 0 {
		add(t, "rules", rs)
	}
}

func (b *builder) policies(t *node, lt *diff.LiveTable) {
	if len(lt.Policies) == 0 {
		return
	}
	ps := newMap()
	for _, name := range sortedKeys(lt.Policies) {
		lp := lt.Policies[name]
		m := newMap()
		if lp.Restrictive {
			add(m, "as", str("restrictive"))
		}
		if lp.Cmd != "all" {
			add(m, "for", str(lp.Cmd))
		}
		if len(lp.Roles) > 0 {
			add(m, "to", list(lp.Roles))
		}
		addStr(m, "using", lp.Using)
		addStr(m, "withCheck", lp.WithCheck)
		addStr(m, "comment", lt.PolicyComments[name])
		add(ps, name, m)
	}
	add(t, "policies", ps)
}

var boundRe = regexp.MustCompile(`(?s)^FOR VALUES (?:FROM \((.*)\) TO \((.*)\)|IN \((.*)\)|WITH \(modulus (\d+), remainder (\d+)\))$`)

func (b *builder) partitionBound(t *node, fq string) {
	bound := b.d.PartitionBounds[fq]
	if bound == "DEFAULT" {
		add(t, "default", boolean(true))
		return
	}
	m := boundRe.FindStringSubmatch(bound)
	if m == nil {
		b.warn("table %s: cannot parse partition bound %s", fq, bound)
		return
	}
	fv := flowMap()
	switch {
	case m[4] != "":
		add(fv, "modulus", num(m[4]))
		add(fv, "remainder", num(m[5]))
	case m[3] != "":
		add(fv, "in", list(boundValues(m[3])))
	default:
		add(fv, "from", list(boundValues(m[1])))
		add(fv, "to", list(boundValues(m[2])))
	}
	add(t, "forValues", fv)
}

func boundValues(s string) []string {
	out := []string{}
	for _, v := range splitTop(s) {
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
			v = strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
		out = append(out, strings.ToLower(v))
	}
	return out
}

func partitionBy(key string) *node {
	m := flowMap()
	strategy, rest, _ := strings.Cut(key, " ")
	add(m, "type", str(strings.ToLower(strategy)))
	rest = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(rest), "("), ")")
	cols := []string{}
	for _, c := range splitTop(rest) {
		cols = append(cols, strings.TrimSpace(c))
	}
	add(m, "columns", list(cols))
	return m
}

func (b *builder) views() {
	for _, k := range sortedKeys(b.d.ViewQueries) {
		mat := b.live.MatViews[k]
		if b.member("rel", k) || (!mat && !b.live.Views[k]) {
			continue
		}
		blk, name, ok := b.block(k)
		if !ok {
			continue
		}
		m := newMap()
		if deps := b.d.ViewDeps[k]; len(deps) > 0 {
			ds := []string{}
			for _, dep := range deps {
				kind := "view "
				if b.live.MatViews[dep] {
					kind = "materialized view "
				}
				ds = append(ds, kind+dep)
			}
			add(m, "dependsOn", list(ds))
		}
		add(m, "query", literal(b.d.ViewQueries[k]))
		key := "view " + name
		if mat {
			key = "materialized view " + name
			if !b.d.Populated[k] {
				add(m, "withData", boolean(false))
			}
			if lt := b.live.MatViewTables[k]; lt != nil {
				b.indexes(m, k, lt)
			}
		} else {
			opts := b.live.ViewOptions[k]
			if opts["security_invoker"] == "true" {
				add(m, "securityInvoker", boolean(true))
			}
			if opts["security_barrier"] == "true" {
				add(m, "securityBarrier", boolean(true))
			}
			addStr(m, "checkOption", opts["check_option"])
			if vt := b.live.ViewTables[k]; vt != nil {
				b.triggers(m, k, vt)
				b.rules(m, vt)
			}
		}
		if g := grantsNode(b.live.TableGrants[k]); g != nil {
			add(m, "grants", g)
		}
		addStr(m, "comment", b.live.RelComments[k])
		b.owner(m, "rel:"+k)
		add(blk, key, m)
	}
}

// database builds database.yml: extensions, roles, default privileges,
// publications and event triggers.
func (b *builder) database() *node {
	doc := newMap()
	if len(b.live.Extensions) > 0 {
		exts := &node{Kind: yaml.SequenceNode}
		for _, name := range sortedKeys(b.live.Extensions) {
			if name == "plpgsql" {
				continue
			}
			le := b.live.ExtensionDefs[name]
			if le == nil || le.Schema == "public" {
				exts.Content = append(exts.Content, str(name))
				continue
			}
			e := flowMap()
			add(e, "name", str(name))
			add(e, "ifNotExists", boolean(true))
			add(e, "schema", str(le.Schema))
			exts.Content = append(exts.Content, e)
		}
		if len(exts.Content) > 0 {
			add(doc, "extensions", exts)
		}
	}
	if b.opts.Roles && len(b.live.RoleDefs) > 0 {
		roles := newMap()
		for _, name := range sortedKeys(b.live.RoleDefs) {
			if name == b.live.CurrentUser || name == "postgres" {
				continue
			}
			lr := b.live.RoleDefs[name]
			r := flowMap()
			for _, a := range []struct {
				key string
				on  bool
			}{{"login", lr.Login}, {"superuser", lr.Superuser}, {"createdb", lr.CreateDB}, {"createrole", lr.CreateRole}, {"replication", lr.Replication}, {"bypassRLS", lr.BypassRLS}} {
				if a.on {
					add(r, a.key, boolean(true))
				}
			}
			if !lr.Inherit {
				add(r, "inherit", boolean(false))
			}
			if lr.ConnectionLimit >= 0 {
				add(r, "connectionLimit", num(strconv.Itoa(lr.ConnectionLimit)))
			}
			if parents := sortedKeys(b.live.RoleMembers[name]); len(parents) > 0 {
				add(r, "inRoles", list(parents))
			}
			addStr(r, "comment", b.live.RoleComments[name])
			add(roles, name, r)
		}
		if len(roles.Content) > 0 {
			add(doc, "roles", roles)
		}
	}
	if dp := b.defaultPrivileges(); dp != nil {
		add(doc, "defaultPrivileges", dp)
	}
	if len(b.live.Publications) > 0 {
		pubs := newMap()
		for _, name := range sortedKeys(b.live.Publications) {
			lp := b.live.Publications[name]
			p := newMap()
			if lp.AllTables {
				add(p, "allTables", boolean(true))
			}
			if len(lp.Tables) > 0 {
				ts := &node{Kind: yaml.SequenceNode}
				for _, t := range sortedKeys(lp.Tables) {
					pt := lp.Tables[t]
					if len(pt.Columns) == 0 && pt.Where == "" {
						ts.Content = append(ts.Content, str(t))
						continue
					}
					e := flowMap()
					add(e, "table", str(t))
					if len(pt.Columns) > 0 {
						add(e, "columns", list(pt.Columns))
					}
					addStr(e, "where", stripParens(pt.Where))
					ts.Content = append(ts.Content, e)
				}
				add(p, "tables", ts)
			}
			if len(lp.Schemas) > 0 {
				add(p, "schemas", list(lp.Schemas))
			}
			if strings.Join(lp.Publish, ",") != "insert,update,delete,truncate" {
				add(p, "publish", list(lp.Publish))
			}
			if lp.ViaRoot {
				add(p, "viaPartitionRoot", boolean(true))
			}
			addStr(p, "comment", lp.Comment)
			add(pubs, name, p)
		}
		add(doc, "publications", pubs)
	}
	ets := newMap()
	for _, name := range sortedKeys(b.live.EventTriggers) {
		le := b.live.EventTriggers[name]
		if le.Extension {
			continue
		}
		e := newMap()
		add(e, "event", str(le.Event))
		if len(le.Tags) > 0 {
			add(e, "tags", list(le.Tags))
		}
		add(e, "function", str(le.Function+"()"))
		if le.Enabled != "origin" {
			add(e, "enabled", str(le.Enabled))
		}
		addStr(e, "comment", le.Comment)
		add(ets, name, e)
	}
	if len(ets.Content) > 0 {
		add(doc, "eventTriggers", ets)
	}
	return doc
}

func (b *builder) defaultPrivileges() *node {
	type scope struct{ role, schema string }
	scopes := map[scope]map[string]map[string]map[string]bool{}
	for key := range b.live.DefaultACLRows {
		parts := strings.SplitN(key, "|", 3)
		if len(parts) != 3 || (parts[1] != "" && !b.wantSchema(parts[1])) {
			continue
		}
		sc := scope{parts[0], parts[1]}
		if scopes[sc] == nil {
			scopes[sc] = map[string]map[string]map[string]bool{}
		}
		grants := map[string]map[string]bool{}
		for r, privs := range b.live.DefaultACLs[key] {
			grants[r] = privs
		}
		if _, implicit := map[string]bool{"functions": true, "types": true}[parts[2]]; implicit && parts[1] == "" && grants["public"] == nil {
			grants["public"] = map[string]bool{}
		}
		scopes[sc][parts[2]] = grants
	}
	if len(scopes) == 0 {
		return nil
	}
	keys := make([]scope, 0, len(scopes))
	for sc := range scopes {
		keys = append(keys, sc)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].role != keys[j].role {
			return keys[i].role < keys[j].role
		}
		return keys[i].schema < keys[j].schema
	})
	seq := &node{Kind: yaml.SequenceNode}
	for _, sc := range keys {
		e := newMap()
		add(e, "for", str(sc.role))
		if sc.schema != "" {
			add(e, "in", str(sc.schema))
		}
		for _, kind := range sortedKeys(scopes[sc]) {
			g := flowMap()
			for _, r := range sortedKeys(scopes[sc][kind]) {
				add(g, r, list(sortedKeys(scopes[sc][kind][r])))
			}
			if len(g.Content) > 0 {
				add(e, kind, g)
			}
		}
		seq.Content = append(seq.Content, e)
	}
	return seq
}

func grantsNode(g map[string]map[string]bool) *node {
	if len(g) == 0 {
		return nil
	}
	m := flowMap()
	for _, r := range sortedKeys(g) {
		add(m, r, list(sortedKeys(g[r])))
	}
	return m
}

// splitIdents splits a constraint column list ("a, \"B\", c") into names.
func splitIdents(s string) []string {
	out := []string{}
	for _, p := range splitTop(s) {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "PERIOD ") {
			p = "period " + strings.Trim(strings.TrimPrefix(p, "PERIOD "), `"`)
		} else if strings.HasSuffix(p, " WITHOUT OVERLAPS") {
			p = strings.Trim(strings.TrimSuffix(p, " WITHOUT OVERLAPS"), `"`) + " without overlaps"
		} else {
			p = strings.Trim(p, `"`)
		}
		out = append(out, p)
	}
	return out
}

// splitTop splits on commas outside parentheses and quotes.
func splitTop(s string) []string {
	out, depth, start := []string{}, 0, 0
	inQ := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			inQ = !inQ
		case inQ:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if strings.TrimSpace(s[start:]) != "" {
		out = append(out, s[start:])
	}
	return out
}

// stripCheck turns "CHECK ((VALUE > 0))" into "VALUE > 0".
func stripCheck(def string) string {
	def = strings.TrimSpace(strings.TrimPrefix(def, "CHECK"))
	return stripParens(def)
}

// stripParens removes parentheses that wrap the whole expression.
func stripParens(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		depth, wraps := 0, true
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 && i != len(s)-1 {
					wraps = false
				}
			}
		}
		if !wraps {
			break
		}
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// qualifyRef qualifies a referenced name with the referring object's schema
// when PostgreSQL printed it unqualified (it was on the search_path).
func qualifyRef(name, fq string) string {
	base := name
	if i := strings.IndexByte(name, '('); i >= 0 {
		base = name[:i]
	}
	if strings.Contains(base, ".") {
		return name
	}
	return "public." + name
}

func schemaOf(fq string) string {
	s, _, _ := strings.Cut(fq, ".")
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
