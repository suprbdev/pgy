package diff

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// ownerNeed is what a statement needs to act on an object it names.
type ownerNeed int

const (
	// needOwner is the privileges of the object's owner.
	needOwner ownerNeed = iota
	// needGrant is the owner's privileges, or each privilege a GRANT or
	// REVOKE names held with the grant option.
	needGrant
	// needTrigger is the owner's privileges, or the TRIGGER privilege.
	needTrigger
	// needNothing is no privilege of the owner: the statement goes with the
	// object, as the rows of a table do.
	needNothing
)

// stmtAct is an object a planned statement acts on. key is an object key
// (see ownerIndex.owner); privs and column are the privileges and the
// column a GRANT or REVOKE names.
type stmtAct struct {
	key    string
	need   ownerNeed
	privs  []string
	column string
}

// stmtInfo is what statementObjects reads from a planned statement.
type stmtInfo struct {
	acts []stmtAct
	// creates holds the objects the statement creates or renames an
	// object to.
	creates []string
	// retyped holds the columns ("schema.table.column") whose type an
	// ALTER TABLE changes.
	retyped []string
	// drop is set for a DROP of one or more objects of one kind: head is
	// the statement up to the list, items the list, keys their object keys.
	drop *dropList
	// update is the extension an ALTER EXTENSION ... UPDATE updates.
	update string
}

type dropList struct {
	head  string
	items []string
	keys  []string
}

// objectKinds maps the words SQL names an object kind with to the prefix
// of its object key; single kinds have names without a schema.
var objectKinds = []struct {
	words, prefix string
	single        bool
}{
	{"materialized view ", "rel:", false},
	{"foreign table ", "rel:", false},
	{"table ", "rel:", false},
	{"view ", "rel:", false},
	{"sequence ", "rel:", false},
	{"index ", "idx:", false},
	{"type ", "type:", false},
	{"domain ", "type:", false},
	{"schema ", "schema:", true},
	{"collation ", "coll:", false},
	{"text search configuration ", "tscfg:", false},
	{"text search dictionary ", "tsdict:", false},
	{"foreign data wrapper ", "fdw:", true},
	{"foreign server ", "srv:", true},
	{"server ", "srv:", true},
	{"publication ", "pub:", true},
	{"event trigger ", "evt:", true},
	{"extension ", "ext:", true},
	{"language ", "lang:", true},
	{"statistics ", "stat:", false},
	{"function ", "proc:", false},
	{"procedure ", "proc:", false},
	{"aggregate ", "proc:", false},
	{"routine ", "proc:", false},
}

func readIdent(s string) (string, string, bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", s, false
	}
	for i := 1; i < len(s); i++ {
		if s[i] != '"' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '"' {
			i++
			continue
		}
		return strings.ReplaceAll(s[1:i], `""`, `"`), s[i+1:], true
	}
	return "", s, false
}

func readName(s string) ([]string, string) {
	var parts []string
	for {
		p, rest, ok := readIdent(s)
		if !ok {
			return parts, s
		}
		parts, s = append(parts, p), rest
		if !strings.HasPrefix(s, `."`) {
			return parts, s
		}
		s = s[1:]
	}
}

func readParens(s string) (string, string, bool) {
	if !strings.HasPrefix(s, "(") {
		return "", s, false
	}
	depth, q := 0, byte(0)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '\'' || c == '"':
			q = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return s[:i+1], s[i+1:], true
			}
		}
	}
	return "", s, false
}

func trimWords(s string, words ...string) string {
	for changed := true; changed; {
		changed = false
		for _, w := range words {
			if strings.HasPrefix(s, w) {
				s, changed = s[len(w):], true
			}
		}
	}
	return s
}

func matchKind(s string) (prefix string, single bool, rest string, ok bool) {
	for _, k := range objectKinds {
		if strings.HasPrefix(s, k.words) {
			return k.prefix, k.single, s[len(k.words):], true
		}
	}
	return "", false, s, false
}

// readObject reads the name of an object of the kind with key prefix
// prefix at the start of s and returns its key.
func readObject(prefix string, single bool, aggregate bool, s string) (string, string, bool) {
	parts, rest := readName(s)
	if len(parts) == 0 || single && len(parts) != 1 {
		return "", s, false
	}
	name := strings.Join(parts, ".")
	if prefix != "proc:" {
		return prefix + name, rest, true
	}
	args, rest, ok := readParens(rest)
	if !ok {
		return "", s, false
	}
	if aggregate {
		args = aggregateArgs(args)
	}
	return "proc:" + normalizeFunctionSignature(name+args), rest, true
}

// topLevelIndex returns the index of the first occurrence of sep in s
// outside quotes and parentheses, or -1.
func topLevelIndex(s, sep string) int {
	depth, q := 0, byte(0)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '\'' || c == '"':
			q = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && strings.HasPrefix(s[i:], sep):
			return i
		}
	}
	return -1
}

// qualifiedNames returns the keys of the schema-qualified names s lists
// outside parentheses, as "rel:schema.name".
func qualifiedNames(s string) []string {
	var out []string
	for _, item := range splitTopLevelQuotedParens(s) {
		item = trimWords(strings.TrimSpace(item), "table ", "only ")
		if parts, _ := readName(item); len(parts) == 2 {
			out = append(out, "rel:"+parts[0]+"."+parts[1])
		}
	}
	return out
}

// statementObjects reads what a statement pgy plans acts on. It knows the
// statements of the forms the planner writes, where every name is quoted,
// and returns nothing for the others, which then need nothing.
func statementObjects(stmt string, ix *ownerIndex) stmtInfo {
	s := strings.TrimSpace(stmt)
	var info stmtInfo
	act := func(key string, need ownerNeed) {
		if key != "" {
			info.acts = append(info.acts, stmtAct{key: key, need: need})
		}
	}
	switch {
	case strings.HasPrefix(s, "alter "):
		rest := s[len("alter "):]
		if strings.HasPrefix(rest, "default privileges") || strings.HasPrefix(rest, "role ") || strings.HasPrefix(rest, "user mapping") {
			return info
		}
		if strings.HasPrefix(rest, "policy ") || strings.HasPrefix(rest, "trigger ") {
			_, rest, ok := readIdent(trimWords(rest, "policy ", "trigger "))
			if ok && strings.HasPrefix(rest, " on ") {
				key, _, _ := readObject("rel:", false, false, rest[len(" on "):])
				act(key, needOwner)
			}
			return info
		}
		if strings.HasPrefix(rest, "database ") {
			act("db:", needOwner)
			return info
		}
		prefix, single, rest, ok := matchKind(rest)
		if !ok {
			return info
		}
		aggregate := strings.HasPrefix(s, "alter aggregate ")
		key, rest, ok := readObject(prefix, single, aggregate, trimWords(rest, "if exists ", "only "))
		if !ok {
			return info
		}
		act(key, needOwner)
		switch {
		case strings.HasPrefix(rest, " attach partition ") || strings.HasPrefix(rest, " detach partition "):
			other, _, _ := readObject(prefix, false, false, trimWords(rest[len(" attach partition "):], "only "))
			act(other, needOwner)
		case prefix == "rel:" && (strings.HasPrefix(rest, " inherit ") || strings.HasPrefix(rest, " no inherit ")):
			other, _, _ := readObject("rel:", false, false, trimWords(rest, " no inherit ", " inherit "))
			act(other, needOwner)
		case prefix == "rel:" && strings.HasPrefix(rest, " owned by "):
			if parts, _ := readName(rest[len(" owned by "):]); len(parts) >= 3 {
				act("rel:"+parts[0]+"."+parts[1], needOwner)
			}
		case prefix == "rel:" && strings.HasPrefix(rest, " alter column "):
			if col, more, ok := readIdent(rest[len(" alter column "):]); ok && (strings.HasPrefix(more, " type ") || strings.HasPrefix(more, " set data type ")) {
				info.retyped = append(info.retyped, strings.TrimPrefix(key, "rel:")+"."+col)
			}
		case prefix == "pub:" && strings.HasPrefix(rest, " set ") && !strings.HasPrefix(rest, " set ("):
			for _, t := range qualifiedNames(strings.TrimSuffix(rest[len(" set "):], ";")) {
				if !ix.published(strings.TrimPrefix(key, "pub:"), strings.TrimPrefix(t, "rel:")) {
					act(t, needOwner)
				}
			}
		case prefix == "ext:" && strings.HasPrefix(rest, " update"):
			info.update = strings.TrimPrefix(key, "ext:")
		case prefix != "proc:" && strings.HasPrefix(rest, " rename to "):
			if name, _, ok := readIdent(rest[len(" rename to "):]); ok {
				info.creates = append(info.creates, prefix+schemaPrefix(key, single)+name)
			}
		case prefix != "proc:" && !single && strings.HasPrefix(rest, " set schema "):
			if nsp, _, ok := readIdent(rest[len(" set schema "):]); ok {
				_, name, _ := strings.Cut(strings.TrimPrefix(key, prefix), ".")
				info.creates = append(info.creates, prefix+nsp+"."+name)
			}
		}
	case strings.HasPrefix(s, "comment on "):
		rest := s[len("comment on "):]
		switch {
		case strings.HasPrefix(rest, "column "):
			if parts, _ := readName(rest[len("column "):]); len(parts) >= 3 {
				act(ix.columnOwner(parts[0]+"."+parts[1]), needOwner)
			}
		case strings.HasPrefix(rest, "constraint ") || strings.HasPrefix(rest, "trigger ") || strings.HasPrefix(rest, "policy ") || strings.HasPrefix(rest, "rule "):
			_, more, ok := readIdent(trimWords(rest, "constraint ", "trigger ", "policy ", "rule "))
			if !ok || !strings.HasPrefix(more, " on ") {
				return info
			}
			more = more[len(" on "):]
			prefix := "rel:"
			if strings.HasPrefix(more, "domain ") {
				prefix, more = "type:", more[len("domain "):]
			}
			key, _, _ := readObject(prefix, false, false, more)
			act(key, needOwner)
		default:
			prefix, single, more, ok := matchKind(rest)
			if !ok {
				return info
			}
			key, _, _ := readObject(prefix, single, strings.HasPrefix(rest, "aggregate "), more)
			act(key, needOwner)
		}
	case strings.HasPrefix(s, "create "):
		statementCreates(trimWords(s[len("create "):], "or replace "), &info)
	case strings.HasPrefix(s, "drop "):
		rest := s[len("drop "):]
		switch {
		case strings.HasPrefix(rest, "trigger ") || strings.HasPrefix(rest, "policy ") || strings.HasPrefix(rest, "rule "):
			_, more, ok := readIdent(trimWords(rest, "trigger ", "policy ", "rule ", "if exists "))
			if ok && strings.HasPrefix(more, " on ") {
				key, _, _ := readObject("rel:", false, false, more[len(" on "):])
				act(key, needOwner)
			}
			return info
		case strings.HasPrefix(rest, "user mapping for "):
			if at := strings.LastIndex(rest, " server "); at >= 0 {
				key, _, _ := readObject("srv:", true, false, rest[at+len(" server "):])
				act(key, needOwner)
			}
			return info
		}
		prefix, single, more, ok := matchKind(rest)
		if !ok {
			return info
		}
		list := trimWords(more, "concurrently ", "if exists ")
		head := s[:len(s)-len(list)]
		list = strings.TrimSuffix(strings.TrimSpace(list), ";")
		d := &dropList{head: head}
		for _, item := range splitTopLevelQuotedParens(list) {
			item = strings.TrimSpace(item)
			key, tail, ok := readObject(prefix, single, strings.HasPrefix(rest, "aggregate "), item)
			if !ok || strings.TrimSpace(tail) != "" {
				d = nil
				break
			}
			d.items, d.keys = append(d.items, item), append(d.keys, key)
			act(key, needOwner)
		}
		info.drop = d
	case strings.HasPrefix(s, "grant ") || strings.HasPrefix(s, "revoke "):
		body := trimWords(s, "grant option for ", "grant ", "revoke ")
		at := topLevelIndex(body, " on ")
		if at < 0 {
			return info
		}
		prefix, single, more, ok := matchKind(body[at+len(" on "):])
		if !ok {
			return info
		}
		key, _, ok := readObject(prefix, single, false, more)
		if !ok {
			return info
		}
		a := stmtAct{key: key, need: needGrant}
		for _, p := range splitTopLevelQuotedParens(body[:at]) {
			p = strings.TrimSpace(p)
			name, cols, _ := strings.Cut(p, " (")
			a.privs = append(a.privs, strings.ToLower(strings.TrimSpace(name)))
			if c, _, ok := readIdent(cols); ok {
				a.column = c
			}
		}
		info.acts = append(info.acts, a)
	case strings.HasPrefix(s, "refresh materialized view "):
		key, _, _ := readObject("rel:", false, false, trimWords(s[len("refresh materialized view "):], "concurrently "))
		act(key, needOwner)
	case strings.HasPrefix(s, "insert into ") || strings.HasPrefix(s, "update ") || strings.HasPrefix(s, "delete from "):
		key, _, _ := readObject("rel:", false, false, trimWords(s, "insert into ", "update ", "delete from ", "only "))
		act(key, needNothing)
	case strings.HasPrefix(s, "do ") && strings.Contains(s, "'alter database %I "):
		act("db:", needOwner)
	case strings.HasPrefix(s, "set search_path"):
		act("db:", needNothing)
	}
	return info
}

// schemaPrefix returns "schema." for the key of an object in a schema, ""
// for single kinds.
func schemaPrefix(key string, single bool) string {
	if single {
		return ""
	}
	_, name, _ := strings.Cut(key, ":")
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i+1]
	}
	return ""
}

// statementCreates fills info for a CREATE statement, s being what follows
// CREATE [OR REPLACE].
func statementCreates(s string, info *stmtInfo) {
	act := func(key string, need ownerNeed) {
		if key != "" {
			info.acts = append(info.acts, stmtAct{key: key, need: need})
		}
	}
	creates := func(key string) {
		if key != "" {
			info.creates = append(info.creates, key)
			act(key, needOwner)
		}
	}
	switch {
	case strings.HasPrefix(s, "unique index ") || strings.HasPrefix(s, "index "):
		name, rest, ok := readIdent(trimWords(s, "unique ", "index ", "concurrently ", "if not exists "))
		if !ok || !strings.HasPrefix(rest, " on ") {
			return
		}
		table, _, ok := readObject("rel:", false, false, trimWords(rest[len(" on "):], "only "))
		if ok {
			act(table, needOwner)
			info.creates = append(info.creates, "idx:"+schemaPrefix(table, false)+name)
		}
	case strings.HasPrefix(s, "trigger ") || strings.HasPrefix(s, "constraint trigger "):
		if at := strings.Index(s, ` on "`); at >= 0 {
			key, _, _ := readObject("rel:", false, false, s[at+len(" on "):])
			act(key, needTrigger)
		}
	case strings.HasPrefix(s, "policy "):
		if _, rest, ok := readIdent(s[len("policy "):]); ok && strings.HasPrefix(rest, " on ") {
			key, _, _ := readObject("rel:", false, false, rest[len(" on "):])
			act(key, needOwner)
		}
	case strings.HasPrefix(s, "rule "):
		if at := strings.Index(s, ` to "`); at >= 0 {
			key, _, _ := readObject("rel:", false, false, s[at+len(" to "):])
			act(key, needOwner)
		}
	case strings.HasPrefix(s, "statistics "):
		key, _, ok := readObject("stat:", false, false, trimWords(s[len("statistics "):], "if not exists "))
		if at := strings.LastIndex(s, ` from "`); ok && at >= 0 {
			table, _, _ := readObject("rel:", false, false, s[at+len(" from "):])
			act(table, needOwner)
			info.creates = append(info.creates, key)
		}
	case strings.HasPrefix(s, "function ") || strings.HasPrefix(s, "procedure ") || strings.HasPrefix(s, "aggregate "):
		prefix, _, rest, _ := matchKind(s)
		key, _, _ := readObject(prefix, false, strings.HasPrefix(s, "aggregate "), rest)
		creates(key)
	case strings.HasPrefix(s, "view ") || strings.HasPrefix(s, "materialized view ") || strings.HasPrefix(s, "type ") || strings.HasPrefix(s, "domain ") ||
		strings.HasPrefix(s, "collation ") || strings.HasPrefix(s, "text search ") || strings.HasPrefix(s, "event trigger "):
		prefix, single, rest, ok := matchKind(s)
		if !ok {
			return
		}
		key, _, _ := readObject(prefix, single, false, trimWords(rest, "if not exists "))
		creates(key)
	case strings.HasPrefix(s, "table ") || strings.HasPrefix(s, "unlogged table ") || strings.HasPrefix(s, "foreign table "):
		key, rest, ok := readObject("rel:", false, false, trimWords(s, "unlogged ", "foreign ", "table ", "if not exists "))
		if !ok {
			return
		}
		info.creates = append(info.creates, key)
		if strings.HasPrefix(rest, " partition of ") {
			parent, _, _ := readObject("rel:", false, false, rest[len(" partition of "):])
			act(parent, needOwner)
		} else if at := strings.LastIndex(rest, ") inherits ("); at >= 0 {
			if list, _, ok := readParens(rest[at+len(") inherits "):]); ok {
				for _, parent := range qualifiedNames(strings.TrimSuffix(strings.TrimPrefix(list, "("), ")")) {
					act(parent, needOwner)
				}
			}
		}
	case strings.HasPrefix(s, "sequence "):
		key, rest, ok := readObject("rel:", false, false, trimWords(s[len("sequence "):], "if not exists "))
		if !ok {
			return
		}
		info.creates = append(info.creates, key)
		if at := strings.Index(rest, " owned by "); at >= 0 {
			if parts, _ := readName(rest[at+len(" owned by "):]); len(parts) >= 3 {
				act("rel:"+parts[0]+"."+parts[1], needOwner)
			}
		}
	case strings.HasPrefix(s, "publication "):
		name, rest, ok := readIdent(s[len("publication "):])
		if !ok {
			return
		}
		info.creates = append(info.creates, "pub:"+name)
		if strings.HasPrefix(rest, " for table ") {
			members := rest[len(" for "):]
			if at := topLevelIndex(members, " with ("); at >= 0 {
				members = members[:at]
			}
			for _, t := range qualifiedNames(strings.TrimSuffix(members, ";")) {
				act(t, needOwner)
			}
		}
	}
}

// ownerIndex looks up the owners of the objects statements act on in the
// snapshots a plan was made from: the live database, and the same after the
// plan's renames.
type ownerIndex struct {
	lives      []*Live
	indexTable map[string]string
	statTable  map[string]string
	// routines maps the identity of each routine (see routineIdentity) to
	// its key, for declarations that spell the arguments differently.
	routines map[string]string
}

func newOwnerIndex(lives ...*Live) *ownerIndex {
	ix := &ownerIndex{indexTable: map[string]string{}, statTable: map[string]string{}, routines: map[string]string{}}
	for _, l := range lives {
		if l == nil {
			continue
		}
		ix.lives = append(ix.lives, l)
		for key := range l.Owners {
			if sig, ok := strings.CutPrefix(key, "proc:"); ok {
				for _, procedure := range []bool{true, false} {
					if id := routineIdentity(sig, procedure); ix.routines[id] == "" {
						ix.routines[id] = key
					}
				}
			}
		}
		for _, tables := range []map[string]*LiveTable{l.Tables, l.MatViewTables} {
			for fq, lt := range tables {
				if lt == nil {
					continue
				}
				nsp := schemaPrefixOf(fq)
				for name := range lt.Indexes {
					if _, ok := ix.indexTable[nsp+name]; !ok {
						ix.indexTable[nsp+name] = fq
					}
				}
				for _, st := range lt.Statistics {
					if st == nil {
						continue
					}
					key := nsp + st.Name
					if st.Schema != "" {
						key = st.Schema + "." + st.Name
					}
					if _, ok := ix.statTable[key]; !ok {
						ix.statTable[key] = fq
					}
				}
			}
		}
	}
	return ix
}

// resolve returns the key of the object whose owner owns key: the table of
// an index or a statistics object, the live key of a routine whose
// arguments key spells differently, key itself for other objects.
func (ix *ownerIndex) resolve(key string) string {
	if sig, ok := strings.CutPrefix(key, "proc:"); ok && ix.owner(key) == "" {
		for _, procedure := range []bool{false, true} {
			if k := ix.routines[routineIdentity(sig, procedure)]; k != "" {
				return k
			}
		}
	}
	if name, ok := strings.CutPrefix(key, "idx:"); ok {
		if t := ix.indexTable[name]; t != "" {
			return "rel:" + t
		}
	}
	if name, ok := strings.CutPrefix(key, "stat:"); ok {
		if t := ix.statTable[name]; t != "" {
			return "rel:" + t
		}
	}
	return key
}

// owner returns the role that owns the object with key key, "" for an
// object the snapshots don't have.
func (ix *ownerIndex) owner(key string) string {
	kind, name, _ := strings.Cut(key, ":")
	for _, l := range ix.lives {
		var owner string
		switch kind {
		case "rel", "type", "schema", "proc", "tsdict", "tscfg", "coll":
			owner = l.Owners[key]
		case "ext":
			if le := l.ExtensionDefs[name]; le != nil {
				owner = le.Owner
			}
		case "pub":
			if lp := l.Publications[name]; lp != nil {
				owner = lp.Owner
			}
		case "srv":
			if ls := l.Servers[name]; ls != nil {
				owner = ls.Owner
			}
		case "fdw":
			if lw := l.Wrappers[name]; lw != nil {
				owner = lw.Owner
			}
		case "lang":
			if lg := l.Languages[name]; lg != nil {
				owner = lg.Owner
			}
		case "evt":
			if le := l.EventTriggers[name]; le != nil {
				owner = le.Owner
			}
		case "db":
			owner = l.DatabaseOwner
		}
		if owner != "" {
			return owner
		}
	}
	return ""
}

// columnOwner returns the key of the table, view or composite type a
// column comment names.
func (ix *ownerIndex) columnOwner(fq string) string {
	for _, l := range ix.lives {
		if _, ok := l.Owners["type:"+fq]; ok && l.Owners["rel:"+fq] == "" {
			return "type:" + fq
		}
	}
	return "rel:" + fq
}

// published reports whether publication pub publishes table fq already.
func (ix *ownerIndex) published(pub, fq string) bool {
	for _, l := range ix.lives {
		if lp := l.Publications[pub]; lp != nil {
			_, ok := lp.Tables[fq]
			return ok
		}
	}
	return false
}

// describe names the object with key key for a warning.
func (ix *ownerIndex) describe(key string) string {
	kind, name, _ := strings.Cut(key, ":")
	for _, l := range ix.lives {
		switch kind {
		case "rel":
			switch {
			case l.Views[name]:
				return "view " + name
			case l.MatViews[name]:
				return "materialized view " + name
			case l.Sequences[name]:
				return "sequence " + name
			case l.Tables[name] != nil && l.Tables[name].Foreign != nil:
				return "foreign table " + name
			case l.Tables[name] != nil:
				return "table " + name
			}
		case "type":
			if l.Domains[name] {
				return "domain " + name
			}
			if l.Types[name] || l.BaseTypes[name] {
				return "type " + name
			}
		case "proc":
			if lf := l.FunctionDefs[name]; lf != nil {
				if lf.Kind == "a" {
					return "aggregate " + lf.Signature
				}
				return "function " + lf.Signature
			}
			if lp := l.ProcedureDefs[name]; lp != nil {
				return "procedure " + lp.Signature
			}
			if ag := l.AggregateDefs[name]; ag != nil {
				return "aggregate " + name
			}
		}
	}
	switch kind {
	case "rel":
		return "relation " + name
	case "proc":
		return "routine " + name
	case "tsdict":
		return "text search dictionary " + name
	case "tscfg":
		return "text search configuration " + name
	case "coll":
		return "collation " + name
	case "ext":
		return "extension " + name
	case "pub":
		return "publication " + name
	case "srv":
		return "server " + name
	case "fdw":
		return "foreign-data wrapper " + name
	case "lang":
		return "language " + name
	case "evt":
		return "event trigger " + name
	case "db":
		return "the database"
	}
	return kind + " " + name
}

// acl returns the live privileges on the object with key key (column for
// a column's), owner excluded.
func (ix *ownerIndex) acl(key, column string) map[string]map[string]bool {
	kind, name, _ := strings.Cut(key, ":")
	for _, l := range ix.lives {
		var g map[string]map[string]bool
		switch kind {
		case "rel":
			if column != "" {
				g = l.ColumnGrants[name][column]
			} else {
				g = l.TableGrants[name]
			}
		case "type":
			g = l.TypeGrants[name]
		case "proc":
			g = l.FunctionGrants[name]
		case "schema":
			g = l.SchemaGrants[name]
		case "srv":
			if ls := l.Servers[name]; ls != nil {
				g = ls.Grants
			}
		case "fdw":
			if lw := l.Wrappers[name]; lw != nil {
				g = lw.Grants
			}
		case "lang":
			if lg := l.Languages[name]; lg != nil {
				g = lg.Grants
			}
		}
		if g != nil {
			return g
		}
	}
	return nil
}

// allowed reports whether the connecting role of live may run a statement
// that acts on the object of a, which owner owns, without the owner's
// privileges: a GRANT or REVOKE whose privileges it holds with the grant
// option, a CREATE TRIGGER on a table it has the TRIGGER privilege on.
func (ix *ownerIndex) allowed(live *Live, a stmtAct) bool {
	held := func(acl map[string]map[string]bool, priv string, public bool) bool {
		for role, privs := range acl {
			if (live.OwnerRoles[role] || public && role == "public") && privs[priv] {
				return true
			}
		}
		return false
	}
	switch a.need {
	case needNothing:
		return true
	case needTrigger:
		acl := ix.acl(a.key, "")
		return held(acl, "trigger", true) || held(acl, "trigger*", true)
	case needGrant:
		if len(a.privs) == 0 {
			return false
		}
		for _, p := range a.privs {
			if p == "all" || p == "all privileges" {
				return false
			}
			if !held(ix.acl(a.key, ""), p+"*", false) && (a.column == "" || !held(ix.acl(a.key, a.column), p+"*", false)) {
				return false
			}
		}
		return true
	}
	return false
}

// anyForeign reports whether live has an object of an owner whose
// privileges the connecting role lacks.
func anyForeign(live *Live) bool {
	foreign := func(owner string) bool { return owner != "" && !live.mayDrop(owner) }
	for _, owner := range live.Owners {
		if foreign(owner) {
			return true
		}
	}
	for _, le := range live.ExtensionDefs {
		if le != nil && foreign(le.Owner) {
			return true
		}
	}
	for _, lp := range live.Publications {
		if lp != nil && foreign(lp.Owner) {
			return true
		}
	}
	for _, ls := range live.Servers {
		if ls != nil && foreign(ls.Owner) {
			return true
		}
	}
	for _, lw := range live.Wrappers {
		if lw != nil && foreign(lw.Owner) {
			return true
		}
	}
	for _, lg := range live.Languages {
		if lg != nil && foreign(lg.Owner) {
			return true
		}
	}
	for _, le := range live.EventTriggers {
		if le != nil && foreign(le.Owner) {
			return true
		}
	}
	return foreign(live.DatabaseOwner)
}

type unionFind map[string]string

func (u unionFind) find(k string) string {
	root := k
	for {
		p, ok := u[root]
		if !ok || p == root {
			break
		}
		root = p
	}
	for k != root {
		k, u[k] = u[k], root
	}
	return root
}

func (u unionFind) union(a, b string) {
	if ra, rb := u.find(a), u.find(b); ra != rb {
		u[ra] = rb
	}
}

// dependentViews returns the views and materialized views that use
// relation rel, directly or through other views, as object keys.
func dependentViews(live *Live, byRelation map[string][]ColumnDependent, rel string) []string {
	var out []string
	seen := map[string]bool{rel: true}
	var visit func(v string)
	visit = func(v string) {
		for _, d := range byRelation[v] {
			if (d.Kind == "view" || d.Kind == "matview") && !seen[d.Name] {
				seen[d.Name] = true
				out = append(out, "rel:"+d.Name)
				visit(d.Name)
			}
		}
		for _, w := range live.ViewDependents[v] {
			if !seen[w] {
				seen[w] = true
				out = append(out, "rel:"+w)
				visit(w)
			}
		}
	}
	visit(rel)
	return out
}

// leaveOutNotOwned removes from plan the statements PostgreSQL would reject
// because the connecting role lacks the privileges of the owner of an
// object they act on (see Live.OwnerRoles): ALTER, COMMENT ON, CREATE OR
// REPLACE of an existing object and the drop that recreates one, the
// indexes, policies, rules and statistics of a table, a GRANT or REVOKE
// whose privileges the role doesn't hold with the grant option, a trigger on
// a table it has no TRIGGER privilege on. What the plan does for such an
// object goes as a whole, and so does what it does for an object it would
// have to drop and create again with it: the dependents of a column whose
// type changes, of a view it drops or of an extension update, and a new
// object that a left-out statement creates. One warning of kind not_owner
// names each such object and its owner, an owner the config's
// external.roles lists included: everything left out here is something the
// YAML asks for, since --prune leaves out what external roles own before.
// base is the snapshot the plan was made from,
// renamed the same after the plan's renames. Without a connection
// (OwnerRoles nil) nothing is left out.
func leaveOutNotOwned(plan *PlanDiff, desired *schema.Database, base, renamed *Live) {
	if base == nil || base.Offline || base.OwnerRoles == nil || !anyForeign(base) {
		return
	}
	ix := newOwnerIndex(renamed, base)
	foreign := func(key string) (string, bool) {
		owner := ix.owner(key)
		return owner, owner != "" && !base.mayDrop(owner)
	}
	byRelation := map[string][]ColumnDependent{}
	for col, deps := range base.ColumnDependents {
		rel := ColumnRelation(col)
		byRelation[rel] = append(byRelation[rel], deps...)
	}
	type planned struct {
		sec  *[]string
		i    int
		info stmtInfo
	}
	sections := []*[]string{&plan.ExtensionUpdates, &plan.Prelude, &plan.Creates, &plan.Alters, &plan.Drops, &plan.Concurrent}
	var stmts []planned
	uf := unionFind{}
	blocked := map[string]bool{}
	denied := map[string][]string{}
	grantsOnly := map[string]bool{}
	dropped := map[string]bool{}
	link := func(anchor, member string) {
		uf.union(anchor, member)
		if _, ok := foreign(member); ok {
			blocked[member] = true
			if _, seen := denied[member]; !seen {
				denied[member] = nil
			}
		}
	}
	for _, sec := range sections {
		for i, s := range *sec {
			info := statementObjects(s, ix)
			stmts = append(stmts, planned{sec, i, info})
			rejected := ""
			for _, a := range info.acts {
				k := ix.resolve(a.key)
				if _, ok := foreign(k); !ok || ix.allowed(base, a) {
					continue
				}
				rejected, blocked[k] = k, true
				if _, seen := denied[k]; !seen {
					grantsOnly[k] = true
				}
				denied[k] = append(denied[k], s)
				grantsOnly[k] = grantsOnly[k] && a.need == needGrant
			}
			if rejected != "" {
				for _, c := range info.creates {
					uf.union(rejected, ix.resolve(c))
				}
			}
			if info.drop != nil {
				for _, k := range info.drop.keys {
					dropped[k] = true
				}
			}
		}
	}
	for _, p := range stmts {
		for _, col := range p.info.retyped {
			anchor := "rel:" + ColumnRelation(col)
			views, objects := columnDependents(base, byRelation, col)
			for _, v := range views {
				link(anchor, "rel:"+v)
			}
			for _, d := range objects {
				link(anchor, "rel:"+d.Table)
			}
		}
		if d := p.info.drop; d != nil && dropsRelations(d.head) {
			for _, k := range d.keys {
				for _, v := range dependentViews(base, byRelation, strings.TrimPrefix(k, "rel:")) {
					link(k, v)
				}
			}
		}
		if e := p.info.update; e != "" && base.ExtensionDefs[e] != nil {
			for _, dep := range base.ExtensionDefs[e].UpdateDependents {
				if (dep.Kind == "view" || dep.Kind == "matview") && dropped["rel:"+dep.Name] {
					link("ext:"+e, "rel:"+dep.Name)
				}
			}
		}
	}
	if len(blocked) == 0 {
		return
	}
	roots := map[string]bool{}
	for k := range blocked {
		roots[uf.find(k)] = true
	}
	isBlocked := func(key string) bool { return roots[uf.find(ix.resolve(key))] }

	removed := map[string][]string{}
	note := func(object, stmt string) { removed[object] = append(removed[object], stmt) }
	gone := map[*[]string]map[int]string{}
	for _, p := range stmts {
		s := (*p.sec)[p.i]
		hit := ""
		for _, a := range p.info.acts {
			if isBlocked(a.key) {
				hit = ix.resolve(a.key)
				break
			}
		}
		for _, c := range p.info.creates {
			if hit == "" && isBlocked(c) {
				hit = ix.resolve(c)
			}
		}
		if hit == "" {
			continue
		}
		if gone[p.sec] == nil {
			gone[p.sec] = map[int]string{}
		}
		gone[p.sec][p.i] = ""
		d := p.info.drop
		if d == nil || len(d.keys) < 2 {
			note(hit, s)
			continue
		}
		var keep []string
		for i, k := range d.keys {
			if isBlocked(k) {
				note(ix.resolve(k), d.head+d.items[i]+";")
			} else {
				keep = append(keep, d.items[i])
			}
		}
		if len(keep) > 0 {
			gone[p.sec][p.i] = d.head + strings.Join(keep, ", ") + ";"
		}
	}
	removedStmts := map[string]bool{}
	for _, sec := range sections {
		g := gone[sec]
		if len(g) == 0 {
			continue
		}
		out := (*sec)[:0:0]
		for i, s := range *sec {
			repl, ok := g[i]
			if ok {
				removedStmts[s] = true
			}
			if !ok || repl != "" {
				out = append(out, cmp.Or(repl, s))
			}
		}
		*sec = out
	}

	kept := plan.Warnings[:0:0]
	for _, w := range plan.Warnings {
		if w.Statement != "" && removedStmts[w.Statement] {
			continue
		}
		if stmt, _, ok := strings.Cut(w.Message, ": "); ok && w.Kind == "public_revoke" && removedStmts[stmt+";"] {
			continue
		}
		kept = append(kept, w)
	}
	plan.Warnings = kept

	for _, k := range sortedKeys(denied) {
		owner, isForeign := foreign(k)
		root := uf.find(k)
		if !isForeign {
			continue
		}
		var coupled, others []string
		for _, o := range sortedKeys(removed) {
			if _, ownDenial := denied[o]; o != k && !ownDenial && uf.find(o) == root {
				coupled = append(coupled, removed[o]...)
				others = append(others, ix.describe(o))
			}
		}
		if len(removed[k])+len(coupled) == 0 {
			continue
		}
		w := notOwnerLeftOutWarning(base, ix.describe(k), owner, removed[k], coupled, others, grantsOnly[k], desired.External.HasRole(owner))
		merged := false
		prefix := ix.describe(k) + " is owned by " + owner + ","
		for i := range plan.Warnings {
			if pw := &plan.Warnings[i]; pw.Kind == "not_owner" && strings.HasPrefix(pw.Message, prefix) {
				if _, clause, ok := strings.Cut(pw.Message, " privileges, so "); ok {
					pw.Message = w.Message + "; for the same reason, " + clause
					merged = true
					break
				}
			}
		}
		if !merged {
			plan.Warnings = append(plan.Warnings, w)
		}
	}
}

// dropsRelations reports whether a DROP statement starting with head drops
// tables, views or materialized views, which views may depend on.
func dropsRelations(head string) bool {
	for _, kind := range []string{"drop view ", "drop materialized view ", "drop table "} {
		if strings.HasPrefix(head, kind) {
			return true
		}
	}
	return false
}

// notOwnerLeftOutWarning returns the not_owner warning for object, owned
// by owner, for which the plan leaves out own, the statements that need
// the owner's privileges, and coupled, those for the objects others names,
// which can't run without them, or while the object depends on those
// objects. grants says that only GRANT and REVOKE statements of its own
// were rejected, external that the config's external.roles lists owner.
func notOwnerLeftOutWarning(live *Live, object, owner string, own, coupled, others []string, grants, external bool) Warning {
	lacks := "does not have that role's privileges"
	if grants {
		lacks = "has neither that role's privileges nor the grant options the statements need"
	}
	count := func(stmts []string) string {
		if len(stmts) == 1 {
			return "the statement"
		}
		return fmt.Sprintf("the %d statements", len(stmts))
	}
	first := func(stmts []string) string {
		line, _, more := strings.Cut(strings.TrimSpace(stmts[0]), "\n")
		if more || len(stmts) > 1 {
			line += " ..."
		}
		return line
	}
	listed := ""
	if external {
		listed = ", which the config's external.roles lists,"
	}
	head := fmt.Sprintf("%s is owned by %s%s and %s, the connecting role, %s, so the plan leaves out ", object, owner, cmp.Or(listed, ","), live.CurrentUser, lacks)
	if len(own) == 0 {
		return Warning{Kind: "not_owner", Message: head + fmt.Sprintf("%s for %s, which PostgreSQL rejects while it depends on them (%s)", count(coupled), strings.Join(others, ", "), first(coupled))}
	}
	needs, them := " that need them", "them"
	if len(own) == 1 {
		needs, them = " that needs them", "it"
	}
	msg := head + count(own) + needs + " (" + first(own) + ")"
	if len(coupled) > 0 {
		msg += fmt.Sprintf(", and with %s %s for %s, which can't go without %s", them, count(coupled), strings.Join(others, ", "), them)
	}
	return Warning{Kind: "not_owner", Message: msg + "; have " + owner + " run " + them}
}
