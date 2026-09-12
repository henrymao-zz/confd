package operations

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"strings"

	"github.com/example/confd/internal/rpc"
	"github.com/example/confd/internal/sysrepoadapter"
)

// <get-config> reads from a named <source> datastore with an optional filter.
type getConfigHandler struct{ deps Deps }

type getConfigParams struct {
	Source struct {
		XMLName xml.Name
		Inner   []byte `xml:",innerxml"`
	} `xml:"source"`
	Filter *filterSpec `xml:"filter"`
}

func (h *getConfigHandler) Handle(ctx rpc.Context, msg *rpc.Message, _, _ string, inner []byte) (*rpc.Reply, error) {
	fmt.Fprintf(os.Stderr, "DEBUG getConfigHandler: deps.Session=%T\n", h.deps.Session)
	var p getConfigParams
	if err := xml.Unmarshal(inner, &p); err != nil {
		return nil, rpc.NewError(rpc.TagBadElement, "invalid <get-config>: "+err.Error())
	}
	srcName := strings.TrimSpace(topElementName(p.Source.Inner))
	if srcName == "" {
		return nil, rpc.NewError(rpc.TagMissingElement, "<get-config> requires <source>")
	}
	ds, err := sysrepoadapter.DatastoreByName(srcName)
	if err != nil {
		return nil, rpc.NewError(rpc.TagInvalidValue, err.Error())
	}
	sess := h.deps.Session
	if sess != nil {
		if err := sess.SwitchDS(ds); err != nil {
			return nil, rpc.AsError(err)
		}
		body, err := applyFilter(context.Background(), h.deps.Encoder, sess, ds, p.Filter)
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
	if err := s.SwitchDS(ds); err != nil {
		return nil, rpc.AsError(err)
	}
	body, err := applyFilter(context.Background(), h.deps.Encoder, s, ds, p.Filter)
	if err != nil {
		return nil, err
	}
	return &rpc.Reply{MessageID: msg.MessageID, Body: dataWrap(body)}, nil
}

func topElementName(inner []byte) string {
	dec := xml.NewDecoder(strings.NewReader(strings.TrimSpace(string(inner))))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}
