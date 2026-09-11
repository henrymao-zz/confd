package operations

import (
	"github.com/example/confd/internal/rpc"
	"github.com/example/confd/internal/sysrepoadapter"
)

// <commit> applies staged candidate edits to the running datastore.
// Per RFC 6241 §8.3.4.1, the session must be on the candidate datastore.
type commitHandler struct{ deps Deps }

func (h *commitHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	sess := h.deps.Session
	if sess == nil {
		return nil, rpc.NewError(rpc.TagOperationFailed, "no per-session datastore available")
	}
	if sess.CurrentDS() != sysrepoadapter.Candidate {
		return nil, rpc.NewError(rpc.TagInvalidValue,
			"<commit> requires the session to be on the candidate datastore (use <edit-config><target><candidate/>)")
	}
	if err := sess.ApplyChanges(0); err != nil {
		return nil, rpc.AsError(err)
	}
	return &rpc.Reply{MessageID: msg.MessageID, Body: rpc.OKReply()}, nil
}

// <discard-changes> discards all staged edits on the candidate datastore.
type discardChangesHandler struct{ deps Deps }

func (h *discardChangesHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	sess := h.deps.Session
	if sess == nil {
		return nil, rpc.NewError(rpc.TagOperationFailed, "no per-session datastore available")
	}
	if sess.CurrentDS() != sysrepoadapter.Candidate {
		return nil, rpc.NewError(rpc.TagInvalidValue,
			"<discard-changes> requires the session to be on the candidate datastore")
	}
	if err := sess.DiscardChanges(); err != nil {
		return nil, rpc.AsError(err)
	}
	return &rpc.Reply{MessageID: msg.MessageID, Body: rpc.OKReply()}, nil
}
