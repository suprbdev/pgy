package schema

import (
	yaml "gopkg.in/yaml.v3"
)

// DecodeYAML decodes the first document of b into out like yaml.Unmarshal,
// except that every mapping key is read as its source text. A key such as
// null, 2024, true or 1.5 then names a column or an option as written,
// where yaml.Unmarshal would resolve it to a non-string key and turn the
// whole mapping into a map that is not keyed by strings. It returns the
// document's node tree; out is left untouched for an empty document.
func DecodeYAML(b []byte, out any) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		return &doc, nil
	}
	stringKeys(&doc)
	return &doc, doc.Decode(out)
}

func stringKeys(n *yaml.Node) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode {
				continue
			}
			if tag := k.ShortTag(); tag != "!!str" && tag != "!!merge" {
				k.Tag = "!!str"
			}
		}
	}
	for _, c := range n.Content {
		stringKeys(c)
	}
}
