package schema

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// fillWithOrderFromNode records the order in which the `schema <name>:`
// blocks of root list the storage parameters (`with`) of tables, their
// indexes, primary keys and constraints, and of materialized views and
// their indexes.
func fillWithOrderFromNode(root *yaml.Node, db *Database) {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return
	}
	top := resolveAlias(root.Content[0])
	if top.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		schemaName, ok := strings.CutPrefix(top.Content[i].Value, "schema ")
		body := resolveAlias(top.Content[i+1])
		if !ok || body.Kind != yaml.MappingNode {
			continue
		}
		schemaName = strings.TrimSpace(schemaName)
		for j := 0; j+1 < len(body.Content); j += 2 {
			key, def := body.Content[j].Value, resolveAlias(body.Content[j+1])
			if def.Kind != yaml.MappingNode {
				continue
			}
			if name, _ := tableKey(key); name != "" {
				if t := db.Tables[qualify(schemaName, name)]; t != nil {
					t.WithOrder = keyOrder(mappingValue(def, "with"))
					namedWithOrders(mappingValue(def, "indexes"), func(name string, order []string) {
						for _, ix := range t.Indexes {
							if ix != nil && ix.Name == name {
								ix.WithOrder = order
							}
						}
					})
					namedWithOrders(mappingValue(def, "constraints"), func(name string, order []string) {
						for _, c := range t.Constraints {
							if c != nil && c.Name == name {
								c.WithOrder = order
							}
						}
					})
					namedWithOrders(mappingValue(def, "primaryKey"), func(_ string, order []string) {
						t.PrimaryKeyWithOrder = order
					})
				}
				continue
			}
			if name, ok := strings.CutPrefix(key, "materialized view "); ok {
				if v := db.Views[qualify(schemaName, strings.TrimSpace(name))]; v != nil {
					v.WithOrder = keyOrder(mappingValue(def, "with"))
					namedWithOrders(mappingValue(def, "indexes"), func(name string, order []string) {
						for _, ix := range v.Indexes {
							if ix != nil && ix.Name == name {
								ix.WithOrder = order
							}
						}
					})
				}
			}
		}
	}
}

// namedWithOrders calls set with the name and the `with` key order of each
// entry of n, a mapping of names to definitions.
func namedWithOrders(n *yaml.Node, set func(name string, order []string)) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if order := keyOrder(mappingValue(resolveAlias(n.Content[i+1]), "with")); order != nil {
			set(n.Content[i].Value, order)
		}
	}
}

// keyOrder returns the keys of mapping node n in the order they are
// written, or nil when n is not a mapping.
func keyOrder(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]string, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, n.Content[i].Value)
	}
	return out
}
