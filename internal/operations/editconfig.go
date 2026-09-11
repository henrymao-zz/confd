package operations

import (
	"encoding/xml"
	"strings"

	"github.com/example/confd/internal/data"
	"github.com/example/confd/internal/rpc"
	"github.com/example/confd/internal/sysrepoadapter"
)

// <edit-config> loads a parsed edit tree into the session's staging area,
// then applies it. The <target> names the datastore (running or candidate).
// The <default-operation> is merge (default), replace, or none.
// The <config> element contains the edit payload.
type editConfigHandler struct{ deps Deps }

type editConfigParams struct {
	Target struct {
		XMLName xml.Name
		Inner   []byte `xml:",innerxml"`
	} `xml:"target"`
	DefaultOp string `xml:"default-operation"`
	// Config is the inner XML of <config>...</config>.
	Config struct {
		InnerXML []byte `xml:",innerxml"`
	} `xml:"config"`
}

func (h *editConfigHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	var p editConfigParams
	if err := xml.Unmarshal(inner, &p); err != nil {
		return nil, rpc.NewError(rpc.TagBadElement, "invalid <edit-config>: "+err.Error())
	}
	target := strings.TrimSpace(topElementName(p.Target.Inner))
	if target == "" {
		return nil, rpc.NewError(rpc.TagMissingElement, "<edit-config> requires <target>")
	}
	ds, err := sysrepoadapter.DatastoreByName(target)
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
	// Parse <config> XML into a DataNode tree.
	editTree, err := data.DecodeData(p.Config.InnerXML)
	if err != nil {
		return nil, rpc.NewError(rpc.TagBadElement, "invalid <config>: "+err.Error())
	}
	// Stage the edit.
	defaultOp := p.DefaultOp
	if defaultOp == "" {
		defaultOp = "merge"
	}
	if err := sess.EditBatch(editTree, defaultOp); err != nil {
		return nil, rpc.AsError(err)
	}
	// On running, apply immediately. On candidate, just stage
	// (the client must <commit> to apply).
	if ds == sysrepoadapter.Running {
		if err := sess.ApplyChanges(0); err != nil {
			return nil, rpc.AsError(err)
		}
	}
	return &rpc.Reply{MessageID: msg.MessageID, Body: rpc.OKReply()}, nil
}
