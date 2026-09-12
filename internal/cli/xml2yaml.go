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
func xmlDataToYAML(xmlData []byte) (string, error) {
	m, err := xmlToMap(xmlData)
	if err != nil {
		return "", err
	}
	out, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// xmlToMap parses XML bytes into a generic map[string]interface{} tree.
// The top-level elements become keys in the returned map. Namespace
// declarations (xmlns*) are stripped. Attributes are prefixed with "@".
// Repeated sibling elements with the same tag name become a slice.
func xmlToMap(xmlData []byte) (map[string]interface{}, error) {
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
			// Strip namespace from element name
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

	// Build map from top-level children of root
	result := buildMap(root.Children)
	return result, nil
}

// xmlNode is an intermediate representation for the XML tree.
type xmlNode struct {
	Name     string
	Attrs    map[string]string
	Text     string
	Children []*xmlNode
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
		// Skip namespace declarations
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

// buildMap converts a slice of xmlNodes into a map. Repeated element
// names become slices.
func buildMap(nodes []*xmlNode) map[string]interface{} {
	m := map[string]interface{}{}
	for _, n := range nodes {
		val := nodeValue(n)
		if existing, ok := m[n.Name]; ok {
			// Already have this tag name — convert to slice
			if s, ok2 := existing.([]interface{}); ok2 {
				m[n.Name] = append(s, val)
			} else {
				m[n.Name] = []interface{}{existing, val}
			}
		} else {
			m[n.Name] = val
		}
	}
	return m
}

// nodeValue converts a single xmlNode into its Go value:
// leaf (text only) → auto-typed scalar
// container (child elements) → nested map
// attributes are added as @-prefixed keys
func nodeValue(n *xmlNode) interface{} {
	// If the node has child elements, build a nested map
	if len(n.Children) > 0 {
		m := buildMap(n.Children)
		// Add attributes as @-prefixed entries
		for k, v := range n.Attrs {
			m["@"+k] = v
		}
		return m
	}

	// Leaf node — use text value, auto-typed
	if n.Text == "" {
		// Check for attributes only
		if len(n.Attrs) > 0 {
			m := map[string]interface{}{}
			for k, v := range n.Attrs {
				m["@"+k] = v
			}
			return m
		}
		return nil
	}

	return autoType(n.Text)
}

// autoType converts a string to bool, int64, or float64 if possible,
// otherwise returns the string as-is.
func autoType(s string) interface{} {
	// Boolean
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	// Integer
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	// Float
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}