package operations

import (
	"encoding/xml"
	"strings"

	"github.com/example/confd/internal/data"
	"github.com/example/confd/internal/rpc"
	"github.com/example/confd/internal/sysrepoadapter"
)

// <copy-config> copies one datastore to another. The <target> names the
// destination datastore; the <source> can be a datastore name or <config>.
type copyConfigHandler struct{ deps Deps }

type copyConfigParams struct {
	Target struct {
		XMLName xml.Name
		Inner   []byte `xml:",innerxml"`
	} `xml:"target"`
	Source struct {
		XMLName xml.Name
		Inner   []byte `xml:",innerxml"`
	} `xml:"source"`
}

func (h *copyConfigHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	var p copyConfigParams
	if err := xml.Unmarshal(inner, &p); err != nil {
		return nil, rpc.NewError(rpc.TagBadElement, "invalid <copy-config>: "+err.Error())
	}
	target := strings.TrimSpace(topElementName(p.Target.Inner))
	if target == "" {
		return nil, rpc.NewError(rpc.TagMissingElement, "<copy-config> requires <target>")
	}
	targetDS, err := sysrepoadapter.DatastoreByName(target)
	if err != nil {
		return nil, rpc.NewError(rpc.TagInvalidValue, err.Error())
	}
	sess := h.deps.Session
	if sess == nil {
		return nil, rpc.NewError(rpc.TagOperationFailed, "no per-session datastore available")
	}
	if err := sess.SwitchDS(targetDS); err != nil {
		return nil, rpc.AsError(err)
	}
	srcName := strings.TrimSpace(topElementName(p.Source.Inner))
	switch srcName {
	case "running", "startup", "candidate":
		srcDS, err := sysrepoadapter.DatastoreByName(srcName)
		if err != nil {
			return nil, rpc.NewError(rpc.TagInvalidValue, err.Error())
		}
		if err := sess.CopyConfig("", srcDS, 0); err != nil {
			return nil, rpc.AsError(err)
		}
	case "config":
		configTree, err := data.DecodeData(p.Source.Inner)
		if err != nil {
			return nil, rpc.NewError(rpc.TagBadElement, "invalid <config>: "+err.Error())
		}
		if err := sess.ReplaceConfig("", configTree, 0); err != nil {
			return nil, rpc.AsError(err)
		}
	case "url":
		return nil, rpc.NewError(rpc.TagOperationNotSupported, "<url> source not supported")
	default:
		return nil, rpc.NewError(rpc.TagInvalidValue, "unknown <source>: "+srcName)
	}
	return &rpc.Reply{MessageID: msg.MessageID, Body: rpc.OKReply()}, nil
}
