package operations

import (
	"context"
	"encoding/xml"
	"strconv"
	"strings"

	"github.com/example/confd/internal/rpc"
)

// <close-session> ends the calling session. <kill-session> ends a named
// session. close-session is detected by the server's session loop (which
// tears down the transport after the OK reply); kill-session is handled
// here by looking up and closing the target session.

// closeSessionHandler is a marker; the server's session loop handles teardown
// by closing the transport when it sees this op.
type closeSessionHandler struct{ deps Deps }

func (h *closeSessionHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, _ []byte) (*rpc.Reply, error) {
	// The server's session loop detects <close-session> by name and tears
	// down the transport after the OK reply is written.
	return &rpc.Reply{MessageID: msg.MessageID, Body: rpc.OKReply()}, nil
}

// killSessionHandler tears down a session by numeric session-id.
type killSessionHandler struct{ deps Deps }

type killSessionParams struct {
	SessionID string `xml:"session-id"`
}

func (h *killSessionHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	var p killSessionParams
	if err := xml.Unmarshal(inner, &p); err != nil {
		return nil, rpc.NewError(rpc.TagBadElement, "invalid <kill-session>: "+err.Error())
	}
	id, err := strconv.ParseUint(strings.TrimSpace(p.SessionID), 10, 64)
	if err != nil {
		return nil, rpc.NewError(rpc.TagInvalidValue, "invalid session-id")
	}
	target, ok := h.deps.Sessions.Get(id)
	if !ok {
		return nil, rpc.NewError(rpc.TagInvalidValue, "session "+strconv.FormatUint(id, 10)+" not found")
	}
	if target.Adapter != nil {
		_ = target.Adapter.Close()
	}
	h.deps.Sessions.Forget(id)
	return &rpc.Reply{MessageID: msg.MessageID, Body: rpc.OKReply()}, nil
}

func nilCtx() context.Context { return context.Background() }
