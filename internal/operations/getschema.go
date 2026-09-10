package operations

import (
	"encoding/xml"
	"strings"

	"github.com/example/confd/internal/rpc"
)

// <get-schema> (RFC 6022) returns a YANG module's source text.
type getSchemaHandler struct{ deps Deps }

type getSchemaParams struct {
	Identifier string `xml:"identifier"`
	Version    string `xml:"version"`
	Format     string `xml:"format"`
}

func (h *getSchemaHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	var p getSchemaParams
	if err := xml.Unmarshal(inner, &p); err != nil {
		return nil, rpc.NewError(rpc.TagBadElement, "invalid <get-schema>: "+err.Error())
	}
	if p.Identifier == "" {
		return nil, rpc.NewError(rpc.TagMissingElement, "<get-schema> requires <identifier>")
	}
	format := strings.TrimSpace(p.Format)
	if format == "" {
		format = "yang"
	}
	if format != "yang" {
		return nil, rpc.NewError(rpc.TagInvalidValue,
			"unsupported schema format '"+format+"' (only 'yang')")
	}
	text, ok := h.deps.Cache.SourceText(p.Identifier)
	if !ok {
		return nil, rpc.NewError(rpc.TagInvalidValue,
			"module '"+p.Identifier+"' not found")
	}
	if p.Version != "" {
		m := h.deps.Cache.Module(p.Identifier)
		if m == nil || m.Revision != p.Version {
			return nil, rpc.NewError(rpc.TagInvalidValue,
				"revision '"+p.Version+"' not found for module '"+p.Identifier+"'")
		}
	}
	encoded := xmlEscape(text)
	body := []byte(`<data xmlns="urn:ietf:params:xml:ns:yang:ietf-netconf-monitoring">` +
		encoded + `</data>`)
	return &rpc.Reply{MessageID: msg.MessageID, Body: body}, nil
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
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
