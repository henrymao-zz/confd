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
//
// Config false nodes are already filtered server-side by
// cf_filter_config_false in cgo_stub.go (using libyang LYS_CONFIG_R).
// Dynamic neighbor/address entries (origin != "static" or missing
// origin on neighbors) are filtered client-side here.
func xmlDataToYAML(xmlData []byte) (string, error) {
	root := parseXML(xmlData)
	filterDynamic(root)
	node := buildYAMLNode(root.Children)
	out, err := yaml.Marshal(node)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// filterDynamic removes dynamic neighbor/address entries from the XML
// tree. These are operational data (ARP/ND cache, DHCP addresses)
// populated by plugins, not user-configured.
//
// - neighbor entries with origin != "static" (or missing origin) are
//   removed — they're dynamic ARP/ND cache entries.
// - address entries with origin != "static" (but only if origin is
//   present; missing origin on address = keep, may be user-configured).
// - origin leaf is removed (it's config false, already handled server-side
//   but may still appear in filtered XML).
func filterDynamic(node *xmlNode) {
	var filtered []*xmlNode
	for _, child := range node.Children {
		if child.Name == "neighbor" {
			origin := findChildText(child, "origin")
			if origin != "static" {
				continue // dynamic ARP/ND entry — skip
			}
		}
		if child.Name == "address" {
			origin := findChildText(child, "origin")
			if origin != "" && origin != "static" {
				continue // DHCP/SLAAC address — skip
			}
		}
		// Remove origin leaf (config false, operational metadata)
		if child.Name == "origin" {
			continue
		}
		filterDynamic(child)
		filtered = append(filtered, child)
	}
	node.Children = filtered
}

// findChildText returns the text content of a named child element,
// or empty string if not found.
func findChildText(node *xmlNode, name string) string {
	for _, child := range node.Children {
		if child.Name == name {
			return child.Text
		}
	}
	return ""
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
		// Collapse ip + prefix-length into CIDR if applicable
		if cidrSeq, ok := tryCIDRSequence(group); ok {
			return cidrSeq
		}
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
			// Collapse ip + prefix-length into CIDR if applicable
			if cidrSeq, ok := tryCIDRSequence(g); ok {
				mapNode.Content = append(mapNode.Content, keyNode, cidrSeq)
				continue
			}
			seqNode := &yaml.Node{Kind: yaml.SequenceNode}
			for _, n := range g.nodes {
				seqNode.Content = append(seqNode.Content, xmlNodeToYAML(n))
			}
			mapNode.Content = append(mapNode.Content, keyNode, seqNode)
		} else {
			// Single node — try CIDR collapse for address with ip+prefix-length
			if cidr, ok := tryCIDRSingle(g.nodes[0]); ok {
				mapNode.Content = append(mapNode.Content, keyNode, cidr)
			} else {
				mapNode.Content = append(mapNode.Content, keyNode, xmlNodeToYAML(g.nodes[0]))
			}
		}
	}
	return mapNode
}

// tryCIDRSequence checks if a group of address nodes each contain an
// "ip" child and a "prefix-length" (or "netmask") child. If so, returns
// a sequence of CIDR-notation scalar nodes (e.g. "192.168.1.1/24").
func tryCIDRSequence(g childGroup) (*yaml.Node, bool) {
	for _, n := range g.nodes {
		if !hasIPAndPrefix(n) {
			return nil, false
		}
	}
	seqNode := &yaml.Node{Kind: yaml.SequenceNode}
	for _, n := range g.nodes {
		cidr := buildCIDR(n)
		seqNode.Content = append(seqNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: cidr})
	}
	return seqNode, true
}

// tryCIDRSingle checks if a single node is an address entry with
// ip + prefix-length children. If so, returns a CIDR scalar node.
func tryCIDRSingle(n *xmlNode) (*yaml.Node, bool) {
	if !hasIPAndPrefix(n) {
		return nil, false
	}
	cidr := buildCIDR(n)
	return &yaml.Node{Kind: yaml.ScalarNode, Value: cidr}, true
}

// hasIPAndPrefix returns true if the node has child elements named
// "ip" and either "prefix-length" or "netmask".
func hasIPAndPrefix(n *xmlNode) bool {
	var hasIP, hasPrefix bool
	for _, c := range n.Children {
		switch c.Name {
		case "ip":
			hasIP = true
		case "prefix-length", "netmask":
			hasPrefix = true
		}
	}
	return hasIP && hasPrefix
}

// buildCIDR extracts the ip and prefix-length (or netmask) from an
// address node's children and returns CIDR notation (e.g. "10.0.0.1/24").
// For netmask, converts to prefix length (e.g. 255.255.255.0 → 24).
func buildCIDR(n *xmlNode) string {
	ip := ""
	prefix := ""
	for _, c := range n.Children {
		switch c.Name {
		case "ip":
			ip = c.Text
		case "prefix-length":
			prefix = c.Text
		case "netmask":
			prefix = netmaskToPrefix(c.Text)
		}
	}
	if prefix == "" {
		return ip
	}
	return ip + "/" + prefix
}

// netmaskToPrefix converts a dotted-decimal netmask to a prefix length.
// e.g. "255.255.255.0" → "24", "255.255.0.0" → "16".
func netmaskToPrefix(netmask string) string {
	octets := strings.Split(netmask, ".")
	if len(octets) != 4 {
		return "0"
	}
	bits := 0
	for _, o := range octets {
		val, err := strconv.Atoi(o)
		if err != nil {
			return "0"
		}
		for i := 7; i >= 0; i-- {
			if val&(1<<i) != 0 {
				bits++
			}
		}
	}
	return strconv.Itoa(bits)
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