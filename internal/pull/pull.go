// Package pull reconstructs pgy schema YAML from an introspected database,
// so an existing database can be adopted without writing YAML by hand. The
// output is meant to diff clean against the database it came from.
package pull

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/suprbdev/pgy/internal/diff"
	"github.com/suprbdev/pgy/internal/schema"
	yaml "gopkg.in/yaml.v3"
)

// Options controls what Build emits.
type Options struct {
	// Schemas limits output to these schemas; empty means every schema.
	Schemas []string
	// ExcludeSchemas leaves these schemas out.
	ExcludeSchemas []string
	// DatabaseSettings writes the database: block (ALTER DATABASE ... SET
	// and ALTER ROLE ... IN DATABASE ... SET) also when Schemas or
	// ExcludeSchemas limits the pull, which otherwise leaves it out.
	DatabaseSettings bool
	// MigrationsTable ("schema.table") is left out; empty means pgy.migrations.
	MigrationsTable string
	// Owners emits owner: for objects not owned by the connecting role.
	Owners bool
	// OwnerKinds lists object kinds, named as the policy kinds
	// (validate.PolicyKinds), whose owner Owners emits also when it is the
	// connecting role, as policy.require_owners asks.
	OwnerKinds map[string]bool
	// Roles emits the cluster's non-system roles.
	Roles bool
	// Data holds rows (from diff.TableRows) to emit as data: blocks, keyed
	// by "schema.table".
	Data map[string][]map[string]any
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
	b.unsupported()
	b.types()
	b.domains()
	b.collations()
	b.textSearch()
	b.sequences()
	b.routines()
	b.aggregates()
	b.tables()
	b.views()
	b.undeclaredMemberGrants()

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
	// identitySeqs: sequence -> "schema.table.column" of the identity column
	// it belongs to
	identitySeqs map[string]string
}

func (b *builder) warn(format string, args ...any) {
	b.res.Warnings = append(b.res.Warnings, fmt.Sprintf(format, args...))
}

func (b *builder) wantSchema(s string) bool {
	if slices.Contains(b.opts.ExcludeSchemas, s) {
		return false
	}
	if len(b.opts.Schemas) == 0 {
		return s != "pgy" || b.schemaHasObjectsBesidesMigrations(s)
	}
	return slices.Contains(b.opts.Schemas, s)
}

// limited reports whether Options.Schemas or Options.ExcludeSchemas leaves
// schemas out, so that database-wide objects are written only as far as
// the pulled schemas need them.
func (b *builder) limited() bool {
	return len(b.opts.Schemas) > 0 || len(b.opts.ExcludeSchemas) > 0
}

// keptExtensions returns the extensions a limited pull writes, or nil when
// every schema is pulled: those installed in a pulled schema, those an
// object of a pulled schema depends on (diff.LiveDetails.ExtensionUsers),
// those owning the language of a languages: entry or the foreign-data
// wrapper of a server or a foreignDataWrappers: entry, which are written
// whatever the schemas, and the extensions these require.
func (b *builder) keptExtensions(languages, wrappers *node) map[string]bool {
	if !b.limited() {
		return nil
	}
	kept := map[string]bool{}
	var keep func(name string)
	keep = func(name string) {
		if name == "" || kept[name] || !b.live.Extensions[name] {
			return
		}
		kept[name] = true
		if le := b.live.ExtensionDefs[name]; le != nil {
			for _, r := range le.Requires {
				keep(r)
			}
		}
	}
	for _, name := range sortedKeys(b.live.Extensions) {
		if le := b.live.ExtensionDefs[name]; le != nil && b.wantSchema(le.Schema) {
			keep(name)
		}
		for s := range b.d.ExtensionUsers[name] {
			if b.wantSchema(s) {
				keep(name)
			}
		}
	}
	for _, ls := range b.live.Servers {
		if ls != nil {
			keep(b.live.MemberExtension["fdw:"+ls.Wrapper])
		}
	}
	for _, n := range []struct {
		kind string
		m    *node
	}{{"language:", languages}, {"fdw:", wrappers}} {
		if n.m == nil {
			continue
		}
		for i := 0; i < len(n.m.Content); i += 2 {
			keep(b.live.MemberExtension[n.kind+n.m.Content[i].Value])
		}
	}
	return kept
}

// keptPublication reports whether a limited pull writes publication name:
// one that publishes a table or schema of the pulled schemas and nothing
// else. A publication of tables in other schemas as well is left out with
// a warning, since a rebuild could not create it and leaving the other
// tables out would remove them from it. FOR ALL TABLES publications are
// database-wide and left out.
func (b *builder) keptPublication(name string, lp *diff.LivePublication) bool {
	if !b.limited() {
		return true
	}
	if lp.AllTables {
		return false
	}
	pulled, other := false, []string{}
	for _, t := range sortedKeys(lp.Tables) {
		if b.wantSchema(schemaOf(t)) {
			pulled = true
		} else {
			other = append(other, t)
		}
	}
	for _, s := range lp.Schemas {
		if b.wantSchema(s) {
			pulled = true
		} else {
			other = append(other, "tables in schema "+s)
		}
	}
	if pulled && len(other) > 0 {
		b.warn("publication %s also publishes %s, outside the pulled schemas, so it was left out", name, strings.Join(other, ", "))
		return false
	}
	return pulled
}

// warnPartialBlock warns that a limited pull wrote a block of kind (which
// --unsafe --prune treats as the complete list) without the objects in
// left, which pgy diff --unsafe --prune would then drop.
func (b *builder) warnPartialBlock(block, kind string, left []string) {
	if len(left) > 0 {
		b.warn("%s lists only the %s of the pulled schemas; pgy diff --unsafe --prune with this YAML would drop the others: %s", block, kind, strings.Join(left, ", "))
	}
}

func (b *builder) schemaHasObjectsBesidesMigrations(s string) bool {
	for k := range b.live.Tables {
		if strings.HasPrefix(k, s+".") && k != b.opts.MigrationsTable {
			return true
		}
	}
	return false
}

// unsupported warns about objects in the pulled schemas that pgy cannot
// express, so the YAML is known to be incomplete.
func (b *builder) unsupported() {
	if b.live.Details == nil {
		return
	}
	for _, o := range b.live.Details.Unsupported {
		switch {
		case o.Schema == "":
			b.warn("%s %s is not supported by pgy and was left out", o.Kind, o.Name)
		case !b.wantSchema(o.Schema):
		case o.Kind == "cast":
			b.warn("cast %s is not supported by pgy and was left out", o.Name)
		default:
			b.warn("%s %s.%s is not supported by pgy and was left out", o.Kind, o.Schema, o.Name)
		}
	}
}

func (b *builder) block(fq string) (*node, string, bool) {
	s, name, ok := strings.Cut(fq, ".")
	if !ok || !b.wantSchema(s) || diff.InDottedSchema(b.live, fq) {
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

// memberGrants returns the body of a declaration that manages the
// privileges of the extension member key (a diff.Live.ExtensionMembers key):
// the grantees whose privileges in current are not the ones CREATE EXTENSION
// left, which is what pg_dump keeps of an extension's objects. It is nil
// when the privileges are unchanged. publicDefault is the privilege PUBLIC
// has by default on objects of that kind ("" for none); losing it is
// written as revokePublic.
func (b *builder) memberGrants(key, publicDefault string, current map[string]map[string]bool) *node {
	initial, ok := b.live.InitialGrants(key, publicDefault)
	if !ok {
		return nil
	}
	changes := diff.GrantChanges(current, initial)
	if len(changes) == 0 {
		return nil
	}
	m := newMap()
	if privs, ok := changes["public"]; ok && len(privs) == 0 && publicDefault != "" {
		add(m, "revokePublic", boolean(true))
		delete(changes, "public")
	}
	if g := grantsNode(changes); g != nil {
		add(m, "grants", g)
	}
	return m
}

// undeclaredMemberGrants warns about the tables, sequences and aggregates of
// extensions whose privileges changed: pgy has no declaration that manages
// an object of those kinds without creating it.
func (b *builder) undeclaredMemberGrants() {
	report := func(kind, key, name, publicDefault string, current map[string]map[string]bool) {
		if b.wantSchema(schemaOf(name)) && b.memberGrants(key, publicDefault, current) != nil {
			b.warn("%s %s belongs to an extension and its privileges are no longer the ones the extension set; pgy cannot declare them and they were left out", kind, name)
		}
	}
	for _, k := range sortedKeys(b.live.Tables) {
		kind := "table"
		if lt := b.live.Tables[k]; lt != nil && lt.Foreign != nil {
			kind = "foreign table"
		}
		report(kind, "rel:"+k, k, "", b.live.TableGrants[k])
	}
	for _, k := range sortedKeys(b.live.Sequences) {
		report("sequence", "rel:"+k, k, "", b.live.TableGrants[k])
	}
	for _, norm := range sortedKeys(b.live.AggregateDefs) {
		if lf := b.live.FunctionDefs[norm]; lf != nil {
			report("aggregate", "proc:"+norm, lf.Signature, "execute", diff.WithPublicDefault(b.live.FunctionGrants[norm], "execute", b.live.FunctionPublicExec[norm]))
		}
	}
}

// memberRoutine declares a function or procedure of an extension without a
// body when its privileges changed.
func (b *builder) memberRoutine(norm string, r *diff.LiveRoutine) {
	blk, _, ok := b.block(r.Schema + "." + r.Name)
	if !ok {
		return
	}
	m := b.memberGrants("proc:"+norm, "execute", diff.WithPublicDefault(b.live.FunctionGrants[norm], "execute", b.live.FunctionPublicExec[norm]))
	if m == nil {
		return
	}
	sig := ""
	if lf := b.live.FunctionDefs[norm]; lf != nil {
		sig = lf.Signature
	} else if lp := b.live.ProcedureDefs[norm]; lp != nil {
		sig = lp.Signature
	}
	if i := strings.IndexByte(sig, '('); i >= 0 {
		add(blk, kindOf(r)+" "+schema.QuoteName(r.Name)+sig[i:], m)
	}
}

// memberView declares a view or materialized view of an extension without a
// query when its privileges changed.
func (b *builder) memberView(k string, materialized bool) {
	blk, name, ok := b.block(k)
	if !ok {
		return
	}
	if m := b.memberGrants("rel:"+k, "", b.live.TableGrants[k]); m != nil {
		kind := "view "
		if materialized {
			kind = "materialized view "
		}
		add(blk, kind+schema.QuoteName(name), m)
	}
}

// memberType declares a type or domain of an extension without a kind when
// its privileges changed.
func (b *builder) memberType(k string) {
	blk, name, ok := b.block(k)
	if !ok {
		return
	}
	if m := b.memberGrants("type:"+k, "usage", diff.WithPublicDefault(b.live.TypeGrants[k], "usage", !b.live.TypePublicRevoked[k])); m != nil {
		add(blk, "type "+schema.QuoteName(name), m)
	}
}

// owner adds the owner of the object with Live.Owners key key, of policy
// kind kind, to m (see addOwner).
func (b *builder) owner(m *node, kind, key string) {
	b.addOwner(m, kind, b.live.Owners[key])
}

// addOwner adds owner: o to m with Options.Owners, unless o is the
// connecting role and Options.OwnerKinds does not list kind.
func (b *builder) addOwner(m *node, kind, o string) {
	if b.opts.Owners && o != "" && (o != b.live.CurrentUser || b.opts.OwnerKinds[kind]) {
		add(m, "owner", str(o))
	}
}

func (b *builder) schemas() {
	b.declaredSchema = map[string]bool{}
	for _, s := range sortedKeys(b.live.Schemas) {
		if s == "pg_toast" || !b.wantSchema(s) {
			continue
		}
		if strings.Contains(s, ".") {
			b.warn("schema %s was left out with its objects: pgy reads a dot as the separator between a schema and an object's name, so it cannot manage a schema whose name contains one", s)
			continue
		}
		blk := newMap()
		b.owner(blk, "schemas", "schema:"+s)
		if c := b.live.SchemaComments[s]; c != "" && (s != "public" || c != "standard public schema") {
			add(blk, "comment", str(c))
		}
		if g := grantsNode(b.live.SchemaGrants[s]); g != nil {
			add(blk, "grants", g)
		}
		if b.extensionSchema(s) && len(blk.Content) == 0 {
			continue
		}
		b.blocks[s] = blk
		b.declaredSchema[s] = s != "public"
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

// serialSequenceName reports whether seq is the name PostgreSQL gives the
// sequence of serial column cn of table fq: <table>_<column>_seq, which it
// shortens, or numbers on a clash, when that is taken or too long.
func serialSequenceName(fq, cn, seq string) bool {
	schema, table, _ := strings.Cut(fq, ".")
	want := schema + "." + table + "_" + cn + "_seq"
	if seq == want {
		return true
	}
	seqSchema, name, _ := strings.Cut(seq, ".")
	if seqSchema != schema || !strings.HasPrefix(name, table) && len(table+"_"+cn+"_seq") <= 63 {
		return false
	}
	return regexp.MustCompile(`_seq\d*$`).MatchString(name)
}

// serialShaped reports whether ls has the type and options a serial column
// of type typ gives its sequence, so that declaring the column serial
// recreates it exactly.
func serialShaped(ls *diff.LiveSequence, typ string) bool {
	max := map[string]string{"integer": "2147483647", "bigint": "9223372036854775807", "smallint": "32767"}[typ]
	return ls.Type == typ && ls.Increment == "1" && ls.Start == "1" && ls.MinValue == "1" && ls.MaxValue == max && ls.Cache == "1" && !ls.Cycle && !ls.Unlogged
}

func (b *builder) collectSerials() {
	b.serials, b.serialSeqs, b.identitySeqs = map[string]string{}, map[string]bool{}, map[string]string{}
	nextvalRe := regexp.MustCompile(`^nextval\('("?[^']+?"?)'::regclass\)$`)
	for fq, lt := range b.live.Tables {
		for cn, lc := range lt.Columns {
			if lc.Identity != "" && lc.IdentitySequence != nil {
				b.identitySeqs[lc.IdentitySequence.Name] = fq + "." + cn
			}
			m := nextvalRe.FindStringSubmatch(strings.TrimSpace(lc.Default))
			if m == nil {
				continue
			}
			seq := strings.ReplaceAll(m[1], `"`, "")
			if !strings.Contains(seq, ".") {
				seq = schemaOf(fq) + "." + seq
			}
			ls := b.live.SequenceDefs[seq]
			if ls == nil || ls.Owner != fq+"."+cn || lc.Nullable || !serialSequenceName(fq, cn, seq) {
				continue
			}
			kind := map[string]string{"integer": "serial", "bigint": "bigserial", "smallint": "smallserial"}[lc.Type]
			if kind == "" || !serialShaped(ls, lc.Type) {
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
			b.memberType(k)
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
				addStr(spec, "collate", b.live.CompositeCollations[k][a])
				addStr(spec, "comment", b.live.CompositeComments[k][a])
				add(attrs, a, spec)
			}
			add(t, "attributes", attrs)
		default:
			continue
		}
		if g := b.grantsFor(b.live.TypeGrants[k], k, "types"); g != nil {
			add(t, "grants", g)
		}
		if b.live.TypePublicRevoked[k] {
			add(t, "revokePublic", boolean(true))
		}
		addStr(t, "comment", b.live.TypeComments[k])
		b.owner(t, "types", "type:"+k)
		add(blk, "type "+schema.QuoteName(name), t)
	}
	for _, k := range sortedKeys(b.live.BaseTypes) {
		if b.member("type", k) {
			b.memberType(k)
		}
	}
}

func (b *builder) domains() {
	for _, k := range sortedKeys(b.live.Domains) {
		if b.member("type", k) {
			b.memberType(k)
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
				if ld.NotNullName != "" && ld.NotNullName != name+"_not_null" {
					add(m, "notNullName", str(ld.NotNullName))
				}
			}
			names := sortedKeys(ld.Checks)
			if len(names) > 1 || len(ld.CheckComments) > 0 {
				cs := newMap()
				for _, n := range names {
					if c := ld.CheckComments[n]; c != "" {
						e := flowMap()
						add(e, "check", str(stripCheck(ld.Checks[n])))
						add(e, "comment", str(c))
						add(cs, n, e)
						continue
					}
					add(cs, n, str(stripCheck(ld.Checks[n])))
				}
				add(m, "checks", cs)
			} else if len(names) == 1 {
				add(m, "check", str(stripCheck(ld.Checks[names[0]])))
				if names[0] != name+"_check" {
					add(m, "constraintName", str(names[0]))
				}
			}
		}
		if g := b.grantsFor(b.live.TypeGrants[k], k, "types"); g != nil {
			add(m, "grants", g)
		}
		if b.live.TypePublicRevoked[k] {
			add(m, "revokePublic", boolean(true))
		}
		addStr(m, "comment", b.live.TypeComments[k])
		b.owner(m, "domains", "type:"+k)
		add(blk, "domain "+schema.QuoteName(name), m)
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
		add(blk, "collation "+schema.QuoteName(name), m)
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
		opts := flowMap()
		for _, o := range inOrder(td.Options, td.OptionOrder) {
			add(opts, o, str(td.Options[o]))
		}
		add(m, "options", opts)
		addStr(m, "comment", b.live.TSComments["dict:"+k])
		add(blk, "text search dictionary "+schema.QuoteName(name), m)
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
			add(maps, tok, list(b.references(b.live.TSConfigs[k][tok])))
		}
		add(m, "mappings", maps)
		addStr(m, "comment", b.live.TSComments["config:"+k])
		add(blk, "text search configuration "+schema.QuoteName(name), m)
	}
}

// identityOptionsNode returns {generated, ...} for identity column cn of
// table fq when its sequence's name or options differ from the defaults, or
// nil.
func identityOptionsNode(fq, cn string, lc *diff.LiveColumn) *node {
	ls := lc.IdentitySequence
	if ls == nil {
		return nil
	}
	m := flowMap()
	if s, table, _ := strings.Cut(fq, "."); ls.Name != "" && ls.Name != s+"."+table+"_"+cn+"_seq" {
		add(m, "sequenceName", str(strings.TrimPrefix(ls.Name, s+".")))
	}
	typeMax := map[string]string{"smallint": "32767", "integer": "2147483647", "bigint": "9223372036854775807"}[ls.Type]
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
	if len(m.Content) == 0 {
		return nil
	}
	m.Content = append([]*node{{Kind: yaml.ScalarNode, Value: "generated"}, str(lc.Identity)}, m.Content...)
	return m
}

func (b *builder) sequences() {
	for _, k := range sortedKeys(b.live.Sequences) {
		ls := b.live.SequenceDefs[k]
		if ls == nil || b.member("rel", k) {
			continue
		}
		if b.serialSeqs[k] {
			b.columnSequence(k, ls.Owner)
			continue
		}
		if column := b.identitySeqs[k]; column != "" {
			b.columnSequence(k, column)
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
		if ls.Unlogged {
			add(m, "unlogged", boolean(true))
		}
		if ls.Owner != "" {
			table, column := b.ownerColumn(ls.Owner)
			add(m, "ownedBy", str(schema.QualifiedName(table)+"."+schema.QuoteName(column)))
		}
		if g := b.grantsFor(b.live.TableGrants[k], k, "sequences"); g != nil {
			add(m, "grants", g)
		}
		addStr(m, "comment", b.live.RelComments[k])
		b.owner(m, "sequences", "rel:"+k)
		add(blk, "sequence "+schema.QuoteName(name), m)
	}
}

// columnSequence writes the grants and comment of the sequence of a serial
// or identity column ("schema.table.column"), which the column creates, as
// a sequence block that depends on the column's table.
func (b *builder) columnSequence(k, column string) {
	g := b.grantsFor(b.live.TableGrants[k], k, "sequences")
	comment := b.live.RelComments[k]
	if g == nil && comment == "" {
		return
	}
	blk, name, ok := b.block(k)
	if !ok {
		return
	}
	m := newMap()
	if g != nil {
		add(m, "grants", g)
	}
	addStr(m, "comment", comment)
	if strings.Count(column, ".") >= 2 {
		table, _ := b.ownerColumn(column)
		add(m, "dependsOn", list([]string{"table " + schema.QualifiedName(table)}))
	}
	add(blk, "sequence "+schema.QuoteName(name), m)
}

func (b *builder) routines() {
	for _, norm := range sortedKeys(b.d.Routines) {
		r := b.d.Routines[norm]
		if b.member("proc", norm) {
			b.memberRoutine(norm, r)
			continue
		}
		blk, _, ok := b.block(r.Schema + "." + r.Name)
		if !ok {
			continue
		}
		if diff.CompiledLanguage(r.Language) {
			b.warn("%s %s.%s is written in %s and cannot be pulled", kindOf(r), r.Schema, r.Name, strings.ToLower(r.Language))
			continue
		}
		m := newMap()
		args := r.Arguments
		if r.Procedure {
			args = procInRe.ReplaceAllString(args, "$1")
		}
		key := fmt.Sprintf("%s %s(%s)", kindOf(r), schema.QuoteName(r.Name), args)
		if r.Procedure {
			lp := b.live.ProcedureDefs[norm]
			if lp == nil {
				continue
			}
			add(m, "language", str(r.Language))
			b.addTransform(m, r.Language, lp.Transform)
			if lp.Security == "definer" {
				add(m, "security", str("definer"))
			}
			addSet(m, lp.Config, lp.ConfigOrder, lp.Security == "definer")
			add(m, "body", routineBody(lp.Body, lp.SQLBody))
			if deps := b.d.RoutineDeps[norm]; len(deps) > 0 {
				add(m, "dependsOn", list(dependencies(deps)))
			}
		} else {
			lf := b.live.FunctionDefs[norm]
			if lf == nil || lf.Kind != "f" {
				continue
			}
			add(m, "returns", str(r.Returns))
			add(m, "language", str(r.Language))
			b.addTransform(m, r.Language, lf.Transform)
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
			if lf.Parallel != "" && lf.Parallel != "unsafe" {
				add(m, "parallel", str(lf.Parallel))
			}
			if lf.Cost != 0 && lf.Cost != 100 {
				add(m, "cost", floatNum(lf.Cost))
			}
			if lf.Rows != 0 && lf.Rows != 1000 {
				add(m, "rows", floatNum(lf.Rows))
			}
			addSet(m, lf.Config, lf.ConfigOrder, lf.Security == "definer")
			add(m, "body", routineBody(lf.Body, lf.SQLBody))
			if deps := b.d.RoutineDeps[norm]; len(deps) > 0 {
				add(m, "dependsOn", list(dependencies(deps)))
			}
		}
		if !b.live.FunctionPublicExec[norm] {
			add(m, "revokePublic", boolean(true))
		}
		if g := b.grantsFor(b.live.FunctionGrants[norm], r.Schema+"."+r.Name, "functions"); g != nil {
			add(m, "grants", g)
		}
		addStr(m, "comment", b.live.FunctionComments[norm])
		b.owner(m, kindOf(r)+"s", "proc:"+norm)
		add(blk, key, m)
	}
}

// routineBody renders a routine's stored body. A string body is written so
// that pgy recreates it byte for byte; a SQL-standard one is PostgreSQL's
// deparsed form, which has no original spelling to keep.
func routineBody(src string, sqlBody bool) *node {
	if sqlBody {
		return literal(src)
	}
	return exact(diff.DeclaredRoutineBody(src))
}

// addTransform writes a routine's transform types. A routine without any
// gets an empty list when its language has transforms, so that one added
// later is noticed; elsewhere the key is left out.
func (b *builder) addTransform(m *node, language string, types []string) {
	if len(types) > 0 || b.d.TransformLanguages[language] {
		add(m, "transform", list(types))
	}
}

var procInRe = regexp.MustCompile(`(^|, )IN `)

var aggregateArgKeywordRe = regexp.MustCompile(`\b(ORDER BY|VARIADIC) `)

func (b *builder) aggregates() {
	for _, norm := range sortedKeys(b.live.AggregateDefs) {
		la := b.live.AggregateDefs[norm]
		lf := b.live.FunctionDefs[norm]
		if lf == nil || b.member("proc", norm) {
			continue
		}
		name := aggregateArgKeywordRe.ReplaceAllStringFunc(lf.Signature, strings.ToLower)
		args := "(*)"
		if i := strings.IndexByte(name, '('); i >= 0 {
			if a := name[i:]; a != "()" {
				args = a
			}
			name = name[:i]
		}
		blk, short, ok := b.block(name)
		if !ok {
			continue
		}
		m := newMap()
		add(m, "sfunc", str(la.SFunc))
		add(m, "stype", str(la.SType))
		addStr(m, "finalfunc", la.FinalFunc)
		addStr(m, "combinefunc", la.CombineFunc)
		if la.HasInitCond {
			add(m, "initcond", str(la.InitCond))
		}
		if la.Parallel != "unsafe" {
			add(m, "parallel", str(la.Parallel))
		}
		for _, o := range schema.AggregateOptions {
			v := la.Options[o[1]]
			switch {
			case strings.HasSuffix(o[1], "_extra") || o[1] == "hypothetical":
				if v == "true" {
					add(m, o[0], boolean(true))
				}
			case strings.HasSuffix(o[1], "_modify"):
				if v != "read_only" {
					addStr(m, o[0], v)
				}
			case strings.HasSuffix(o[1], "space"):
				if v != "0" && v != "" {
					add(m, o[0], num(v))
				}
			case o[1] == "minitcond":
				if la.HasMInitCond {
					add(m, o[0], str(v))
				}
			default:
				addStr(m, o[0], v)
			}
		}
		addStr(m, "comment", b.live.FunctionComments[norm])
		b.owner(m, "aggregates", "proc:"+norm)
		add(blk, "aggregate "+schema.QuoteName(short)+args, m)
	}
}

func kindOf(r *diff.LiveRoutine) string {
	if r.Procedure {
		return "procedure"
	}
	return "function"
}

// addSet writes a routine's settings, those named in order first and in
// that order. A SECURITY DEFINER routine without any gets an empty map, so
// that one set on it later, such as a search_path, is noticed and reset;
// elsewhere the key is left out and the routine's settings are unmanaged.
func addSet(m *node, config map[string]string, order []string, definer bool) {
	if len(config) == 0 && !definer {
		return
	}
	set := newMap()
	for _, k := range inOrder(config, order) {
		add(set, k, setting(k, config[k]))
	}
	add(m, "set", set)
}

// inOrder returns the keys of m, those named in order first and in that
// order, then the others sorted.
func inOrder(m map[string]string, order []string) []string {
	out := make([]string, 0, len(m))
	seen := map[string]bool{}
	for _, k := range append(append([]string{}, order...), sortedKeys(m)...) {
		if _, ok := m[k]; ok && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// setting is the YAML value of a stored setting of the parameter name. A
// value that has to be written as an SQL string literal is double-quoted in
// the YAML, which keeps the literal's own quotes readable.
func setting(name, value string) *node {
	n := str(diff.ConfigValueYAML(name, value))
	if n.Value != value {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
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
		if lt.Foreign != nil {
			add(t, "server", str(lt.Foreign.Server))
			add(t, "options", stringsNode(lt.Foreign.Options))
		}
		if parent == "" {
			if len(lt.Inherits) > 0 {
				add(t, "inherits", list(qualifiedNames(lt.Inherits)))
			}
			if lt.OfType != "" {
				add(t, "of", str(schema.QualifiedName(lt.OfType)))
				if cols := b.columns(fq, lt); len(cols.Content) > 0 {
					add(t, "columns", cols)
				}
			} else {
				add(t, "columns", b.columns(fq, lt))
			}
			if pk := pkColumns(lt); len(pk) > 0 {
				name := diff.LivePrimaryKeyName(lt)
				li := lt.IndexDefs[name]
				params := li != nil && (len(li.Include) > 0 || len(li.Options) > 0 || li.Tablespace != "")
				if note := lt.ConstraintComments[name]; name != "" && (name != diff.DefaultPrimaryKeyName(fq) || note != "" || params) {
					cols := flowMap()
					add(cols, "columns", list(pk))
					indexParameters(cols, li)
					addStr(cols, "comment", note)
					m := newMap()
					add(m, name, cols)
					add(t, "primaryKey", m)
				} else {
					add(t, "primaryKey", list(pk))
				}
			}
		} else {
			add(t, "partitionOf", str(schema.QualifiedName(parent)))
			b.partitionBound(t, fq)
			if cols := b.columns(fq, lt); len(cols.Content) > 0 {
				add(t, "columns", cols)
			}
		}
		if key := b.d.PartitionKeys[fq]; key != "" {
			add(t, "partitionBy", partitionBy(key))
		}
		if lt.Unlogged {
			add(t, "unlogged", boolean(true))
		}
		if len(lt.Options) > 0 {
			add(t, "with", storageParams(lt.Options, lt.OptionOrder))
		}
		if lt.Foreign == nil {
			addStr(t, "tablespace", lt.Tablespace)
		}
		b.data(t, fq, lt)
		b.indexes(t, fq, lt)
		b.constraints(t, fq, lt)
		b.triggers(t, fq, lt)
		b.rules(t, lt)
		b.statistics(t, fq, lt)
		if ri := lt.ReplicaIdentity; ri != "" && ri != "default" && lt.Foreign == nil {
			add(t, "replicaIdentity", str(ri))
		}
		addStr(t, "clusterOn", lt.ClusteredOn)
		if lt.RLSEnabled {
			add(t, "rowLevelSecurity", boolean(true))
		}
		if lt.RLSForced {
			add(t, "forceRowLevelSecurity", boolean(true))
		}
		b.policies(t, lt)
		if g := b.grantsFor(b.live.TableGrants[fq], fq, "tables"); g != nil {
			add(t, "grants", g)
		}
		addStr(t, "comment", b.live.RelComments[fq])
		b.owner(t, "tables", "rel:"+fq)
		key := "table "
		if lt.Foreign != nil {
			key = "foreign table "
		}
		add(blk, key+schema.QuoteName(name), t)
	}
}

// stringsNode renders a map of strings as a flow mapping in key order.
func stringsNode(m map[string]string) *node {
	n := flowMap()
	for _, k := range sortedKeys(m) {
		add(n, k, str(m[k]))
	}
	return n
}

// servers renders the foreign servers no extension owns.
func (b *builder) servers() *node {
	if len(b.live.Servers) == 0 {
		return nil
	}
	out := newMap()
	for _, name := range sortedKeys(b.live.Servers) {
		ls := b.live.Servers[name]
		s := newMap()
		add(s, "wrapper", str(ls.Wrapper))
		addStr(s, "type", ls.Type)
		addStr(s, "version", ls.Version)
		add(s, "options", stringsNode(ls.Options))
		b.addOwner(s, "servers", ls.Owner)
		if g := grantsNode(ls.Grants); g != nil {
			add(s, "grants", g)
		}
		addStr(s, "comment", ls.Comment)
		add(out, name, s)
	}
	return out
}

func (b *builder) columns(fq string, lt *diff.LiveTable) *node {
	cols := newMap()
	for _, cn := range b.d.ColumnOrder[fq] {
		lc := lt.Columns[cn]
		if lc == nil {
			continue
		}
		if lc.Inherited || lt.OfType != "" {
			parent := b.parentColumn(fq, lt, cn)
			if lt.OfType != "" {
				parent = &diff.LiveColumn{Nullable: true, Storage: lc.TypeStorage}
			}
			c := inheritedColumnNode(lc, parent)
			if len(lc.FDWOptions) > 0 {
				if c == nil {
					c = flowMap()
				}
				add(c, "options", stringsNode(lc.FDWOptions))
			}
			if c != nil {
				add(cols, cn, c)
			}
			continue
		}
		c := flowMap()
		serial := b.serials[fq+"."+cn]
		if serial != "" {
			add(c, "type", str(serial))
		} else {
			add(c, "type", str(lc.Type))
		}
		if lc.Collation != "" && lc.CollationSchema != "" {
			add(c, "collate", str(schema.QualifiedName(lc.CollationSchema+"."+lc.Collation)))
		} else {
			addStr(c, "collate", schema.QuoteName(lc.Collation))
		}
		if lc.Storage != "" && lc.TypeStorage != "" && lc.Storage != lc.TypeStorage {
			add(c, "storage", str(lc.Storage))
		}
		addStr(c, "compression", lc.Compression)
		if lc.StatisticsTarget != nil {
			add(c, "statisticsTarget", num(strconv.Itoa(*lc.StatisticsTarget)))
		}
		if len(lc.FDWOptions) > 0 {
			add(c, "options", stringsNode(lc.FDWOptions))
		}
		for _, o := range [][2]string{{"n_distinct", "nDistinct"}, {"n_distinct_inherited", "nDistinctInherited"}} {
			if v, err := strconv.ParseFloat(lc.Options[o[0]], 64); err == nil {
				add(c, o[1], floatNum(v))
			}
		}
		if lc.Nullable {
			add(c, "nullable", boolean(true))
		} else {
			if lc.NotNullNotValid != "" {
				add(c, "notValid", boolean(true))
			}
			if lc.NotNullName != "" && lc.NotNullName != diff.DefaultNotNullName(fq, cn) {
				add(c, "notNullName", str(lc.NotNullName))
			}
		}
		switch {
		case lc.Generated != "":
			add(c, "generated", str(lc.Generated))
			if lc.Virtual {
				add(c, "virtual", boolean(true))
			}
		case lc.Identity != "":
			if opts := identityOptionsNode(fq, cn, lc); opts != nil {
				add(c, "identity", opts)
			} else {
				add(c, "identity", str(lc.Identity))
			}
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
	fkRe       = regexp.MustCompile(`(?s)^FOREIGN KEY \((.*?)\) REFERENCES (.+?)\((.*?)\)(.*)$`)
	uniqueRe   = regexp.MustCompile(`(?s)^UNIQUE( NULLS NOT DISTINCT)? \((.*?)\)(.*)$`)
	checkRe    = regexp.MustCompile(`(?s)^CHECK (.*?)( NO INHERIT)?( NOT VALID)?( NOT ENFORCED)?$`)
	fkActionRe = regexp.MustCompile(`ON (UPDATE|DELETE) (CASCADE|RESTRICT|SET NULL(?: \([^)]*\))?|SET DEFAULT(?: \([^)]*\))?|NO ACTION)`)
)

// parentColumn returns the column cn of table fq's partition parent, or of
// the first INHERITS parent that has it, or nil.
func (b *builder) parentColumn(fq string, lt *diff.LiveTable, cn string) *diff.LiveColumn {
	parents := lt.Inherits
	if p := b.d.PartitionOf[fq]; p != "" {
		parents = []string{p}
	}
	for _, p := range parents {
		if pt := b.live.Tables[p]; pt != nil && pt.Columns[cn] != nil {
			return pt.Columns[cn]
		}
	}
	return nil
}

// inheritedColumnNode returns the settings a partition or inheritance child
// keeps of an inherited column that it doesn't simply copy from parent: a
// different default, storage or compression, NOT NULL the parent lacks, and
// the statistics target, n_distinct and comment, which it never copies. It
// returns nil when there are none.
func inheritedColumnNode(lc, parent *diff.LiveColumn) *node {
	if parent == nil {
		parent = &diff.LiveColumn{Nullable: true}
	}
	c := flowMap()
	if lc.Default != "" && lc.Default != parent.Default {
		add(c, "default", str(lc.Default))
	}
	if !lc.Nullable && parent.Nullable {
		add(c, "nullable", boolean(false))
	}
	if lc.Storage != "" && lc.Storage != parent.Storage {
		add(c, "storage", str(lc.Storage))
	}
	if lc.Compression != "" && lc.Compression != parent.Compression {
		add(c, "compression", str(lc.Compression))
	}
	if lc.StatisticsTarget != nil {
		add(c, "statisticsTarget", num(strconv.Itoa(*lc.StatisticsTarget)))
	}
	for _, o := range [][2]string{{"n_distinct", "nDistinct"}, {"n_distinct_inherited", "nDistinctInherited"}} {
		if v, err := strconv.ParseFloat(lc.Options[o[0]], 64); err == nil {
			add(c, o[1], floatNum(v))
		}
	}
	addStr(c, "comment", lc.Comment)
	if len(c.Content) == 0 {
		return nil
	}
	return c
}

func pkColumns(lt *diff.LiveTable) []string {
	for _, def := range lt.ConstraintDefs {
		if rest, ok := strings.CutPrefix(def, "PRIMARY KEY "); ok {
			if inner, ok := parenGroup(rest); ok {
				return splitIdents(inner)
			}
		}
	}
	return nil
}

// parenGroup returns the text inside the parenthesis s starts with, up to
// its matching ")", skipping parentheses inside quotes.
func parenGroup(s string) (string, bool) {
	if !strings.HasPrefix(s, "(") {
		return "", false
	}
	depth, quote := 0, byte(0)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth--; depth == 0 {
				return s[1:i], true
			}
		}
	}
	return "", false
}

// indexParameters adds the include, with and tablespace keys for the index
// backing a primary key or unique constraint.
func indexParameters(m *node, li *diff.LiveIndex) {
	if li == nil {
		return
	}
	if len(li.Include) > 0 {
		add(m, "include", list(plainIdents(li.Include)))
	}
	if len(li.Options) > 0 {
		add(m, "with", storageParams(li.Options, li.OptionOrder))
	}
	addStr(m, "tablespace", li.Tablespace)
}

// storageParams writes storage parameters as a flow mapping, those named in
// order first and in that order, then the others sorted.
func storageParams(options map[string]string, order []string) *node {
	w := flowMap()
	seen := map[string]bool{}
	for _, k := range append(append([]string{}, order...), sortedKeys(options)...) {
		if v, ok := options[k]; ok && !seen[k] {
			seen[k] = true
			add(w, k, str(v))
		}
	}
	return w
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
			add(ref, "table", str(schema.QualifiedName(relationKey(m[2]))))
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
				action, cols, _ := strings.Cut(a[2], " (")
				if cols != "" {
					cols = " (" + cols
				}
				add(fk, key, str(strings.ToLower(action)+cols))
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
			indexParameters(c, lt.IndexDefs[name])
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
			if li := lt.IndexDefs[name]; li != nil {
				addStr(c, "tablespace", li.Tablespace)
			}
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
		if li == nil || li.Constraint {
			continue
		}
		if li.Inherited && name == diff.PartitionIndexName(fq, li, lt.IndexOpclasses[name], lt.Columns) && lt.IndexComments[name] == "" {
			continue
		}
		if li.Invalid {
			b.warn("table %s: index %s is invalid, as an interrupted build or an index on only some partitions leaves it; it is pulled as a complete index", fq, name)
		}
		m := newMap()
		cols := make([]string, len(li.Keys))
		opcs := lt.IndexOpclasses[name]
		var perCol, colls, targets *node
		for i, k := range li.Keys {
			col := plainIdents([]string{k})[0]
			if li.Descending[i] {
				col += " desc"
				if !li.NullsFirst[i] {
					col += " nulls last"
				}
			} else if li.NullsFirst[i] {
				col += " nulls first"
			}
			cols[i] = col
			if i < len(opcs) && (!opcs[i].Default || len(opcs[i].Params) > 0) {
				if perCol == nil {
					perCol = flowMap()
				}
				add(perCol, col, str(opcs[i].Declaration()))
			}
			if i < len(opcs) && opcs[i].Collation != "" {
				if colls == nil {
					colls = flowMap()
				}
				add(colls, col, str(opcs[i].Collation))
			}
			if i < len(opcs) && opcs[i].StatisticsTarget >= 0 {
				if targets == nil {
					targets = flowMap()
				}
				add(targets, col, num(strconv.Itoa(opcs[i].StatisticsTarget)))
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
		if colls != nil {
			add(m, "collations", colls)
		}
		if targets != nil {
			add(m, "statisticsTargets", targets)
		}
		if len(li.Include) > 0 {
			add(m, "include", list(plainIdents(li.Include)))
		}
		if li.NullsNotDistinct {
			add(m, "nullsNotDistinct", boolean(true))
		}
		if len(li.Options) > 0 {
			add(m, "with", storageParams(li.Options, li.OptionOrder))
		}
		addStr(m, "tablespace", li.Tablespace)
		addStr(m, "where", li.Predicate)
		addStr(m, "comment", lt.IndexComments[name])
		add(idx, name, m)
	}
	if len(idx.Content) > 0 {
		add(t, "indexes", idx)
	}
}

const identPart = `(?:"(?:[^"]|"")*"|[^\s".]+)`

var (
	triggerRe  = regexp.MustCompile(`(?s)^CREATE (CONSTRAINT )?TRIGGER (` + identPart + `) (BEFORE|AFTER|INSTEAD OF) (.+?) ON ((?:` + identPart + `\.)*` + identPart + `)(.*?) FOR EACH (ROW|STATEMENT)(?: WHEN \((.*)\))? EXECUTE (?:FUNCTION|PROCEDURE) (.+)$`)
	oldTableRe = regexp.MustCompile(`OLD TABLE AS (` + identPart + `)`)
	newTableRe = regexp.MustCompile(`NEW TABLE AS (` + identPart + `)`)
)

// unquoteIdent removes the double quotes PostgreSQL puts around a single
// identifier, undoubling embedded quotes. Anything else, such as an
// expression over several quoted names, is returned unchanged.
func unquoteIdent(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	inner := s[1 : len(s)-1]
	if strings.Contains(strings.ReplaceAll(inner, `""`, ""), `"`) {
		return s
	}
	return strings.ReplaceAll(inner, `""`, `"`)
}

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
		if mm := oldTableRe.FindStringSubmatch(middle); mm != nil || strings.Contains(middle, "NEW TABLE AS") {
			ref := flowMap()
			if mm != nil {
				add(ref, "old", str(unquoteIdent(mm[1])))
			}
			if nn := newTableRe.FindStringSubmatch(middle); nn != nil {
				add(ref, "new", str(unquoteIdent(nn[1])))
			}
			add(tr, "referencing", ref)
		}
		deferral(tr, middle)
		addStr(tr, "when", m[8])
		add(tr, "procedure", str(b.triggerFunction(m[9])))
		if e := lt.TriggerEnabled[name]; e != "" && e != "origin" {
			add(tr, "enabled", str(e))
		}
		addStr(tr, "comment", lt.TriggerComments[name])
		add(trs, name, tr)
	}
	if len(trs.Content) > 0 {
		add(t, "triggers", trs)
	}
}

// data emits the rows requested with Options.Data as a data: block, keyed
// by the primary key and sorted by it. Generated columns are left out.
func (b *builder) data(t *node, fq string, lt *diff.LiveTable) {
	rows, ok := b.opts.Data[fq]
	if !ok {
		return
	}
	key := pkColumns(lt)
	if len(key) == 0 {
		b.warn("table %s: data needs a primary key to identify rows; not pulled", fq)
		return
	}
	sorted := append([]map[string]any(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		for _, k := range key {
			if c := compareJSON(sorted[i][k], sorted[j][k]); c != 0 {
				return c < 0
			}
		}
		return false
	})
	list := &node{Kind: yaml.SequenceNode}
	for _, row := range sorted {
		m := flowMap()
		for _, cn := range b.d.ColumnOrder[fq] {
			lc := lt.Columns[cn]
			if lc == nil || lc.Generated != "" {
				continue
			}
			if v, ok := row[cn]; ok {
				add(m, cn, dataNode(v, lc.Type))
			}
		}
		list.Content = append(list.Content, m)
	}
	d := newMap()
	add(d, "rows", list)
	add(t, "data", d)
}

func compareJSON(a, b any) int {
	if x, ok := a.(json.Number); ok {
		if y, ok := b.(json.Number); ok {
			fx, _ := x.Float64()
			fy, _ := y.Float64()
			switch {
			case fx < fy:
				return -1
			case fx > fy:
				return 1
			}
			return 0
		}
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

// dataNode renders a data value of a column of type typ. Outside json
// and jsonb columns, a number YAML would round through float64 inexactly,
// such as a numeric(38,12) value, is quoted, so the row is written with all
// its digits.
func dataNode(v any, typ string) *node {
	if t := strings.ToLower(typ); t == "json" || t == "jsonb" {
		return jsonNode(v)
	}
	switch x := v.(type) {
	case json.Number:
		if !floatExact(x) {
			return str(x.String())
		}
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, e := range x {
			n.Content = append(n.Content, dataNode(e, typ))
		}
		return n
	}
	return jsonNode(v)
}

// floatExact reports whether n survives YAML's float64 decoding: it is an
// int64, or its shortest float64 form has the same decimal value.
func floatExact(n json.Number) bool {
	if _, err := n.Int64(); err == nil {
		return true
	}
	f, err := n.Float64()
	if err != nil {
		return false
	}
	want, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return false
	}
	got, ok := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
	return ok && want.Cmp(got) == 0
}

// jsonNode renders a value decoded from JSON as a YAML node.
func jsonNode(v any) *node {
	switch x := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	case bool:
		return boolean(x)
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return num(x.String())
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: x.String()}
	case string:
		return str(x)
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, e := range x {
			n.Content = append(n.Content, jsonNode(e))
		}
		return n
	case map[string]any:
		m := flowMap()
		for _, k := range sortedKeys(x) {
			add(m, k, jsonNode(x[k]))
		}
		return m
	}
	return str(fmt.Sprint(v))
}

// statistics writes the extended statistics of table fq, with the schema of
// those in another schema than the table's.
func (b *builder) statistics(t *node, fq string, lt *diff.LiveTable) {
	if len(lt.Statistics) == 0 {
		return
	}
	sts := newMap()
	written := map[string]bool{}
	keys := sortedKeys(lt.Statistics)
	sort.SliceStable(keys, func(i, j int) bool { return lt.Statistics[keys[i]].Schema == "" && lt.Statistics[keys[j]].Schema != "" })
	for _, key := range keys {
		ls, name := lt.Statistics[key], key
		if ls.Schema != "" {
			name = ls.Name
		}
		if written[name] {
			b.warn("table %s: statistics %s.%s was left out: the table has another statistics object named %s, and the YAML names a table's statistics objects by name alone", fq, ls.Schema, name, name)
			continue
		}
		written[name] = true
		m := newMap()
		if ls.Schema != "" {
			add(m, "schema", str(ls.Schema))
		}
		if len(ls.Kinds) > 0 && len(ls.Kinds) < 3 {
			add(m, "kinds", list(ls.Kinds))
		}
		cols := make([]string, len(ls.Items))
		for i, it := range ls.Items {
			cols[i] = unquoteIdent(stripParens(it))
		}
		add(m, "columns", list(cols))
		if ls.Target >= 0 {
			add(m, "statisticsTarget", num(strconv.Itoa(ls.Target)))
		}
		addStr(m, "comment", ls.Comment)
		add(sts, name, m)
	}
	add(t, "statistics", sts)
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

// boundValues splits a partition bound list from pg_get_expr. Keywords and
// numbers are lower-cased; string literals keep their text, and stay
// written as SQL literals ('null', '42') when the bare text would read as
// a keyword or number, or has surrounding spaces.
func boundValues(s string) []string {
	out := []string{}
	for _, v := range splitTop(s) {
		v = strings.TrimSpace(v)
		if len(v) < 2 || !strings.HasPrefix(v, "'") || !strings.HasSuffix(v, "'") {
			out = append(out, strings.ToLower(v))
			continue
		}
		text := strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		switch strings.ToLower(text) {
		case "minvalue", "maxvalue", "true", "false", "null":
			out = append(out, v)
			continue
		}
		if text == "" || text != strings.TrimSpace(text) || numericRe.MatchString(text) || strings.HasPrefix(text, "'") {
			out = append(out, v)
			continue
		}
		out = append(out, text)
	}
	return out
}

var numericRe = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)

var quotedIdentRe = regexp.MustCompile(`^"(?:[^"]|"")+"$`)

// plainIdents unquotes entries that are just a quoted identifier, as
// PostgreSQL prints mixed-case names; expressions are left alone.
func plainIdents(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n
		if quotedIdentRe.MatchString(n) {
			out[i] = unquoteIdent(n)
		}
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
		c = strings.TrimSpace(c)
		if quotedIdentRe.MatchString(c) {
			c = unquoteIdent(c)
		}
		cols = append(cols, c)
	}
	add(m, "columns", list(cols))
	return m
}

func (b *builder) views() {
	for _, k := range sortedKeys(b.d.ViewQueries) {
		mat := b.live.MatViews[k]
		if !mat && !b.live.Views[k] {
			continue
		}
		if b.member("rel", k) {
			b.memberView(k, mat)
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
				ds = append(ds, kind+schema.QualifiedName(dep))
			}
			add(m, "dependsOn", list(ds))
		}
		add(m, "query", literal(b.d.ViewQueries[k]))
		key := "view " + schema.QuoteName(name)
		if mat {
			key = "materialized view " + schema.QuoteName(name)
			if !b.d.Populated[k] {
				add(m, "withData", boolean(false))
			}
			if lt := b.live.MatViewTables[k]; lt != nil {
				if len(lt.Options) > 0 {
					add(m, "with", storageParams(lt.Options, lt.OptionOrder))
				}
				addStr(m, "tablespace", lt.Tablespace)
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
		if g := b.grantsFor(b.live.TableGrants[k], k, "tables"); g != nil {
			add(m, "grants", g)
		}
		if cg := b.live.ColumnGrants[k]; len(cg) > 0 {
			gm := newMap()
			for _, col := range sortedKeys(cg) {
				if g := grantsNode(cg[col]); g != nil {
					add(gm, col, g)
				}
			}
			if len(gm.Content) > 0 {
				add(m, "columnGrants", gm)
			}
		}
		addStr(m, "comment", b.live.RelComments[k])
		if cc := b.live.ViewColumnComments[k]; len(cc) > 0 {
			cm := newMap()
			for _, col := range sortedKeys(cc) {
				add(cm, col, str(cc[col]))
			}
			add(m, "columnComments", cm)
		}
		if cd := b.live.ViewColumnDefaults[k]; len(cd) > 0 {
			dm := flowMap()
			for _, col := range sortedKeys(cd) {
				add(dm, col, str(cd[col]))
			}
			add(m, "columnDefaults", dm)
		}
		b.owner(m, "views", "rel:"+k)
		add(blk, key, m)
	}
}

// database builds database.yml: extensions, roles, default privileges,
// publications, language privileges, foreign-data wrappers, servers and
// event triggers.
func (b *builder) database() *node {
	doc := newMap()
	languages, wrappers := b.languages(), b.wrappers()
	kept := b.keptExtensions(languages, wrappers)
	if len(b.live.Extensions) > 0 {
		exts := &node{Kind: yaml.SequenceNode}
		for _, name := range sortedKeys(b.live.Extensions) {
			if name == "plpgsql" || kept != nil && !kept[name] {
				continue
			}
			le := b.live.ExtensionDefs[name]
			if le == nil {
				exts.Content = append(exts.Content, str(name))
				continue
			}
			e := flowMap()
			add(e, "name", str(name))
			add(e, "ifNotExists", boolean(true))
			add(e, "schema", str(le.Schema))
			if le.DefaultVersion != "" && le.Version != le.DefaultVersion {
				add(e, "version", str(le.Version))
			}
			if le.Comment != "" && le.Comment != le.DefaultComment {
				add(e, "comment", str(le.Comment))
			}
			deps := []string{}
			for _, r := range le.Requires {
				if r != "plpgsql" {
					deps = append(deps, "extension "+r)
				}
			}
			if len(deps) > 0 {
				add(e, "dependsOn", list(deps))
			}
			exts.Content = append(exts.Content, e)
		}
		if len(exts.Content) > 0 {
			add(doc, "extensions", exts)
		}
	}
	if b.opts.Roles && len(b.live.RoleDefs) > 0 {
		roles := newMap()
		for _, name := range sortedKeys(b.live.RoleDefs) {
			lr := b.live.RoleDefs[name]
			if lr.Bootstrap {
				continue
			}
			connecting := name == b.live.CurrentUser
			r := flowMap()
			for _, a := range []struct {
				key string
				on  bool
			}{{"login", lr.Login}, {"superuser", lr.Superuser}, {"createdb", lr.CreateDB}, {"createrole", lr.CreateRole}, {"replication", lr.Replication}, {"bypassRLS", lr.BypassRLS}} {
				if a.on && !connecting {
					add(r, a.key, boolean(true))
				}
			}
			if !lr.Inherit && !connecting {
				add(r, "inherit", boolean(false))
			}
			if lr.ConnectionLimit >= 0 && !connecting {
				add(r, "connectionLimit", num(strconv.Itoa(lr.ConnectionLimit)))
			}
			if lr.ValidUntil != "" && !connecting {
				add(r, "validUntil", str(lr.ValidUntil))
			}
			parents := []string{}
			for _, p := range sortedKeys(b.live.RoleMembers[name]) {
				if !connecting || !b.live.CreatorGrant(name, p) {
					parents = append(parents, p)
				}
			}
			if len(parents) > 0 {
				items := &node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
				for _, p := range parents {
					lm := b.live.RoleMemberOptions[name][p]
					if lm == nil || (!lm.Admin && lm.Inherit == lr.Inherit && lm.Set) {
						items.Content = append(items.Content, str(p))
						continue
					}
					m := flowMap()
					add(m, "role", str(p))
					if lm.Admin {
						add(m, "admin", boolean(true))
					}
					if lm.Inherit != lr.Inherit {
						add(m, "inherit", boolean(lm.Inherit))
					}
					if !lm.Set {
						add(m, "set", boolean(false))
					}
					items.Content = append(items.Content, m)
				}
				add(r, "inRoles", items)
			}
			if len(lr.Config) > 0 {
				set := flowMap()
				for _, k := range sortedKeys(lr.Config) {
					add(set, k, setting(k, lr.Config[k]))
				}
				add(r, "set", set)
			}
			addStr(r, "comment", b.live.RoleComments[name])
			if connecting && len(r.Content) == 0 {
				continue
			}
			add(roles, name, r)
		}
		if len(roles.Content) > 0 {
			add(doc, "roles", roles)
		}
	}
	if (len(b.live.DatabaseConfig) > 0 || len(b.live.DatabaseRoleConfig) > 0) && (!b.limited() || b.opts.DatabaseSettings) {
		db := newMap()
		settings := func(m map[string]string) *node {
			set := flowMap()
			for _, k := range sortedKeys(m) {
				add(set, k, setting(k, m[k]))
			}
			return set
		}
		if len(b.live.DatabaseConfig) > 0 {
			add(db, "set", settings(b.live.DatabaseConfig))
		}
		if len(b.live.DatabaseRoleConfig) > 0 {
			roles := newMap()
			for _, r := range sortedKeys(b.live.DatabaseRoleConfig) {
				add(roles, r, settings(b.live.DatabaseRoleConfig[r]))
			}
			add(db, "roles", roles)
		}
		add(doc, "database", db)
	}
	if dp := b.defaultPrivileges(); dp != nil {
		add(doc, "defaultPrivileges", dp)
	}
	if len(b.live.Publications) > 0 {
		pubs, left := newMap(), []string{}
		for _, name := range sortedKeys(b.live.Publications) {
			lp := b.live.Publications[name]
			if !b.keptPublication(name, lp) {
				left = append(left, name)
				continue
			}
			p := newMap()
			if lp.AllTables {
				add(p, "allTables", boolean(true))
			}
			if len(lp.Tables) > 0 {
				ts := &node{Kind: yaml.SequenceNode}
				for _, t := range sortedKeys(lp.Tables) {
					pt := lp.Tables[t]
					if len(pt.Columns) == 0 && pt.Where == "" && !pt.Only {
						ts.Content = append(ts.Content, str(schema.QualifiedName(t)))
						continue
					}
					e := flowMap()
					add(e, "table", str(schema.QualifiedName(t)))
					if pt.Only {
						add(e, "only", boolean(true))
					}
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
			if lp.GeneratedColumns == "stored" {
				add(p, "generatedColumns", str("stored"))
			}
			if b.opts.Owners && lp.Owner != b.live.CurrentUser {
				addStr(p, "owner", lp.Owner)
			}
			addStr(p, "comment", lp.Comment)
			add(pubs, name, p)
		}
		if len(pubs.Content) > 0 || !b.limited() {
			add(doc, "publications", pubs)
			b.warnPartialBlock("publications:", "publications", left)
		}
	}
	if languages != nil {
		add(doc, "languages", languages)
	}
	if wrappers != nil {
		add(doc, "foreignDataWrappers", wrappers)
	}
	if srv := b.servers(); srv != nil {
		add(doc, "servers", srv)
	}
	ets, left := newMap(), []string{}
	for _, name := range sortedKeys(b.live.EventTriggers) {
		le := b.live.EventTriggers[name]
		if le.Extension {
			continue
		}
		if b.limited() && !b.wantSchema(schemaOf(le.Function)) {
			left = append(left, name)
			continue
		}
		e := newMap()
		add(e, "event", str(le.Event))
		if len(le.Tags) > 0 {
			add(e, "tags", list(le.Tags))
		}
		add(e, "function", str(schema.QualifiedName(le.Function)+"()"))
		if le.Enabled != "origin" {
			add(e, "enabled", str(le.Enabled))
		}
		addStr(e, "comment", le.Comment)
		add(ets, name, e)
	}
	if len(ets.Content) > 0 {
		add(doc, "eventTriggers", ets)
		b.warnPartialBlock("eventTriggers:", "event triggers", left)
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
				privs := sortedKeys(scopes[sc][kind][r])
				for i, p := range privs {
					if base, ok := strings.CutSuffix(p, "*"); ok {
						privs[i] = base + " with grant option"
					}
				}
				add(g, r, list(privs))
			}
			if len(g.Content) > 0 {
				add(e, kind, g)
			}
		}
		seq.Content = append(seq.Content, e)
	}
	return seq
}

// grantsFor returns grantsNode(g) for object fq of kind (tables,
// sequences, functions or types). An object with no grants gets an empty
// mapping when some role's default privileges for its schema would grant
// objects of that kind something: the object predates them, and without
// the mapping a rebuild would hand it those grants.
func (b *builder) grantsFor(g map[string]map[string]bool, fq, kind string) *node {
	if n := grantsNode(g); n != nil {
		return n
	}
	nsp, _, _ := strings.Cut(fq, ".")
	for key, grants := range b.live.DefaultACLs {
		parts := strings.Split(key, "|")
		if len(parts) != 3 || parts[2] != kind || (parts[1] != "" && parts[1] != nsp) {
			continue
		}
		for grantee, privs := range grants {
			if grantee != "public" && len(privs) > 0 {
				return flowMap()
			}
		}
	}
	return nil
}

func grantsNode(g map[string]map[string]bool) *node {
	if len(g) == 0 {
		return nil
	}
	m := flowMap()
	for _, r := range sortedKeys(g) {
		privs := sortedKeys(g[r])
		for i, p := range privs {
			if base, ok := strings.CutSuffix(p, "*"); ok {
				privs[i] = base + " with grant option"
			}
		}
		add(m, r, list(privs))
	}
	return m
}

// splitIdents splits a constraint column list ("a, \"B\", c") into names.
func splitIdents(s string) []string {
	out := []string{}
	for _, p := range splitTop(s) {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "PERIOD ") {
			p = "period " + unquoteIdent(strings.TrimPrefix(p, "PERIOD "))
		} else if strings.HasSuffix(p, " WITHOUT OVERLAPS") {
			p = unquoteIdent(strings.TrimSuffix(p, " WITHOUT OVERLAPS")) + " without overlaps"
		} else {
			p = unquoteIdent(p)
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

// relationKey returns the model key of a relation as SQL names it, as in
// pg_get_constraintdef: quoted where needed, in public when unqualified.
func relationKey(ident string) string {
	parts, ok := schema.SplitName(ident)
	if !ok {
		parts = strings.Split(strings.TrimSpace(ident), ".")
	}
	if len(parts) == 1 {
		return "public." + parts[0]
	}
	return strings.Join(parts, ".")
}

// qualifiedNames writes the model keys fqs as references, with
// schema.QualifiedName.
func qualifiedNames(fqs []string) []string {
	out := make([]string, len(fqs))
	for i, fq := range fqs {
		out[i] = schema.QualifiedName(fq)
	}
	return out
}

// dependencyKinds are the kinds a dependsOn entry pull writes starts with.
var dependencyKinds = []string{"materialized view ", "view ", "sequence ", "foreign table ", "table ", "function ", "procedure ", "aggregate ", "type ", "domain "}

// dependencies writes dependsOn entries, a kind and a model key (with the
// argument types of a routine), with the names quoted where
// schema.QuoteName quotes them.
func dependencies(deps []string) []string {
	out := make([]string, len(deps))
	for i, dep := range deps {
		out[i] = dep
		for _, kind := range dependencyKinds {
			key, ok := strings.CutPrefix(dep, kind)
			if !ok {
				continue
			}
			args := ""
			if strings.HasSuffix(key, ")") {
				if j := strings.IndexByte(key, '('); j >= 0 {
					key, args = key[:j], key[j:]
				}
			}
			out[i] = kind + schema.QualifiedName(key) + args
			break
		}
	}
	return out
}

// references writes names as the database reports them, schema.name or a
// name alone, as references: a schema.name of a schema the database has
// with schema.QualifiedName, anything else as it is.
func (b *builder) references(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n
		if s, _, ok := strings.Cut(n, "."); ok && b.live.Schemas[s] {
			out[i] = schema.QualifiedName(n)
		}
	}
	return out
}

// ownerColumn splits the live owner of a sequence, schema.table.column, into
// the key of its table and its column, at a table the database has, since
// either name may contain dots.
func (b *builder) ownerColumn(owner string) (table, column string) {
	table = diff.ColumnRelation(owner)
	if lt := b.live.Tables[table]; lt != nil && lt.Columns[owner[len(table)+1:]] != nil {
		return table, owner[len(table)+1:]
	}
	for _, fq := range sortedKeys(b.live.Tables) {
		if col, ok := strings.CutPrefix(owner, fq+"."); ok && b.live.Tables[fq].Columns[col] != nil {
			return fq, col
		}
	}
	return table, strings.TrimPrefix(owner, table+".")
}

// triggerFunction qualifies the function call of a trigger definition.
// pg_get_triggerdef leaves it unqualified when it is visible on the
// search_path: a public function then gets public., while a pg_catalog
// builtin such as tsvector_update_trigger stays as written.
func (b *builder) triggerFunction(call string) string {
	base := call
	if i := strings.IndexByte(call, '('); i >= 0 {
		base = call[:i]
	}
	if strings.Contains(base, ".") {
		return call
	}
	prefix := "public." + strings.ToLower(unquoteIdent(base)) + "("
	for norm := range b.live.FunctionDefs {
		if strings.HasPrefix(norm, prefix) {
			return "public." + call
		}
	}
	return call
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
