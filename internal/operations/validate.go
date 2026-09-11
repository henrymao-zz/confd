package operations

import (
	"encoding/xml"
	"strings"

	"github.com/example/confd/internal/rpc"
	"github.com/example/confd/internal/sysrepoadapter"
)

// <validate> validates the current datastore + staged edits without
// applying them. Per RFC 6241 §7.5.
type validateHandler struct{ deps Deps }

type validateParams struct {
	Source struct {
		XMLName xml.Name
		Inner   []byte `xml:",innerxml"`
	} `xml:"source"`
}

func (h *validateHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	var p validateParams
	if err := xml.Unmarshal(inner, &p); err != nil {
		return nil, rpc.NewError(rpc.TagBadElement, "invalid <validate>: "+err.Error())
	}
	srcName := strings.TrimSpace(topElementName(p.Source.Inner))
	if srcName == "" {
		return nil, rpc.NewError(rpc.TagMissingElement, "<validate> requires <source>")
	}
	ds, err := sysrepoadapter.DatastoreByName(srcName)
	if err != nil {
		return nil, rpc.NewError(rpc.TagInvalidValue, err.Error())
	}
	sess := h.deps.Session
	if sess == nil {
		return nil, rpc.NewError(rpc.TagOperationFailed, "no per-session datastore available")
	}
	if err := sess.SwitchDS(ds); err != nil {
		return nil, rpc.AsError(err)
	}
	if err := sess.Validate("", 0); err != nil {
		return nil, rpc.AsError(err)
	}
	return &rpc.Reply{MessageID: msg.MessageID, Body: rpc.OKReply()}, nil
}
