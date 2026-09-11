package operations

import (
	"context"
	"encoding/xml"

	"github.com/example/confd/internal/rpc"
	"github.com/example/confd/internal/sysrepoadapter"
)

// <get> has an optional <filter> child and reads from the operational
// datastore.
type getHandler struct{ deps Deps }

type getParams struct {
	Filter *filterSpec `xml:"filter"`
}

func (h *getHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	var p getParams
	if err := xml.Unmarshal(inner, &p); err != nil {
		return nil, rpc.NewError(rpc.TagBadElement, "invalid <get>: "+err.Error())
	}
	sess := h.deps.Session
	if sess != nil {
		if err := sess.SwitchDS(sysrepoadapter.Operational); err != nil {
			return nil, rpc.AsError(err)
		}
		body, err := applyFilter(context.Background(), h.deps.Encoder, sess, sysrepoadapter.Operational, p.Filter)
		if err != nil {
			return nil, err
		}
		return &rpc.Reply{MessageID: msg.MessageID, Body: dataWrap(body)}, nil
	}
	// Fallback: open a short-lived session (backward compat with per-RPC mode).
	s, err := h.deps.Conn.OpenSession(context.Background(), ctx.PeerUser)
	if err != nil {
		return nil, rpc.AsError(err)
	}
	defer s.Close()
	if err := s.SwitchDS(sysrepoadapter.Operational); err != nil {
		return nil, rpc.AsError(err)
	}
	body, err := applyFilter(context.Background(), h.deps.Encoder, s, sysrepoadapter.Operational, p.Filter)
	if err != nil {
		return nil, err
	}
	return &rpc.Reply{MessageID: msg.MessageID, Body: dataWrap(body)}, nil
}

func dataWrap(inner []byte) []byte {
	out := make([]byte, 0, len(inner)+len("<data></data>"))
	out = append(out, []byte("<data>")...)
	out = append(out, inner...)
	out = append(out, []byte("</data>")...)
	return out
}
