// Package rpc implements NETCONF <rpc> message parsing, dispatch, and the
// <rpc-error> / <rpc-reply> builders per RFC 6241.
package rpc

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
)

// ErrorTag mirrors RFC 6241 Appendix A "error-tag" values.
type ErrorTag string

const (
	TagInUse           ErrorTag = "in-use"
	TagInvalidValue    ErrorTag = "invalid-value"
	TagBadElement      ErrorTag = "bad-element"
	TagBadAttribute   ErrorTag = "bad-attribute"
	TagMissingElement ErrorTag = "missing-element"
	TagMissingAttribute ErrorTag = "missing-attribute"
	TagUnknownElement ErrorTag = "unknown-element"
	TagUnknownAttribute ErrorTag = "unknown-attribute"
	TagUnknownNamespace ErrorTag = "unknown-namespace"
	TagAccessDenied    ErrorTag = "access-denied"
	TagLockDenied      ErrorTag = "lock-denied"
	TagResourceDenied  ErrorTag = "resource-denied"
	TagRollbackFailed  ErrorTag = "rollback-failed"
	TagDataMissing     ErrorTag = "data-missing"
	TagOperationFailed ErrorTag = "operation-failed"
	TagOperationNotSupported ErrorTag = "operation-not-supported"
)

// ErrorSeverity is "error" or "warning" per RFC 6241 §4.8.
type ErrorSeverity string

const (
	SeverityError   ErrorSeverity = "error"
	SeverityWarning ErrorSeverity = "warning"
)

// Error is a typed NETCONF rpc-error. It implements the error interface.
type Error struct {
	Tag        ErrorTag
	Severity   ErrorSeverity
	AppTag     string
	Message    string
	Path       string
	InfoAttrs  []Attr
	InfoBody   string // optional <error-info> inner XML
}

// Attr is a key/value attribute for <error-info>.
type Attr struct {
	Name  string
	Value string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("netconf: %s: %s", e.Tag, e.Message)
	}
	return fmt.Sprintf("netconf: %s", e.Tag)
}

// NewError returns an *Error with severity=error.
func NewError(tag ErrorTag, msg string) *Error {
	return &Error{Tag: tag, Severity: SeverityError, Message: msg}
}

// Is implements errors.Is so callers can match on *Error.Tag.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Tag == other.Tag
}

// AsError unwraps an error into a *Error, filling in defaults.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var ne *Error
	if errors.As(err, &ne) {
		if ne.Severity == "" {
			ne.Severity = SeverityError
		}
		return ne
	}
	return &Error{
		Tag:      TagOperationFailed,
		Severity: SeverityError,
		Message:  err.Error(),
	}
}

// Message is the decoded <rpc> envelope.
type Message struct {
	XMLName    xml.Name `xml:"rpc"`
	MessageID  string   `xml:"message-id,attr"`
	InnerXML   []byte   `xml:",innerxml"`
}

// ParseMessage parses a raw NETCONF message payload into a Message.
func ParseMessage(payload []byte) (*Message, error) {
	var m Message
	if err := xml.Unmarshal(payload, &m); err != nil {
		return nil, fmt.Errorf("rpc: invalid <rpc>: %w", err)
	}
	if m.MessageID == "" {
		return nil, NewError(TagMissingAttribute, "missing message-id")
	}
	return &m, nil
}

// InnerOp returns the local name of the single operation element inside the
// <rpc>, e.g. "get" or "get-config". Returns an error if the inner content is
// not a single element.
func (m *Message) InnerOp() (name string, namespace string, inner []byte, err error) {
	dec := xml.NewDecoder(strings.NewReader(string(m.InnerXML)))
	for {
		tok, derr := dec.Token()
		if derr != nil {
			return "", "", nil, fmt.Errorf("rpc: empty or invalid operation: %w", derr)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		// Capture the full inner element (including the op element itself)
		// by re-encoding the StartElement + remaining tokens.
		var b strings.Builder
		enc := xml.NewEncoder(&b)
		if eerr := enc.EncodeToken(se); eerr != nil {
			return "", "", nil, eerr
		}
		if eerr := encodeRest(enc, dec, se.Name); eerr != nil {
			return "", "", nil, eerr
		}
		if eerr := enc.Flush(); eerr != nil {
			return "", "", nil, eerr
		}
		return se.Name.Local, se.Name.Space, []byte(b.String()), nil
	}
}

// encodeRest emits all tokens until the matching end element for name.
func encodeRest(enc *xml.Encoder, dec *xml.Decoder, name xml.Name) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if err := enc.EncodeToken(tok); err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name == name || true {
				depth++
			}
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// Reply builds a <rpc-reply message-id="..."> ... </rpc-reply> envelope.
type Reply struct {
	MessageID string
	// Body is the inner XML (already-encoded). Use OK() or Data(...) to
	// construct common bodies.
	Body []byte
}

// OKReply returns a `<ok/>` reply body.
func OKReply() []byte { return []byte(`<ok/>`) }

// NewReply constructs a Reply with the given inner body bytes.
func NewReply(msgID string, body []byte) *Reply {
	return &Reply{MessageID: msgID, Body: body}
}

// Encode serializes the Reply into NETCONF XML.
func (r *Reply) Encode() []byte {
	return []byte(fmt.Sprintf(`<rpc-reply message-id="%s" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">%s</rpc-reply>`,
		xmlEscape(r.MessageID), string(r.Body)))
}

// EncodeError serializes a *Error into a <rpc-reply><rpc-error>...</rpc-error></rpc-reply>.
func EncodeError(msgID string, e *Error) []byte {
	var b strings.Builder
	b.WriteString(`<rpc-reply message-id="`)
	b.WriteString(xmlEscape(msgID))
	b.WriteString(`" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><rpc-error>`)
	b.WriteString(`<error-tag>`)
	b.WriteString(string(e.Tag))
	b.WriteString(`</error-tag>`)
	sev := e.Severity
	if sev == "" {
		sev = SeverityError
	}
	b.WriteString(`<error-severity>`)
	b.WriteString(string(sev))
	b.WriteString(`</error-severity>`)
	if e.AppTag != "" {
		b.WriteString(`<error-app-tag>`)
		b.WriteString(xmlEscape(e.AppTag))
		b.WriteString(`</error-app-tag>`)
	}
	if e.Message != "" {
		b.WriteString(`<error-message>`)
		b.WriteString(xmlEscape(e.Message))
		b.WriteString(`</error-message>`)
	}
	if e.Path != "" {
		b.WriteString(`<error-path>`)
		b.WriteString(xmlEscape(e.Path))
		b.WriteString(`</error-path>`)
	}
	if e.InfoBody != "" || len(e.InfoAttrs) > 0 {
		b.WriteString(`<error-info>`)
		for _, a := range e.InfoAttrs {
			b.WriteString(fmt.Sprintf(`<%s>%s</%s>`, a.Name, xmlEscape(a.Value), a.Name))
		}
		b.WriteString(e.InfoBody)
		b.WriteString(`</error-info>`)
	}
	b.WriteString(`</rpc-error></rpc-reply>`)
	return []byte(b.String())
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Handler processes a parsed <rpc> and returns a reply body (for OK/Data
// replies) or an error (which will be rendered as <rpc-error>).
type Handler interface {
	Handle(ctx Context, msg *Message, opName, opNS string, inner []byte) (*Reply, error)
}

// Context carries per-session state to handlers.
type Context struct {
	SessionID  uint64
	PeerUser   string
	Dispatcher *Dispatcher
}

// Dispatcher maps operation names to Handlers.
type Dispatcher struct {
	handlers map[string]Handler
}

// NewDispatcher returns an empty dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{handlers: map[string]Handler{}}
}

// Register adds a handler for the given operation local-name.
func (d *Dispatcher) Register(op string, h Handler) {
	d.handlers[op] = h
}

// Handle parses an <rpc> payload, dispatches, and returns the encoded reply
// (either <rpc-reply>...</rpc-reply> or a <rpc-error>... reply).
func (d *Dispatcher) Handle(ctx Context, payload []byte) []byte {
	msg, err := ParseMessage(payload)
	if err != nil {
		return EncodeError("", AsError(err))
	}
	opName, opNS, inner, err := msg.InnerOp()
	if err != nil {
		return EncodeError(msg.MessageID, AsError(err))
	}
	h, ok := d.handlers[opName]
	if !ok {
		ne := NewError(TagOperationNotSupported,
			"operation '"+opName+"' is not supported")
		return EncodeError(msg.MessageID, ne)
	}
	_ = opNS
	reply, err := h.Handle(ctx, msg, opName, opNS, inner)
	if err != nil {
		return EncodeError(msg.MessageID, AsError(err))
	}
	if reply == nil {
		reply = &Reply{MessageID: msg.MessageID, Body: OKReply()}
	}
	reply.MessageID = msg.MessageID
	return reply.Encode()
}
