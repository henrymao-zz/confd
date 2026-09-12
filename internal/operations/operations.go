// Package operations implements the NETCONF protocol operation handlers for
// confd's MVP: <get>, <get-config>, <get-schema>, <lock>, <unlock>,
// <close-session>, and <kill-session>.
package operations

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"strings"

	"github.com/example/confd/internal/data"
	"github.com/example/confd/internal/rpc"
	"github.com/example/confd/internal/schema"
	"github.com/example/confd/internal/sysrepoadapter"
	"github.com/example/confd/internal/transport"
)

// Deps are the shared dependencies passed to every operation handler.
type Deps struct {
	Cache    *schema.Cache
	Conn     sysrepoadapter.Conn
	Encoder  *data.Encoder
	Sessions *SessionRegistry
	Session  sysrepoadapter.Session // per-NETCONF-session datastore session (for edit ops)
}

// SessionState is the per-NETCONF-session state handlers can read/mutate.
type SessionState struct {
	ID       uint64
	User     string
	Adapter  sysrepoadapter.Session
}

// SessionRegistry tracks live NETCONF sessions (for <kill-session>) and
// provides the next session id.
type SessionRegistry struct {
	nextID  uint64
	live    map[uint64]*SessionState
}

// NewSessionRegistry returns an empty registry.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{nextID: 1, live: map[uint64]*SessionState{}}
}

// Alloc returns the next session id and reserves it.
func (r *SessionRegistry) Alloc() uint64 {
	r.nextID++
	return r.nextID - 1
}

// Register adds a session to the live set.
func (r *SessionRegistry) Register(s *SessionState) {
	r.live[s.ID] = s
}

// Forget removes a session from the live set.
func (r *SessionRegistry) Forget(id uint64) {
	delete(r.live, id)
}

// Get returns a live session by id.
func (r *SessionRegistry) Get(id uint64) (*SessionState, bool) {
	s, ok := r.live[id]
	return s, ok
}

// List returns the ids of all live sessions.
func (r *SessionRegistry) List() []uint64 {
	out := make([]uint64, 0, len(r.live))
	for id := range r.live {
		out = append(out, id)
	}
	return out
}

// Register all operations onto a dispatcher.
func Register(d *rpc.Dispatcher, deps Deps) {
	d.Register("get", &getHandler{deps: deps})
	d.Register("get-config", &getConfigHandler{deps: deps})
	d.Register("get-schema", &getSchemaHandler{deps: deps})
	d.Register("edit-config", &editConfigHandler{deps: deps})
	d.Register("copy-config", &copyConfigHandler{deps: deps})
	d.Register("delete-config", &deleteConfigHandler{deps: deps})
	d.Register("commit", &commitHandler{deps: deps})
	d.Register("discard-changes", &discardChangesHandler{deps: deps})
	d.Register("validate", &validateHandler{deps: deps})
	d.Register("lock", &lockHandler{deps: deps})
	d.Register("unlock", &unlockHandler{deps: deps})
	d.Register("close-session", &closeSessionHandler{deps: deps})
	d.Register("kill-session", &killSessionHandler{deps: deps})
}

// BuildHandlers returns the operation handler map for transport.ServerLoop.
// Each handler takes (msgID, innerXML) and returns (replyBody, error).
// The replyBody is raw XML bytes to embed in <rpc-reply>; the error is
// converted to an <rpc-error> by transport.ServerLoop.
func BuildHandlers(deps Deps, sessionID uint64, peerUser string) map[string]transport.Handler {
	d := rpc.NewDispatcher()
	Register(d, deps)
	rctx := rpc.Context{SessionID: sessionID, PeerUser: peerUser, Dispatcher: d}

	handlers := make(map[string]transport.Handler)

	wrap := func(opName string) transport.Handler {
		return func(msgID string, innerXML []byte) (any, error) {
			rpcXML := fmt.Sprintf(`<rpc message-id="%s" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">%s</rpc>`,
				msgID, string(innerXML))
			fmt.Fprintf(os.Stderr, "DEBUG wrap: opName=%q rpcXML=%s\n", opName, rpcXML)
			out := d.Handle(rctx, []byte(rpcXML))
			fmt.Fprintf(os.Stderr, "DEBUG wrap: out=%s\n", string(out))
			return extractRPCReplyInner(out), nil
		}
	}

	for _, op := range []string{"get", "get-config", "get-schema", "edit-config", "copy-config", "delete-config", "commit", "discard-changes", "validate", "lock", "unlock", "close-session", "kill-session"} {
		handlers[op] = wrap(op)
	}
	return handlers
}

// extractRPCReplyInner extracts the inner XML from a <rpc-reply> envelope.
func extractRPCReplyInner(reply []byte) []byte {
	s := string(reply)
	startTag := "<rpc-reply"
	endTag := "</rpc-reply>"
	startIdx := strings.Index(s, startTag)
	if startIdx < 0 {
		return []byte("<ok/>")
	}
	// Find the end of the opening <rpc-reply ...> tag.
	gtIdx := strings.Index(s[startIdx:], ">")
	if gtIdx < 0 {
		return []byte("<ok/>")
	}
	innerStart := startIdx + gtIdx + 1
	endIdx := strings.LastIndex(s, endTag)
	if endIdx < 0 || endIdx <= innerStart {
		return []byte("<ok/>")
	}
	return []byte(s[innerStart:endIdx])
}

// --- shared helpers ---------------------------------------------------------

// filterSpec is the parsed <filter> element.
type filterSpec struct {
	Type    string `xml:"type,attr"`
	Inner   []byte `xml:",innerxml"`
}

func applyFilter(ctx context.Context, enc *data.Encoder, sess sysrepoadapter.Session, ds sysrepoadapter.Datastore, f *filterSpec) ([]byte, error) {
	root, err := sess.Get(ctx, "/")
	if err != nil {
		return nil, err
	}
	if f == nil {
		return enc.EncodeData(root), nil
	}
	switch f.Type {
	case "", "subtree":
		return applySubtreeFilter(enc, root, f.Inner)
	case "xpath":
		xpath := strings.TrimSpace(string(f.Inner))
		node, err := sess.Get(ctx, xpath)
		if err != nil {
			return nil, err
		}
		return enc.EncodeData(node), nil
	default:
		return nil, rpc.NewError(rpc.TagOperationNotSupported,
			"filter type '"+f.Type+"' not supported")
	}
}

// applySubtreeFilter selects, from the full data tree, the subtrees named by
// the top-level element(s) of the filter body. This is a simplified
// implementation of RFC 6241 §6.4 sufficient for the MVP: it matches by
// element local name.
func applySubtreeFilter(enc *data.Encoder, root *sysrepoadapter.DataNode, filterXML []byte) ([]byte, error) {
	names, err := topElementNames(filterXML)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return enc.EncodeData(root), nil
	}
	var selected []*sysrepoadapter.DataNode
	for _, n := range names {
		if m := findByLocalName(root, n); m != nil {
			selected = append(selected, m)
		}
	}
	if len(selected) == 0 {
		return []byte(""), nil
	}
	out := &sysrepoadapter.DataNode{XPath: "/", Name: "root", Children: selected}
	return enc.EncodeData(out), nil
}

func topElementNames(filterXML []byte) ([]string, error) {
	dec := xml.NewDecoder(strings.NewReader(strings.TrimSpace(string(filterXML))))
	var names []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok {
			names = append(names, se.Name.Local)
			// skip its children; just collect top-level start elements
		}
	}
	return names, nil
}

func findByLocalName(root *sysrepoadapter.DataNode, name string) *sysrepoadapter.DataNode {
	if root.Name == name {
		return root
	}
	for _, c := range root.Children {
		if c.Name == name {
			return c
		}
		if r := findByLocalName(c, name); r != nil {
			return r
		}
	}
	return nil
}

func parseFilter(inner []byte) (*filterSpec, error) {
	trimmed := strings.TrimSpace(string(inner))
	if trimmed == "" {
		return nil, nil
	}
	var f filterSpec
	if err := xml.Unmarshal([]byte(trimmed), &f); err != nil {
		return nil, fmt.Errorf("operations: invalid <filter>: %w", err)
	}
	if f.Type == "" {
		f.Type = "subtree"
	}
	return &f, nil
}
