package operations

import (
	"encoding/xml"
	"strings"

	"github.com/example/confd/internal/rpc"
	"github.com/example/confd/internal/sysrepoadapter"
)

// <lock> / <unlock> take a <target> (or <source> for unlock) naming a datastore.
type lockHandler struct{ deps Deps }
type unlockHandler struct{ deps Deps }

type targetParams struct {
	Target struct {
		XMLName xml.Name
		Inner   []byte `xml:",innerxml"`
	} `xml:"target"`
}

func (h *lockHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	return doLock(ctx, msg, inner, h.deps, true)
}

func (h *unlockHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	return doLock(ctx, msg, inner, h.deps, false)
}

func doLock(ctx rpc.Context, msg *rpc.Message, inner []byte, deps Deps, lock bool) (*rpc.Reply, error) {
	var p targetParams
	if err := xml.Unmarshal(inner, &p); err != nil {
		return nil, rpc.NewError(rpc.TagBadElement, "invalid lock request: "+err.Error())
	}
	name := strings.TrimSpace(topElementName(p.Target.Inner))
	if name == "" {
		return nil, rpc.NewError(rpc.TagMissingElement, "missing <target>")
	}
	ds, err := sysrepoadapter.DatastoreByName(name)
	if err != nil {
		return nil, rpc.NewError(rpc.TagInvalidValue, err.Error())
	}
	sess, err := deps.Conn.OpenSession(nilCtx(), ctx.PeerUser)
	if err != nil {
		return nil, rpc.AsError(err)
	}
	defer sess.Close()
	if lock {
		if err := sess.Lock(ds); err != nil {
			return nil, rpc.NewError(rpc.TagLockDenied, err.Error())
		}
	} else {
		if err := sess.Unlock(ds); err != nil {
			return nil, rpc.AsError(err)
		}
	}
	return &rpc.Reply{MessageID: msg.MessageID, Body: rpc.OKReply()}, nil
}
