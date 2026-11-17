package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// keptInheritanceChildren returns the undeclared tables and foreign tables
// that prune keeps although it would otherwise drop them, with a warning
// naming them. A table that inherits (INHERITS, not partitioning) from a
// table the plan keeps holds rows that queries on that parent return, as
// TimescaleDB chunks and inheritance-based partition sets do, so dropping
// it loses data the declared parent still shows. An undeclared parent of a
// kept child is kept as well, since DROP TABLE refuses to drop it alone.
func keptInheritanceChildren(live *Live, desired *schema.Database, migrationsTable string) (map[string]bool, []Warning) {
	if migrationsTable == "" {
		migrationsTable = "pgy.migrations"
	}
	managed := managedSchemas(desired)
	candidate := map[string]bool{}
	for k, lt := range live.Tables {
		i := strings.IndexByte(k, '.')
		if lt == nil || lt.IsPartition || k == migrationsTable || i <= 0 || !managed[k[:i]] || desired.Tables[k] != nil || live.ExtensionMembers["rel:"+k] {
			continue
		}
		candidate[k] = true
	}
	keptBy := map[string]string{}
	heldBy := map[string]string{}
	for changed := true; changed; {
		changed = false
		for _, k := range sortedKeys(candidate) {
			if _, ok := keptBy[k]; ok {
				continue
			}
			if _, ok := heldBy[k]; ok {
				continue
			}
			for _, p := range live.Tables[k].Inherits {
				_, pKept := keptBy[p]
				_, pHeld := heldBy[p]
				if !candidate[p] || pKept || pHeld {
					keptBy[k], changed = p, true
					break
				}
			}
		}
		for _, k := range sortedKeys(keptBy) {
			for _, p := range live.Tables[k].Inherits {
				_, pKept := keptBy[p]
				_, pHeld := heldBy[p]
				if candidate[p] && !pKept && !pHeld {
					heldBy[p], changed = k, true
				}
			}
		}
	}
	if len(keptBy) == 0 {
		return nil, nil
	}
	kept := map[string]bool{}
	byParent := map[string][]string{}
	for k, p := range keptBy {
		kept[k] = true
		byParent[p] = append(byParent[p], k)
	}
	var warnings []Warning
	for _, p := range sortedKeys(byParent) {
		children := byParent[p]
		sort.Strings(children)
		warnings = append(warnings, Warning{Table: p, Message: fmt.Sprintf("%s is kept, so its undeclared inheritance children are not dropped: %s; their rows are part of %s, so declare them with inherits, or drop them by hand if they should go", p, strings.Join(children, ", "), p)})
	}
	for _, p := range sortedKeys(heldBy) {
		kept[p] = true
		warnings = append(warnings, Warning{Table: p, Message: fmt.Sprintf("table %s is not declared but is not dropped because %s inherits from it and is kept", p, heldBy[p])})
	}
	return kept, warnings
}
