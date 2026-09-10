package rpc

import (
	"strings"
	"testing"
)

func TestParseMessage(t *testing.T) {
	in := []byte(`<rpc message-id="42"><get/></rpc>`)
	m, err := ParseMessage(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.MessageID != "42" {
		t.Errorf("message-id: got %q want 42", m.MessageID)
	}
	name, _, inner, err := m.InnerOp()
	if err != nil {
		t.Fatalf("innerop: %v", err)
	}
	if name != "get" {
		t.Errorf("op name: got %q want get", name)
	}
	if !strings.Contains(string(inner), "get") {
		t.Errorf("inner: %q", inner)
	}
}

func TestParseMessage_MissingMessageID(t *testing.T) {
	in := []byte(`<rpc><get/></rpc>`)
	_, err := ParseMessage(in)
	if err == nil {
		t.Fatal("expected error for missing message-id")
	}
	ne, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if ne.Tag != TagMissingAttribute {
		t.Errorf("tag: got %q want missing-attribute", ne.Tag)
	}
}

func TestDispatcher_Unknown(t *testing.T) {
	d := NewDispatcher()
	out := d.Handle(Context{}, []byte(`<rpc message-id="1"><bogus/></rpc>`))
	if !strings.Contains(string(out), "operation-not-supported") {
		t.Errorf("expected operation-not-supported, got: %s", out)
	}
}

func TestDispatcher_OK(t *testing.T) {
	d := NewDispatcher()
	d.Register("get", okHandler{})
	out := d.Handle(Context{}, []byte(`<rpc message-id="7"><get/></rpc>`))
	if !strings.Contains(string(out), `<ok/>`) {
		t.Errorf("expected <ok/>, got: %s", out)
	}
	if !strings.Contains(string(out), `message-id="7"`) {
		t.Errorf("missing message-id: %s", out)
	}
}

func TestDispatcher_Error(t *testing.T) {
	d := NewDispatcher()
	d.Register("bad", errHandler{&Error{Tag: TagBadElement, Message: "nope"}})
	out := d.Handle(Context{}, []byte(`<rpc message-id="9"><bad/></rpc>`))
	if !strings.Contains(string(out), "bad-element") || !strings.Contains(string(out), "nope") {
		t.Errorf("expected rpc-error with msg, got: %s", out)
	}
}

func TestEncodeError(t *testing.T) {
	e := &Error{Tag: TagAccessDenied, Message: "no perms", AppTag: "denied"}
	out := EncodeError("101", e)
	if !strings.Contains(string(out), "access-denied") ||
		!strings.Contains(string(out), "no perms") ||
		!strings.Contains(string(out), "denied") ||
		!strings.Contains(string(out), `message-id="101"`) {
		t.Errorf("encode error: %s", out)
	}
}

type okHandler struct{}

func (okHandler) Handle(_ Context, _ *Message, _, _ string, _ []byte) (*Reply, error) {
	return &Reply{Body: OKReply()}, nil
}

type errHandler struct{ e *Error }

func (h errHandler) Handle(_ Context, _ *Message, _, _ string, _ []byte) (*Reply, error) {
	return nil, h.e
}
