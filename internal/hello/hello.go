// Package hello implements NETCONF <hello> message construction and the
// base:1.0/1.1 capability negotiation that drives the framing upgrade.
package hello

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/example/confd/internal/framing"
)

const (
	// Base capability URIs per RFC 6241/4741.
	Base10 = "urn:ietf:params:netconf:base:1.0"
	Base11 = "urn:ietf:params:netconf:base:1.1"
	NS     = "urn:ietf:params:xml:ns:netconf:base:1.0"
)

// Capability describes one advertised <capability> URI plus optional
// query parameters, for the server's own advertisement.
type Capability struct {
	URI      string
	Revision string
	Features []string
}

// URI renders the full capability URI with query suffix when applicable.
func (c Capability) URIString() string {
	if c.Revision == "" && len(c.Features) == 0 {
		return c.URI
	}
	q := []string{}
	if c.Revision != "" {
		q = append(q, "revision="+c.Revision)
	}
	if len(c.Features) > 0 {
		q = append(q, "features="+strings.Join(c.Features, ","))
	}
	return c.URI + "?" + strings.Join(q, "&")
}

// Hello is the parsed <hello> message.
type Hello struct {
	XMLName       xml.Name `xml:"hello"`
	Capabilities  []string `xml:"capabilities>capability"`
	SessionIDAttr string   `xml:"session-id,attr,omitempty"`
}

// Build constructs a server <hello> with the given session-id and capability
// list. The base:1.1 capability is always included first.
func Build(sessionID uint64, caps []Capability) []byte {
	var b strings.Builder
	b.WriteString(`<hello xmlns="`)
	b.WriteString(NS)
	b.WriteString(`">`)
	fmt.Fprintf(&b, `<session-id>%d</session-id>`, sessionID)
	b.WriteString(`<capabilities>`)
	for _, c := range caps {
		b.WriteString(`<capability>`)
		b.WriteString(xmlEscape(c.URIString()))
		b.WriteString(`</capability>`)
	}
	b.WriteString(`</capabilities></hello>`)
	return []byte(b.String())
}

// Parse decodes a peer <hello> and returns the list of advertised capability URIs.
func Parse(payload []byte) (*Hello, error) {
	var h Hello
	if err := xml.Unmarshal(payload, &h); err != nil {
		return nil, fmt.Errorf("hello: invalid <hello>: %w", err)
	}
	return &h, nil
}

// SupportsBase11 reports whether the peer advertised base:1.1.
func (h *Hello) SupportsBase11() bool {
	for _, c := range h.Capabilities {
		if c == Base11 {
			return true
		}
	}
	return false
}

// Negotiate upgrades both framing endpoints to base:1.1 when the peer hello
// advertises base:1.1. Returns true if the upgrade happened.
func Negotiate(h *Hello, r *framing.Reader, w *framing.Writer) bool {
	if h == nil || !h.SupportsBase11() {
		return false
	}
	r.Upgrade()
	w.Upgrade()
	return true
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}
