package pull

import (
	"bytes"
	"math"
	"strconv"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// node is a yaml.Node with ordered-mapping helpers.
type node = yaml.Node

func newMap() *node { return &yaml.Node{Kind: yaml.MappingNode} }

func flowMap() *node { return &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle} }

func str(s string) *node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }

func num(s string) *node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: s} }

// floatNum renders a number as an integer when it has no fraction.
func floatNum(v float64) *node {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return num(strconv.FormatFloat(v, 'f', -1, 64))
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(float64(float32(v)), 'g', -1, 32)}
}

func boolean(b bool) *node {
	v := "false"
	if b {
		v = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}
}

// literal renders multi-line text as a block scalar.
func literal(s string) *node {
	n := str(strings.TrimSpace(s))
	if strings.Contains(n.Value, "\n") {
		n.Style = yaml.LiteralStyle
	}
	return n
}

func list(items []string) *node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, it := range items {
		n.Content = append(n.Content, str(it))
	}
	return n
}

func addStr(m *node, key, value string) {
	if value != "" {
		add(m, key, str(value))
	}
}

func render(doc *node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{doc}}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// add appends a key/value pair to a mapping node, keeping insertion order.
func add(m *node, key string, v *node) {
	k := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
	m.Content = append(m.Content, k, v)
}
