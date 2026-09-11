package operations

import (
	"encoding/xml"
	"strings"

	"github.com/example/confd/internal/rpc"
	"github.com/example/confd/internal/sysrepoadapter"
)

// <delete-config> clears a datastore. Per RFC 6241 §7.4, only
// <startup> can be deleted (running and candidate cannot be deleted).
type deleteConfigHandler struct{ deps Deps }

type deleteConfigParams struct {
	Target struct {
		XMLName xml.Name
		Inner   []byte `xml:",innerxml"`
	} `xml:"target"`
}

func (h *deleteConfigHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	var p deleteConfigParams
	if err := xml.Unmarshal(inner, &p); err != nil {
		return nil, rpc.NewError(rpc.TagBadElement, "invalid <delete-config>: "+err.Error())
	}
	target := strings.TrimSpace(topElementName(p.Target.Inner))
	if target == "" {
		return nil, rpc.NewError(rpc.TagMissingElement, "<delete-config> requires <target>")
	}
	if target != "startup" {
		return nil, rpc.NewError(rpc.TagInvalidValue,
			"<delete-config> only supports <startup> as target, got: "+target)
	}
	sess := h.deps.Session
	if sess == nil {
		return nil, rpc.NewError(rpc.TagOperationFailed, "no per-session datastore available")
	}
	if err := sess.SwitchDS(sysrepoadapter.Startup); err != nil {
		return nil, rpc.AsError(err)
	}
	if err := sess.ReplaceConfig("", nil, 0); err != nil {
		return nil, rpc.AsError(err)
	}
	return &rpc.Reply{MessageID: msg.MessageID, Body: rpc.OKReply()}, nil
}
