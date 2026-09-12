package cli

import (
	"encoding/xml"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// xmlDataToYAML converts the inner XML of a <get-config> <data> element
// into a YAML string. All xmlns attributes are stripped, repeated sibling
// elements become YAML sequences, and leaf text values are auto-typed.
// YANG key leaf names (name, ip, key, id, etc.) are rendered first in
// each mapping, followed by remaining fields alphabetically.
func xmlDataToYAML(xmlData []byte) (string, error) {
	root := parseXML(xmlData)
	node := buildYAMLNode(root.Children)
	out, err := yaml.Marshal(node)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// keyPriority defines the rendering order for common YANG key leaf names.
// Keys listed here are rendered first (in this order), then all other
// fields alphabetically.
var keyPriority = map[string]int{
	"name":      0,
	"ip":        1,
	"key":       2,
	"id":        3,
	"identifier": 4,
	"vlan-id":   5,
}

// xmlNode is an intermediate representation for the XML tree.
type xmlNode struct {
	Name     string
	Attrs    map[string]string
	Text     string
	Children []*xmlNode
}

// parseXML builds an xmlNode tree from raw XML bytes.
func parseXML(xmlData []byte) *xmlNode {
	dec := xml.NewDecoder(strings.NewReader(string(xmlData)))
	root := &xmlNode{Name: "", Children: []*xmlNode{}}
	stack := []*xmlNode{root}

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := localName(t.Name.Local)
			node := &xmlNode{
				Name:     name,
				Attrs:    attrsMap(t.Attr),
				Children: []*xmlNode{},
			}
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, node)
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			text := strings.TrimSpace(string(t))
			if text != "" {
				node := stack[len(stack)-1]
				node.Text = text
			}
		}
	}
	return root
}

// localName strips any namespace prefix from an element name.
func localName(name string) string {
	if idx := strings.Index(name, ":"); idx > 0 {
		return name[idx+1:]
	}
	return name
}

// attrsMap converts xml.Attr slice to a map, stripping xmlns attributes.
func attrsMap(attrs []xml.Attr) map[string]string {
	m := map[string]string{}
	for _, a := range attrs {
		if a.Name.Local == "xmlns" || a.Name.Space == "xmlns" {
			continue
		}
		key := localName(a.Name.Local)
		m[key] = a.Value
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// buildYAMLNode converts a slice of xmlNodes (siblings) into a yaml.Node.
// If all siblings have the same tag name, the result is a sequence node.
// Otherwise, it's a mapping node with key-priority ordering.
func buildYAMLNode(children []*xmlNode) *yaml.Node {
	// Group siblings by tag name to detect repeated elements
	groups := groupChildren(children)

	// Check if this should be a sequence (single repeated tag name)
	if len(groups) == 1 && len(groups[0].nodes) > 1 {
		group := groups[0]
		seqNode := &yaml.Node{Kind: yaml.SequenceNode}
		for _, n := range group.nodes {
			seqNode.Content = append(seqNode.Content, xmlNodeToYAML(n))
		}
		return seqNode
	}

	// Mapping node with sorted keys
	mapNode := &yaml.Node{Kind: yaml.MappingNode}
	for _, g := range groups {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: g.name}
		if len(g.nodes) > 1 {
			// Multiple siblings with same name → sequence
			seqNode := &yaml.Node{Kind: yaml.SequenceNode}
			for _, n := range g.nodes {
				seqNode.Content = append(seqNode.Content, xmlNodeToYAML(n))
			}
			mapNode.Content = append(mapNode.Content, keyNode, seqNode)
		} else {
			mapNode.Content = append(mapNode.Content, keyNode, xmlNodeToYAML(g.nodes[0]))
		}
	}
	return mapNode
}

// childGroup holds siblings with the same tag name.
type childGroup struct {
	name  string
	nodes []*xmlNode
}

// groupChildren groups sibling xmlNodes by tag name, preserving first
// appearance order, then sorts groups by key priority.
func groupChildren(children []*xmlNode) []childGroup {
	var order []string
	groupMap := map[string]*childGroup{}
	for _, n := range children {
		g, ok := groupMap[n.Name]
		if !ok {
			g = &childGroup{name: n.Name}
			groupMap[n.Name] = g
			order = append(order, n.Name)
		}
		g.nodes = append(g.nodes, n)
	}
	// Sort by key priority, then alphabetically
	result := make([]childGroup, 0, len(order))
	for _, name := range order {
		result = append(result, *groupMap[name])
	}
	sortGroups(result)
	return result
}

// sortGroups sorts child groups by key priority (name, ip, key, id
// first), then alphabetically.
func sortGroups(groups []childGroup) {
	for i := 1; i < len(groups); i++ {
		for j := i; j > 0 && lessGroup(groups[j], groups[j-1]); j-- {
			groups[j], groups[j-1] = groups[j-1], groups[j]
		}
	}
}

// lessGroup returns true if group a should be rendered before group b.
func lessGroup(a, b childGroup) bool {
	pa, oka := keyPriority[a.name]
	pb, okb := keyPriority[b.name]
	if oka && okb {
		return pa < pb
	}
	if oka {
		return true
	}
	if okb {
		return false
	}
	return a.name < b.name
}

// xmlNodeToYAML converts a single xmlNode into a yaml.Node:
// leaf (text only) → scalar node
// container (child elements) → mapping node
func xmlNodeToYAML(n *xmlNode) *yaml.Node {
	// If the node has child elements, build a nested mapping
	if len(n.Children) > 0 {
		childNode := buildYAMLNode(n.Children)
		// Add attributes as @-prefixed entries at the end
		if len(n.Attrs) > 0 {
			for k, v := range n.Attrs {
				keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "@" + k}
				valNode := &yaml.Node{Kind: yaml.ScalarNode, Value: v}
				childNode.Content = append(childNode.Content, keyNode, valNode)
			}
		}
		return childNode
	}

	// Leaf node
	if n.Text == "" {
		if len(n.Attrs) > 0 {
			mapNode := &yaml.Node{Kind: yaml.MappingNode}
			for k, v := range n.Attrs {
				keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: "@" + k}
				valNode := &yaml.Node{Kind: yaml.ScalarNode, Value: v}
				mapNode.Content = append(mapNode.Content, keyNode, valNode)
			}
			return mapNode
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Value: "null"}
	}

	return &yaml.Node{Kind: yaml.ScalarNode, Value: n.Text, Tag: yamlTag(n.Text)}
}

// yamlTag returns the YAML tag for auto-typed scalar values.
func yamlTag(s string) string {
	if s == "true" || s == "false" {
		return "!!bool"
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return "!!int"
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return "!!float"
	}
	return ""
}