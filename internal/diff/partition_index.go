package diff

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// PartitionIndexName returns the name PostgreSQL gives the index it creates
// on partition (schema.table) for index li of the partition's parent, when
// li is created on the parent or the partition is created or attached: the
// partition's name, the names of the index columns and "idx", shortened to
// 63 bytes the way PostgreSQL shortens them. A key column is named after the
// table column, one of columns, and an expression by the name PostgreSQL
// gave its index column, from opclasses (expr when there is none).
// PostgreSQL appends a number to the name when a relation of that name
// exists.
func PartitionIndexName(partition string, li *LiveIndex, opclasses []LiveIndexOpclass, columns map[string]*LiveColumn) string {
	names := make([]string, 0, len(li.Keys)+len(li.Include))
	for i, k := range li.Keys {
		name := identName(k)
		if columns[name] == nil {
			name = "expr"
			if i < len(opclasses) && opclasses[i].Column != "" {
				name = opclasses[i].Column
			}
		}
		names = append(names, name)
	}
	for _, k := range li.Include {
		names = append(names, identName(k))
	}
	_, rel := splitQualified(partition)
	return chooseObjectName(rel, indexNameAddition(indexColumnNames(names)), "idx")
}

// indexColumnNames numbers repeated index column names as PostgreSQL's
// ChooseIndexColumnNames does: expr, expr1, expr2.
func indexColumnNames(names []string) []string {
	out := make([]string, 0, len(names))
	taken := map[string]bool{}
	for _, n := range names {
		cur := n
		for i := 1; taken[cur]; i++ {
			suffix := strconv.Itoa(i)
			cur = clipBytes(n, maxIdentifierLength-len(suffix)) + suffix
		}
		taken[cur] = true
		out = append(out, cur)
	}
	return out
}

// indexNameAddition joins index column names with underscores until the
// result reaches 64 bytes, as PostgreSQL's ChooseIndexNameAddition does.
func indexNameAddition(names []string) string {
	buf := ""
	for _, n := range names {
		if buf != "" {
			buf += "_"
		}
		buf += clipBytes(n, maxIdentifierLength)
		if len(buf) > maxIdentifierLength {
			break
		}
	}
	return buf
}

// chooseObjectName joins name1, name2 and label with underscores, shortening
// the longer of name1 and name2 until the result fits in 63 bytes, as
// PostgreSQL's makeObjectName does.
func chooseObjectName(name1, name2, label string) string {
	n1, n2 := len(name1), len(name2)
	avail := maxIdentifierLength - len(label) - 2
	for n1+n2 > avail {
		if n1 > n2 {
			n1--
		} else {
			n2--
		}
	}
	return clipBytes(name1, n1) + "_" + clipBytes(name2, n2) + "_" + label
}

// partitionIndexPlan coordinates the indexes of partition trees within one
// plan. PostgreSQL gives a partition a copy of every index of its parent
// when the partition is created or attached, and an index created on a
// partitioned table creates one on every partition, unless the partition
// already has a matching index, which it attaches instead. So a partition's
// own declared index that matches an index of its parent is created before
// that index, which then attaches it, or it is the copy PostgreSQL made,
// renamed. Dropping an index drops the indexes attached to it as well.
type partitionIndexPlan struct {
	live     *Live
	desired  *schema.Database
	children map[string][]string
	// held and heldAlters are CREATE INDEX statements kept until the
	// partitions below their tables have their own indexes.
	held, heldAlters []heldIndex
	// dropped holds the indexes (schema.name) the main migration drops
	// before it creates them again; rebuilt holds those the no-transaction
	// migration drops and builds again, with their declarations.
	dropped map[string]bool
	rebuilt map[string]*schema.Index
}

type heldIndex struct {
	depth int
	stmts []string
}

func newPartitionIndexPlan(live *Live, desired *schema.Database) *partitionIndexPlan {
	p := &partitionIndexPlan{live: live, desired: desired, children: map[string][]string{}, dropped: map[string]bool{}, rebuilt: map[string]*schema.Index{}}
	for _, fq := range sortedKeys(desired.Tables) {
		if dt := desired.Tables[fq]; dt != nil && dt.PartitionOf != "" {
			p.children[dt.PartitionOf] = append(p.children[dt.PartitionOf], fq)
		}
	}
	return p
}

// holds reports whether a new index on partitioned table must wait until
// the declared partitions below the table exist with their own indexes,
// because one of them declares an index the database does not have yet.
func (p *partitionIndexPlan) holds(table string) bool {
	if p == nil {
		return false
	}
	for _, child := range p.children[table] {
		lt := p.live.Tables[child]
		for _, ix := range p.desired.Tables[child].Indexes {
			if ix != nil && len(ix.Columns) > 0 && (lt == nil || !lt.Indexes[LiveIndexName(child, ix, lt.Indexes)]) {
				return true
			}
		}
		if p.holds(child) {
			return true
		}
	}
	return false
}

// attachedBelow reports whether an index declared on a partition below
// table exists and is attached to index idxFq, or to an index attached to
// it, so that dropping idxFq drops it too.
func (p *partitionIndexPlan) attachedBelow(table, idxFq string) bool {
	if p == nil {
		return false
	}
	for _, child := range p.children[table] {
		lt := p.live.Tables[child]
		if lt == nil {
			continue
		}
		declared := map[string]bool{}
		for _, ix := range p.desired.Tables[child].Indexes {
			if ix != nil {
				declared[LiveIndexName(child, ix, lt.Indexes)] = true
			}
		}
		for _, name := range sortedKeys(lt.IndexDefs) {
			if li := lt.IndexDefs[name]; li != nil && li.Parent == idxFq && (declared[name] || p.attachedBelow(child, schemaPrefixOf(child)+name)) {
				return true
			}
		}
	}
	return false
}

// hold keeps stmts, a CREATE INDEX on table with what follows it, for
// heldStmts, or for heldAlterStmts when alter is true.
func (p *partitionIndexPlan) hold(table string, alter bool, stmts ...string) {
	depth := 0
	for dt := p.desired.Tables[table]; dt != nil && dt.PartitionOf != "" && depth < len(p.desired.Tables); dt = p.desired.Tables[dt.PartitionOf] {
		depth++
	}
	if alter {
		p.heldAlters = append(p.heldAlters, heldIndex{depth: depth, stmts: stmts})
	} else {
		p.held = append(p.held, heldIndex{depth: depth, stmts: stmts})
	}
}

// heldStmts returns the statements hold kept for the creates, those on the
// deepest partitions first, so that each finds the indexes below it to
// attach.
func (p *partitionIndexPlan) heldStmts() []string {
	if p == nil {
		return nil
	}
	return deepestFirst(p.held)
}

// heldAlterStmts is heldStmts for the statements hold kept for the alters.
func (p *partitionIndexPlan) heldAlterStmts() []string {
	if p == nil {
		return nil
	}
	return deepestFirst(p.heldAlters)
}

func deepestFirst(held []heldIndex) []string {
	sort.SliceStable(held, func(i, j int) bool { return held[i].depth > held[j].depth })
	out := []string{}
	for _, h := range held {
		out = append(out, h.stmts...)
	}
	return out
}

// partitions returns the partitions of table: those in the database, then
// the declared ones the plan creates.
func (p *partitionIndexPlan) partitions(live *Live, table string) []string {
	out := append([]string{}, live.Partitions[table]...)
	if p == nil {
		return out
	}
	for _, child := range p.children[table] {
		if live.Tables[child] == nil {
			out = append(out, child)
		}
	}
	return out
}

// partitioned reports whether partition table is partitioned itself.
func (p *partitionIndexPlan) partitioned(live *Live, table string) bool {
	if lt := live.Tables[table]; lt != nil {
		return lt.Partitioned
	}
	return p != nil && p.desired.Tables[table] != nil && p.desired.Tables[table].PartitionBy != nil
}

// hasForeignPartition reports whether a declared partition below table,
// which the plan creates, is a foreign table.
func (p *partitionIndexPlan) hasForeignPartition(table string) bool {
	if p == nil {
		return false
	}
	for _, child := range p.children[table] {
		if dt := p.desired.Tables[child]; dt.Foreign != nil && p.live.Tables[child] == nil {
			return true
		}
		if p.hasForeignPartition(child) {
			return true
		}
	}
	return false
}

// declaredMatch returns the name of the index partition declares with the
// same definition as ix, an index of its parent; "" when it declares none.
func (p *partitionIndexPlan) declaredMatch(partition string, ix *schema.Index) string {
	if p == nil || p.desired.Tables[partition] == nil {
		return ""
	}
	var live map[string]bool
	if lt := p.live.Tables[partition]; lt != nil {
		live = lt.Indexes
	}
	for _, d := range p.desired.Tables[partition].Indexes {
		if d != nil && len(d.Columns) > 0 && sameIndexDefinition(d, ix) {
			return LiveIndexName(partition, d, live)
		}
	}
	return ""
}

// sameIndexDefinition reports whether declared indexes a and b define the
// same index, whatever their names, storage parameters, tablespaces and
// comments, which PostgreSQL does not compare either when it attaches a
// partition's index to its parent's.
func sameIndexDefinition(a, b *schema.Index) bool {
	text := func(ix *schema.Index) string {
		c := *ix
		c.With, c.Tablespace = nil, ""
		using := strings.ToLower(strings.TrimSpace(c.Using))
		if using == "" {
			using = "btree"
		}
		return fmt.Sprintf("%t %s (%s)%s", c.Unique, using, renderIndexElems(&c, nil), renderIndexTail(&c))
	}
	return constraintDefsEqual(text(a), text(b))
}

// attachedIndex returns the index of live partition lt that is attached to
// index idxFq of its parent; "" when there is none.
func attachedIndex(lt *LiveTable, idxFq string) string {
	if lt == nil {
		return ""
	}
	for _, name := range sortedKeys(lt.IndexDefs) {
		if li := lt.IndexDefs[name]; li != nil && li.Parent == idxFq {
			return name
		}
	}
	return ""
}

// adoptableIndex returns the index of live partition lt that is attached
// to an index of its parent, that no index declared on the partition (dt)
// names and that ix, declared on the partition but missing from the
// database, describes: renaming it gives ix. Names in taken are already
// adopted. "" when there is none.
func adoptableIndex(fq string, dt *schema.Table, lt *LiveTable, ix *schema.Index, taken map[string]bool) string {
	declared := map[string]bool{}
	for _, d := range dt.Indexes {
		if d != nil {
			declared[LiveIndexName(fq, d, lt.Indexes)] = true
		}
	}
	for _, name := range sortedKeys(lt.IndexDefs) {
		li := lt.IndexDefs[name]
		if li == nil || li.Parent == "" || li.Constraint || declared[name] || taken[name] {
			continue
		}
		if !indexChanged(ix, lt, name) {
			return name
		}
	}
	return ""
}

// clonedIndex returns the name of the copy of an index of its parent that
// PostgreSQL creates on partition fq when the plan creates it, if ix,
// declared on fq, describes that copy; "" when it describes none. ok is
// false when it does, but the copy's name cannot be told in advance,
// because another index of the parent would get the same name or a
// relation of that name could exist first. Names in taken are already
// claimed.
func (p *partitionIndexPlan) clonedIndex(fq string, ix *schema.Index, taken map[string]bool) (name string, ok bool) {
	if p == nil {
		return "", true
	}
	root := fq
	for {
		dt := p.desired.Tables[root]
		if dt == nil || dt.PartitionOf == "" || dt.Foreign != nil {
			return "", true
		}
		root = dt.PartitionOf
		if p.live.Tables[root] != nil {
			break
		}
	}
	lt, dt := p.live.Tables[root], p.desired.Tables[root]
	if dt == nil {
		return "", true
	}
	match := ""
	for _, d := range dt.Indexes {
		if d == nil || len(d.Columns) == 0 {
			continue
		}
		n := LiveIndexName(root, d, lt.Indexes)
		if li := lt.IndexDefs[n]; li != nil && !li.Constraint && !indexChanged(d, lt, n) && !indexChanged(ix, lt, n) {
			match = n
			break
		}
	}
	if match == "" {
		return "", true
	}
	for i, k := range lt.IndexDefs[match].Keys {
		if ocs := lt.IndexOpclasses[match]; i >= len(ocs) || (lt.Columns[identName(k)] != nil && ocs[i].Column != identName(k)) {
			return "", false
		}
	}
	clone := PartitionIndexName(fq, lt.IndexDefs[match], lt.IndexOpclasses[match], lt.Columns)
	for _, n := range sortedKeys(lt.IndexDefs) {
		if li := lt.IndexDefs[n]; n != match && li != nil && !li.Constraint && PartitionIndexName(fq, li, lt.IndexOpclasses[n], lt.Columns) == clone {
			return "", false
		}
	}
	sch, _ := splitQualified(fq)
	if taken[clone] || p.relationNameTaken(sch, clone) {
		return "", false
	}
	return clone, true
}

// relationNameTaken reports whether a table, view, sequence or index named
// name exists in schema sch or is declared there.
func (p *partitionIndexPlan) relationNameTaken(sch, name string) bool {
	fq := sch + "." + name
	if p.live.Tables[fq] != nil || p.live.Views[fq] || p.live.MatViews[fq] || p.live.Sequences[fq] {
		return true
	}
	if p.desired.Tables[fq] != nil || p.desired.Views[fq] != nil || p.desired.Sequences[fq] != nil {
		return true
	}
	for _, tables := range []map[string]*LiveTable{p.live.Tables, p.live.MatViewTables} {
		for k, lt := range tables {
			if lt != nil && strings.HasPrefix(k, sch+".") && lt.Indexes[name] {
				return true
			}
		}
	}
	for k, dt := range p.desired.Tables {
		if dt != nil && strings.HasPrefix(k, sch+".") && declaresIndex(k, dt.Indexes, name) {
			return true
		}
	}
	for k, v := range p.desired.Views {
		if v != nil && strings.HasPrefix(k, sch+".") && declaresIndex(k, v.Indexes, name) {
			return true
		}
	}
	return false
}

func declaresIndex(fq string, indexes []*schema.Index, name string) bool {
	for _, ix := range indexes {
		if ix != nil && indexName(fq, ix) == name {
			return true
		}
	}
	return false
}

func liveIndexDef(lt *LiveTable, name string) *LiveIndex {
	if lt == nil || !lt.Indexes[name] {
		return nil
	}
	return lt.IndexDefs[name]
}
