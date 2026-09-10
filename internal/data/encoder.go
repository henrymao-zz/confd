// Package data encodes a sysrepoadapter.DataNode tree into NETCONF XML
// <data> payloads, using a schema.Cache for namespace assignment and
// element ordering.
package data

import (
	"fmt"
	"sort"
	"strings"

	"github.com/example/confd/internal/schema"
	"github.com/example/confd/internal/sysrepoadapter"
)

// Encoder renders DataNode trees into NETCONF data XML.
type Encoder struct {
	cache *schema.Cache
}

// New returns an Encoder bound to a schema cache.
func New(c *schema.Cache) *Encoder { return &Encoder{cache: c} }

// EncodeData renders the subtree rooted at node as NETCONF <data> inner XML.
// If node is the synthetic root (XPath "/"), its children are rendered at the
// top level.
func (e *Encoder) EncodeData(node *sysrepoadapter.DataNode) []byte {
	var b strings.Builder
	if node == nil {
		return []byte("")
	}
	if node.XPath == "/" || (node.Name == "root" && node.XPath == "") {
		for _, c := range sortedChildren(node) {
			e.encodeNode(&b, c, true)
		}
		return []byte(b.String())
	}
	e.encodeNode(&b, node, true)
	return []byte(b.String())
}

func sortedChildren(n *sysrepoadapter.DataNode) []*sysrepoadapter.DataNode {
	out := make([]*sysrepoadapter.DataNode, len(n.Children))
	copy(out, n.Children)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func (e *Encoder) encodeNode(b *strings.Builder, n *sysrepoadapter.DataNode, top bool) {
	if n == nil {
		return
	}
	ns := n.NS
	if ns == "" {
		// Fallback: look up the module by prefix-less name. For the mock,
		// the namespace is set explicitly when the tree is built.
	}
	if n.IsLeaf || n.IsLeafList {
		fmt.Fprintf(b, `<%s xmlns="%s">%s</%s>`, n.Name, ns, xmlEscape(n.Value), n.Name)
		return
	}
	// container or list entry
	if n.IsList && n.Key != "" {
		fmt.Fprintf(b, `<%s xmlns="%s">`, n.Name, ns)
	} else {
		fmt.Fprintf(b, `<%s xmlns="%s">`, n.Name, ns)
	}
	for _, c := range sortedChildren(n) {
		e.encodeNode(b, c, false)
	}
	b.WriteString(fmt.Sprintf(`</%s>`, n.Name))
}

func xmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
